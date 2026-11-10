#Requires -Version 7
<#

The backup is always a NEW timestamped file and is checked read-only by db_backup.ps1. The copy is first built
under a hidden .tmp name in the destination directory and only then moved to bot-YYYYMMDD-HHMMSS.db, so a crash
or a full disk can never publish a half-written file under the name retention and the monitor trust. The
published file is verified once more with db_inspect.ps1 -Counts (integrity_check included) after the move.

Retention then runs through db_rotate.ps1: without -Apply it is only a dry-run; -Apply explicitly permits
deletion of expired generated names. -Source is optional and has the same DB_PATH/process ENV/.env precedence as
db_backup.ps1. The destination directory must be data/backups (or a child) or zzscratch (or a child), matching
the rotation safety boundary. -ExternalDirectory adds a verified copy after local backup and rotation; -Encrypt
makes that external copy AES-256-GCM encrypted (the operator decided against off-host copies, so neither switch
is used by the production schedule).

Usage:
  .\scripts\db_maintenance.ps1                              # backup + dry-run retention, keep 7
  .\scripts\db_maintenance.ps1 -Keep 14 -Apply              # backup + delete expired copies, keep 14
  .\scripts\db_maintenance.ps1 -Source .\data\bot.db -Directory .\zzscratch\maintenance -Keep 2
  .\scripts\db_maintenance.ps1 -ExternalDirectory E:\tg-llm-proxy-backups

Task Scheduler should invoke pwsh with -NoLogo -NoProfile -File and the full path to this script. Registration is
intentionally not performed by this script; test the exact command manually before scheduling it with -Apply.

Exit: 0 — backup, retention, and any requested external copy passed; 1 — any stage failed.
#>

[CmdletBinding()]
param(
    [string]$Source,
    [string]$Directory,
    [ValidateRange(1, 1000000)]
    [int]$Keep = 7,
    [string]$ExternalDirectory,
    [switch]$Encrypt,
    [string]$EncryptionKeyPath,
    [switch]$Apply,
    [switch]$Rebuild
)

. (Join-Path $PSScriptRoot 'MaintenancePaths.ps1')
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$backupRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot 'data\backups'))
$scratchRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot 'zzscratch'))

if (-not $Directory) { $Directory = $backupRoot }
elseif (-not [System.IO.Path]::IsPathRooted($Directory)) { $Directory = Join-Path $repoRoot $Directory }
$Directory = [System.IO.Path]::GetFullPath($Directory)
if ((-not (Test-MaintenancePathWithinRoot $Directory $backupRoot)) -and
    (-not (Test-MaintenancePathWithinRoot $Directory $scratchRoot))) {
    Write-Output "db_maintenance: directory $Directory is outside the allowed roots $backupRoot and $scratchRoot"
    exit 1
}

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$Dest = Join-Path $Directory "bot-$stamp.db"
if (Test-Path -LiteralPath $Dest) {
    Write-Output "db_maintenance: destination $Dest already exists — backups never overwrite; retry after the timestamp changes"
    exit 1
}
# The hidden sibling is never matched by db_rotate.ps1 (^bot-\d{8}-\d{6}\.db$) nor by watch_container.ps1.
$stagingDest = Join-Path $Directory ".bot-$stamp.$PID.$([Guid]::NewGuid().ToString('N')).tmp"

Push-Location $repoRoot
try {
    try {
        $backupArgs = @('-Dest', $stagingDest)
        if ($Source) { $backupArgs += @('-Source', $Source) }
        if ($Rebuild) { $backupArgs += '-Rebuild' }
        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_backup.ps1') @backupArgs
        if ($LASTEXITCODE -ne 0) {
            Write-Output 'db_maintenance: backup failed; rotation was not run'
            exit 1
        }

        # Atomic publish: File.Move on the same volume either renames the verified temp file or fails without
        # touching $Dest. No reader can observe a partially written bot-*.db.
        [System.IO.File]::Move($stagingDest, $Dest)

        $verifyOutput = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_inspect.ps1') -Path $Dest -Counts 2>&1
        if ($LASTEXITCODE -ne 0 -or (($verifyOutput | Out-String) -notmatch '(?m)^integrity=ok\r?$')) {
            Write-Output (($verifyOutput | Out-String).Trim())
            Remove-Item -LiteralPath $Dest -Force -ErrorAction SilentlyContinue
            Write-Output "db_maintenance: the published backup failed the post-publish integrity check and was removed: $Dest"
            exit 1
        }

        $rotateArgs = @('-Directory', $Directory, '-Keep', $Keep)
        if ($Apply) { $rotateArgs += '-Apply' }
        if ($Rebuild) { $rotateArgs += '-Rebuild' }
        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_rotate.ps1') @rotateArgs
        if ($LASTEXITCODE -ne 0) {
            Write-Output "db_maintenance: the new backup exists and is verified, but retention failed: $Dest"
            exit 1
        }

        if ($ExternalDirectory) {
            $offsiteArgs = @('-Source', $Dest, '-Directory', $ExternalDirectory)
            if ($Encrypt) { $offsiteArgs += '-Encrypt' }
            if ($EncryptionKeyPath) { $offsiteArgs += @('-KeyPath', $EncryptionKeyPath) }
            & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_offsite_copy.ps1') @offsiteArgs
            if ($LASTEXITCODE -ne 0) {
                Write-Output "db_maintenance: the local backup exists and is verified, but external copy failed: $Dest"
                exit 1
            }
        }

        $mode = 'dry-run retention; no old backup was deleted'
        if ($Apply) { $mode = 'apply retention; only listed expired backups were deleted' }
        Write-Output "db_maintenance: OK — verified backup $Dest; $mode (keep $Keep)"
        exit 0
    }
    finally {
        if (Test-Path -LiteralPath $stagingDest) {
            Remove-Item -LiteralPath $stagingDest -Force -ErrorAction SilentlyContinue
        }
    }
}
finally {
    Pop-Location
}
