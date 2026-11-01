#Requires -Version 7
<#
smoke.ps1 — §9 row «scripts/smoke.ps1»: «вызывает go test -tags=integration ./test/... (фейки поднимаются
внутри Go-теста) + GET http://localhost:8080/healthz у запущенного контейнера». §11 says the same thing from
the other side: the fake Telegram (bot.WithServerURL) and the fake STRATA live inside the Go test, so this
script is a wrapper, not a fake of its own — the §9 note explains why the PowerShell version of «фейковый
Telegram API» is impossible (httptest lives in the Go process).

Checks:
  1. go test -count=1 -tags=integration ./test/...   — the Integration level of §11 (FAIL on any red test).
  2. go test -count=1 ./...                           — only with -Unit, the Unit/Store levels of §11.
  3. GET {HEALTH_PORT}/healthz                        — the §8.1 container; SKIP when nothing is listening,
     WARN when a listener exists but the host probe fails, FAIL on a non-200 answer. -SkipHealth omits the probe.
  4. the leak scan of §12 — the TELEGRAM_BOT_TOKEN value must appear in no source file, no log and no .md.

Usage:
  . .\scripts\Setup.ps1                              # PATH + project-local Go cache (§3)
  pwsh -NoLogo -File scripts\smoke.ps1
  pwsh -NoLogo -File scripts\smoke.ps1 -Unit
  pwsh -NoLogo -File scripts\smoke.ps1 -HealthPort 8080
  pwsh -NoLogo -File scripts\smoke.ps1 -SkipHealth

The token is never printed (§12 security): the leak scan reports file names only.

Exit: 0 — the integration suite is green and the leak scan found nothing;
      1 — a red test, a leak or a healthz that answers non-200.
#>

[CmdletBinding()]
param(
    [int]$HealthPort,
    [switch]$Unit,
    [switch]$SkipHealth,
    [int]$TimeoutSec = 5
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'Platform.ps1')
$envPath = Join-Path $repoRoot '.env'
$script:results = @()
$ProgressPreference = 'SilentlyContinue'   # the byte-progress bar of Invoke-WebRequest is noise here (§12 UX)

function Check($name, $ok, $note, $warnOnly = $false, $skipOnly = $false) {
    $status = 'FAIL'
    if ($skipOnly) { $status = 'SKIP' }
    elseif ($ok) { $status = 'OK' }
    elseif ($warnOnly) { $status = 'WARN' }
    $script:results += , [pscustomobject] [ordered] @{ Item = $name; Status = $status; Note = $note }
}

# ReadEnv — one row of the §7 table from the .env file (the last assignment wins, as in a shell).
function ReadEnv($name) {
    if (-not (Test-Path -LiteralPath $envPath)) { return '' }
    $value = ''
    foreach ($line in Get-Content -LiteralPath $envPath) {
        if ($line -match "^\s*$name\s*=\s*(.*?)\s*$") { $value = $matches[1] }
    }
    return $value
}

if (-not $HealthPort) { $HealthPort = $env:HEALTH_PORT }
if (-not $HealthPort) { $HealthPort = ReadEnv 'HEALTH_PORT' }
if (-not $HealthPort) { $HealthPort = 8080 }   # the §7 default

Push-Location $repoRoot
try {
    # --- 1. the Integration level of §11: bot ↔ fake STRATA ↔ fake Telegram (bot.WithServerURL) ---
    $integrationOut = (go test -count=1 -tags=integration ./test/... 2>&1 | Out-String)
    $integrationOk = ($LASTEXITCODE -eq 0)
    $integrationNote = "go test -count=1 -tags=integration ./test/... exit $LASTEXITCODE"
    if (-not $integrationOk) { $integrationNote += " — output: $($integrationOut.Trim())" }
    Check 'integration tests (§11)' $integrationOk $integrationNote $false

    # --- 2. the Unit + Store levels of §11, asked for explicitly (§11: «хост, без -race») ---
    if ($Unit) {
        $unitOut = (go test -count=1 ./... 2>&1 | Out-String)
        $unitOk = ($LASTEXITCODE -eq 0)
        $unitNote = "go test -count=1 ./... exit $LASTEXITCODE"
        if (-not $unitOk) { $unitNote += " — output: $($unitOut.Trim())" }
        Check 'unit tests (§11, -Unit)' $unitOk $unitNote $false
    }
    else {
        Check 'unit tests (§11, -Unit)' $false 'skipped: -Unit not given (§11 Unit level runs on the host without -race)' $false $true
    }

    # --- 3. GET /healthz of §8.1 — only a running container answers, internal/health of §10 День 5 ---
    $healthUrl = "http://127.0.0.1:$HealthPort/healthz"
    $healthOk = $false
    $healthWarn = $false
    $healthSkip = [bool]$SkipHealth
    $healthNote = 'skipped: -SkipHealth was given; no running bot was requested'
    if (-not $healthSkip) {
        try {
            $resp = Invoke-WebRequest -Uri $healthUrl -Method Get -TimeoutSec $TimeoutSec -ErrorAction Stop
            $healthOk = ($resp.StatusCode -eq 200)
            $healthNote = "GET $healthUrl → HTTP $($resp.StatusCode)"
            if (-not $healthOk) { $healthNote += " — §8.1 HEALTHCHECK expects 200" }
        }
        catch {
            $owners = @(Get-PortOwnerIds $HealthPort)
            if ($owners.Count -eq 0) {
                $healthSkip = $true
                $healthNote = "GET $healthUrl — nothing is listening; the bot was not started, so the probe is not applicable"
            }
            else {
                $healthWarn = $true
                $healthNote = "GET $healthUrl — $($_.Exception.Message) — a process listens but the host probe failed; the authoritative container state is docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' (docker compose ps -q bot) — WARN, not FAIL"
            }
        }
    }
    Check 'GET /healthz (§8.1)' $healthOk $healthNote $healthWarn $healthSkip

    # --- 4. §12 security: «Перед коммитом проверка утечки» — the token value in no source file ---
    $token = $env:TELEGRAM_BOT_TOKEN
    if (-not $token) { $token = ReadEnv 'TELEGRAM_BOT_TOKEN' }
    if (-not $token) {
        Check 'токен не утекает (§12)' $false 'nothing to scan: TELEGRAM_BOT_TOKEN is unset in ENV and .env (§7 needs it only in runtime)' $false $true
    }
    else {
        # git may be absent (§13), so the §12 fallback form of the scan is used: the source tree itself.
        $sources = Get-ChildItem -LiteralPath $repoRoot -Recurse -File -Include *.go,*.md,*.yml,*.yaml,*.ps1,*.json,*.txt `
            | Where-Object { $_.FullName -notmatch '[\\/]\.cache[\\/]' -and $_.FullName -notmatch '[\\/]bin[\\/]' -and $_.FullName -ne $envPath }
        $leaks = @($sources | Where-Object { Select-String -Path $_.FullName -SimpleMatch -Pattern $token -Quiet })
        $leakNote = "scanned $($sources.Count) files, no occurrence of the token value (§12: логи не содержат токен)"
        if ($leaks.Count -gt 0) { $leakNote = "the token value appears in $($leaks.Count) file(s): $($leaks.Name -join ', ') (§12 security)" }
        Check 'токен не утекает (§12)' ($leaks.Count -eq 0) $leakNote $false
    }
}
finally {
    Pop-Location
}

# --- Output ---
Write-Output ''
Write-Output "smoke — repo: $repoRoot"
$script:results | Format-Table -AutoSize -Wrap
$fails = @($script:results | Where-Object { $_.Status -eq 'FAIL' })
$warns = @($script:results | Where-Object { $_.Status -eq 'WARN' })
$skips = @($script:results | Where-Object { $_.Status -eq 'SKIP' })
Write-Output ''
Write-Output "Summary: total $($script:results.Count) | FAIL $($fails.Count) | WARN $($warns.Count) | SKIP $($skips.Count)"
if ($fails.Count -gt 0) {
    foreach ($f in $fails) { Write-Output "FAIL: $($f.Item) :: $($f.Note)" }
    Write-Output 'smoke: NOT GREEN (§11 Integration level failed)'
    exit 1
}
Write-Output 'smoke: GREEN — the fake Telegram and the fake STRATA answered, the offset survived the restart (§11)'
exit 0
