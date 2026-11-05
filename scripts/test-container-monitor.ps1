#Requires -Version 7
<#
test-container-monitor.ps1 — isolated Docker acceptance for health, maintenance lease and backup freshness monitoring.

Covers failure, suppression and recovery plus fresh/stale/dead-PID leases and legacy/receipt backup age checks.
No alerts leave the host. The test refuses to run over an existing production maintenance lease.
#>

[CmdletBinding()]
param()

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$work = Join-Path $repoRoot 'zzscratch\container-monitor-acceptance'
$maintenanceWork = Join-Path $repoRoot 'zzscratch\volume-maintenance'
$maintenanceLease = Join-Path $maintenanceWork 'lease.json'
$state = Join-Path $work 'state.json'
$backups = Join-Path $work 'backups'
$verification = Join-Path $work 'backup-verified.json'
$backupState = Join-Path $work 'backup-alert.json'
$backup = Join-Path $backups 'bot-20000101-000000.db'
$name = "tg-llm-proxy-monitor-fixture-$([Guid]::NewGuid().ToString('N'))"
$failure = $null

function Write-Lease([datetime]$StartedAt, [int]$LeasePid) {
    [void](New-Item -ItemType Directory -Path $maintenanceWork)
    $lease = [pscustomobject]@{ started_at = $StartedAt.ToUniversalTime().ToString('o'); pid = $LeasePid }
    [System.IO.File]::WriteAllText($maintenanceLease, ($lease | ConvertTo-Json), [System.Text.UTF8Encoding]::new($false))
}

function Write-Verification([datetime]$VerifiedAt) {
    $receipt = [pscustomobject]@{ path = $backup; verified_at = $VerifiedAt.ToUniversalTime().ToString('o') }
    [System.IO.File]::WriteAllText($verification, ($receipt | ConvertTo-Json), [System.Text.UTF8Encoding]::new($false))
}

function Remove-TestLease {
    if (Test-Path -LiteralPath $maintenanceWork) { Remove-Item -LiteralPath $maintenanceWork -Recurse -Force }
}

Push-Location $repoRoot
try {
    if (Test-Path -LiteralPath $work) { throw "monitor acceptance directory already exists: $work" }
    if (Test-Path -LiteralPath $maintenanceWork) { throw 'test refused to replace an existing production maintenance lease' }
    [void](New-Item -ItemType Directory -Path $backups -Force)
    [System.IO.File]::WriteAllText($backup, 'verified backup fixture')
    (Get-Item -LiteralPath $backup).LastWriteTimeUtc = [DateTime]::UtcNow
    $monitorArgs = @(
        '-BackupDirectory', $backups,
        '-BackupVerificationPath', $verification,
        '-BackupStatePath', $backupState,
        '-NoNotify'
    )

    Write-Lease ([DateTime]::UtcNow) $PID
    $freshLease = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -ContainerId 'not-inspected' -StatePath $state @monitorArgs
    if ($LASTEXITCODE -ne 0 -or ($freshLease | Out-String) -notmatch 'fresh active production backup maintenance lease') {
        throw 'fresh active lease did not skip monitoring successfully'
    }
    if (-not (Test-Path -LiteralPath $maintenanceLease -PathType Leaf)) { throw 'fresh active lease was removed' }
    Remove-TestLease
    Write-Output 'test-container-monitor: fresh active lease skips checks and remains intact'

    docker create --name $name tg-llm-proxy-bot:latest | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'docker create fixture failed' }

    Write-Lease ([DateTime]::UtcNow.AddHours(-4)) $PID
    $staleLease = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -ContainerId $name -StatePath $state @monitorArgs
    if ($LASTEXITCODE -ne 1 -or ($staleLease | Out-String) -notmatch 'stale maintenance lease removed') {
        throw 'expired lease was not cleaned and alerted'
    }
    if (($staleLease | Out-String) -notmatch 'stale maintenance lease alert sent') { throw 'expired lease alert was not sent' }
    if (Test-Path -LiteralPath $maintenanceWork) { throw 'expired lease directory survived cleanup' }

    Write-Lease ([DateTime]::UtcNow) 999999
    $rebootLease = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -ContainerId $name -StatePath $state @monitorArgs
    if ($LASTEXITCODE -ne 1 -or ($rebootLease | Out-String) -notmatch 'lease PID 999999 is not running') {
        throw 'dead-PID lease left behind by a reboot was not cleaned'
    }
    if (Test-Path -LiteralPath $maintenanceWork) { throw 'dead-PID lease directory survived cleanup' }
    Write-Output 'test-container-monitor: stale and dead-PID leases are removed and alerted'

    $first = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -ContainerId $name -StatePath $state @monitorArgs
    if ($LASTEXITCODE -ne 1) { throw 'created fixture must produce monitor exit 1' }
    if (($first | Out-String) -notmatch 'state=created') { throw 'monitor did not report the created fixture state' }
    $second = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -ContainerId $name -StatePath $state @monitorArgs
    if ($LASTEXITCODE -ne 1) { throw 'repeated failure must still produce monitor exit 1' }
    if (($second | Out-String) -notmatch 'repeated alert suppressed') { throw 'repeated failure was not suppressed' }

    $unverified = Join-Path $backups 'bot-29990101-000000.db'
    [System.IO.File]::WriteAllText($unverified, 'newer unverified leftover fixture')
    (Get-Item -LiteralPath $unverified).LastWriteTimeUtc = [DateTime]::UtcNow
    (Get-Item -LiteralPath $backup).LastWriteTimeUtc = [DateTime]::UtcNow
    Write-Verification ([DateTime]::UtcNow.AddHours(-48))
    $oldBackup = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -ContainerId $name -StatePath $state @monitorArgs
    if ($LASTEXITCODE -ne 1 -or ($oldBackup | Out-String) -notmatch 'backup alert sent — status=stale') {
        throw 'old verified backup did not cross the freshness threshold'
    }
    if (($oldBackup | Out-String) -notmatch 'source=receipt') { throw 'newer unverified file displaced the verification receipt' }
    $oldBackupAgain = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -ContainerId $name -StatePath $state @monitorArgs
    if ($LASTEXITCODE -ne 1 -or ($oldBackupAgain | Out-String) -notmatch 'backup alert suppressed — status=stale') {
        throw 'repeated stale-backup alert was not suppressed'
    }
    Write-Verification ([DateTime]::UtcNow)
    $freshBackup = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -ContainerId $name -StatePath $state @monitorArgs
    if ($LASTEXITCODE -ne 1 -or ($freshBackup | Out-String) -notmatch 'backup OK') { throw 'fresh verification receipt did not restore backup health' }
    if (($freshBackup | Out-String) -notmatch 'backup recovery notification sent') { throw 'backup recovery was not recognized' }
    Write-Output 'test-container-monitor: backup age threshold, suppression and recovery verified with no notifications'

    Remove-Item -LiteralPath $verification -Force
    (Get-Item -LiteralPath $backup).LastWriteTimeUtc = [DateTime]::UtcNow
    Remove-Item -LiteralPath $unverified -Force
    $legacyFresh = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -ContainerId $name -StatePath $state @monitorArgs
    if ($LASTEXITCODE -ne 1 -or ($legacyFresh | Out-String) -notmatch 'backup OK.*source=legacy') {
        throw 'legacy timestamped backup fallback did not remain supported'
    }

    $recovery = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -StatePath $state @monitorArgs
    if ($LASTEXITCODE -ne 0) { throw 'healthy production container must produce monitor exit 0' }
    if (($recovery | Out-String) -notmatch 'recovered state=healthy') { throw 'failure-to-healthy transition was not recognized' }

    $whisperState = Join-Path $work 'whisper-state.json'
    $whisperFailure = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -WhisperContainerId $name -StatePath $whisperState @monitorArgs
    if ($LASTEXITCODE -ne 1) { throw 'created Whisper fixture must produce monitor exit 1' }
    if (($whisperFailure | Out-String) -notmatch 'state=whisper-created') { throw 'Whisper failure was not attributed to its service' }
    Write-Output 'test-container-monitor: GREEN — health, lease, backup freshness and suppression verified; no notifications sent'
}
catch {
    $failure = $_
    Write-Output "test-container-monitor: FAIL — $($_.Exception.Message)"
}
finally {
    docker rm --force $name 2>&1 | Out-Null
    Remove-TestLease
    if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force }
    Pop-Location
}
if ($failure) { exit 1 }
exit 0
