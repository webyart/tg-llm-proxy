#Requires -Version 7
<#

`_journal_mode`, no `_txlock` — the script can look at the database of a RUNNING bot and cannot change it
(internal/store/inspect.go, TestOpenReadOnlyCannotWriteAndCannotRemode).

ENV lines wholesale the way `Get-Content .env` would).

Usage:
  . .\scripts\Setup.ps1
  .\scripts\db_inspect.ps1 -Limit 100           # more rows
  .\scripts\db_inspect.ps1 -Chat 4242           # one chat (chats + messages sections)
  .\scripts\db_inspect.ps1 -Counts              # one key=value line per number (the block scripts/test-restart.ps1 compares)
  .\scripts\db_inspect.ps1 -Path .\zzscratch\x.db
  .\scripts\db_inspect.ps1 -Rebuild             # force the go build of bin\dbinspect.exe

Exit: 0 — the report was printed; 1 — the database could not be opened/read (missing file, not a database);
#>

[CmdletBinding()]
param(
    [string]$Path,
    [int]$Limit = 20,
    [long]$Chat = 0,
    [switch]$Integrity,
    [switch]$Counts,
    [switch]$Rebuild
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$exe = Join-Path $repoRoot 'bin\dbinspect.exe'

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

if ($Limit -le 0) {
    Write-Output "db_inspect: -Limit is $Limit, want a positive number of rows "
    exit 1
}

if (-not $Path) { $Path = $env:DB_PATH }
if (-not $Path) { $Path = ReadEnvValue 'DB_PATH' }
if (-not $Path) {
    Write-Output 'db_inspect: DB_PATH is not set in the process ENV or in .env — pass -Path ..\data\bot.db '
    exit 1
}

# which is what scripts/run.ps1 uses. GetFullPath collapses the ".\" and any ".." so the report names ONE
# reliability), and Join-Path alone would print C:\Tools\tg-llm-proxy.\data\bot.db.
if (-not [System.IO.Path]::IsPathRooted($Path)) { $Path = Join-Path $repoRoot $Path }
$Path = [System.IO.Path]::GetFullPath($Path)

Push-Location $repoRoot
try {
    # it is made of are newer than it is. Without that check a reader written last week would keep answering
    $needsBuild = $Rebuild.IsPresent -or -not (Test-Path -LiteralPath $exe)
    if (-not $needsBuild) {
        $exeStamp = (Get-Item -LiteralPath $exe).LastWriteTimeUtc
        $sources = @(Get-ChildItem -LiteralPath (Join-Path $repoRoot 'cmd\dbinspect') -File -Filter *.go)
        $sources += Get-ChildItem -LiteralPath (Join-Path $repoRoot 'internal\store') -File -Filter *.go
        $sources += Get-ChildItem -LiteralPath (Join-Path $repoRoot 'internal\store\migrations') -File -Filter *.sql
        foreach ($src in $sources) {
            if ($src.LastWriteTimeUtc -gt $exeStamp) { $needsBuild = $true; break }
        }
    }
    if ($needsBuild) {
        $env:CGO_ENABLED = '0'
        New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'bin') | Out-Null
        $build = (go build -o bin/dbinspect.exe ./cmd/dbinspect 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output "db_inspect: go build ./cmd/dbinspect exit $LASTEXITCODE — the reasons above "
            exit 1
        }
        Write-Output 'db_inspect: built bin\dbinspect.exe '
    }

    $toolArgs = @('-path', $Path, '-limit', $Limit)
    if ($Chat -ne 0) { $toolArgs += @('-chat', $Chat) }
    if ($Integrity) { $toolArgs += '-integrity' }
    if ($Counts) { $toolArgs += '-counts' }
    & $exe @toolArgs
    $code = $LASTEXITCODE
    if ($code -eq 0) {
        Write-Output ''
        Write-Output 'db_inspect: OK — read through mode=ro : the file of was not written to and its journal_mode was not changed'
        exit 0
    }
    if ($code -eq 2) {
        Write-Output 'db_inspect: integrity_check is NOT ok  — stop the bot, copy the file to a backup of, then decide about a restore'
        exit 2
    }
    Write-Output "db_inspect: exit $code — the database was not read "
    exit 1
}
finally {
    Pop-Location
}
