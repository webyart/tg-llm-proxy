#Requires -Version 7
<#
db_rotate.ps1 — retain the newest timestamped backups made by scripts/db_backup.ps1.

Only regular files named bot-YYYYMMDD-HHMMSS.db are candidates. Manual backup names and all other files are ignored.
The newest -Keep copies always survive; -Keep must be at least 1. The default is a dry-run, and deletion happens
only with -Apply. For safety the directory must be data/backups (or a child) or zzscratch (or a child).

Usage:
  .\scripts\db_rotate.ps1                         # dry-run: data/backups, keep 7
  .\scripts\db_rotate.ps1 -Keep 14                # another dry-run
  .\scripts\db_rotate.ps1 -Keep 7 -Apply          # explicitly delete the listed old backups
  .\scripts\db_rotate.ps1 -Directory .\zzscratch\rotation-test -Keep 2 -Apply

Exit: 0 — the plan or requested deletion completed; 1 — invalid path/retention, build, or deletion failure.
#>

[CmdletBinding()]
param(
    [string]$Directory,
    [ValidateRange(1, 1000000)]
    [int]$Keep = 7,
    [switch]$Apply,
    [switch]$Rebuild
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$exe = Join-Path $repoRoot 'bin\dbrotate.exe'
$backupRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot 'data\backups'))
$scratchRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot 'zzscratch'))

function Test-WithinRoot([string]$path, [string]$root) {
    $prefix = $root + [System.IO.Path]::DirectorySeparatorChar
    return $path.Equals($root, [System.StringComparison]::OrdinalIgnoreCase) -or
        $path.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)
}

if (-not $Directory) { $Directory = $backupRoot }
elseif (-not [System.IO.Path]::IsPathRooted($Directory)) { $Directory = Join-Path $repoRoot $Directory }
$Directory = [System.IO.Path]::GetFullPath($Directory)
if ((-not (Test-WithinRoot $Directory $backupRoot)) -and (-not (Test-WithinRoot $Directory $scratchRoot))) {
    Write-Output "db_rotate: directory $Directory is outside the allowed roots $backupRoot and $scratchRoot"
    exit 1
}
if (-not (Test-Path -LiteralPath $Directory -PathType Container)) {
    Write-Output "db_rotate: directory $Directory does not exist — make a backup first"
    exit 1
}

Push-Location $repoRoot
try {
    $needsBuild = $Rebuild.IsPresent -or -not (Test-Path -LiteralPath $exe)
    if (-not $needsBuild) {
        $exeStamp = (Get-Item -LiteralPath $exe).LastWriteTimeUtc
        $sources = @(Get-ChildItem -LiteralPath (Join-Path $repoRoot 'cmd\dbrotate') -File -Filter *.go)
        foreach ($src in $sources) {
            if ($src.LastWriteTimeUtc -gt $exeStamp) { $needsBuild = $true; break }
        }
    }
    if ($needsBuild) {
        $env:CGO_ENABLED = '0'
        New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'bin') | Out-Null
        $build = (go build -o bin/dbrotate.exe ./cmd/dbrotate 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output $build.Trim()
            Write-Output "db_rotate: go build ./cmd/dbrotate exit $LASTEXITCODE"
            exit 1
        }
        Write-Output 'db_rotate: built bin\dbrotate.exe (a maintenance tool, deliberately not in the runtime image)'
    }

    $toolArgs = @('-dir', $Directory, '-keep', $Keep)
    if ($Apply) { $toolArgs += '-apply' }
    & $exe @toolArgs
    $code = $LASTEXITCODE
    if ($code -eq 0) {
        if ($Apply) {
            Write-Output 'db_rotate: OK — only the explicitly listed expired backups were deleted'
        }
        else {
            Write-Output 'db_rotate: dry-run only — no files were deleted; rerun with -Apply to execute this plan'
        }
        exit 0
    }
    Write-Output "db_rotate: exit $code — rotation failed"
    exit 1
}
finally {
    Pop-Location
}
