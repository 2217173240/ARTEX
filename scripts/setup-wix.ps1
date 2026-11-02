[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Destination
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# WiX 4.0.6 使用 MS-RL，早于维护费用政策；固定并校验官方 NuGet 包。
$packageSha256 = 'a94dd42ae1fb56b32da180e2173ceda4f0d10b4c8871c5ee59ecb502131a1eb6'
$toolVersion = '4.0.6'
$destinationPath = [IO.Path]::GetFullPath($Destination)
$previousRollForward = $env:DOTNET_ROLL_FORWARD
$work = Join-Path ([IO.Path]::GetTempPath()) ('artex-wix-' + [guid]::NewGuid().ToString('N'))

try {
    if (Test-Path -LiteralPath $destinationPath -PathType Leaf) { throw "WiX destination is a file: $destinationPath" }
    if (Test-Path -LiteralPath $destinationPath) {
        $existing = @(Get-ChildItem -LiteralPath $destinationPath -Force)
        if ($existing.Count -ne 0) { throw "WiX destination must be empty: $destinationPath" }
    } else {
        New-Item -ItemType Directory -Path $destinationPath | Out-Null
    }

    New-Item -ItemType Directory -Path $work | Out-Null
    $feed = Join-Path $work 'feed'
    New-Item -ItemType Directory -Path $feed | Out-Null
    $package = Join-Path $feed 'wix.4.0.6.nupkg'
    Invoke-WebRequest 'https://api.nuget.org/v3-flatcontainer/wix/4.0.6/wix.4.0.6.nupkg' -OutFile $package
    if ((Get-FileHash $package -Algorithm SHA256).Hash.ToLowerInvariant() -ne $packageSha256) { throw 'WiX package checksum mismatch.' }
    $nugetConfig = Join-Path $work 'NuGet.Config'
    Set-Content $nugetConfig '<configuration><packageSources><clear /></packageSources></configuration>' -Encoding UTF8

    $env:DOTNET_ROLL_FORWARD = 'Major'
    & dotnet tool install wix --version $toolVersion --tool-path $destinationPath --configfile $nugetConfig --add-source $feed | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'WiX tool installation failed.' }
    $tool = Join-Path $destinationPath 'wix.exe'
    if (-not (Test-Path -LiteralPath $tool -PathType Leaf)) { throw "WiX executable was not installed: $tool" }
    $versionOutput = (& $tool --version | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $versionOutput -notmatch '^4\.0\.6(?:\+.*)?$') { throw "Unexpected WiX version: $versionOutput" }
    Write-Output ([IO.Path]::GetFullPath($tool))
} finally {
    $env:DOTNET_ROLL_FORWARD = $previousRollForward
    if (Test-Path -LiteralPath $work) { Remove-Item $work -Recurse -Force }
}
