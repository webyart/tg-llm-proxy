#Requires -Version 7
<#
the other side: the fake Telegram (bot.WithServerURL) and the fake STRATA live inside the Go test, so this
Telegram API» is impossible (httptest lives in the Go process).

By default the tg-llm-proxy-test:latest image is rebuilt from the Dockerfile test target and both the
integration suite and the optional -Unit suite run in that container. -Host is the explicit host Go fallback;
-SkipBuild reuses the local image. Health probes and the leak scan still run on the host.

Checks:
     WARN when a listener exists but the host probe fails, FAIL on a non-200 answer. -SkipHealth omits the probe.

Usage:
  pwsh -NoLogo -File scripts\smoke.ps1
  pwsh -NoLogo -File scripts\smoke.ps1 -Unit
  pwsh -NoLogo -File scripts\smoke.ps1 -SkipBuild
  pwsh -NoLogo -File scripts\smoke.ps1 -Host
  pwsh -NoLogo -File scripts\smoke.ps1 -HealthPort 8080
  pwsh -NoLogo -File scripts\smoke.ps1 -SkipHealth


Exit: 0 — the integration suite is green and the leak scan found nothing;
      1 — a red test, a leak or a healthz that answers non-200.
#>

[CmdletBinding()]
param(
    [int]$HealthPort,
    [switch]$Unit,
    [switch]$SkipHealth,
    [int]$TimeoutSec = 5,
    # $Host is a read-only PowerShell automatic variable, so expose -Host through an alias.
    [Alias('Host')]
    [switch]$HostTests,
    [switch]$SkipBuild
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'Platform.ps1')
$envPath = Join-Path $repoRoot '.env'
$image = 'tg-llm-proxy-test:latest'
$script:results = @()
$ProgressPreference = 'SilentlyContinue'   # the byte-progress bar of Invoke-WebRequest is noise here

function Check($name, $ok, $note, $warnOnly = $false, $skipOnly = $false) {
    $status = 'FAIL'
    if ($skipOnly) { $status = 'SKIP' }
    elseif ($ok) { $status = 'OK' }
    elseif ($warnOnly) { $status = 'WARN' }
    $script:results += , [pscustomobject] [ordered] @{ Item = $name; Status = $status; Note = $note }
}

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
if (-not $HealthPort) { $HealthPort = 8080 }   # the default

$testsReady = [bool]$HostTests
$testSetupError = ''
if (-not $HostTests) {
    if (Get-Command -Name docker -ErrorAction SilentlyContinue) {
        $testsReady = $true
    }
    else {
        $testSetupError = 'docker is not on PATH — use -Host for the explicit host Go fallback'
    }
}

Push-Location $repoRoot
try {
    if ($testsReady -and -not $HostTests -and -not $SkipBuild) {
        $build = (docker build --target test -t $image . 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            $testsReady = $false
            $buildTail = (($build -split "`r?`n" | Select-Object -Last 25) -join "`n")
            $testSetupError = "docker build --target test exit $LASTEXITCODE — output: $buildTail"
        }
    }

    $integrationExit = 1
    if ($testsReady) {
        $goArgs = @('test', '-count=1', '-shuffle=on', '-tags=integration', './test/...')
        if ($HostTests) {
            $integrationOut = (& go @goArgs 2>&1 | Out-String)
        }
        else {
            $dockerArgs = @('run', '--rm', $image, 'go') + $goArgs
            $integrationOut = (& docker @dockerArgs 2>&1 | Out-String)
        }
        $integrationExit = $LASTEXITCODE
    }
    else {
        $integrationOut = $testSetupError
    }
    $integrationOk = ($integrationExit -eq 0)
    $integrationNote = "go test -count=1 -shuffle=on -tags=integration ./test/... exit $integrationExit"
    if (-not $integrationOk) { $integrationNote += " — output: $($integrationOut.Trim())" }
    Check 'integration tests ' $integrationOk $integrationNote $false

    if ($Unit) {
        $unitExit = 1
        if ($testsReady) {
            $goArgs = @('test', '-count=1', '-shuffle=on', './...')
            if ($HostTests) {
                $unitOut = (& go @goArgs 2>&1 | Out-String)
            }
            else {
                $dockerArgs = @('run', '--rm', $image, 'go') + $goArgs
                $unitOut = (& docker @dockerArgs 2>&1 | Out-String)
            }
            $unitExit = $LASTEXITCODE
        }
        else {
            $unitOut = $testSetupError
        }
        $unitOk = ($unitExit -eq 0)
        $unitNote = "go test -count=1 -shuffle=on ./... exit $unitExit"
        if (-not $unitOk) { $unitNote += " — output: $($unitOut.Trim())" }
        Check 'unit tests ' $unitOk $unitNote $false
    }
    else {
        Check 'unit tests ' $false 'skipped: -Unit not given ' $false $true
    }

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
            if (-not $healthOk) { $healthNote += " — HEALTHCHECK expects 200" }
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
    Check 'GET /healthz ' $healthOk $healthNote $healthWarn $healthSkip

    $secretNames = @('TELEGRAM_BOT_TOKEN', 'STRATA_API_KEY', 'WEBHOOK_SECRET_TOKEN')
    $sources = @(Get-ChildItem -LiteralPath $repoRoot -Recurse -File -Include *.go,*.md,*.yml,*.yaml,*.ps1,*.json,*.txt `
        | Where-Object { $_.FullName -notmatch '[\\/]\.cache[\\/]' -and $_.FullName -notmatch '[\\/]bin[\\/]' -and $_.FullName -ne $envPath })
    $checkedSecrets = 0
    foreach ($name in $secretNames) {
        $secret = [System.Environment]::GetEnvironmentVariable($name)
        if (-not $secret) { $secret = ReadEnv $name }
        if (-not $secret) { continue }
        $checkedSecrets++
        $leaks = @($sources | Where-Object { Select-String -Path $_.FullName -SimpleMatch -Pattern $secret -Quiet })
        $leakNote = "scanned $($sources.Count) files; $name was not found"
        if ($leaks.Count -gt 0) { $leakNote = "$name appears in $($leaks.Count) file(s): $($leaks.Name -join ', ')" }
        Check "секрет $name не утекает" ($leaks.Count -eq 0) $leakNote $false
    }
    if ($checkedSecrets -eq 0) {
        Check 'секреты не утекают' $false 'nothing to scan: runtime secrets are unset in ENV and .env' $false $true
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
    Write-Output 'smoke: NOT GREEN '
    exit 1
}
Write-Output 'smoke: GREEN — the fake Telegram and the fake STRATA answered, the offset survived the restart '
exit 0
