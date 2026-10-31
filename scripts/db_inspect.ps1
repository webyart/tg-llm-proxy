#Requires -Version 7
<#
db_inspect.ps1 — the reader of the §5 database. README «Environment» promised it (§2: «sqlite3 CLI | absent |
expected (§5) → use scripts/db_inspect.ps1 (Go-tool)»), and it is the only way to look at the chat history
without a Telegram account: /history of §6 needs a chat the bot already serves.

It runs `go build -o bin\dbinspect.exe ./cmd/dbinspect` on demand and then that binary, because §3.1 says the
host has no C toolchain and §5 says the DSN of a viewer is a separate, read-only one: mode=ro, no
`_journal_mode`, no `_txlock` — the script can look at the database of a RUNNING bot and cannot change it
(internal/store/inspect.go, TestOpenReadOnlyCannotWriteAndCannotRemode).

DB_PATH comes from -Path, from the process ENV, or from `.env` (§7 in that order of precedence), and the value
is never printed with a token-like shape (§12 security: a path is not a secret, but the script does not echo
ENV lines wholesale the way `Get-Content .env` would).

Usage:
  . .\scripts\Setup.ps1
  .\scripts\db_inspect.ps1                      # DB_PATH of §7, 20 rows of each section
  .\scripts\db_inspect.ps1 -Limit 100           # more rows
  .\scripts\db_inspect.ps1 -Chat 4242           # one chat (chats + messages sections)
  .\scripts\db_inspect.ps1 -Integrity           # PRAGMA integrity_check of §15
  .\scripts\db_inspect.ps1 -Path .\zzscratch\x.db
  .\scripts\db_inspect.ps1 -Rebuild             # force the go build of bin\dbinspect.exe

Exit: 0 — the report was printed; 1 — the database could not be opened/read (missing file, not a database);
2 — the file opened and integrity_check said it is NOT sound (§10 День 2 DoD: о битом файле нельзя молчать).
#>

[CmdletBinding()]
param(
    [string]$Path,
    [int]$Limit = 20,
    [long]$Chat = 0,
    [switch]$Integrity,
    [switch]$Rebuild
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$exe = Join-Path $repoRoot 'bin\dbinspect.exe'

function ReadEnvValue([string]$name) {
    # The .env loader of §7 — the same rule run.ps1 uses: KEY=VALUE, optional quotes. Only the one name is
    # taken out of the file, so a token on another line cannot reach the console by accident (§12 security).
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
    Write-Output "db_inspect: -Limit is $Limit, want a positive number of rows (§9: 0 would print an empty section and look like an empty database)"
    exit 1
}

if (-not $Path) { $Path = $env:DB_PATH }
if (-not $Path) { $Path = ReadEnvValue 'DB_PATH' }
if (-not $Path) {
    Write-Output 'db_inspect: DB_PATH is not set in the process ENV or in .env — pass -Path ..\data\bot.db (§7 row DB_PATH)'
    exit 1
}

# A relative DB_PATH (§7 default ./data/bot.db) is relative to the bot's working directory = the repo root,
# which is what scripts/run.ps1 uses. GetFullPath collapses the ".\" and any ".." so the report names ONE
# canonical path — the operator must be able to compare it with the path in the bot's own log line (§12
# reliability), and Join-Path alone would print C:\Tools\tg-llm-proxy.\data\bot.db.
if (-not [System.IO.Path]::IsPathRooted($Path)) { $Path = Join-Path $repoRoot $Path }
$Path = [System.IO.Path]::GetFullPath($Path)

Push-Location $repoRoot
try {
    # bin/ is gitignored (§10 День 1) — this binary is an artifact, so the script rebuilds it when the sources
    # it is made of are newer than it is. Without that check a reader written last week would keep answering
    # questions about a schema that has moved on, and a stale VIEWER is worse than no viewer (§12 reliability).
    $needsBuild = $Rebuild.IsPresent -or -not (Test-Path -LiteralPath $exe)
    if (-not $needsBuild) {
        $exeStamp = (Get-Item -LiteralPath $exe).LastWriteTimeUtc
        $sources = @(Get-ChildItem -LiteralPath (Join-Path $repoRoot 'cmd\dbinspect') -File -Filter *.go)
        $sources += Get-ChildItem -LiteralPath (Join-Path $repoRoot 'internal\store') -File -Filter *.go
        foreach ($src in $sources) {
            if ($src.LastWriteTimeUtc -gt $exeStamp) { $needsBuild = $true; break }
        }
    }
    if ($needsBuild) {
        # CGO_ENABLED=0 (§3.1): the pure-Go driver of §5 needs no C toolchain, so the viewer builds on a host
        # where -race cannot (§11).
        $env:CGO_ENABLED = '0'
        New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'bin') | Out-Null
        $build = (go build -o bin\dbinspect.exe ./cmd/dbinspect 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output "db_inspect: go build ./cmd/dbinspect exit $LASTEXITCODE — the reasons above (§9)"
            exit 1
        }
        Write-Output 'db_inspect: built bin\dbinspect.exe (a maintenance reader, deliberately not in the image of §8.1)'
    }

    $toolArgs = @('-path', $Path, '-limit', $Limit)
    if ($Chat -ne 0) { $toolArgs += @('-chat', $Chat) }
    if ($Integrity) { $toolArgs += '-integrity' }
    & $exe @toolArgs
    $code = $LASTEXITCODE
    if ($code -eq 0) {
        Write-Output ''
        Write-Output 'db_inspect: OK — read through mode=ro (§5): the file of §5 was not written to and its journal_mode was not changed'
        exit 0
    }
    if ($code -eq 2) {
        Write-Output 'db_inspect: integrity_check is NOT ok (§10 День 2 DoD) — stop the bot, copy the file to a backup of §15, then decide about a restore'
        exit 2
    }
    Write-Output "db_inspect: exit $code — the database was not read (§12 reliability: an unreadable file is reported, never shown as an empty history)"
    exit 1
}
finally {
    Pop-Location
}
