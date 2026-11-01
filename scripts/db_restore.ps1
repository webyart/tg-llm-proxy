#Requires -Version 7
<#
db_restore.ps1 — verify a §5 backup and restore it into a NEW file under zzscratch.

Both source and destination are opened read-only for integrity_check and the required bot-schema check. The copy
uses SQLite VACUUM INTO rather than copying database pages by hand. Existing destinations are never overwritten.
This script deliberately cannot target data/bot.db or the Docker named volume; production replacement stays a
separate, explicit operator procedure.

Usage:
  .\scripts\db_restore.ps1 -Source .\data\backups\bot-20261101-120000.db
  .\scripts\db_restore.ps1 -Source .\zzscratch\backup.db -Dest .\zzscratch\restored.db
  .\scripts\db_restore.ps1 -Source .\zzscratch\backup.db -Rebuild

Exit: 0 — source and restored file passed verification; 1 — validation, copy, or verification failed.
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Source,
    [string]$Dest,
    [switch]$Rebuild
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$exe = Join-Path $repoRoot 'bin\dbrestore.exe'
$scratchRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot 'zzscratch'))
$scratchPrefix = $scratchRoot + [System.IO.Path]::DirectorySeparatorChar

if (-not [System.IO.Path]::IsPathRooted($Source)) { $Source = Join-Path $repoRoot $Source }
$Source = [System.IO.Path]::GetFullPath($Source)
if (-not (Test-Path -LiteralPath $Source -PathType Leaf)) {
    Write-Output "db_restore: source $Source does not exist or is not a file"
    exit 1
}

if (-not $Dest) {
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $Dest = Join-Path $scratchRoot "restored-bot-$stamp.db"
}
elseif (-not [System.IO.Path]::IsPathRooted($Dest)) {
    $Dest = Join-Path $repoRoot $Dest
}
$Dest = [System.IO.Path]::GetFullPath($Dest)
if (-not $Dest.StartsWith($scratchPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    Write-Output "db_restore: destination $Dest is outside $scratchRoot — scratch restore never targets a live database"
    exit 1
}
if (Test-Path -LiteralPath $Dest) {
    Write-Output "db_restore: destination $Dest already exists — choose a new file"
    exit 1
}

Push-Location $repoRoot
try {
    $needsBuild = $Rebuild.IsPresent -or -not (Test-Path -LiteralPath $exe)
    if (-not $needsBuild) {
        $exeStamp = (Get-Item -LiteralPath $exe).LastWriteTimeUtc
        $sources = @(Get-ChildItem -LiteralPath (Join-Path $repoRoot 'cmd\dbrestore') -File -Filter *.go)
        $sources += Get-ChildItem -LiteralPath (Join-Path $repoRoot 'internal\store') -File -Filter *.go
        foreach ($src in $sources) {
            if ($src.LastWriteTimeUtc -gt $exeStamp) { $needsBuild = $true; break }
        }
    }
    if ($needsBuild) {
        $env:CGO_ENABLED = '0'
        New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'bin') | Out-Null
        $build = (go build -o bin/dbrestore.exe ./cmd/dbrestore 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output $build.Trim()
            Write-Output "db_restore: go build ./cmd/dbrestore exit $LASTEXITCODE"
            exit 1
        }
        Write-Output 'db_restore: built bin\dbrestore.exe (a maintenance tool, deliberately not in the runtime image)'
    }

    & $exe -source $Source -dest $Dest
    $code = $LASTEXITCODE
    if ($code -eq 0) {
        Write-Output 'db_restore: OK — source and restored file passed integrity_check and the bot-schema check'
        Write-Output "db_restore: inspect counts with: .\scripts\db_inspect.ps1 -Path `"$Dest`" -Counts"
        Write-Output 'db_restore: the live database and Docker volume were not changed'
        exit 0
    }
    Write-Output "db_restore: exit $code — restore or verification failed"
    exit 1
}
finally {
    Pop-Location
}
