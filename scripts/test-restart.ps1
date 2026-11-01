#Requires -Version 7
<#
test-restart.ps1 — the kill/restart case of §10 День 4 («kill → рестарт → офсет не потерян, повторных ответов
нет») at the level of a real process. Until 2026-10-31 the reason this script did not exist was honest: the
Windows host could not reach api.telegram.org at all, so there was no live process to kill (§10 День 4 stopped
at getMe). The host reaches Telegram through the registry proxy of §2 since then (run.ps1 -UseSystemProxy),
so the case is testable end to end — and this script is that test.

One line of what it does: start the bot, wait for /healthz = 200, read the numbers of §5 through
`db_inspect.ps1 -Counts` (cmd/dbinspect, mode=ro), hard-kill the process with `taskkill /F` on Windows or
Stop-Process -Force on macOS/Linux, read the numbers again, start a second instance, and compare.

What is asserted (§5 «Offset long polling», §10 День 4 kill/restart):
  - the file survives a hard death: journal_mode is still wal and integrity_check is still ok (§5 WAL;
    §10 День 2 DoD «о битом файле нельзя молчать»);
  - bot_state still holds the offset the killed process had — not 0, not lost (§5);
  - the second instance resumes from exactly that offset: the log line of main.go «bot is polling Telegram,
    bot_state last_update_id …» names it, so Telegram is not asked for updates already answered;
  - neither run printed a panic — the update-without-message bug of 2026-10-31 killed the process inside the
    library's goroutine AFTER Telegram had deleted the update, i.e. a silent loss (§12 надёжность);
  - the token of §7 never lands in the log files: the sanitized form «bot***» belongs there, the value does
    not (§12 security, the same scan scripts/smoke.ps1 runs over the source tree).

What it does NOT check, and where that lives instead (§11):
  - «no repeated answers» as behavior: uq_messages_chat_tg_id of §5 makes a duplicate row impossible to
    write, and test/integration (TestOffsetSurvivesRestart, TestRedeliveredUpdateIsAnsweredOnce) proves the
    behavior against a fake Telegram, where an update can be redelivered on purpose;
  - the graceful stop (SIGTERM, the `signal.NotifyContext` of §10 День 1): Windows has no signal for a
    detached process (§3.1), that half of the case belongs to `docker compose stop` of §8.1.

Usage:
  . .\scripts\Setup.ps1
  .\scripts\test-restart.ps1                    # -UseSystemProxy is ON by default (§2: the only route here)
  .\scripts\test-restart.ps1 -StopExisting      # take over a bot instance started outside this script
  .\scripts\test-restart.ps1 -StopAfter         # leave nothing running when the checks are done
  .\scripts\test-restart.ps1 -Wait 90           # seconds for /healthz and the polling line
  .\scripts\test-restart.ps1 -UseSystemProxy:$false   # direct route, for a host where api.telegram.org is open

Exit: 0 — no FAIL (a WARN is printed and named); 1 — at least one FAIL, each printed with its note.
#>

[CmdletBinding()]
param(
    [int]$Wait = 60,
    [bool]$UseSystemProxy = $true,
    [switch]$StopExisting,
    [switch]$StopAfter,
    [string]$EnvFile
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not $EnvFile) { $EnvFile = Join-Path $repoRoot '.env' }
else { $EnvFile = (Resolve-Path -LiteralPath $EnvFile).Path }
$exe = Join-Path $repoRoot 'bin\bot.exe'
$script:results = @()
$ProgressPreference = 'SilentlyContinue'   # the byte-progress bar of Invoke-WebRequest is noise here (§12 UX)

# ReadEnv — one row of the §7 table out of the .env file (the last assignment wins, as in a shell). The file
# comes before the process ENV, exactly like strata-check.ps1: what the bot answers to is the .env that
# run.ps1 writes over the inherited ENV of the child.
function ReadEnv([string]$name) {
    if (-not (Test-Path -LiteralPath $EnvFile)) { return '' }
    $value = ''
    foreach ($line in Get-Content -LiteralPath $EnvFile) {
        if ($line -match "^\s*$name\s*=\s*(.*?)\s*$") {
            $value = $matches[1]
            if ($value.Length -ge 2 -and (($value[0] -eq '"' -and $value[-1] -eq '"') -or ($value[0] -eq "'" -and $value[-1] -eq "'"))) {
                $value = $value.Substring(1, $value.Length - 2)
            }
        }
    }
    return $value
}

# Check — the same three columns as scripts/strata-check.ps1 (§9: one output shape for every check script, so
# the human reads one table format all day). $warnOnly marks a fact worth naming that the case does not need.
function Check([string]$name, [bool]$ok, [string]$note, [bool]$warnOnly = $false) {
    $status = 'FAIL'
    if ($ok) { $status = 'OK' }
    elseif ($warnOnly) { $status = 'WARN' }
    $script:results += , [pscustomobject] [ordered] @{ Item = $name; Status = $status; Note = $note }
}

# BotProcesses — the bot instances of THIS repository. Get-Process matches the image name, and the repo also
# carries a root bot.exe from День 1: the path filter is what keeps that artifact out of a check about the
# poller of §5.
function BotProcesses {
    @(Get-Process -Name bot -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe })
}

# ReadCounts — the machine block of cmd/dbinspect -counts (§9). Only the `key=value` lines are parsed; the
# build note and the mode=ro trailer of db_inspect.ps1 are noise here. An empty block means the file was not
# read, and comparing two empty hashtables would be a PASS invented out of nothing (§12 reliability).
function ReadCounts() {
    $raw = (& (Join-Path $PSScriptRoot 'db_inspect.ps1') -Counts -Path $dbPath 2>&1 | Out-String)
    $table = @{}
    foreach ($line in ($raw -split "`r?`n")) {
        if ($line -match '^([a-z0-9_]+)=(\S*)$') { $table[$matches[1]] = $matches[2] }
    }
    return [pscustomobject]@{ Values = $table; Text = $raw; Code = $LASTEXITCODE }
}

# WaitForHealthz — the /healthz of §9/§8.1 seen from outside. -NoProxy on purpose: the probe goes to the
# loopback, and a proxy must never be asked to reach this machine's own port (the same reason run.ps1 sets
# NO_PROXY of §16 change 30).
function WaitForHealthz([int]$port, [int]$seconds) {
    $url = "http://127.0.0.1:$port/healthz"
    $deadline = (Get-Date).AddSeconds($seconds)
    $last = ''
    while ((Get-Date) -lt $deadline) {
        try {
            $resp = Invoke-WebRequest -Uri $url -Method Get -TimeoutSec 3 -NoProxy -ErrorAction Stop
            if ($resp.StatusCode -eq 200) {
                return [pscustomobject]@{ Ok = $true; Note = "GET $url → HTTP 200, body: $($resp.Content.Trim())" }
            }
            $last = "GET $url → HTTP $($resp.StatusCode)"
        }
        catch {
            $last = "GET $url — $($_.Exception.Message)"
        }
        Start-Sleep -Milliseconds 1000
    }
    return [pscustomobject]@{ Ok = $false; Note = "$last within ${seconds}s (§9: the JSON log of the process names the reason it is not up)" }
}

# WaitForLine — wait until the log file of a detached run contains a line. run.ps1 -Background redirects the
# stdout of the child (slog of §12 writes JSON there), so the file is the only trace of a detached process.
function WaitForLine([string]$path, [string]$pattern, [int]$seconds) {
    $deadline = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $deadline) {
        if (Test-Path -LiteralPath $path) {
            $text = Get-Content -LiteralPath $path -Raw -ErrorAction SilentlyContinue
            if ($text -and $text -match $pattern) { return $true }
        }
        Start-Sleep -Milliseconds 500
    }
    return $false
}

# StartBot — one instance through scripts/run.ps1 -Background, reported with its PID and log files. The build
# is done by the first start and skipped for the second one: the restart case is about the process and the
# file, not about compiling twice.
function StartBot([bool]$SkipBuild) {
    # A hashtable splat, NOT an array: `& script.ps1 @array` hands every element to the target .ps1 as a
    # POSITIONAL value, so '-Background' landed in the first parameter slot instead of binding by name — the
    # first version of this script failed exactly there (measured 2026-10-31 with a scratch probe: a paramless
    # .ps1 that prints the raw list it was handed; the child saw EnvFile=-Background). A hashtable binds by
    # name, and a switch parameter takes a boolean.
    $runArgs = [ordered]@{ Background = $true; EnvFile = $EnvFile; UseSystemProxy = [bool]$UseSystemProxy }
    if ($SkipBuild) { $runArgs['SkipBuild'] = $true }
    $text = (& (Join-Path $PSScriptRoot 'run.ps1') @runArgs 2>&1 | Out-String)
    $procId = 0
    $outLog = ''
    $errLog = ''
    if ($text -match 'run: PID (\d+)') { $procId = [int]$matches[1] }
    if ($text -match 'logs: (\S+) / (\S+)') { $outLog = $matches[1]; $errLog = $matches[2] }
    return [pscustomobject]@{ Id = $procId; OutLog = $outLog; ErrLog = $errLog; Text = $text; Code = $LASTEXITCODE }
}

# --- The §7 rows this script needs (DB_PATH for the reader, HEALTH_PORT for the probe) ---
$dbPath = ReadEnv 'DB_PATH'
if (-not $dbPath) { $dbPath = $env:DB_PATH }
if (-not $dbPath) { $dbPath = './data/bot.db' }        # the §16 change 9 local default
if (-not [System.IO.Path]::IsPathRooted($dbPath)) { $dbPath = Join-Path $repoRoot $dbPath }
$dbPath = [System.IO.Path]::GetFullPath($dbPath)

$port = ReadEnv 'HEALTH_PORT'
if (-not $port) { $port = $env:HEALTH_PORT }
if (-not $port) { $port = 8080 }                       # the §7 default

Write-Output ''
Write-Output "test-restart — repo: $repoRoot"
Write-Output "database: $dbPath (§7 DB_PATH, read through mode=ro — §5 case D)"
Write-Output "health: http://127.0.0.1:$port/healthz (§7 HEALTH_PORT, §9)"
Write-Output "env: $(Split-Path -Leaf $EnvFile) (§7 table; values are never printed, §12 security)"


# Finish — one table for every way this script can end (§9: the same shape as strata-check.ps1, and a FAIL is
# printed with its note because «the check failed» is not something an operator can act on (§12 надёжность).
function Finish() {
    Write-Output ''
    $script:results | Format-Table -AutoSize -Wrap
    $fails = @($script:results | Where-Object { $_.Status -eq 'FAIL' })
    $warns = @($script:results | Where-Object { $_.Status -eq 'WARN' })
    Write-Output ''
    Write-Output "Summary: total $($script:results.Count) | FAIL $($fails.Count) | WARN $($warns.Count)"
    foreach ($f in $fails) { Write-Output "FAIL: $($f.Item) :: $($f.Note)" }
    if ($fails.Count -gt 0) {
        Write-Output 'test-restart: RED — the kill/restart case of §10 День 4 did not hold (§5 offset, the WAL of §5)'
        exit 1
    }
    Write-Output 'test-restart: GREEN — the process died hard, the file answered sound, and the second instance resumed on the same offset (§5, §10 День 4)'
    exit 0
}

# --- 0. one poller at a time (§5: getUpdates has one consumer per token; a second instance would be met with
#     a 409 by Telegram, and whichever of them answers last would move the offset of bot_state) ---
$running = BotProcesses
if ($running.Count -gt 0) {
    if (-not $StopExisting) {
        Check 'one poller' $false "$($running.Count) instance(s) of bin\bot.exe are running (PID $(@($running.Id) -join ', ')) — Telegram serves one getUpdates consumer per token (§5), so this script cannot be the process it kills. Stop it, or pass -StopExisting"
        Finish
    }
    foreach ($stale in $running) { Stop-Process -Id $stale.Id -Force }
    Start-Sleep -Seconds 2
    Check 'one poller' $true "stopped $($running.Count) instance(s) started outside this script (PID $(@($running.Id) -join ', ')) — -StopExisting was asked for"
}
else {
    Check 'one poller' $true 'no instance of bin\bot.exe was running — this script starts both runs it compares'
}

if (-not (Test-Path -LiteralPath $dbPath)) {
    Check 'the database is there' $false "DB_PATH of §7 points at \"$dbPath\" and that file is absent — the restart case needs a database that already holds an offset (§5): start the bot once through scripts/run.ps1, or point the script at the file with -EnvFile"
    Finish
}

# --- 1. the numbers before anything happens (§5 through cmd/dbinspect -counts, mode=ro) ---
$before = ReadCounts
Check 'snapshot before the run' ($before.Values.Count -gt 0) "read $($before.Values.Count) numbers: last_update_id=$($before.Values['last_update_id']), table_messages=$($before.Values['table_messages']), journal_mode=$($before.Values['journal_mode']) (exit $($before.Code))"
if ($before.Values.Count -eq 0) {
    Write-Output $before.Text
    Finish
}

# --- 2. the first instance ---
$first = StartBot $false
Check 'the first start' ($first.Code -eq 0 -and $first.Id -gt 0) $(if ($first.Id -gt 0) {
        "PID $($first.Id), logs: $($first.OutLog) / $($first.ErrLog) (§9 scripts/run.ps1 -Background)"
    }
    else {
        "run.ps1 exit $($first.Code) and printed no PID — the last lines: $(@($first.Text -split "`r?`n" | Select-Object -Last 6) -join ' | ')"
    })
if ($first.Id -le 0) { Finish }

$pollPattern = 'bot is polling Telegram, bot_state last_update_id (\d+) found (true|false)'
$health1 = WaitForHealthz ([int]$port) $Wait
Check 'healthz of the first instance' $health1.Ok $health1.Note

$polling1 = WaitForLine $first.OutLog $pollPattern $Wait
$offset1 = -1
$found1 = ''
$text1 = Get-Content -LiteralPath $first.OutLog -Raw -ErrorAction SilentlyContinue
if ($text1 -and $text1 -match $pollPattern) { $offset1 = [int64]$matches[1]; $found1 = $matches[2] }
Check 'the first instance polls Telegram' $polling1 $(if ($polling1) {
        "the log names last_update_id $offset1 (bot_state row found: $found1) — §10 День 4 DoD: the poller runs, which means getMe answered with the token of §7"
    }
    else {
        "no «bot is polling Telegram» line in $($first.OutLog) within ${Wait}s — getMe needs the network and the token (§10 День 4: without them the bot stops with exit 1); the log tail: $(@((Get-Content -LiteralPath $first.OutLog -ErrorAction SilentlyContinue) | Select-Object -Last 5) -join ' | ')"
    })


# --- 3. the numbers of the live process, then a death it did not choose ---
$atKill = ReadCounts
Check 'snapshot before the kill' ($atKill.Values.Count -gt 0) "read $($atKill.Values.Count) numbers: last_update_id=$($atKill.Values['last_update_id']), table_messages=$($atKill.Values['table_messages']) (exit $($atKill.Code))"
if ($atKill.Values.Count -eq 0) {
    Write-Output $atKill.Text
    Finish
}
$offsetAtKill = [int64]$atKill.Values['last_update_id']

if ($IsWindows) {
    $killNote = (taskkill /F /PID $first.Id 2>&1 | Out-String).Trim()
}
else {
    $killNote = "Stop-Process -Id $($first.Id) -Force (SIGKILL)"
    Stop-Process -Id $first.Id -Force
}
$gone = $false
$goneDeadline = (Get-Date).AddSeconds(15)
while ((Get-Date) -lt $goneDeadline) {
    if (-not (Get-Process -Id $first.Id -ErrorAction SilentlyContinue)) { $gone = $true; break }
    Start-Sleep -Milliseconds 300
}
Check 'the process is dead' $gone "taskkill /F /PID $($first.Id) → «$killNote»; the PID is gone from the process list: $gone (§10 День 4 asks about a hard death, so no graceful Stop-Process here)"
if (-not $gone) { Finish }

# --- 4. the file of §5 after a hard death: a reader with mode=ro recovers the WAL by itself, and the reader
#     that cannot open the file is the failure this script exists to catch (§10 День 2 DoD, §10 День 4) ---
$afterKill = ReadCounts
Check 'the database answers after the kill' ($afterKill.Values.Count -gt 0) "read $($afterKill.Values.Count) numbers through mode=ro (exit $($afterKill.Code)) — no bot was running to checkpoint the WAL of §5 for it"
if ($afterKill.Values.Count -eq 0) {
    Write-Output $afterKill.Text
    Finish
}
Check 'journal_mode is still wal' ($afterKill.Values['journal_mode'] -eq 'wal') "journal_mode=$($afterKill.Values['journal_mode']) (§5: WAL is what makes a hard death survivable; §9 the viewer never changes it)"
Check 'integrity_check after the kill' ($afterKill.Values['integrity'] -eq 'ok') "integrity=$($afterKill.Values['integrity']) (§15; §10 День 2: о битом файле нельзя молчать)"
Check 'the offset survived the kill' ($afterKill.Values['last_update_id'] -eq $offsetAtKill -and $afterKill.Values['bot_state_offset_found'] -eq 'true') "bot_state last_update_id: $($atKill.Values['last_update_id']) while it ran → $($afterKill.Values['last_update_id']) after the kill (§5: the offset is written after every processed update, because the library keeps it in memory only)"

# --- 5. the second instance: the same binary, the same file, the offset it left behind ---
$second = StartBot $true
Check 'the second start' ($second.Code -eq 0 -and $second.Id -gt 0) $(if ($second.Id -gt 0) {
        "PID $($second.Id) (scripts/run.ps1 -SkipBuild: the same bin\bot.exe that was killed, rebuilt by nothing)"
    }
    else {
        "run.ps1 exit $($second.Code) and printed no PID — the last lines: $(@($second.Text -split "`r?`n" | Select-Object -Last 6) -join ' | ')"
    })
if ($second.Id -le 0) { Finish }

$health2 = WaitForHealthz ([int]$port) $Wait
Check 'healthz of the second instance' $health2.Ok "$($health2.Note) — the port of §7 was free again after the kill, so nothing had to be retried"

$polling2 = WaitForLine $second.OutLog $pollPattern $Wait
$offset2 = -1
$found2 = ''
$text2 = Get-Content -LiteralPath $second.OutLog -Raw -ErrorAction SilentlyContinue
if ($text2 -and $text2 -match $pollPattern) { $offset2 = [int64]$matches[1]; $found2 = $matches[2] }
Check 'the second instance resumes on the same offset' ($offset2 -eq $offsetAtKill -and $found2 -eq 'true') "stored last_update_id $offsetAtKill → WithInitialOffset $offset2 (bot_state found: $found2). §5: starting from 0 would re-deliver every recent update of the chat, which is what «restart does not repeat answers» of §10 День 4 is about"

# --- 6. what the two runs printed (§12 надёжность + §12 security) ---
$logs = @($first.OutLog, $first.ErrLog, $second.OutLog, $second.ErrLog) | Where-Object { $_ -and (Test-Path -LiteralPath $_) }
# The text of every log is read ONCE and kept: an empty file answers Get-Content -Raw with $null, and a method
# call on that would throw — noise on stderr in the middle of a security check is exactly what makes a real
# finding invisible (§12 надёжность: a check must not need the reader to know which of its errors are harmless).
$logText = @{}
$empty = 0
foreach ($log in $logs) {
    $body = Get-Content -LiteralPath $log -Raw -ErrorAction SilentlyContinue
    $logText[$log] = $body
    if (-not $body) { $empty += 1 }
}

$panicPattern = 'panic:|invalid memory address|nil pointer dereference|runtime error'
$panicked = @($logs | Where-Object { $logText[$_] -and $logText[$_] -match $panicPattern })
Check 'neither run panicked' ($panicked.Count -eq 0) $(if ($panicked.Count) {
        "panic text found in: $($panicked -join ', ') — a panic outside Dispatch loses the update in silence: getUpdates has already deleted it and bot_state moves only after Dispatch (the bug closed on 2026-10-31 by the recover of internal/bot/run.go)"
    }
    else {
        "$($logs.Count) log file(s) scanned for panic / nil-pointer / runtime error text, $empty of them empty (§12 надёжность; slog of §12 writes JSON to stdout, so an empty stderr means nothing bypassed the logger)"
    })

$token = ReadEnv 'TELEGRAM_BOT_TOKEN'
if (-not $token) { $token = $env:TELEGRAM_BOT_TOKEN }
if ($token.Length -ge 8) {
    # The value itself is never printed, and the comparison is a string Contains (not a regex): a token carries
    # characters that would otherwise be read as a pattern. The sanitized «bot***» form is what §6 Sanitize is
    # supposed to leave in the log, and scripts/smoke.ps1 runs the same scan over the source tree (§12 security).
    $leaked = @($logs | Where-Object { $logText[$_] -and $logText[$_].Contains($token) })
    Check 'no token in the logs' ($leaked.Count -eq 0) $(if ($leaked.Count) {
            "the value of TELEGRAM_BOT_TOKEN of §7 appears in: $($leaked -join ', ') — §12 security: the token never reaches a log file"
        }
        else {
            "the token of §7 is absent from all $($logs.Count) log file(s); library errors go through bot.Sanitize (§6), which is why a live getMe failure reads as https://api.telegram.org/bot***/getMe"
        })
}
else {
    Check 'no token in the logs' $true "TELEGRAM_BOT_TOKEN of §7 is not set in $(Split-Path -Leaf $EnvFile), so there was nothing to scan for — §7 gives it no default and the start stops without it (§10 День 4)" $true
}

$afterRestart = ReadCounts
Check 'the numbers after the restart' ($afterRestart.Values.Count -gt 0) "read $($afterRestart.Values.Count) numbers again: table_messages=$($afterRestart.Values['table_messages']), table_events=$($afterRestart.Values['table_events']), last_update_id=$($afterRestart.Values['last_update_id'])"
$lost = [int64]$before.Values['table_messages'] - [int64]$afterRestart.Values['table_messages']
Check 'no stored row was lost' ($lost -le 0) "table_messages: $($before.Values['table_messages']) before the first start → $($afterRestart.Values['table_messages']) after the second (the difference is $lost rows). Growth is Telegram delivering what arrived while the process was dead (§5 keeps undelivered updates); a decrease would be a WAL of §5 that did not survive the kill"

# --- 7. who is left running ---
if ($StopAfter) {
    Stop-Process -Id $second.Id -Force
    Check 'nothing is left running' $true "-StopAfter: PID $($second.Id) stopped — the operator starts the bot with scripts/run.ps1 when they want it back"
}
else {
    Check 'the bot is left running' $true "PID $($second.Id) is the live instance, log: $($second.OutLog) — stop it with Stop-Process -Id $($second.Id) (or taskkill /PID $($second.Id))"
}

Finish


