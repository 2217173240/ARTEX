[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Binary,
    [Parameter(Mandatory)][ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version,
    [Parameter(Mandatory)][ValidateSet('amd64', 'arm64')][string]$Architecture,
    [Parameter(Mandatory)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
# The pinned tool targets .NET 6; permit the installed SDK 8 runtime.
$previousRollForward = $env:DOTNET_ROLL_FORWARD
Set-StrictMode -Version Latest
if ($env:OS -ne 'Windows_NT') { throw 'MSI packaging requires Windows and .NET SDK 8.' }
$repo = Split-Path $PSScriptRoot -Parent
$binaryPath = (Resolve-Path $Binary).Path
# Reject mislabeled payloads before producing installer architecture metadata.
$bytes = [IO.File]::ReadAllBytes($binaryPath)
if ($bytes.Length -lt 64 -or $bytes[0] -ne 0x4d -or $bytes[1] -ne 0x5a) { throw 'Binary is not a Windows PE executable.' }
$peOffset = [BitConverter]::ToInt32($bytes, 60)
if ($peOffset -lt 0 -or $peOffset + 6 -gt $bytes.Length -or [BitConverter]::ToUInt32($bytes, $peOffset) -ne 0x4550) { throw 'Invalid PE signature.' }
$expectedMachine = if ($Architecture -eq 'amd64') { 0x8664 } else { 0xaa64 }
if ([BitConverter]::ToUInt16($bytes, $peOffset + 4) -ne $expectedMachine) { throw "Binary architecture does not match $Architecture." }
$parts = $Version.Split('.') | ForEach-Object { [int]$_ }
if ($parts[0] -gt 255 -or $parts[1] -gt 255 -or $parts[2] -gt 65535) { throw 'Version exceeds MSI version limits.' }
$outputPath = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Force $outputPath | Out-Null
$work = Join-Path ([IO.Path]::GetTempPath()) ('artex-msi-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $work | Out-Null
try {
    $env:DOTNET_ROLL_FORWARD = 'Major'
    $stage = Join-Path $work 'payload'
    New-Item -ItemType Directory $stage | Out-Null
    Copy-Item $binaryPath (Join-Path $stage 'artex.exe')
    foreach ($name in @('config.example.json', 'LICENSE', 'CHANGELOG.md')) {
        Copy-Item (Join-Path $repo $name) $stage -Recurse
    }
    & python (Join-Path $repo 'scripts/stage-skills.py') (Join-Path $stage 'skills')
    if ($LASTEXITCODE -ne 0) { throw 'Tracked skill staging failed.' }
    Copy-Item (Join-Path $repo 'packaging/windows/launch.vbs') $stage
    Copy-Item (Join-Path $repo 'packaging/windows/README.txt') $stage
    # WiX 4.0.6 is the MS-RL release predating WiX's maintenance-fee policy.
    # Pin and verify the exact official NuGet artifact; use an isolated local feed.
    $feed = Join-Path $work 'feed'
    New-Item -ItemType Directory $feed | Out-Null
    $package = Join-Path $feed 'wix.4.0.6.nupkg'
    Invoke-WebRequest 'https://api.nuget.org/v3-flatcontainer/wix/4.0.6/wix.4.0.6.nupkg' -OutFile $package
    if ((Get-FileHash $package -Algorithm SHA256).Hash.ToLowerInvariant() -ne 'a94dd42ae1fb56b32da180e2173ceda4f0d10b4c8871c5ee59ecb502131a1eb6') { throw 'WiX package checksum mismatch.' }
    $nugetConfig = Join-Path $work 'NuGet.Config'
    Set-Content $nugetConfig '<configuration><packageSources><clear /></packageSources></configuration>' -Encoding UTF8
    $toolPath = Join-Path $work 'tools'
    & dotnet tool install wix --version 4.0.6 --tool-path $toolPath --configfile $nugetConfig --add-source $feed
    if ($LASTEXITCODE -ne 0) { throw 'WiX tool installation failed.' }
    function Escape-Xml([string]$value) { [System.Security.SecurityElement]::Escape($value) }
    $upgradeCode = if ($Architecture -eq 'amd64') { 'E96F624E-6FB5-4F10-9DBD-168C3D34DC48' } else { 'B8F78FAD-E689-4167-9A3D-3F7259613933' }
    $xml = [Text.StringBuilder]::new()
    [void]$xml.AppendLine('<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">')
    [void]$xml.AppendLine("<Package Name=`"ARTEX`" Manufacturer=`"ARTEX`" Version=`"$Version`" Language=`"1033`" UpgradeCode=`"$upgradeCode`" Scope=`"perUser`" InstallerVersion=`"500`">")
    [void]$xml.AppendLine('<MajorUpgrade DowngradeErrorMessage="A newer ARTEX version is already installed." Schedule="afterInstallInitialize" />')
    [void]$xml.AppendLine('<MediaTemplate EmbedCab="yes" /><Property Id="DESKTOP_SHORTCUT" Value="0" />')
    [void]$xml.AppendLine('<StandardDirectory Id="LocalAppDataFolder"><Directory Id="ProgramsFolder" Name="Programs"><Directory Id="INSTALLFOLDER" Name="ARTEX">')
    $directories = @{ '' = 'INSTALLFOLDER' }
    $counter = 0
    function Add-Directory([string]$relative) {
        if ($directories.ContainsKey($relative)) { return $directories[$relative] }
        $parent = [IO.Path]::GetDirectoryName($relative)
        if ($null -eq $parent) { $parent = '' }
        $parentId = Add-Directory $parent
        $id = 'D' + $directories.Count
        $directories[$relative] = $id
        [void]$xml.AppendLine("<DirectoryRef Id=`"$parentId`"><Directory Id=`"$id`" Name=`"$(Escape-Xml ([IO.Path]::GetFileName($relative)))`" /></DirectoryRef>")
        return $id
    }
    [void]$xml.AppendLine('</Directory></Directory></StandardDirectory>')
    $components = [Collections.Generic.List[string]]::new()
    foreach ($file in (Get-ChildItem $stage -Recurse -File | Sort-Object FullName)) {
        $relative = $file.FullName.Substring($stage.Length + 1)
        $directory = [IO.Path]::GetDirectoryName($relative)
        if ($null -eq $directory) { $directory = '' }
        $directoryId = Add-Directory $directory
        $id = 'Payload' + $counter++
        $components.Add($id)
        [void]$xml.AppendLine("<DirectoryRef Id=`"$directoryId`"><Component Id=`"$id`" Guid=`"*`"><File Id=`"File$id`" Source=`"$(Escape-Xml $file.FullName)`" KeyPath=`"no`" />")
        [void]$xml.AppendLine("<RegistryValue Root=`"HKCU`" Key=`"Software\ARTEX\Installer\$Architecture`" Name=`"$(Escape-Xml $relative)`" Type=`"integer`" Value=`"1`" KeyPath=`"yes`" /></Component></DirectoryRef>")
    }
    foreach ($entry in $directories.GetEnumerator()) {
        $id = 'Cleanup' + $entry.Value
        $components.Add($id)
        [void]$xml.AppendLine("<DirectoryRef Id=`"$($entry.Value)`"><Component Id=`"$id`" Guid=`"*`"><RemoveFolder Id=`"Remove$($entry.Value)`" On=`"uninstall`" /><RegistryValue Root=`"HKCU`" Key=`"Software\ARTEX\Installer\$Architecture`" Name=`"$id`" Type=`"integer`" Value=`"1`" KeyPath=`"yes`" /></Component></DirectoryRef>")
    }
    [void]$xml.AppendLine('<StandardDirectory Id="ProgramMenuFolder"><Directory Id="ARTEXMenuFolder" Name="ARTEX"><Component Id="StartShortcut" Guid="*"><Shortcut Id="ARTEXStart" Name="ARTEX" Description="Open ARTEX in your browser" Target="[SystemFolder]wscript.exe" Arguments="&quot;[INSTALLFOLDER]launch.vbs&quot;" WorkingDirectory="INSTALLFOLDER" /><RemoveFolder Id="RemoveARTEXMenu" On="uninstall" />')
    [void]$xml.AppendLine('<Shortcut Id="ARTEXControl" Name="ARTEX Control" Description="Open the ARTEX local control page" Target="[SystemFolder]wscript.exe" Arguments="&quot;[INSTALLFOLDER]launch.vbs&quot; control" WorkingDirectory="INSTALLFOLDER" /><Shortcut Id="ARTEXStop" Name="Stop ARTEX" Description="Stop the ARTEX local application" Target="[SystemFolder]wscript.exe" Arguments="&quot;[INSTALLFOLDER]launch.vbs&quot; stop" WorkingDirectory="INSTALLFOLDER" />')
    [void]$xml.AppendLine("<RegistryValue Root=`"HKCU`" Key=`"Software\ARTEX\Installer\$Architecture`" Name=`"StartShortcut`" Type=`"integer`" Value=`"1`" KeyPath=`"yes`" /></Component></Directory></StandardDirectory>")
    [void]$xml.AppendLine('<StandardDirectory Id="DesktopFolder"><Component Id="DesktopShortcut" Guid="*" Condition="DESKTOP_SHORTCUT = 1"><Shortcut Id="ARTEXDesktop" Name="ARTEX" Description="Open ARTEX in your browser" Target="[SystemFolder]wscript.exe" Arguments="&quot;[INSTALLFOLDER]launch.vbs&quot;" WorkingDirectory="INSTALLFOLDER" />')
    [void]$xml.AppendLine("<RegistryValue Root=`"HKCU`" Key=`"Software\ARTEX\Installer\$Architecture`" Name=`"DesktopShortcut`" Type=`"integer`" Value=`"1`" KeyPath=`"yes`" /></Component></StandardDirectory>")
    [void]$xml.AppendLine('<Feature Id="MainFeature" Title="ARTEX" Level="1"><ComponentRef Id="StartShortcut" /><ComponentRef Id="DesktopShortcut" />')
    foreach ($id in $components) { [void]$xml.AppendLine("<ComponentRef Id=`"$id`" />") }
    [void]$xml.AppendLine('</Feature></Package></Wix>')
    $source = Join-Path $work 'artex.wxs'
    [IO.File]::WriteAllText($source, $xml.ToString(), [Text.UTF8Encoding]::new($false))
    $platform = if ($Architecture -eq 'amd64') { 'x64' } else { 'arm64' }
    $msi = Join-Path $outputPath "artex-$Version-windows-$Architecture.msi"
    & (Join-Path $toolPath 'wix.exe') build $source -arch $platform -o $msi -pdbtype none
    if ($LASTEXITCODE -ne 0) { throw 'WiX MSI build or validation failed.' }
    Write-Output $msi
} finally {
    $env:DOTNET_ROLL_FORWARD = $previousRollForward
    Remove-Item $work -Recurse -Force
}
