#Requires -Version 7
<#
db_volume_maintenance.ps1 — clean-stop production snapshot, verified local backup, in-volume retention, restart.

The production volume is a NAMED Docker volume, so the only honest way to copy it is with the container stopped.
This run therefore:
  1. stops the bot and WAITS until Docker reports `exited` (docker compose stop has a 60 s timeout and the compose
     file carries `stop_grace_period: 60s`, above the measured ~11 s shutdown budget);
  2. copies EVERYTHING under /data (bot.db, bot.db-wal, bot.db-shm, if present) to the lease directory with
     `docker compose cp`, publishes the copy by directory rename, and compares every copied file with the size
     Docker reports for the volume — a non-empty -wal that was not copied fails the run loudly;
  3. verifies the snapshot read-only with db_inspect.ps1 -Counts (integrity_check, schema_version, WAL side file,
     counters) and requires schema_version to equal the head of internal/store/migrations;
  4. creates bot-YYYYMMDD-HHMMSS.db from that snapshot with VACUUM INTO (db_maintenance.ps1), so the WAL frames are
     folded into ONE self-contained file that is published by rename and re-verified; the counters of the backup
     must equal the counters of the stopped-volume snapshot;
  5. while the bot is still stopped, applies age-based retention with cmd/dbprune through db_prune.ps1
     -DockerService bot: the seven classes prune the live volume file under SQLite's own transaction, the WAL is
     checkpointed and VACUUM runs with no writer; the pre-prune backup is copied out and rotated by count/age;
  6. restarts through docker-up.ps1 -NoBuild and waits for /healthz.

After the verified backup, data/backup-status.prom is atomically replaced with backup_last_success_unixtime,
backup_last_size_bytes, backup_last_duration_seconds and backup_last_ok (0 on a later failure, preserving the last
successful numbers). data/monitoring/backup-verified.json remains the freshness receipt for watch_container.ps1.

Failures are loud (clear log line + exit 1 + Telegram alert unless -NoNotify); a restart is always attempted if the
bot was stopped. Retention deletes only generated backup names; -Apply is required for any deletion. The operator
decided backups stay on this PC, so -ExternalDirectory/-Encrypt remain optional and are NOT used by the schedule.

The work directory is an atomic lease. Its lease.json is written before the directory is moved into place and contains
a unique lease ID, UTC start time and PowerShell PID, allowing watch_container.ps1 to distinguish active maintenance
from an abandoned lease. Cleanup removes the directory only when its lease ID still matches this run.

Usage:
  .\scripts\db_volume_maintenance.ps1
  .\scripts\db_volume_maintenance.ps1 -Keep 14 -Apply
  .\scripts\db_volume_maintenance.ps1 -Apply                  # also applies bot-* and pre-prune-* rotation
  .\scripts\db_volume_maintenance.ps1 -SkipPrune              # snapshot/backup only
  .\scripts\db_volume_maintenance.ps1 -NoNotify

Exit: 0 — the bot was restarted and every requested backup/snapshot/prune check passed; 1 — any stage failed.
#>

[CmdletBinding()]
param(
    [ValidateRange(1, 1000000)]
    [int]$Keep = 7,
    [string]$ExternalDirectory,
    [switch]$Encrypt,
    [string]$EncryptionKeyPath,
    [switch]$Apply,
    [switch]$Rebuild,
    [switch]$NoNotify,
    [switch]$SkipPrune,
    [ValidateRange(1, 1000)]
    [int]$PrePruneKeep = 7,
    [ValidateRange(1, 3650)]
    [int]$PrePruneMaxAgeDays = 30,
    [ValidateRange(10, 600)]
    [int]$StopTimeoutSeconds = 60,
    # Test-only override: scripts/test-maintenance.ps1 exercises the failure paths with a fake docker and must
    # not flip the production telemetry to backup_last_ok=0 while doing so.
    [string]$StatusPath
)

. (Join-Path $PSScriptRoot 'BackupStatus.ps1')
. (Join-Path $PSScriptRoot 'MaintenancePaths.ps1')

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$work = Join-Path $repoRoot 'zzscratch\volume-maintenance'
$snapshot = Join-Path $work 'snapshot'
$source = Join-Path $snapshot 'bot.db'
$backupDirectory = Join-Path $repoRoot 'data\backups'
# Not $statusPath: PowerShell variable names are case-insensitive, and that would silently overwrite the
# -StatusPath parameter used by the isolated acceptance tests.
$statusFile = Join-Path $repoRoot 'data\backup-status.prom'
if ($StatusPath) {
    $statusFile = if ([System.IO.Path]::IsPathRooted($StatusPath)) { $StatusPath } else { Join-Path $repoRoot $StatusPath }
}
$verificationPath = Join-Path $repoRoot 'data\monitoring\backup-verified.json'
$leaseStaging = $null
$leaseId = [Guid]::NewGuid().ToString('N')
$stopped = $false
$restarted = $false
$restartFailure = $false
$failure = $null
$backupSize = [long]0
$backupDuration = [double]0
$backupSchema = 0
$walBytes = [long]0

function Wait-BotContainerStopped {
    param([Parameter(Mandatory)][int]$TimeoutSeconds)
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $lastState = 'unknown'
    while ([DateTime]::UtcNow -lt $deadline) {
        $cid = (& docker compose ps -a -q bot 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) { throw 'docker compose ps failed while waiting for the bot to stop' }
        if (-not $cid) { throw 'the bot container disappeared while waiting for it to stop; refusing to copy the volume' }
        $lastState = (& docker inspect --format '{{.State.Status}}' $cid 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) { throw 'docker inspect failed while waiting for the bot to stop' }
        if ($lastState -in @('exited', 'created', 'dead')) { return $lastState }
        Start-Sleep -Seconds 1
    }
    throw "the bot container did not reach a stopped state within $TimeoutSeconds s (last state: $lastState)"
}

function Get-VolumeDbFileSizes {
    $output = (& docker compose run --rm -T --no-deps --entrypoint /bin/sh bot -c 'stat -c "%n %s" /data/bot.db* 2>/dev/null || true' 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0) { throw 'could not stat the database files inside the bot volume' }
    $sizes = @{}
    foreach ($line in ($output -split "`r?`n")) {
        if ($line -match '^\s*(/data/bot\.db(?:-wal|-shm)?)\s+([0-9]+)\s*$') {
            $sizes[$matches[1]] = [long]$matches[2]
        }
    }
    if (-not $sizes.ContainsKey('/data/bot.db')) { throw 'the bot volume does not contain /data/bot.db' }
    return $sizes
}

function Assert-SnapshotMatchesVolume([hashtable]$VolumeSizes) {
    foreach ($remotePath in $VolumeSizes.Keys) {
        $leaf = Split-Path -Leaf $remotePath
        $localPath = Join-Path $snapshot $leaf
        $remoteSize = [long]$VolumeSizes[$remotePath]
        if (-not (Test-Path -LiteralPath $localPath -PathType Leaf)) {
            throw "the stopped volume contains $remotePath ($remoteSize bytes) but the snapshot is missing it"
        }
        $localSize = (Get-Item -LiteralPath $localPath).Length
        if ($localSize -ne $remoteSize) {
            throw "snapshot copy of $remotePath is $localSize bytes but the volume file is $remoteSize bytes — refusing a partial snapshot"
        }
    }
    $remoteWal = if ($VolumeSizes.ContainsKey('/data/bot.db-wal')) { [long]$VolumeSizes['/data/bot.db-wal'] } else { [long]0 }
    $localWal = Join-Path $snapshot 'bot.db-wal'
    if ($remoteWal -gt 0) {
        if (-not (Test-Path -LiteralPath $localWal -PathType Leaf)) {
            throw "the stopped volume has a non-empty bot.db-wal ($remoteWal bytes) that is not in the snapshot"
        }
        if ((Get-Item -LiteralPath $localWal).Length -ne $remoteWal) {
            throw 'the copied bot.db-wal size does not match the volume — the WAL was not copied faithfully'
        }
    }
    return $remoteWal
}

function Get-DbCounts {
    param([Parameter(Mandatory)][string]$Path)
    $output = (& pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_inspect.ps1') -Path $Path -Counts 2>&1 | Out-String)
    $code = $LASTEXITCODE
    if ($code -ne 0) { throw "db_inspect failed for $Path (exit $code): $($output.Trim())" }
    $counts = @{}
    foreach ($line in ($output -split "`r?`n")) {
        if ($line -match '^([a-z0-9_]+)=(.*)$') { $counts[$matches[1]] = $matches[2] }
    }
    if (-not $counts.ContainsKey('integrity') -or $counts['integrity'] -ne 'ok') {
        throw "db_inspect did not report integrity=ok for $Path"
    }
    return $counts
}

function Get-ExpectedSchemaVersion {
    $migrationRoot = Join-Path $repoRoot 'internal\store\migrations'
    $version = 0
    foreach ($file in Get-ChildItem -LiteralPath $migrationRoot -File -Filter '*.sql') {
        if ($file.Name -match '^([0-9]+)_') {
            $number = [int]$matches[1]
            if ($number -gt $version) { $version = $number }
        }
    }
    if ($version -le 0) { throw 'could not read the compiled schema version from internal\store\migrations' }
    return $version
}

function Assert-SameDatabaseCounts {
    param(
        [Parameter(Mandatory)][hashtable]$SourceCounts,
        [Parameter(Mandatory)][hashtable]$BackupCounts,
        [Parameter(Mandatory)][string]$Label
    )
    $keys = @('integrity', 'schema_version', 'last_update_id', 'bot_state_offset_found')
    $keys += @($SourceCounts.Keys | Where-Object { $_ -match '^(table_|events_)' })
    $differences = @()
    foreach ($key in ($keys | Sort-Object -Unique)) {
        $expected = if ($SourceCounts.ContainsKey($key)) { $SourceCounts[$key] } else { '<missing>' }
        $actual = if ($BackupCounts.ContainsKey($key)) { $BackupCounts[$key] } else { '<missing>' }
        if ($expected -ne $actual) { $differences += "$key snapshot=$expected backup=$actual" }
    }
    if ($differences.Count -gt 0) {
        throw "$Label does not match the stopped-volume snapshot: $($differences -join '; ')"
    }
}

Push-Location $repoRoot
try {
    if (Test-Path -LiteralPath $work) { throw "work directory $work already exists — another run may be in progress or its lease is stale" }
    if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) { throw 'docker is not on PATH' }
    try {
        $leaseStaging = Join-Path $repoRoot "zzscratch\volume-maintenance.$PID.$([Guid]::NewGuid().ToString('N')).tmp"
        [void](New-Item -ItemType Directory -Path $leaseStaging)
        $lease = [ordered]@{
            lease_id = $leaseId
            started_at = [DateTime]::UtcNow.ToString('o')
            pid = $PID
        }
        [System.IO.File]::WriteAllText(
            (Join-Path $leaseStaging 'lease.json'),
            ([pscustomobject]$lease | ConvertTo-Json),
            [System.Text.UTF8Encoding]::new($false)
        )
        try {
            [System.IO.Directory]::Move($leaseStaging, $work)
            $leaseStaging = $null
        }
        catch {
            throw "could not acquire maintenance lease $work — another run may be in progress: $($_.Exception.Message)"
        }

        # --- 1. clean stop, then wait for Docker's own state -----------------------------
        docker compose stop -t $StopTimeoutSeconds bot
        # Mark the bot as potentially stopped BEFORE the exit-code check: even a failed stop may have taken it
        # down, and the finally block's idempotent docker-up is the safe response.
        $stopped = $true
        if ($LASTEXITCODE -ne 0) { throw 'docker compose stop bot failed' }
        $state = Wait-BotContainerStopped -TimeoutSeconds $StopTimeoutSeconds
        Write-Output "db_volume_maintenance: bot container state=$state; copying the stopped volume"

        # --- 2. copy /data atomically and prove it matches the volume -------------------
        $volumeSizes = Get-VolumeDbFileSizes
        $snapshotTmp = Join-Path $work "snapshot.$([Guid]::NewGuid().ToString('N')).tmp"
        try {
            [void](New-Item -ItemType Directory -Path $snapshotTmp)
            & docker compose cp 'bot:/data/.' $snapshotTmp
            if ($LASTEXITCODE -ne 0) { throw 'docker compose cp of /data failed' }
            [System.IO.Directory]::Move($snapshotTmp, $snapshot)
        }
        finally {
            if (Test-Path -LiteralPath $snapshotTmp) { Remove-Item -LiteralPath $snapshotTmp -Recurse -Force -ErrorAction SilentlyContinue }
        }
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) { throw "the snapshot does not contain bot.db: $source" }
        $walBytes = Assert-SnapshotMatchesVolume -VolumeSizes $volumeSizes
        if ($walBytes -gt 0) {
            Write-Output "db_volume_maintenance: snapshot includes bot.db-wal ($walBytes bytes); it will be folded into the backup by VACUUM INTO"
        }

        # --- 3. verify the stopped-volume snapshot ---------------------------------------
        $sourceCounts = Get-DbCounts -Path $source
        if ($walBytes -gt 0 -and $sourceCounts['wal_side_file'] -ne 'present') {
            throw "the snapshot has a non-empty bot.db-wal but db_inspect did not open it (wal_side_file=$($sourceCounts['wal_side_file']))"
        }
        $expectedSchema = Get-ExpectedSchemaVersion
        $sourceSchema = [int]$sourceCounts['schema_version']
        if ($sourceSchema -ne $expectedSchema) {
            throw "the production database is schema_version=$sourceSchema but this checkout compiles $expectedSchema — rebuild/restart the bot before pruning, so the retained backup and the running binary agree"
        }
        Write-Output "db_volume_maintenance: snapshot integrity=ok schema_version=$sourceSchema wal_side_file=$($sourceCounts['wal_side_file'])"

        # --- 4. one verified, atomically published backup --------------------------------
        $backupStageStart = [DateTime]::UtcNow
        $maintenanceArgs = @('-Source', $source, '-Directory', $backupDirectory, '-Keep', $Keep)
        if ($ExternalDirectory) { $maintenanceArgs += @('-ExternalDirectory', $ExternalDirectory) }
        if ($Encrypt) { $maintenanceArgs += '-Encrypt' }
        if ($EncryptionKeyPath) { $maintenanceArgs += @('-EncryptionKeyPath', $EncryptionKeyPath) }
        if ($Apply) { $maintenanceArgs += '-Apply' }
        if ($Rebuild) { $maintenanceArgs += '-Rebuild' }
        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_maintenance.ps1') @maintenanceArgs
        if ($LASTEXITCODE -ne 0) { throw 'verified backup, retention, or external copy failed' }

        $newestBackup = Get-ChildItem -LiteralPath $backupDirectory -File -Filter 'bot-*.db' |
            Where-Object { $_.Name -match '^bot-[0-9]{8}-[0-9]{6}\.db$' } |
            Sort-Object -Property LastWriteTimeUtc -Descending |
            Select-Object -First 1
        if (-not $newestBackup) { throw 'verified backup completed but no timestamped local backup was found' }

        $backupCounts = Get-DbCounts -Path $newestBackup.FullName
        Assert-SameDatabaseCounts -SourceCounts $sourceCounts -BackupCounts $backupCounts -Label 'the verified backup'
        $backupSchema = [int]$backupCounts['schema_version']
        $backupSize = [long]$newestBackup.Length
        $backupDuration = ([DateTime]::UtcNow - $backupStageStart).TotalSeconds
        $backupVerifiedAt = [DateTime]::UtcNow
        if ($backupSize -le 0) { throw "the verified backup has no bytes: $($newestBackup.FullName)" }

        Write-BackupStatus -Path $statusFile -Ok $true -SuccessUtc $backupVerifiedAt -SizeBytes $backupSize -DurationSeconds $backupDuration
        Write-Output "db_volume_maintenance: verified backup $($newestBackup.Name) ($backupSize bytes, schema_version=$backupSchema, wal_bytes=$walBytes)"

        $verificationTemp = "$verificationPath.$PID.$([Guid]::NewGuid().ToString('N')).tmp"
        try {
            [void](New-Item -ItemType Directory -Path (Split-Path -Parent $verificationPath) -Force)
            $verification = [ordered]@{
                path = $newestBackup.FullName
                verified_at = $backupVerifiedAt.ToString('o')
                size_bytes = $backupSize
                schema_version = $backupSchema
                wal_bytes = $walBytes
                duration_seconds = [Math]::Round($backupDuration, 3)
            }
            [System.IO.File]::WriteAllText(
                $verificationTemp,
                ([pscustomobject]$verification | ConvertTo-Json),
                [System.Text.UTF8Encoding]::new($false)
            )
            [System.IO.File]::Move($verificationTemp, $verificationPath, $true)
        }
        finally {
            if (Test-Path -LiteralPath $verificationTemp) { Remove-Item -LiteralPath $verificationTemp -Force -ErrorAction SilentlyContinue }
        }

        # --- 5. retention while the bot is still stopped --------------------------------
        if (-not $SkipPrune) {
            $pruneArgs = @(
                '-DockerService', 'bot',
                '-BackupDirectory', $backupDirectory,
                '-PrePruneKeep', $PrePruneKeep,
                '-PrePruneMaxAgeDays', $PrePruneMaxAgeDays
            )
            if ($Apply) { $pruneArgs += '-Apply' }
            & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_prune.ps1') @pruneArgs
            if ($LASTEXITCODE -ne 0) { throw 'production retention (db_prune -DockerService) failed' }
        }
        else {
            Write-Output 'db_volume_maintenance: -SkipPrune — age-based retention was not run'
        }

        # --- 6. restart and wait for health ---------------------------------------------
        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'docker-up.ps1') -NoBuild
        if ($LASTEXITCODE -ne 0) { throw 'bot restart after snapshot failed' }
        $restarted = $true
        Write-Output 'db_volume_maintenance: OK — verified local backup, retention applied, bot is running'
    }
    finally {
        if ($stopped -and -not $restarted) {
            Write-Output 'db_volume_maintenance: restarting the bot after a failed run'
            & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'docker-up.ps1') -NoBuild
            if ($LASTEXITCODE -ne 0) {
                $restartFailure = $true
                Write-Output 'db_volume_maintenance: FAIL — the bot did not come back after maintenance; inspect docker compose logs bot'
            }
        }
        if (Test-Path -LiteralPath $work) {
            $ownsWork = $false
            try {
                $currentLease = Get-Content -LiteralPath (Join-Path $work 'lease.json') -Raw | ConvertFrom-Json -ErrorAction Stop
                $ownsWork = $currentLease.lease_id -eq $leaseId
            }
            catch {
                Write-Output 'db_volume_maintenance: lease ownership could not be confirmed; leaving the work directory for monitor cleanup'
            }
            if ($ownsWork) { Remove-Item -LiteralPath $work -Recurse -Force }
        }
        if ($leaseStaging -and (Test-Path -LiteralPath $leaseStaging)) { Remove-Item -LiteralPath $leaseStaging -Recurse -Force }
    }
}
catch {
    $failure = $_
    $failureMessage = $_.Exception.Message
    Write-Output "db_volume_maintenance: FAIL — $failureMessage"
    try {
        Write-BackupStatus -Path $statusFile -Ok $false
    }
    catch {
        Write-Output "db_volume_maintenance: could not update $statusFile — $($_.Exception.Message)"
    }
    if (-not $NoNotify) {
        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'notify-telegram.ps1') -Message "tg-llm-proxy backup failed: $failureMessage"
        if ($LASTEXITCODE -ne 0) { Write-Output 'db_volume_maintenance: failure notification was not delivered to every recipient' }
    }
}
finally {
    Pop-Location
}
if ($restartFailure -and -not $failure) {
    try { Write-BackupStatus -Path $statusFile -Ok $false } catch { }
}
if ($failure -or $restartFailure) { exit 1 }
exit 0
