#Requires -Version 7
<#
install_monitoring_schedule.ps1 — preview or register the Windows container-health monitor.

Default: inspect the bot every five minutes and alert through notify-telegram.ps1 on failure/recovery.
Existing tasks are never overwritten (/F is not used). On macOS/Linux use cron or systemd instead.

Usage:
  .\scripts\install_monitoring_schedule.ps1
  .\scripts\install_monitoring_schedule.ps1 -Register
  .\scripts\install_monitoring_schedule.ps1 -IntervalMinutes 10 -TaskName tg-llm-proxy-container-monitor -Register
#>

[CmdletBinding()]
param(
    [string]$TaskName = 'tg-llm-proxy-container-monitor',
    [ValidateRange(1, 1439)]
    [int]$IntervalMinutes = 5,
    [switch]$Register
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$monitor = Join-Path $repoRoot 'scripts\watch_container.ps1'
if (-not $IsWindows) {
    Write-Output 'install_monitoring_schedule: Task Scheduler exists only on Windows; on macOS/Linux register watch_container.ps1 through cron or systemd'
    exit 1
}
if (-not (Test-Path -LiteralPath $monitor -PathType Leaf)) {
    Write-Output "install_monitoring_schedule: $monitor is missing"
    exit 1
}
if (-not (Get-Command -Name schtasks -ErrorAction SilentlyContinue)) {
    Write-Output 'install_monitoring_schedule: schtasks is not available on this host'
    exit 1
}

$pwsh = (Get-Process -Id $PID).Path
$action = "`"$pwsh`" -NoLogo -NoProfile -File `"$monitor`""
$createArgs = @('/Create', '/TN', $TaskName, '/SC', 'MINUTE', '/MO', $IntervalMinutes, '/TR', $action)
Write-Output "Task action: $action"
Write-Output "Registration command: schtasks $($createArgs -join ' ')"
if (-not $Register) {
    Write-Output 'Preview only: nothing was registered. Add -Register to create the task.'
    exit 0
}
& schtasks @createArgs
if ($LASTEXITCODE -ne 0) {
    Write-Output "install_monitoring_schedule: schtasks /Create failed with exit $LASTEXITCODE"
    exit 1
}
Write-Output "install_monitoring_schedule: registered $TaskName every $IntervalMinutes minutes"
& schtasks /Query /TN $TaskName /FO LIST /V
if ($LASTEXITCODE -ne 0) {
    Write-Output 'install_monitoring_schedule: task was created but query failed'
    exit 1
}
