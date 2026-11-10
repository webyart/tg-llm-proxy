#Requires -Version 7
<#
ScheduledTaskHelpers.ps1 — shared registration pieces for the two Windows Task Scheduler entries.

The PowerShell path is the stability boundary: an MSI-installed PowerShell 7 lives at the version-independent
C:\Program Files\PowerShell\7\pwsh.exe, while the Store build lives under a versioned WindowsApps directory that
disappears when the Store updates it. The installer prefers the stable path and only falls back to the executable
that is running the installer when the stable one is absent (it prints a warning, because a later Store update can
still break that fallback). Task settings are set explicitly instead of inheriting schtasks defaults:
StartWhenAvailable (run a missed 04:00 after sleep/downtime), WakeToRun, battery start allowed and battery stop
disallowed (stopping a half-finished maintenance run would leave the bot down).
#>

function Get-TgPwshExecutable {
    $stable = Join-Path $env:ProgramFiles 'PowerShell\7\pwsh.exe'
    if (Test-Path -LiteralPath $stable -PathType Leaf) {
        return [pscustomobject]@{ Path = $stable; Stable = $true }
    }
    return [pscustomobject]@{ Path = ((Get-Process -Id $PID).Path); Stable = $false }
}

function New-TgTaskSettings {
    return New-ScheduledTaskSettingsSet `
        -StartWhenAvailable `
        -WakeToRun `
        -AllowStartIfOnBatteries `
        -DontStopIfGoingOnBatteries
}

function New-TgTaskPrincipal {
    return New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
}

function Register-TgScheduledTask {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$TaskName,
        [Parameter(Mandatory)][string]$Description,
        [Parameter(Mandatory)][object]$Action,
        [Parameter(Mandatory)][object]$Trigger,
        [Parameter(Mandatory)][object]$Settings,
        [Parameter(Mandatory)][object]$Principal
    )
    # -Force is intentional: the installer owns these two task names and must bring an existing registration
    # (created earlier by schtasks with the old WindowsApps path) up to the current script.
    Register-ScheduledTask -TaskName $TaskName -Description $Description -Action $Action -Trigger $Trigger `
        -Settings $Settings -Principal $Principal -Force | Out-Null
}

function Assert-TgTaskSettings {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$TaskName)
    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction Stop
    $problems = @()
    if (-not $task.Settings.StartWhenAvailable) { $problems += 'StartWhenAvailable is not True' }
    if (-not $task.Settings.WakeToRun) { $problems += 'WakeToRun is not True' }
    if ($task.Settings.DisallowStartIfOnBatteries) { $problems += 'DisallowStartIfOnBatteries is not False' }
    if ($task.Settings.StopIfGoingOnBatteries) { $problems += 'StopIfGoingOnBatteries is not False' }
    if ($problems.Count -gt 0) {
        Write-Output "schedule: FAIL — $TaskName : $($problems -join '; ')"
        return $false
    }
    Write-Output "schedule: $TaskName settings StartWhenAvailable=True WakeToRun=True DisallowStartIfOnBatteries=False StopIfGoingOnBatteries=False"
    return $true
}
