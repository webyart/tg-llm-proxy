#Requires -Version 7
<#

The source stays available while the bot is running: store.Backup opens it read-only and SQLite's VACUUM INTO
copies a consistent snapshot. The destination must be a new file; backups never overwrite existing files.
After the copy, dbbackup opens the destination read-only and runs PRAGMA integrity_check, so a file that exists
but is not a sound backup is reported as a failure.

under .\data\backups (gitignored with data/).

Usage:
  . .\scripts\Setup.ps1
  .\scripts\db_backup.ps1
  .\scripts\db_backup.ps1 -Source .\data\bot.db -Dest .\zzscratch\bot-copy.db
  .\scripts\db_backup.ps1 -Rebuild       # force the Go rebuild of bin\dbbackup.exe

Exit: 0 — the destination exists and integrity_check answered ok and the bot schema is present; 1 — the backup or its verification failed.
#>

[CmdletBinding()]
param(
    [string]$Source,
    [string]$Dest,
    [switch]$Rebuild
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$exe = Join-Path $repoRoot 'bin\dbbackup.exe'

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
    Write-Output 'db_backup: DB_PATH is not set in the process ENV or in .env — pass -Source ..\data\bot.db '
    exit 1
}
if (-not $Dest) {
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $Dest = Join-Path $repoRoot "data\backups\bot-$stamp.db"
}

if (-not [System.IO.Path]::IsPathRooted($Source)) { $Source = Join-Path $repoRoot $Source }
if (-not [System.IO.Path]::IsPathRooted($Dest)) { $Dest = Join-Path $repoRoot $Dest }
$Source = [System.IO.Path]::GetFullPath($Source)
$Dest = [System.IO.Path]::GetFullPath($Dest)

if (-not (Test-Path -LiteralPath $Source)) {
    Write-Output "db_backup: source $Source does not exist — for the Docker volume copy bot:/data/bot.db to a local file first , or pass -Source"
    exit 1
}

Push-Location $repoRoot
try {
    $needsBuild = $Rebuild.IsPresent -or -not (Test-Path -LiteralPath $exe)
    if (-not $needsBuild) {
        $exeStamp = (Get-Item -LiteralPath $exe).LastWriteTimeUtc
        $sources = @(Get-ChildItem -LiteralPath (Join-Path $repoRoot 'cmd\dbbackup') -File -Filter *.go)
        $sources += Get-ChildItem -LiteralPath (Join-Path $repoRoot 'internal\store') -File -Filter *.go
        foreach ($src in $sources) {
            if ($src.LastWriteTimeUtc -gt $exeStamp) { $needsBuild = $true; break }
        }
    }
    if ($needsBuild) {
        $env:CGO_ENABLED = '0'
        New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'bin') | Out-Null
        $build = (go build -o bin/dbbackup.exe ./cmd/dbbackup 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output "db_backup: go build ./cmd/dbbackup exit $LASTEXITCODE — the reasons above "
            exit 1
        }
        Write-Output 'db_backup: built bin\dbbackup.exe '
    }

    & $exe -source $Source -dest $Dest
    $code = $LASTEXITCODE
    if ($code -eq 0) {
        Write-Output 'db_backup: OK — the destination opened read-only and integrity_check answered ok and the bot schema is present'
        exit 0
    }
    Write-Output "db_backup: exit $code — the backup was not made, or the copy failed verification "
    exit 1
}
finally {
    Pop-Location
}
