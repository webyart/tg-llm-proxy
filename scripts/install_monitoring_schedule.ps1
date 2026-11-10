#Requires -Version 7
<#
install_monitoring_schedule.ps1 — preview or (re)register the hidden Windows container-health monitor.

conhost.exe --headless prevents a console window; pwsh also receives -WindowStyle Hidden. The action prefers the
stable MSI path C:\Program Files\PowerShell\7\pwsh.exe and falls back to the current executable (with a warning)
only when that path is absent. Task settings are explicit: StartWhenAvailable=True, WakeToRun=True,
DisallowStartIfOnBatteries=False, StopIfGoingOnBatteries=False. Existing tasks ARE updated (-Force), so an old
registration that pointed at the versioned WindowsApps\Microsoft.PowerShell_* path is replaced.

Default: inspect the bot every five minutes and alert through notify-telegram.ps1 on failure/recovery.
On macOS/Linux use cron or systemd instead.

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

. (Join-Path $PSScriptRoot 'ScheduledTaskHelpers.ps1')

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
if (-not (Get-Command -Name Register-ScheduledTask -ErrorAction SilentlyContinue)) {
    Write-Output 'install_monitoring_schedule: the ScheduledTasks PowerShell module is not available on this host'
    exit 1
}

$conhost = Join-Path $env:SystemRoot 'System32\conhost.exe'
if (-not (Test-Path -LiteralPath $conhost -PathType Leaf)) {
    Write-Output "install_monitoring_schedule: headless console host is missing: $conhost"
    exit 1
}

$pwshInfo = Get-TgPwshExecutable
if (-not $pwshInfo.Stable) {
    Write-Output "install_monitoring_schedule: WARNING — $($pwshInfo.Path) is the current Store/executable path, not C:\Program Files\PowerShell\7\pwsh.exe; a Store update can still move it. Install PowerShell 7 with the MSI to make the task path permanent."
}
else {
    Write-Output "install_monitoring_schedule: using stable PowerShell $($pwshInfo.Path)"
}

$actionArgs = "--headless `"$($pwshInfo.Path)`" -NoLogo -NoProfile -WindowStyle Hidden -File `"$monitor`""
$action = New-ScheduledTaskAction -Execute $conhost -Argument $actionArgs -WorkingDirectory $repoRoot
$trigger = New-ScheduledTaskTrigger -Once -At ((Get-Date).AddMinutes(1)) -RepetitionInterval (New-TimeSpan -Minutes $IntervalMinutes)
$settings = New-TgTaskSettings
$principal = New-TgTaskPrincipal

Write-Output "Task name: $TaskName"
Write-Output "Task action: `"$conhost`" $actionArgs"
Write-Output "Task trigger: every $IntervalMinutes minutes (indefinitely)"
Write-Output 'Task settings: StartWhenAvailable=True WakeToRun=True DisallowStartIfOnBatteries=False StopIfGoingOnBatteries=False'
if (-not $Register) {
    Write-Output 'Preview only: nothing was registered. Add -Register to create or update the task.'
    exit 0
}

Register-TgScheduledTask -TaskName $TaskName -Description 'tg-llm-proxy: container health, backup freshness, runtime error monitor' `
    -Action $action -Trigger $trigger -Settings $settings -Principal $principal
if (-not (Assert-TgTaskSettings -TaskName $TaskName)) { exit 1 }
$registered = Get-ScheduledTask -TaskName $TaskName
$registered | Select-Object TaskName, State | Format-List | Out-String | Write-Output
$registered.Actions | Select-Object Execute, Arguments, WorkingDirectory | Format-List | Out-String | Write-Output
$registered.Triggers | Select-Object StartBoundary, Repetition | Format-List | Out-String | Write-Output
Write-Output "install_monitoring_schedule: registered $TaskName every $IntervalMinutes minutes"
