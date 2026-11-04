#Requires -Version 7
<#

Defaults: events older than 90 days are candidates; message pruning is disabled. -Apply requires and automatically
creates a NEW verified pre-prune backup before opening the source for writing. Existing backup paths are refused.
-Source follows the same DB_PATH/process ENV/.env precedence as db_backup.ps1. No scheduler enables deletion.

Usage:
  .\scripts\db_prune.ps1
  .\scripts\db_prune.ps1 -EventsDays 30
  .\scripts\db_prune.ps1 -EventsDays 90 -Apply
  .\scripts\db_prune.ps1 -EventsDays 0 -MessagesDays 365        # dry-run message retention
  .\scripts\db_prune.ps1 -EventsDays 0 -MessagesDays 365 -Apply # explicit history deletion after backup

Exit: 0 — dry-run completed, or backup + deletion + post-prune integrity_check passed; 1 — any stage failed.
#>

[CmdletBinding()]
param(
    [string]$Source,
    [ValidateRange(0, 100000)]
    [int]$EventsDays = 90,
    [ValidateRange(0, 100000)]
    [int]$MessagesDays = 0,
    [string]$Backup,
    [switch]$Apply,
    [switch]$Rebuild
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$exe = Join-Path $repoRoot 'bin\dbprune.exe'

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
    $Backup = Join-Path $repoRoot "data\backups\pre-prune-$stamp.db"
}
elseif (-not [System.IO.Path]::IsPathRooted($Backup)) {
    $Backup = Join-Path $repoRoot $Backup
}
$Backup = [System.IO.Path]::GetFullPath($Backup)

Push-Location $repoRoot
try {
    $needsBuild = $Rebuild.IsPresent -or -not (Test-Path -LiteralPath $exe)
    if (-not $needsBuild) {
        $exeStamp = (Get-Item -LiteralPath $exe).LastWriteTimeUtc
        $sources = @(Get-ChildItem -LiteralPath (Join-Path $repoRoot 'cmd\dbprune') -File -Filter *.go)
        $sources += Get-ChildItem -LiteralPath (Join-Path $repoRoot 'internal\store') -File -Filter *.go
        $sources += Get-ChildItem -LiteralPath (Join-Path $repoRoot 'internal\store\migrations') -File -Filter *.sql
        foreach ($src in $sources) {
            if ($src.LastWriteTimeUtc -gt $exeStamp) { $needsBuild = $true; break }
        }
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

    $toolArgs = @('-source', $Source, '-backup', $Backup, '-events-days', $EventsDays, '-messages-days', $MessagesDays)
    if ($Apply) { $toolArgs += '-apply' }
    & $exe @toolArgs
    $code = $LASTEXITCODE
    if ($code -eq 0) {
        if ($Apply) { Write-Output "db_prune: OK — pre-prune backup is $Backup" }
        else { Write-Output 'db_prune: dry-run only — no backup or deletion performed' }
        exit 0
    }
    Write-Output "db_prune: exit $code — retention was not completed"
    exit 1
}
finally {
    Pop-Location
}
