#Requires -Version 7
<#
install_maintenance_schedule.ps1 — preview or register the hidden daily Windows Task Scheduler entry for volume maintenance.

conhost.exe --headless prevents a console window; pwsh also receives -WindowStyle Hidden.
Default action: db_volume_maintenance.ps1 -Keep 7, with retention in dry-run mode, so no old backup is deleted.
-ExternalDirectory adds a verified external copy. -EncryptBackups encrypts it with AES-256-GCM. -ApplyRetention adds -Apply explicitly. Existing tasks are never
overwritten (/F is not used). Failure alerts use the existing Telegram token and allowlist.

Usage:
  .\scripts\install_maintenance_schedule.ps1
  .\scripts\install_maintenance_schedule.ps1 -Register
  .\scripts\install_maintenance_schedule.ps1 -ExternalDirectory E:\tg-llm-proxy-backups -Register
  .\scripts\install_maintenance_schedule.ps1 -Time 04:30 -TaskName tg-llm-proxy-maintenance -Register
  .\scripts\install_maintenance_schedule.ps1 -ApplyRetention -Register
#>

[CmdletBinding()]
param(
    [string]$TaskName = 'tg-llm-proxy-maintenance',
    [ValidatePattern('^([01][0-9]|2[0-3]):[0-5][0-9]$')]
    [string]$Time = '04:00',
    [string]$ExternalDirectory,
    [switch]$EncryptBackups,
    [switch]$ApplyRetention,
    [switch]$Register
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$maintenance = Join-Path $repoRoot 'scripts\db_volume_maintenance.ps1'
if (-not $IsWindows) {
    Write-Output 'install_maintenance_schedule: Task Scheduler exists only on Windows; on macOS/Linux register db_volume_maintenance.ps1 through cron or systemd'
    exit 1
}
if (-not (Test-Path -LiteralPath $maintenance -PathType Leaf)) {
    Write-Output "install_maintenance_schedule: $maintenance is missing"
    exit 1
}
if (-not (Get-Command -Name schtasks -ErrorAction SilentlyContinue)) {
    Write-Output 'install_maintenance_schedule: schtasks is not available on this host'
    exit 1
}

$conhost = Join-Path $env:SystemRoot 'System32\conhost.exe'
if (-not (Test-Path -LiteralPath $conhost -PathType Leaf)) {
    Write-Output "install_maintenance_schedule: headless console host is missing: $conhost"
    exit 1
}
$pwsh = (Get-Process -Id $PID).Path
$action = "`"$conhost`" --headless `"$pwsh`" -NoLogo -NoProfile -WindowStyle Hidden -File `"$maintenance`" -Keep 7"
if ($ExternalDirectory) {
    if (-not [System.IO.Path]::IsPathRooted($ExternalDirectory)) { $ExternalDirectory = Join-Path $repoRoot $ExternalDirectory }
    $ExternalDirectory = [System.IO.Path]::GetFullPath($ExternalDirectory)
    $action += " -ExternalDirectory `"$ExternalDirectory`""
}
if ($EncryptBackups) { $action += ' -Encrypt' }
if ($ApplyRetention) { $action += ' -Apply' }
$createArgs = @('/Create', '/TN', $TaskName, '/SC', 'DAILY', '/ST', $Time, '/TR', $action)

Write-Output "Task action: $action"
Write-Output "Registration command: schtasks $($createArgs -join ' ')"
if (-not $Register) {
    Write-Output 'Preview only: nothing was registered. Add -Register to create the task.'
    exit 0
}

& schtasks @createArgs
if ($LASTEXITCODE -ne 0) {
    Write-Output "install_maintenance_schedule: schtasks /Create failed with exit $LASTEXITCODE"
    exit 1
}
Write-Output "install_maintenance_schedule: registered $TaskName daily at $Time"
& schtasks /Query /TN $TaskName /FO LIST /V
if ($LASTEXITCODE -ne 0) {
    Write-Output 'install_maintenance_schedule: task was created but query failed'
    exit 1
}
