#Requires -Version 7
<#
env-check.ps1 — §2: green checks "before work": Go, git, pwsh, C-compileryes/no,
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
elseif (Test-Path -LiteralPath $gitExe) {
    $gitVerAbs = (& $gitExe '--version' 2>&1 | Select-Object -First 1)
    Check 'git (absolute resolve)' $true "$gitVerAbs via $gitExe — PATH-resolve failed in this pwsh session (command cache); HKCU\Environment!Path has git dir → new terminals / VS Code resolve `git`. Current session: dot-source . .\scripts\Setup.ps1" $true
}
else {
    Check 'git on PATH' $false 'git not installed — winget install --id Git.Git --accept-package-agreements --accept-source-agreements' $false
}

# --- 4. git repo initialized at repo root (§2) ---
Check 'git repo initialized' (Test-Path -LiteralPath (Join-Path $repoRoot '.git')) "repo root: $repoRoot" $false

# --- 5. Docker demon + compose ---
$dockerCmd = Get-Command -Name docker -ErrorAction SilentlyContinue
if ($dockerCmd) {
    $composeOut = (docker compose version 2>&1 | Select-Object -First 1)
    $composeOk = ($composeOut -match 'Compose version v[2-9]')
    Check 'Demon Docker + compose' $composeOk "compose: $composeOut" $false
}
else {
    Check 'Demon Docker + compose' $false 'docker not on PATH — start Docker Desktop ("Start Docker Desktop")' $false
}

# --- 6. C-compiler: not needed (pebbled std pure-Go sqlite3) — info ---
$cc = ''
foreach ($cand in @('gcc', 'cl', 'clang')) {
    $found = Get-Command -Name $cand -ErrorAction SilentlyContinue
    if ($found) { $cc = $cand; break }
}
Check 'C-compiler' $true "present: $(if ($cc) { $cc } else { 'none' }) — not needed: pebbled std uses pure-Go modernc.org/sqlite (§2)" $true

# --- 7. Go cache + proxy vars (§3) ---
$gopath = $env:GOPATH
if (-not $gopath) { $gopath = (go env GOPATH 2>&1 | Select-Object -First 1) }
$gocache = $env:GOCACHE
if (-not $gocache) { $gocache = (go env GOCACHE 2>&1 | Select-Object -First 1) }
$goproxy = $env:GOPROXY
if (-not $goproxy) { $goproxy = (go env GOPROXY 2>&1 | Select-Object -First 1) }
$cacheNote = "GOPROXY=$goproxy GOCACHE=$gocache GOPATH=$gopath"
Check 'Go cache + proxy vars' (($goproxy -ne '') -and ($gocache -ne '')) "$cacheNote — for project-local cache use scripts\Setup.ps1 (§3 .cache/)" $true

# --- 8. dirs structure (§4 Day 1: cmd/bot, internal, bin, data, scripts) ---
$missingDirs = @()
foreach ($d in @('cmd\bot', 'internal', 'bin', 'data', 'scripts')) {
    if (-not (Test-Path -LiteralPath (Join-Path $repoRoot $d) -PathType 'Container')) { $missingDirs += , $d }
}
Check 'dirs structure' ($missingDirs.Count -eq 0) "missing: $(if ($missingDirs.Count) { $missingDirs -join ', ' } else { 'none' }) — Day 1 code creation in progress" $true

# --- 9. .env: TELEGRAM_BOT_TOKEN (§7) — required in runtime, checked in env-check ---
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
Check '.env: token' $tokenSet "$envNote — §7 required: copy .env.example → .env or export (Docker compose: env_file)" $true

# --- 10. STRATA network (§2: network for Go + STRATA) ---
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
Check 'STRATA network' $modelsOk "$modelsNote (Day 4 checks — network for Go proxy + STRATA, §2)" $true

# --- 11. sqlite3 CLI (§5: expected absent → use Go-tool) ---
$sqliteCli = Get-Command -Name sqlite3 -ErrorAction SilentlyContinue
Check 'sqlite3 CLI' $true "present: $(if ($sqliteCli) { 'yes' } else { 'no' }) — plan §5: no CLI expected, use Go-tool scripts/db_inspect.ps1" $true

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

