#Requires -Version 7
<#
install_maintenance_schedule.ps1 — preview or (re)register the hidden daily Windows Task Scheduler entry for
volume maintenance.

conhost.exe --headless prevents a console window; pwsh also receives -WindowStyle Hidden. The action prefers the
stable MSI path C:\Program Files\PowerShell\7\pwsh.exe and falls back to the current executable (with a warning)
only when that path is absent. Task settings are explicit: StartWhenAvailable=True, WakeToRun=True,
DisallowStartIfOnBatteries=False, StopIfGoingOnBatteries=False.

Default action: db_volume_maintenance.ps1 -Keep 7 -Apply. -Apply permits deletion of expired generated backups and
runs age-based retention in the same stopped-bot window; use -DryRunRetention to keep the old preview-only
behavior. -ExternalDirectory adds a verified external copy; the operator decided backups stay on this PC, so the
production task does not use it. Existing tasks ARE updated (-Force): the old schtasks registration pointed at a
versioned WindowsApps\Microsoft.PowerShell_* path that a Store update breaks.

Usage:
  .\scripts\install_maintenance_schedule.ps1
  .\scripts\install_maintenance_schedule.ps1 -Register
  .\scripts\install_maintenance_schedule.ps1 -ExternalDirectory E:\tg-llm-proxy-backups -Register
  .\scripts\install_maintenance_schedule.ps1 -Time 04:30 -TaskName tg-llm-proxy-maintenance -Register
  .\scripts\install_maintenance_schedule.ps1 -DryRunRetention -Register
#>

[CmdletBinding()]
param(
    [string]$TaskName = 'tg-llm-proxy-maintenance',
    [ValidatePattern('^([01][0-9]|2[0-3]):[0-5][0-9]$')]
    [string]$Time = '04:00',
    [string]$ExternalDirectory,
    [switch]$EncryptBackups,
    [switch]$ApplyRetention,
    [switch]$DryRunRetention,
    [switch]$Register
)

. (Join-Path $PSScriptRoot 'ScheduledTaskHelpers.ps1')

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
if (-not (Get-Command -Name Register-ScheduledTask -ErrorAction SilentlyContinue)) {
    Write-Output 'install_maintenance_schedule: the ScheduledTasks PowerShell module is not available on this host'
    exit 1
}

$conhost = Join-Path $env:SystemRoot 'System32\conhost.exe'
if (-not (Test-Path -LiteralPath $conhost -PathType Leaf)) {
    Write-Output "install_maintenance_schedule: headless console host is missing: $conhost"
    exit 1
}

$pwshInfo = Get-TgPwshExecutable
if (-not $pwshInfo.Stable) {
    Write-Output "install_maintenance_schedule: WARNING — $($pwshInfo.Path) is the current Store/executable path, not C:\Program Files\PowerShell\7\pwsh.exe; a Store update can still move it. Install PowerShell 7 with the MSI to make the task path permanent."
}
else {
    Write-Output "install_maintenance_schedule: using stable PowerShell $($pwshInfo.Path)"
}

$actionArgs = "--headless `"$($pwshInfo.Path)`" -NoLogo -NoProfile -WindowStyle Hidden -File `"$maintenance`" -Keep 7"
if (-not $DryRunRetention) { $actionArgs += ' -Apply' }
if ($ExternalDirectory) {
    if (-not [System.IO.Path]::IsPathRooted($ExternalDirectory)) { $ExternalDirectory = Join-Path $repoRoot $ExternalDirectory }
    $ExternalDirectory = [System.IO.Path]::GetFullPath($ExternalDirectory)
    $actionArgs += " -ExternalDirectory `"$ExternalDirectory`""
}
if ($EncryptBackups) { $actionArgs += ' -Encrypt' }

$action = New-ScheduledTaskAction -Execute $conhost -Argument $actionArgs -WorkingDirectory $repoRoot
$trigger = New-ScheduledTaskTrigger -Daily -At $Time
$settings = New-TgTaskSettings
$principal = New-TgTaskPrincipal

Write-Output "Task name: $TaskName"
Write-Output "Task action: `"$conhost`" $actionArgs"
Write-Output 'Task settings: StartWhenAvailable=True WakeToRun=True DisallowStartIfOnBatteries=False StopIfGoingOnBatteries=False'
if (-not $Register) {
    Write-Output 'Preview only: nothing was registered. Add -Register to create or update the task.'
    exit 0
}

Register-TgScheduledTask -TaskName $TaskName -Description 'tg-llm-proxy: verified local SQLite backup, retention and bot restart' `
    -Action $action -Trigger $trigger -Settings $settings -Principal $principal
if (-not (Assert-TgTaskSettings -TaskName $TaskName)) { exit 1 }
$registered = Get-ScheduledTask -TaskName $TaskName
$registered | Select-Object TaskName, State | Format-List | Out-String | Write-Output
$registered.Actions | Select-Object Execute, Arguments, WorkingDirectory | Format-List | Out-String | Write-Output
$registered.Triggers | Select-Object StartBoundary, DaysInterval | Format-List | Out-String | Write-Output
Write-Output "install_maintenance_schedule: registered $TaskName daily at $Time"
