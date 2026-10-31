#Requires -Version 7
<#
strata-check.ps1 — §10 День 3 («scripts/strata-check.ps1 + /model check (GET {base}/models)») and
§13 risk «Нет доступа к STRATA из контейнера». It answers one question: is the endpoint of §7 ready
BEFORE the bot of День 4 starts, and if not — WHAT exactly is not ready (§12 reliability: a clear
message instead of a failure in the middle of a day).

The URL rule of §7.5 / §16 change 26 is mirrored here: STRATA_BASE_URL is stored WITHOUT "/v1", the
probes are GET {base}/v1/models and POST {base}/v1/chat/completions. A base written with "/v1" is
cut, exactly like config.NormalizeBase does, so this script can never probe "/v1/v1/...".

Usage:
  pwsh -NoLogo -File scripts\strata-check.ps1
  pwsh -NoLogo -File scripts\strata-check.ps1 -Base http://127.0.0.1:8000 -Model qwen2.5-7b-instruct
  pwsh -NoLogo -File scripts\strata-check.ps1 -SkipChat      # only the model list (§6 /model check)

Values are taken in this order: the parameter of the command line, the §7 table of the .env file, the ENV
of the shell, the default of §7. The file comes before the ENV because LoadEnv of run.ps1 writes the rows
of .env over the inherited ENV of the bot process, so .env is what the bot answers to — a check that read
the ENV first would describe the shell instead of that process (measured 2026-10-31, see Pick below).
Every value names its source on the output. The key is NEVER printed (§12 security).

Exit: 0 — the endpoint answered 2xx on both probes and the chat answer was not empty;
      1 — any FAIL below explains what to fix.
#>

[CmdletBinding()]
param(
    [string]$Base,
    [string]$Model,
    [string]$Key,
    [int]$TimeoutSec = 8,
    [switch]$SkipChat
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$envPath = Join-Path $repoRoot '.env'
$script:results = @()
$ProgressPreference = 'SilentlyContinue'   # the byte-progress bar of Invoke-WebRequest is noise here (§12 UX)

function Check($name, $ok, $note, $warnOnly = $false) {
    $status = 'FAIL'
    if ($ok) { $status = 'OK' }
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

# NormalizeBase — config.NormalizeBase of internal/config (§16 change 8, §16 change 26): trim the
# trailing slash, cut every trailing "/v1", then append exactly one "/v1".
function NormalizeBase([string]$raw) {
    $cut = $raw.Trim()
    while ($cut.EndsWith('/')) { $cut = $cut.Substring(0, $cut.Length - 1) }
    while ($cut.EndsWith('/v1')) {
        $cut = $cut.Substring(0, $cut.Length - 3)
        while ($cut.EndsWith('/')) { $cut = $cut.Substring(0, $cut.Length - 1) }
    }
    if ($cut -eq '') { return '/v1' }
    return $cut + '/v1'
}

# Pick — the value of a §7 row by the same precedence run.ps1 uses: the .env file of §7 first, then the
# ENV of the shell, then the caller's default. The order is not decoration: LoadEnv of run.ps1 writes the
# rows of .env over the inherited ENV of the child process, so the bot answers to what .env says. A check
# that reads the ambient ENV first describes the shell, not the process that has to work. Measured
# 2026-10-31: STRATA_MODEL of .env was changed to the model the endpoint serves, while this session still
# carried the previous value from an earlier run.ps1 — the script warned about a model nobody had
# configured. The source is returned with the value and printed, so the line cannot be ambiguous.
function Pick([string]$name) {
    $fromFile = ReadEnv $name
    if ($fromFile) { return [pscustomobject]@{ Name = $name; Value = $fromFile; Source = "$envPath" } }
    $fromEnv = [Environment]::GetEnvironmentVariable($name)
    if ($fromEnv) { return [pscustomobject]@{ Name = $name; Value = $fromEnv; Source = 'ENV of this shell' } }
    return [pscustomobject]@{ Name = $name; Value = ''; Source = 'not set' }
}

$basePick = Pick 'STRATA_BASE_URL'
$modelPick = Pick 'STRATA_MODEL'
$keyPick = Pick 'STRATA_API_KEY'
$healthPick = Pick 'HEALTH_PORT'

# A parameter of the command line replaces the picked value and its source, so the printed origin stays
# true when the caller overrides the §7 table for one probe.
if ($Base) { $basePick = [pscustomobject]@{ Name = 'STRATA_BASE_URL'; Value = $Base; Source = '-Base of the command line' } }
if ($Model) { $modelPick = [pscustomobject]@{ Name = 'STRATA_MODEL'; Value = $Model; Source = '-Model of the command line' } }
if ($Key) { $keyPick = [pscustomobject]@{ Name = 'STRATA_API_KEY'; Value = $Key; Source = '-Key of the command line' } }

if (-not $Base) { $Base = $basePick.Value }
if (-not $Base) { $Base = 'http://host.docker.internal:8000'; $basePick.Source = 'the §7 default' }   # §7
if (-not $Model) { $Model = $modelPick.Value }
if (-not $Model) { $Model = 'qwen2.5-7b-instruct'; $modelPick.Source = 'the §7 default' }            # §7
if (-not $Key) { $Key = $keyPick.Value }

$v1 = NormalizeBase $Base
$modelsUrl = $v1 + '/models'
$chatUrl = $v1 + '/chat/completions'
$headers = @{}
if ($Key) { $headers['Authorization'] = "Bearer $Key" }

Write-Output ''
Write-Output "strata-check — repo: $repoRoot"
Write-Output "base (as configured): $Base   ->  probes at $v1 (§7.5: the stored value carries no /v1) — from $($basePick.Source)"
Write-Output "model (as configured): $Model — from $($modelPick.Source); the §7 row must name an id the endpoint serves"
Write-Output ("key: " + $(if ($Key) { "set (not printed, §12 security) — from $($keyPick.Source)" } else { 'empty (§7 STRATA_API_KEY may be empty)' }))

# --- 0. HEALTH_PORT of §7 must not be the port of STRATA (§12 reliability) ---
# Measured on this machine: STRATA answered on 127.0.0.1:8080 while .env still carried HEALTH_PORT=8080, so
# the bot could not bind /healthz (the start now stops and names the port) and `docker compose up` could not
# publish it either ("ports are not available"). This is a configuration collision, not a STRATA failure, so
# it is a WARN here — strata-check answers for the endpoint, the port row belongs to the bot.
$healthPort = $healthPick.Value
if (-not $healthPort) { $healthPort = '8080'; $healthPick.Source = 'the §7 default' }   # §7
$probeUri = $null
try { $probeUri = [Uri]$v1 } catch { $probeUri = $null }
if ($probeUri -and $probeUri.Port -gt 0 -and "$healthPort" -eq "$($probeUri.Port)") {
    Check 'HEALTH_PORT vs STRATA port' $false "HEALTH_PORT=$healthPort is the port $v1 answers on: the bot cannot bind /healthz there and ``docker compose up`` cannot publish it — §7: point HEALTH_PORT at a free port" $true
}
else {
    $strataPort = if ($probeUri) { $probeUri.Port } else { '?' }
    Check 'HEALTH_PORT vs STRATA port' $true "HEALTH_PORT=$healthPort, STRATA answers on $strataPort (§10 День 5 DoD: /healthz must bind its own port)"
}

# --- 1. GET {base}/v1/models — the probe of §6 /model check ---
$models = @()
$modelsOk = $false
$modelsNote = ''
try {
    $resp = Invoke-WebRequest -Uri $modelsUrl -Method Get -Headers $headers -TimeoutSec $TimeoutSec -ErrorAction Stop
    $modelsOk = ($resp.StatusCode -eq 200)
    $parsed = $null
    try { $parsed = $resp.Content | ConvertFrom-Json } catch { $parsed = $null }
    if ($parsed -is [array]) {
        $models = @($parsed)
    }
    elseif ($parsed -and $parsed.data) {
        $models = @($parsed.data | ForEach-Object { $_.id })
    }
    $modelsNote = "HTTP $($resp.StatusCode), models: $(if ($models.Count) { $models -join ', ' } else { 'the answer carried no ids' })"
}
catch {
    $modelsNote = "GET $modelsUrl — $($_.Exception.Message)"
    if ($_.Exception.Response) {
        $code = [int]$_.Exception.Response.StatusCode
        $modelsNote += " (HTTP $code)"
        if ($code -eq 401 -or $code -eq 403) { $modelsNote += ' — the key of §7 is wrong or absent' }
        if ($code -eq 404) { $modelsNote += ' — the endpoint does not serve /v1/models (§16 change 26: check STRATA_BASE_URL)' }
    }
    else {
        $modelsNote += ' — the host is unreachable. From the host use http://127.0.0.1:PORT; inside compose use host.docker.internal with extra_hosts (§8.1, §13)'
    }
}
Check 'GET /v1/models' $modelsOk $modelsNote $false

# --- 2. STRATA_MODEL is listed (§7 row, §6 /model check) — WARN when the list is empty ---
$modelOk = $false
$modelNote = "STRATA_MODEL=$Model is in the list of §7"
if ($models.Count) {
    $modelOk = [bool]($models | Where-Object { $_ -eq $Model })
    if (-not $modelOk) { $modelNote = "STRATA_MODEL=$Model is NOT among: $($models -join ', ') — §7 STRATA_MODEL must name an id the endpoint serves" }
}
else {
    $modelOk = $true
    if ($modelsOk) {
        $modelNote = "cannot verify STRATA_MODEL=$Model against a list (§6 /model check gave no ids) — the chat probe below is the real check"
    }
    else {
        $modelNote = "not checked: GET /v1/models did not answer 200, so the list is unknown — the chat probe below is the real check (§6 /model check)"
    }
}
Check 'STRATA_MODEL listed' $modelOk $modelNote $true

# --- 3. POST {base}/v1/chat/completions — the shape the client of §10 День 3 sends (§14 item 5) ---
if ($SkipChat) {
    Check 'POST /v1/chat/completions' $true 'skipped (-SkipChat)' $true
}
else {
    $chatOk = $false
    $chatNote = ''
    $body = @{ model = $Model; messages = @(
            @{ role = 'user'; content = 'reply with exactly one word: ok' }
        ); stream = $false } | ConvertTo-Json -Depth 5
    $started = Get-Date
    try {
        $resp = Invoke-WebRequest -Uri $chatUrl -Method Post -Headers ($headers + @{ 'Content-Type' = 'application/json' }) `
            -Body $body -TimeoutSec $TimeoutSec -ErrorAction Stop
        $elapsed = ((Get-Date) - $started).TotalSeconds
        $json = $null
        try { $json = $resp.Content | ConvertFrom-Json } catch { $json = $null }
        $answer = ''
        if ($json -and $json.choices -and $json.choices.Count) { $answer = [string]$json.choices[0].message.content }
        $usage = ''
        if ($json -and $json.usage) { $usage = " tokens $($json.usage.prompt_tokens)/$($json.usage.completion_tokens) (§5 tokens_in/tokens_out)" }
        if ($resp.StatusCode -eq 200 -and $answer.Trim() -ne '') {
            $chatOk = $true
            $chatNote = "HTTP 200 in $([math]::Round($elapsed, 2))s, answer: '$($answer.Trim())'$usage (§10 День 3: an empty answer is an error)"
        }
        else {
            $chatNote = "HTTP $($resp.StatusCode) in $([math]::Round($elapsed, 2))s with an EMPTY answer — §10 День 3 treats it as ErrEmptyAnswer, the bot will not store a row"
        }
    }
    catch {
        $chatNote = "POST $chatUrl — $($_.Exception.Message)"
        if ($_.Exception.Response) {
            $code = [int]$_.Exception.Response.StatusCode
            $chatNote += " (HTTP $code)"
            if ($code -ge 500) { $chatNote += ' — retryable class of §10 День 3 (2 попытки, экспоненциальная задержка)' }
            if ($code -eq 401 -or $code -eq 403) { $chatNote += ' — the key of §7: not retryable, fix STRATA_API_KEY' }
            if ($code -eq 404) { $chatNote += ' — wrong URL: STRATA_BASE_URL must carry no /v1 (§16 change 26)' }
            if ($code -eq 400) { $chatNote += " — the endpoint rejected the request, check STRATA_MODEL=$Model" }
        }
        else {
            $chatNote += " — no answer within ${TimeoutSec}s (§7 STRATA_TIMEOUT_SECONDS is the runtime budget)"
        }
    }
    Check 'POST /v1/chat/completions' $chatOk $chatNote $false
}

# --- Output ---
Write-Output ''
$script:results | Format-Table -AutoSize -Wrap
$fails = @($script:results | Where-Object { $_.Status -eq 'FAIL' })
$warns = @($script:results | Where-Object { $_.Status -eq 'WARN' })
Write-Output ''
Write-Output "Summary: total $($script:results.Count) | FAIL $($fails.Count) | WARN $($warns.Count)"
if ($fails.Count -gt 0) {
    foreach ($f in $fails) { Write-Output "FAIL: $($f.Item) :: $($f.Note)" }
    Write-Output 'strata-check: STRATA is NOT ready (§14 item 5 must be solved before День 4)'
    exit 1
}
Write-Output 'strata-check: GREEN — the endpoint speaks the OpenAI shape of §14 item 5'
exit 0

