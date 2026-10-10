# Execute on a disposable Windows runner, under the installing user's identity.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Msi,
    [Parameter(Mandatory)][ValidateSet('amd64', 'arm64')][string]$Architecture,
    [string]$PreviousMsi,
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
function Read-Property([string]$name) {
    $view = $db.OpenView("SELECT ``Value`` FROM ``Property`` WHERE ``Property`` = '$name'")
    $view.Execute()
    $record = $view.Fetch()
    if ($null -eq $record) { return '' }
    return $record.StringData(1)
}
if ((Read-Property 'ALLUSERS') -ne '') { throw 'Installer must be per-user.' }
if ($MetadataOnly) { Write-Output "Verified $template per-user MSI metadata."; return }
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
try {
    if ($PreviousMsi) { Invoke-Msi '/i' (Resolve-Path $PreviousMsi).Path }
    Invoke-Msi '/i' $Msi
    foreach ($name in @('artex.exe', 'launch.vbs', 'config.example.json', 'LICENSE', 'CHANGELOG.md', 'README.txt', 'skills')) {
        if (-not (Test-Path (Join-Path $target $name))) { throw "Missing installed payload: $name" }
    }
    $repo = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
    if ((Get-FileHash (Join-Path $target 'config.example.json')).Hash -ne (Get-FileHash (Join-Path $repo 'config.example.json')).Hash) { throw 'Installed config example differs from source.' }
    $shortcut = Join-Path ([Environment]::GetFolderPath('Programs')) 'ARTEX/ARTEX.lnk'
    if (-not (Test-Path $shortcut)) { throw 'Start menu shortcut missing.' }
    $link = (New-Object -ComObject WScript.Shell).CreateShortcut($shortcut)
    if ($link.Arguments -ne "`"$target\launch.vbs`"") { throw "Shortcut arguments incorrect: $($link.Arguments)" }
    if ($PreviousMsi) { Invoke-Msi '/i' (Resolve-Path $PreviousMsi).Path $true }
    if (-not (Test-Path $sentinel)) { throw 'Upgrade erased user data.' }
    & python (Join-Path $repo 'scripts/native-smoke.py') (Join-Path $target 'artex.exe')
    if ($LASTEXITCODE -ne 0) { throw 'Installed native launcher failed its smoke check.' }
    Invoke-Msi '/x' $Msi
    if (Test-Path (Join-Path $target 'artex.exe')) { throw 'Uninstall left the executable.' }
    if (Test-Path $shortcut) { throw 'Uninstall left Start menu shortcut.' }
    if (-not (Test-Path $sentinel)) { throw 'Uninstall erased user data.' }
    Write-Output 'Install, payload, shortcuts, optional upgrade/downgrade, and uninstall checks passed.'
} finally {
    Remove-Item $sentinel -Force -ErrorAction SilentlyContinue
}
