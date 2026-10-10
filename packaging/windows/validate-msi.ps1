# Execute on a disposable Windows runner, under the installing user's identity.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Msi,
    [Parameter(Mandatory)][ValidateSet('amd64', 'arm64')][string]$Architecture,
    [string]$PreviousMsi,
    [ValidateSet('amd64', 'arm64')][string]$PreviousMsiArchitecture,
    [string]$SameVersionMsi,
    [switch]$MetadataOnly
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$Msi = (Resolve-Path $Msi).Path
$installer = New-Object -ComObject WindowsInstaller.Installer
$db = $installer.OpenDatabase($Msi, 0)
$template = $db.SummaryInformation(0).Property(7)
$expected = if ($Architecture -eq 'amd64') { 'x64' } else { 'Arm64' }
if ($template.Split(';')[0] -cne $expected) { throw "Wrong MSI architecture: $template; expected $expected" }
function Read-Property([string]$name, $database = $db) {
    $view = $database.OpenView("SELECT ``Value`` FROM ``Property`` WHERE ``Property`` = '$name'")
    $view.Execute()
    $record = $view.Fetch()
    if ($null -eq $record) { return '' }
    return $record.StringData(1)
}
if ((Read-Property 'ALLUSERS') -ne '') { throw 'Installer must be per-user.' }
if ((Read-Property 'UpgradeCode') -ne '{E96F624E-6FB5-4F10-9DBD-168C3D34DC48}') { throw 'Installer upgrade family differs across architectures.' }
$upgradeView = $db.OpenView('SELECT `Attributes` FROM `Upgrade` WHERE `ActionProperty` = ''WIX_UPGRADE_DETECTED''')
$upgradeView.Execute()
$upgradeRecord = $upgradeView.Fetch()
if ($null -eq $upgradeRecord -or ($upgradeRecord.IntegerData(1) -band 512) -eq 0) { throw 'Same-version upgrades must include the maximum version.' }
if ($MetadataOnly) { Write-Output "Verified $template per-user MSI metadata."; return }
if (-not $PreviousMsiArchitecture) { $PreviousMsiArchitecture = $Architecture }
$productCode = Read-Property 'ProductCode'
$previousCode = ''
$sameVersionCode = ''
$sameVersionArchitecture = if ($Architecture -eq 'amd64') { 'arm64' } else { 'amd64' }
function Read-Package([string]$path, [string]$arch) {
    $database = $installer.OpenDatabase($path, 0)
    $wantedTemplate = if ($arch -eq 'amd64') { 'x64' } else { 'Arm64' }
    if ($database.SummaryInformation(0).Property(7).Split(';')[0] -cne $wantedTemplate) { throw 'Auxiliary MSI architecture mismatch.' }
    if ((Read-Property 'UpgradeCode' $database) -ne (Read-Property 'UpgradeCode')) { throw 'Auxiliary MSI is from a different upgrade family.' }
    return $database
}
if ($PreviousMsi) {
    $PreviousMsi = (Resolve-Path $PreviousMsi).Path
    $previousDb = Read-Package $PreviousMsi $PreviousMsiArchitecture
    if ([version](Read-Property 'ProductVersion' $previousDb) -ge [version](Read-Property 'ProductVersion')) { throw 'Previous MSI must have an older version.' }
    $previousCode = Read-Property 'ProductCode' $previousDb
}
if ($SameVersionMsi) {
    $SameVersionMsi = (Resolve-Path $SameVersionMsi).Path
    $sameVersionDb = Read-Package $SameVersionMsi $sameVersionArchitecture
    if ((Read-Property 'ProductVersion' $sameVersionDb) -ne (Read-Property 'ProductVersion')) { throw 'Cross-architecture MSI versions must match.' }
    $sameVersionCode = Read-Property 'ProductCode' $sameVersionDb
    if ($sameVersionCode -eq $productCode) { throw 'Different architectures require different product codes.' }
}
$target = Join-Path $env:LOCALAPPDATA 'Programs/ARTEX'
if (Test-Path $target) { throw 'Use a disposable runner without an existing ARTEX installation.' }
$data = Join-Path $env:LOCALAPPDATA 'ARTEX'
New-Item -ItemType Directory -Force $data | Out-Null
$sentinel = Join-Path $data ('installer-preservation-' + [guid]::NewGuid().ToString('N'))
Set-Content $sentinel 'must survive install, upgrade, and uninstall'
function Invoke-Msi([string]$operation, [string]$path, [bool]$expectFailure = $false) {
    $log = Join-Path $env:TEMP ('artex-msi-' + [guid]::NewGuid().ToString('N') + '.log')
    $process = Start-Process msiexec.exe -ArgumentList @($operation, "`"$path`"", '/qn', '/norestart', 'DESKTOP_SHORTCUT=1', '/l*v', "`"$log`"") -Wait -PassThru
    if ($expectFailure) {
        if ($process.ExitCode -ne 1603) { throw "Expected downgrade rejection 1603, got $($process.ExitCode); $log" }
    } elseif ($process.ExitCode -notin @(0, 3010)) { throw "msiexec exited $($process.ExitCode); $log" }
}
function Assert-Installed([string]$code, [string]$arch, [string]$replacedCode = '') {
    if ($installer.ProductState($code) -ne 5) { throw 'Expected MSI product is not installed.' }
    if ($replacedCode -and $installer.ProductState($replacedCode) -ne -1) { throw 'Upgrade left the replaced product registered.' }
    $bytes = [IO.File]::ReadAllBytes((Join-Path $target 'artex.exe'))
    $offset = [BitConverter]::ToInt32($bytes, 60)
    $machine = if ($arch -eq 'amd64') { 0x8664 } else { 0xaa64 }
    if ([BitConverter]::ToUInt16($bytes, $offset + 4) -ne $machine) { throw 'Installed executable architecture differs from target.' }
    if (-not (Test-Path $sentinel)) { throw 'Upgrade erased user data.' }
}
try {
    if ($PreviousMsi) {
        Invoke-Msi '/i' $PreviousMsi
        Assert-Installed $previousCode $PreviousMsiArchitecture
    }
    Invoke-Msi '/i' $Msi
    Assert-Installed $productCode $Architecture $previousCode
    if ($SameVersionMsi) {
        Invoke-Msi '/i' $SameVersionMsi
        Assert-Installed $sameVersionCode $sameVersionArchitecture $productCode
        Invoke-Msi '/i' $Msi
        Assert-Installed $productCode $Architecture $sameVersionCode
    }
    foreach ($name in @('artex.exe', 'launch.vbs', 'config.example.json', 'LICENSE', 'CHANGELOG.md', 'README.txt', 'skills')) {
        if (-not (Test-Path (Join-Path $target $name))) { throw "Missing installed payload: $name" }
    }
    $repo = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
    if ((Get-FileHash (Join-Path $target 'config.example.json')).Hash -ne (Get-FileHash (Join-Path $repo 'config.example.json')).Hash) { throw 'Installed config example differs from source.' }
    $shortcut = Join-Path ([Environment]::GetFolderPath('Programs')) 'ARTEX/ARTEX.lnk'
    if (-not (Test-Path $shortcut)) { throw 'Start menu shortcut missing.' }
    $link = (New-Object -ComObject WScript.Shell).CreateShortcut($shortcut)
    if ($link.Arguments -ne "`"$target\launch.vbs`"") { throw "Shortcut arguments incorrect: $($link.Arguments)" }
    if ($PreviousMsi) {
        Invoke-Msi '/i' $PreviousMsi $true
        Assert-Installed $productCode $Architecture $previousCode
    }
    if (-not (Test-Path $sentinel)) { throw 'Upgrade erased user data.' }
    & python (Join-Path $repo 'scripts/native-smoke.py') (Join-Path $target 'artex.exe')
    if ($LASTEXITCODE -ne 0) { throw 'Installed native launcher failed its smoke check.' }
    Invoke-Msi '/x' $Msi
    if ($installer.ProductState($productCode) -ne -1) { throw 'Uninstall left the MSI product registered.' }
    if (Test-Path (Join-Path $target 'artex.exe')) { throw 'Uninstall left the executable.' }
    if (Test-Path $shortcut) { throw 'Uninstall left Start menu shortcut.' }
    if (-not (Test-Path $sentinel)) { throw 'Uninstall erased user data.' }
    Write-Output 'Install, payload, shortcuts, optional cross-architecture upgrade/downgrade and same-version switches, and uninstall checks passed.'
} finally {
    Remove-Item $sentinel -Force -ErrorAction SilentlyContinue
}
