#Requires -Version 7
<#

Defaults: events older than 90 days are candidates; message pruning is disabled. -Apply requires and automatically
creates a NEW verified pre-prune backup before opening the source for writing. Existing backup paths are refused.
-Source follows the same DB_PATH/process ENV/.env precedence as db_backup.ps1. No scheduler enables deletion.

All seven retention classes of internal/store/prune.go are wired here: events, messages, RAG documents, daily quota
reservations, terminal tasks, terminal reminders and saved links. A zero -days value disables that class; -Apply with
every class at zero is refused by cmd/dbprune. cmd/dbprune deletes each class in ONE immediate transaction (it has no
batch/chunk switch), then checkpoints the WAL, re-runs integrity_check and VACUUMs while the caller guarantees no other
writer. -NoVacuum skips the VACUUM but keeps the checkpoint and the integrity check.

-DockerService <name> runs the same binary inside a one-off compose container against the named volume of that
service (/data/bot.db) instead of a host file. That is the production path: scripts/db_volume_maintenance.ps1 stops
the bot and calls this script while the container is down, so VACUUM never runs against a live writer. The pre-prune
backup is written inside the volume (native Linux filesystem, never a Windows bind mount), copied out to
data/backups with docker compose cp, verified read-only on the host, and then removed from the volume. The Linux
helper bin/dbprune-linux is cross-compiled from the same sources; the running bot image must already be at the
schema version compiled here (db_volume_maintenance refuses to prune otherwise).

pre-prune-YYYYMMDD-HHMMSS.db copies in -BackupDirectory are rotated by count (-PrePruneKeep, newest always kept) and
by age (-PrePruneMaxAgeDays); deletion happens only with -Apply and only for that generated name pattern.

Usage:
  .\scripts\db_prune.ps1
  .\scripts\db_prune.ps1 -EventsDays 30
  .\scripts\db_prune.ps1 -EventsDays 90 -Apply
  .\scripts\db_prune.ps1 -EventsDays 0 -MessagesDays 365        # dry-run message retention
  .\scripts\db_prune.ps1 -EventsDays 0 -MessagesDays 365 -Apply # explicit history deletion after backup
  .\scripts\db_prune.ps1 -EventsDays 90 -QuotaDays 180 -TasksDays 90 -RemindersDays 90 -LinksDays 365 -Apply
  .\scripts\db_prune.ps1 -DockerService bot -Apply              # production volume, bot must be stopped
  .\scripts\db_prune.ps1 -DockerService bot -EventsDays 90      # production dry-run, writer may stay up

Exit: 0 — dry-run completed, or backup + deletion + post-prune integrity_check passed; 1 — any stage failed.
#>

[CmdletBinding()]
param(
    [string]$Source,
    [ValidateRange(0, 100000)]
    [int]$EventsDays = 90,
    [ValidateRange(0, 100000)]
    [int]$MessagesDays = 0,
    [ValidateRange(0, 100000)]
    [int]$RAGDays = 0,
    [ValidateRange(0, 100000)]
    [int]$QuotaDays = 0,
    [ValidateRange(0, 100000)]
    [int]$TasksDays = 0,
    [ValidateRange(0, 100000)]
    [int]$RemindersDays = 0,
    [ValidateRange(0, 100000)]
    [int]$LinksDays = 0,
    [string]$Backup,
    [switch]$Apply,
    [switch]$NoVacuum,
    [string]$DockerService,
    [string]$BackupDirectory,
    [ValidateRange(1, 1000)]
    [int]$PrePruneKeep = 7,
    [ValidateRange(1, 3650)]
    [int]$PrePruneMaxAgeDays = 30,
    [switch]$Rebuild
)

. (Join-Path $PSScriptRoot 'MaintenancePaths.ps1')
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$exe = Join-Path $repoRoot 'bin\dbprune.exe'
$linuxExe = Join-Path $repoRoot 'bin\dbprune-linux'
$backupRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot 'data\backups'))
$scratchRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot 'zzscratch'))

function ReadEnvValue([string]$name) {
    $envFile = Join-Path $repoRoot '.env'
    if (-not (Test-Path -LiteralPath $envFile)) { return '' }
    foreach ($line in Get-Content -LiteralPath $envFile) {
        if ($line -match "^\s*$name\s*=\s*(.*?)\s*$") {
            $value = $matches[1]
            if ($value.Length -ge 2 -and (($value[0] -eq '"' -and $value[-1] -eq '"') -or ($value[0] -eq "'" -and $value[-1] -eq "'"))) {
                $value = $value.Substring(1, $value.Length - 2)
            }
            return $value
        }
    }
    return ''
}

function Test-PruneSourcesNewerThan([string]$Stamp) {
    $sources = @(Get-ChildItem -LiteralPath (Join-Path $repoRoot 'cmd\dbprune') -File -Filter *.go)
    $sources += Get-ChildItem -LiteralPath (Join-Path $repoRoot 'internal\store') -File -Filter *.go
    $sources += Get-ChildItem -LiteralPath (Join-Path $repoRoot 'internal\store\migrations') -File -Filter *.sql
    foreach ($src in $sources) {
        if ($src.LastWriteTimeUtc -gt $Stamp) { return $true }
    }
    return $false
}

function Build-DbpruneLinux {
    $needsBuild = $Rebuild.IsPresent -or -not (Test-Path -LiteralPath $linuxExe)
    if (-not $needsBuild) {
        $needsBuild = Test-PruneSourcesNewerThan (Get-Item -LiteralPath $linuxExe).LastWriteTimeUtc
    }
    if (-not $needsBuild) { return }
    $oldGOOS = $env:GOOS
    $oldGOARCH = $env:GOARCH
    try {
        $env:CGO_ENABLED = '0'
        $env:GOOS = 'linux'
        $env:GOARCH = 'amd64'
        New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'bin') | Out-Null
        $build = (go build -o $linuxExe ./cmd/dbprune 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output $build.Trim()
            Write-Output "db_prune: go build -o bin/dbprune-linux ./cmd/dbprune exit $LASTEXITCODE"
            exit 1
        }
        Write-Output 'db_prune: built bin\dbprune-linux (linux/amd64 helper for the production volume)'
    }
    finally {
        $env:GOOS = $oldGOOS
        $env:GOARCH = $oldGOARCH
    }
}

function Invoke-PrePruneRotation {
    param(
        [Parameter(Mandatory)][string]$Directory,
        [Parameter(Mandatory)][int]$Keep,
        [Parameter(Mandatory)][int]$MaxAgeDays,
        [Parameter(Mandatory)][bool]$Execute
    )
    $files = @(Get-ChildItem -LiteralPath $Directory -File -Filter 'pre-prune-*.db' -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -match '^pre-prune-[0-9]{8}-[0-9]{6}\.db$' } |
        Sort-Object -Property LastWriteTimeUtc -Descending)
    if ($files.Count -eq 0) {
        Write-Output 'db_prune: pre-prune retention: no generated pre-prune backups found'
        return
    }
    $cutoff = [DateTime]::UtcNow.AddDays(-$MaxAgeDays)
    $expired = @()
    for ($index = 0; $index -lt $files.Count; $index++) {
        if (($index -ge $Keep) -or ($files[$index].LastWriteTimeUtc -lt $cutoff)) { $expired += $files[$index] }
    }
    foreach ($file in $expired) {
        if ($Execute) {
            Remove-Item -LiteralPath $file.FullName -Force -ErrorAction Stop
            Write-Output "db_prune: rotated pre-prune backup $($file.Name)"
        }
        else {
            Write-Output "db_prune: would rotate pre-prune backup $($file.Name) (keep $Keep, max age $MaxAgeDays days)"
        }
    }
    if ($expired.Count -eq 0) {
        Write-Output "db_prune: pre-prune retention: nothing to rotate (keep $Keep, max age $MaxAgeDays days)"
    }
}

function Get-RetentionArgs {
    # Built once so the host path and the container path cannot drift in their class wiring.
    $vacuumText = if ($NoVacuum) { 'false' } else { 'true' }
    return @(
        '-events-days', $EventsDays,
        '-messages-days', $MessagesDays,
        '-rag-days', $RAGDays,
        '-quota-days', $QuotaDays,
        '-tasks-days', $TasksDays,
        '-reminders-days', $RemindersDays,
        '-links-days', $LinksDays,
        "-vacuum=$vacuumText"
    )
}

Push-Location $repoRoot
try {
    if ($DockerService) {
        # ---- production named-volume path ------------------------------------------------
        if ($DockerService -notmatch '^[A-Za-z0-9][A-Za-z0-9_.-]*$') {
            Write-Output "db_prune: -DockerService '$DockerService' is not a compose service name"
            exit 1
        }
        if ($Backup) {
            Write-Output 'db_prune: -Backup is a host path; with -DockerService use -BackupDirectory for the copied-out pre-prune backup'
            exit 1
        }
        if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) {
            Write-Output 'db_prune: docker is not on PATH'
            exit 1
        }
        if (-not $BackupDirectory) { $BackupDirectory = $backupRoot }
        elseif (-not [System.IO.Path]::IsPathRooted($BackupDirectory)) { $BackupDirectory = Join-Path $repoRoot $BackupDirectory }
        $BackupDirectory = [System.IO.Path]::GetFullPath($BackupDirectory)
        if (-not (Test-MaintenancePathWithinRoot $BackupDirectory $backupRoot)) {
            Write-Output "db_prune: backup directory $BackupDirectory is outside $backupRoot"
            exit 1
        }
        [void](New-Item -ItemType Directory -Path $BackupDirectory -Force)

        $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
        $volumeBackup = "/data/pre-prune-$stamp.db"
        $localBackup = Join-Path $BackupDirectory "pre-prune-$stamp.db"
        if (Test-Path -LiteralPath $localBackup) {
            Write-Output "db_prune: destination $localBackup already exists; retry after the timestamp changes"
            exit 1
        }

        Build-DbpruneLinux
        $toolArgs = @('-source', '/data/bot.db') + (Get-RetentionArgs)
        if ($Apply) { $toolArgs += @('-backup', $volumeBackup, '-apply') }
        $composeArgs = @(
            'compose', 'run', '--rm', '-T', '--no-deps',
            '-v', "$(Join-Path $repoRoot 'bin'):/tools:ro",
            '--entrypoint', '/tools/dbprune-linux',
            $DockerService
        ) + $toolArgs
        $output = (& docker @composeArgs 2>&1 | Out-String).Trim()
        $code = $LASTEXITCODE
        if ($output) { Write-Output $output }
        if ($code -ne 0) {
            Write-Output "db_prune: the container prune exited $code — retention was not completed"
            exit 1
        }
        if (-not $Apply) {
            Write-Output 'db_prune: dry-run only — the bot volume was opened read-only; no backup or deletion performed'
            exit 0
        }

        $localTemp = "$localBackup.$PID.$([Guid]::NewGuid().ToString('N')).tmp"
        try {
            & docker compose cp "${DockerService}:$volumeBackup" $localTemp
            if ($LASTEXITCODE -ne 0) {
                Write-Output "db_prune: the verified pre-prune backup is still in the volume ($volumeBackup) but could not be copied out"
                exit 1
            }
            $verifyOutput = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_inspect.ps1') -Path $localTemp -Counts 2>&1
            if ($LASTEXITCODE -ne 0 -or (($verifyOutput | Out-String) -notmatch '(?m)^integrity=ok\r?$')) {
                Write-Output (($verifyOutput | Out-String).Trim())
                Write-Output "db_prune: the copied pre-prune backup failed verification; the volume copy was left at $volumeBackup"
                exit 1
            }
            [System.IO.File]::Move($localTemp, $localBackup)
            Write-Output "db_prune: copied and verified pre-prune backup $localBackup"
        }
        finally {
            if (Test-Path -LiteralPath $localTemp) { Remove-Item -LiteralPath $localTemp -Force -ErrorAction SilentlyContinue }
        }

        # Only after the host copy is safe may the in-volume copy be removed.
        & docker compose run --rm -T --no-deps --entrypoint /bin/rm $DockerService -f $volumeBackup | Out-Null
        if ($LASTEXITCODE -ne 0) {
            Write-Output "db_prune: warning — could not remove $volumeBackup from the bot volume; it will be left for the next run"
        }

        Invoke-PrePruneRotation -Directory $BackupDirectory -Keep $PrePruneKeep -MaxAgeDays $PrePruneMaxAgeDays -Execute $true
        Write-Output "db_prune: OK — verified pre-prune backup is $localBackup"
        exit 0
    }

    # ---- host file path ----------------------------------------------------------------
    if (-not $Source) { $Source = $env:DB_PATH }
    if (-not $Source) { $Source = ReadEnvValue 'DB_PATH' }
    if (-not $Source) {
        Write-Output 'db_prune: DB_PATH is not set — pass -Source .\data\bot.db'
        exit 1
    }
    if (-not [System.IO.Path]::IsPathRooted($Source)) { $Source = Join-Path $repoRoot $Source }
    $Source = [System.IO.Path]::GetFullPath($Source)
    if (-not (Test-Path -LiteralPath $Source -PathType Leaf)) {
        Write-Output "db_prune: source $Source does not exist"
        exit 1
    }
    if (-not $Backup) {
        $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
        $Backup = Join-Path $backupRoot "pre-prune-$stamp.db"
    }
    elseif (-not [System.IO.Path]::IsPathRooted($Backup)) {
        $Backup = Join-Path $repoRoot $Backup
    }
    $Backup = [System.IO.Path]::GetFullPath($Backup)
    if (-not (Test-MaintenancePathWithinRoot $Backup $backupRoot) -and
        (-not (Test-MaintenancePathWithinRoot $Backup $scratchRoot))) {
        Write-Output "db_prune: backup $Backup is outside the allowed roots $backupRoot and $scratchRoot"
        exit 1
    }

    $needsBuild = $Rebuild.IsPresent -or -not (Test-Path -LiteralPath $exe)
    if (-not $needsBuild) {
        $needsBuild = Test-PruneSourcesNewerThan (Get-Item -LiteralPath $exe).LastWriteTimeUtc
    }
    if ($needsBuild) {
        $env:CGO_ENABLED = '0'
        New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'bin') | Out-Null
        $build = (go build -o bin/dbprune.exe ./cmd/dbprune 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output $build.Trim()
            Write-Output "db_prune: go build ./cmd/dbprune exit $LASTEXITCODE"
            exit 1
        }
    }

    $toolArgs = @('-source', $Source, '-backup', $Backup) + (Get-RetentionArgs)
    if ($Apply) { $toolArgs += '-apply' }
    & $exe @toolArgs
    $code = $LASTEXITCODE
    if ($code -eq 0) {
        if ($Apply) {
            Invoke-PrePruneRotation -Directory (Split-Path -Parent $Backup) -Keep $PrePruneKeep -MaxAgeDays $PrePruneMaxAgeDays -Execute $true
            Write-Output "db_prune: OK — pre-prune backup is $Backup"
        }
        else { Write-Output 'db_prune: dry-run only — no backup or deletion performed' }
        exit 0
    }
    Write-Output "db_prune: exit $code — retention was not completed"
    exit 1
}
finally {
    Pop-Location
}
