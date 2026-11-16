#Requires -Version 7
<#
restore_drill.ps1 — restore a verified backup into a throw-away file, inspect it, and compare the counters.

This is the production-backup counterpart of scripts/restore-drill.sh (which rehearses the same pipeline on a
fixture database in CI, without Docker and without a real backup). It changes nothing outside zzscratch:

  1. select the newest data/backups/bot-*.db, or take a fresh verified backup first with -Fresh;
  2. restore it into a new file under zzscratch with cmd/dbrestore (via db_restore.ps1), which verifies the
     source read-only and the restored file again;
  3. run db_inspect -Counts on the source backup and on the restored file (integrity, schema_version,
     content_hash, table_* and events_* counters) and refuse to report OK if any of them differ;
  4. prove the restored file opens read-write and that the source backup's SHA-256 did not change.

The production database (data/bot.db), the Docker volume and data/backups are never written: the only writes
are the new scratch directory and, with -Fresh, a new backup file under that same scratch directory. The
script deliberately does not call Docker: a running bot is not stopped or inspected.

Usage:
  .\scripts\restore_drill.ps1
  .\scripts\restore_drill.ps1 -Source .\data\backups\bot-20261116-044428.db
  .\scripts\restore_drill.ps1 -Fresh                 # back up DB_PATH first, then drill that copy
  .\scripts\restore_drill.ps1 -Fresh -SourceDb .\data\bot.db
  .\scripts\restore_drill.ps1 -Keep                  # keep the scratch restore for inspection

Exit: 0 — the selected backup restored, inspected and matched; 1 — any stage failed.
#>

[CmdletBinding()]
param(
    [string]$Source,
    [switch]$Fresh,
    [string]$SourceDb,
    [string]$BackupDirectory,
    [switch]$Keep,
    [switch]$Rebuild
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'MaintenancePaths.ps1')

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$scratchRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot 'zzscratch'))
if (-not $BackupDirectory) { $BackupDirectory = Join-Path $repoRoot 'data\backups' }
if (-not [System.IO.Path]::IsPathRooted($BackupDirectory)) { $BackupDirectory = Join-Path $repoRoot $BackupDirectory }
$BackupDirectory = [System.IO.Path]::GetFullPath($BackupDirectory)

function Read-EnvValue([string]$name) {
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

function Invoke-Tool {
    param([string]$Script, [string[]]$Arguments)
    $output = @(& pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot $Script) @Arguments 2>&1 |
            ForEach-Object { [string]$_ })
    $code = $LASTEXITCODE
    if ($code -ne 0) {
        Write-Output ($output -join "`n")
        throw "$Script exited $code"
    }
    return , $output
}

function Get-CounterValue {
    param([string[]]$Counts, [string]$Name)
    $line = $Counts | Where-Object { $_ -like "$Name=*" } | Select-Object -First 1
    if (-not $line) { return $null }
    return $line.Substring($Name.Length + 1)
}

$work = Join-Path $scratchRoot ("restore-drill-" + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
$restored = Join-Path $work 'restored.db'
$freshBackup = $null
$sourcePath = $null
$sourceHashBefore = $null
$success = $false

try {
    if ($Fresh -and $Source) {
        throw '-Fresh and -Source are mutually exclusive: -Fresh creates the backup it drills'
    }
    if ($Fresh) {
        if (-not $SourceDb) { $SourceDb = $env:DB_PATH }
        if (-not $SourceDb) { $SourceDb = Read-EnvValue 'DB_PATH' }
        if (-not $SourceDb) { $SourceDb = Join-Path (Join-Path $repoRoot 'data') 'bot.db' }
        if (-not [System.IO.Path]::IsPathRooted($SourceDb)) { $SourceDb = Join-Path $repoRoot $SourceDb }
        $SourceDb = [System.IO.Path]::GetFullPath($SourceDb)
        if (-not (Test-Path -LiteralPath $SourceDb -PathType Leaf)) {
            throw "source database $SourceDb does not exist — the production database lives in the Docker volume, so drill the daily backup (no -Fresh) or pass -SourceDb with an exported snapshot"
        }
        [void](New-Item -ItemType Directory -Path (Join-Path $work 'fresh') -Force)
        $freshBackup = Join-Path (Join-Path $work 'fresh') ('bot-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '.db')
        Write-Output "restore_drill: taking a fresh verified backup of $SourceDb"
        $freshOut = Invoke-Tool 'db_backup.ps1' @('-Source', $SourceDb, '-Dest', $freshBackup)
        Write-Output ($freshOut -join "`n")
        $sourcePath = $freshBackup
    }
    else {
        if (-not $Source) {
            if (-not (Test-Path -LiteralPath $BackupDirectory -PathType Container)) {
                throw "backup directory $BackupDirectory does not exist — pass -Source"
            }
            $latest = Get-ChildItem -LiteralPath $BackupDirectory -File -Filter 'bot-*.db' |
                Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1
            if (-not $latest) { throw "no bot-*.db backup in $BackupDirectory — pass -Source or use -Fresh" }
            $Source = $latest.FullName
        }
        if (-not [System.IO.Path]::IsPathRooted($Source)) { $Source = Join-Path $repoRoot $Source }
        $Source = [System.IO.Path]::GetFullPath($Source)
        if (-not (Test-Path -LiteralPath $Source -PathType Leaf)) { throw "source backup $Source does not exist" }
        $sourcePath = $Source
    }

    $sourceItem = Get-Item -LiteralPath $sourcePath
    $sourceHashBefore = (Get-FileHash -LiteralPath $sourcePath -Algorithm SHA256).Hash
    Write-Output ("restore_drill: source {0} ({1} bytes, SHA-256 {2})" -f $sourcePath, $sourceItem.Length, $sourceHashBefore)

    [void](New-Item -ItemType Directory -Path $work -Force)
    if (-not (Test-MaintenancePathWithinRoot $work $scratchRoot)) {
        throw "scratch directory $work is outside $scratchRoot"
    }

    $restoreArgs = @('-Source', $sourcePath, '-Dest', $restored)
    if ($Rebuild) { $restoreArgs += '-Rebuild' }
    $restoreOut = Invoke-Tool 'db_restore.ps1' $restoreArgs
    Write-Output ($restoreOut -join "`n")

    $sourceCounts = Invoke-Tool 'db_inspect.ps1' @('-Path', $sourcePath, '-Counts')
    Write-Output ($sourceCounts -join "`n")
    $restoredCounts = Invoke-Tool 'db_inspect.ps1' @('-Path', $restored, '-Counts')
    Write-Output ($restoredCounts -join "`n")

    foreach ($report in @(@{ Label = 'source backup'; Counts = $sourceCounts }, @{ Label = 'restored file'; Counts = $restoredCounts })) {
        if (-not ($report.Counts -contains 'integrity=ok')) {
            throw "$($report.Label) did not report integrity=ok"
        }
        $schema = Get-CounterValue -Counts $report.Counts -Name 'schema_version'
        if (-not $schema -or $schema -notmatch '^[0-9]+$') {
            throw "$($report.Label) has no numeric schema_version (got '$schema')"
        }
    }

    $sourceSchema = Get-CounterValue -Counts $sourceCounts -Name 'schema_version'
    $restoredSchema = Get-CounterValue -Counts $restoredCounts -Name 'schema_version'
    if ($sourceSchema -ne $restoredSchema) {
        throw "schema_version differs: source $sourceSchema, restored $restoredSchema"
    }
    if ($sourceSchema -eq '0') {
        Write-Output 'restore_drill: WARN — schema_version=0: the backup predates schema versioning; the bot migrates it on the next writable start'
    }

    $filter = '^(integrity|schema_version|content_hash|last_update_id|bot_state_offset_found|table_|events_)'
    $sourceFiltered = @($sourceCounts | Where-Object { $_ -match $filter } | Sort-Object)
    $restoredFiltered = @($restoredCounts | Where-Object { $_ -match $filter } | Sort-Object)
    $difference = Compare-Object -ReferenceObject $sourceFiltered -DifferenceObject $restoredFiltered
    if ($difference) {
        Write-Output 'restore_drill: counter difference (source vs restored):'
        $difference | ForEach-Object { Write-Output ("  {0} {1}" -f $_.SideIndicator, $_.InputObject) }
        throw 'restored counters differ from the source backup'
    }

    $restoredItem = Get-Item -LiteralPath $restored
    if ($restoredItem.Length -le 0) { throw "restored file is empty: $restored" }
    $handle = [System.IO.File]::Open($restored, [System.IO.FileMode]::Open, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
    $handle.Dispose()

    $sourceHashAfter = (Get-FileHash -LiteralPath $sourcePath -Algorithm SHA256).Hash
    if ($sourceHashAfter -ne $sourceHashBefore) {
        throw "source backup changed during the drill: $sourceHashBefore -> $sourceHashAfter"
    }

    Write-Output ''
    Write-Output ("restore_drill: counts compared ({0} lines), schema_version={1}, integrity=ok, content_hash={2}" -f `
            $sourceFiltered.Count, $sourceSchema, (Get-CounterValue -Counts $restoredCounts -Name 'content_hash'))
    Write-Output ("restore_drill: restored {0} ({1} bytes) opens read-write; source backup SHA-256 is unchanged" -f $restored, $restoredItem.Length)
    Write-Output 'restore_drill: OK — the backup restores into a usable database; the production database, data/backups and the Docker volume were not modified'
    $success = $true
}
catch {
    Write-Output "restore_drill: FAIL — $($_.Exception.Message)"
}
finally {
    if ($Keep -and (Test-Path -LiteralPath $work)) {
        Write-Output "restore_drill: scratch kept at $work"
    }
    elseif (Test-Path -LiteralPath $work) {
        if (Test-MaintenancePathWithinRoot $work $scratchRoot) {
            Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

if ($success) { exit 0 }
exit 1
