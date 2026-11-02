#Requires -Version 7
<#
  Docker demon + допдо (network GOPROXY, .env token, STRATA network, dirs structure, git repo).

Usage:
  . .\scripts\Setup.ps1                              # PATH: git + GOPROXY/GOCACHE/GOPATH
  pwsh -NoLogo -File scripts\env-check.ps1           # or just: .\scripts\env-check.ps1

Exit: 0 — all required items green; 1 — red FAILs (WARN-rotm not blocking).
#>

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$script:results = @()

function Check($name, $ok, $note, $warnOnly = $false) {
    $status = 'FAIL'
    if ($ok) { $status = 'OK' }
    elseif ($warnOnly) { $status = 'WARN' }
    $script:results += , [pscustomobject] [ordered] @{ Item = $name; Status = $status; Note = $note }
}

# --- 1. pwsh ---
$psMajor = $PSVersionTable.PSVersion.Major
Check 'pwsh >= 7' ($psMajor -ge 7) "current: $($PSVersionTable.PSVersion)" $false

# --- 2. Go (plan: 1.27 pebbled std) ---
$goVer = ''
if (Get-Command -Name go -ErrorAction SilentlyContinue) {
    $goVer = (go version 2>&1 | Select-Object -First 1)
}
Check 'Go on PATH' ($goVer -ne '') "go version: $goVer" $false

# --- 3. git ---
$gitFound = Get-Command -Name git -ErrorAction SilentlyContinue
$gitExe = 'C:\Program Files\Git\cmd\git.exe'
if ($gitFound) {
    $gitVer = (git --version 2>&1 | Select-Object -First 1)
    Check 'git on PATH' ($gitVer -ne '') "$gitVer" $false
}
elseif ($IsWindows -and (Test-Path -LiteralPath $gitExe)) {
    $gitVerAbs = (& $gitExe '--version' 2>&1 | Select-Object -First 1)
    Check 'git (absolute resolve)' $true "$gitVerAbs via $gitExe — PATH-resolve failed in this pwsh session (command cache); HKCU\Environment!Path has git dir → new terminals / VS Code resolve `git`. Current session: dot-source . .\scripts\Setup.ps1" $true
}
else {
    $installHint = 'install Git with your platform package manager (winget on Windows, Homebrew on macOS, apt/dnf on Linux)'
    Check 'git on PATH' $false "git not installed — $installHint" $false
}

Check 'git repo initialized' (Test-Path -LiteralPath (Join-Path $repoRoot '.git')) "repo root: $repoRoot" $false

# --- 5. Docker demon + compose ---
$dockerCmd = Get-Command -Name docker -ErrorAction SilentlyContinue
if ($dockerCmd) {
    $composeOut = (docker compose version 2>&1 | Select-Object -First 1)
    $composeOk = ($composeOut -match 'Compose version v[2-9]')
    Check 'Demon Docker + compose' $composeOk "compose: $composeOut" $false
}
else {
    Check 'Demon Docker + compose' $false 'docker not on PATH — start Docker Desktop on Windows/macOS or Docker Engine on Linux' $false
}

# --- 6. C-compiler: not needed (pebbled std pure-Go sqlite3) — info ---
$cc = ''
foreach ($cand in @('gcc', 'cl', 'clang')) {
    $found = Get-Command -Name $cand -ErrorAction SilentlyContinue
    if ($found) { $cc = $cand; break }
}
Check 'C-compiler' $true "present: $(if ($cc) { $cc } else { 'none' }) — not needed: pebbled std uses pure-Go modernc.org/sqlite " $true

$gopath = $env:GOPATH
if (-not $gopath) { $gopath = (go env GOPATH 2>&1 | Select-Object -First 1) }
$gocache = $env:GOCACHE
if (-not $gocache) { $gocache = (go env GOCACHE 2>&1 | Select-Object -First 1) }
$goproxy = $env:GOPROXY
if (-not $goproxy) { $goproxy = (go env GOPROXY 2>&1 | Select-Object -First 1) }
$cacheNote = "GOPROXY=$goproxy GOCACHE=$gocache GOPATH=$gopath"
Check 'Go cache + proxy vars' (($goproxy -ne '') -and ($gocache -ne '')) "$cacheNote — for project-local cache use scripts\Setup.ps1 " $true

$missingDirs = @()
foreach ($d in @('cmd\bot', 'internal', 'bin', 'data', 'scripts')) {
    if (-not (Test-Path -LiteralPath (Join-Path $repoRoot $d) -PathType 'Container')) { $missingDirs += , $d }
}
Check 'dirs structure' ($missingDirs.Count -eq 0) "missing: $(if ($missingDirs.Count) { $missingDirs -join ', ' } else { 'none' }) — Day 1 code creation in progress" $true

$envPath = Join-Path $repoRoot '.env'
$tokenSet = $false
$envNote = 'TOKEN not set'
if (Test-Path -LiteralPath $envPath) {
    foreach ($line in Get-Content -LiteralPath $envPath) {
        if ($line -match '^\s*TELEGRAM_BOT_TOKEN\s*=\s*\S') { $tokenSet = $true }
    }
    $envNote = "from $envPath"
}
elseif ($env:TELEGRAM_BOT_TOKEN) {
    $tokenSet = true
    $envNote = 'from process ENV (no .env file)'
}
if ($tokenSet) { $envNote = "TELEGRAM_BOT_TOKEN set ($envNote)" }
Check '.env: token' $tokenSet "$envNote — required: copy .env.example → .env or export (Docker compose: env_file)" $true

$base = $env:STRATA_BASE_URL
if (-not $base) {
    if (Test-Path -LiteralPath $envPath) {
        foreach ($line in Get-Content -LiteralPath $envPath) {
            if ($line -match '^\s*STRATA_BASE_URL\s*=\s*(\S+)') { $base = $matches[1] }
        }
    }
}
if (-not $base) { $base = 'http://host.docker.internal:8000' }
$modelsOk = $false
$modelsNote = "GET $base/v1/models — not reachable (network error)"
try {
    $resp = Invoke-WebRequest -Uri "$base/v1/models" -Method Get -TimeoutSec 3 -ErrorAction Stop
    $modelsOk = ($resp.StatusCode -eq 200)
    $modelsNote = "GET $base/v1/models → HTTP $($resp.StatusCode)"
}
catch {
    $modelsNote = "GET $base/v1/models — error: $($_.Exception.Message)"
}
Check 'STRATA network' $modelsOk "$modelsNote " $true

$sqliteCli = Get-Command -Name sqlite3 -ErrorAction SilentlyContinue
Check 'sqlite3 CLI' $true "present: $(if ($sqliteCli) { 'yes' } else { 'no' }) — plan: no CLI expected, use Go-tool scripts/db_inspect.ps1" $true

# Measured 2026-10-31 on this machine: `Invoke-RestMethod …/getMe` answers 200 (WinINET proxy
# 127.0.0.1:10808 is enabled in HKCU\…\Internet Settings), while the same request from a Go process times
# out: http.Transport honours only HTTPS_PROXY/HTTP_PROXY of the PROCESS ENV, and the registry proxy is not
# part of that ENV. So a check that asks PowerShell reports «network OK» while run.ps1 dies at getMe of
# the check). Inside the container the story is different — there the same binary polls Telegram directly.
$tcpOk = $false
$tcpNote = ''
try {
    $client = [System.Net.Sockets.TcpClient]::new()
    $begin = $client.BeginConnect('api.telegram.org', 443, $null, $null)
    $tcpOk = $begin.AsyncWaitHandle.WaitOne(2000) -and $client.Connected
    $client.Close()
}
catch {
    $tcpOk = $false
}
if ($tcpOk) { $tcpNote = 'a direct TCP connect to api.telegram.org:443 succeeds' }
else { $tcpNote = 'a direct TCP connect to api.telegram.org:443 fails' }

$goProxy = $env:HTTPS_PROXY
if (-not $goProxy) { $goProxy = $env:https_proxy }
$systemProxy = ''
if ($IsWindows) {
    try {
        $inet = Get-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' -ErrorAction SilentlyContinue
        if ($inet -and $inet.ProxyEnable -eq 1) { $systemProxy = $inet.ProxyServer }
    }
    catch {
        $systemProxy = ''
    }
}
if ($tcpOk -or $goProxy) {
    $bypassNote = if ($env:NO_PROXY) { "NO_PROXY=$env:NO_PROXY" } else { 'NO_PROXY is empty, so a model of on the loopback would be sent through the proxy — run.ps1 -UseSystemProxy sets the bypass' }
    $proxyNote = if ($goProxy) { "the Go process will use HTTPS_PROXY=$goProxy ($bypassNote)" } else { 'no HTTPS_PROXY is needed' }
    Check 'Telegram API from Go' $true "$tcpNote; $proxyNote "
}
else {
    $hint = if ($systemProxy) { "the system proxy is $systemProxy — start the bot with scripts/run.ps1 -UseSystemProxy or export HTTPS_PROXY=http://$systemProxy" } else { 'no system proxy is enabled either — the bot needs a route to api.telegram.org or a container ' }
    Check 'Telegram API from Go' $false "$tcpNote and HTTPS_PROXY is empty — run.ps1 will stop at getMe; $hint" $true
}

# --- Output ---
Write-Output ''
Write-Output "env-check — repo: $repoRoot"
$script:results | Format-Table -AutoSize -Wrap
$fails = @($script:results | Where-Object { $_.Status -eq 'FAIL' })
$warns = @($script:results | Where-Object { $_.Status -eq 'WARN' })
Write-Output ''
Write-Output "Summary: total $($script:results.Count) | FAIL $($fails.Count) | WARN $($warns.Count)"
if ($fails.Count -gt 0) {
    foreach ($f in $fails) { Write-Output "FAIL: $($f.Item) :: $($f.Note)" }
    exit 1
}
Write-Output 'env-check: GREEN (required items OK)'
exit 0

