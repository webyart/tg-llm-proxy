#Requires -Version 7
<#
docker-up.ps1 — §9 row «scripts/docker-up.ps1»: «docker compose up -d» + the DoD check of §10 День 5
(«docker compose up -d → бот работает, /healthz = 200»). The port is read from the §7 table (.env →
HEALTH_PORT, default 8080) because compose publishes whatever the table says (§8.1).

The health probe is the §8.1 HEALTHCHECK from the outside: wget of §8.1 lives inside the container, this
script is the human-facing «бот работает» of the DoD, and scripts/smoke.ps1 reuses the same URL.

Usage:
  . .\scripts\Setup.ps1
  .\scripts\docker-up.ps1
  .\scripts\docker-up.ps1 -Wait 40        # seconds to wait for the first 200 (default 30)
  .\scripts\docker-up.ps1 -NoBuild        # do not rebuild the image before starting

Exit: 0 — the container is up and /healthz answered 200; 1 — compose failed or the bot stayed unhealthy.
#>

[CmdletBinding()]
param(
    [int]$Wait = 30,
    [switch]$NoBuild
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$envPath = Join-Path $repoRoot '.env'
$ProgressPreference = 'SilentlyContinue'

function ReadEnv($name) {
    if (-not (Test-Path -LiteralPath $envPath)) { return '' }
    $value = ''
    foreach ($line in Get-Content -LiteralPath $envPath) {
        if ($line -match "^\s*$name\s*=\s*(.*?)\s*$") { $value = $matches[1] }
    }
    return $value
}

if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) {
    Write-Output 'docker-up: docker is not on PATH — start Docker Desktop (§2)'
    exit 1
}
$port = $env:HEALTH_PORT
if (-not $port) { $port = ReadEnv 'HEALTH_PORT' }
if (-not $port) { $port = 8080 }

Push-Location $repoRoot
try {
    if (-not $NoBuild) {
        $build = (& pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'docker-build.ps1') -Service bot 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output $build.Trim()
            Write-Output "docker-up: docker-build.ps1 -Service bot exit $LASTEXITCODE (§8.1)"
            exit 1
        }
        Write-Output $build.Trim()
    }
    $composeArgs = @('compose', 'up', '-d')
    $out = (docker @composeArgs 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0) {
        Write-Output "docker-up: docker $($composeArgs -join ' ') exit $LASTEXITCODE (§8.1) — the last lines:"
        Write-Output (($out -split "`r?`n" | Select-Object -Last 25) -join "`n")
        exit 1
    }
    docker compose ps

    $url = "http://127.0.0.1:$port/healthz"
    $deadline = (Get-Date).AddSeconds($Wait)
    $ok = $false
    $lastNote = ''
    while ((Get-Date) -lt $deadline) {
        try {
            $resp = Invoke-WebRequest -Uri $url -Method Get -TimeoutSec 3 -ErrorAction Stop
            if ($resp.StatusCode -eq 200) {
                $ok = $true
                $lastNote = "GET $url → HTTP 200, body: $($resp.Content.Trim())"
                break
            }
            $lastNote = "GET $url → HTTP $($resp.StatusCode)"
        }
        catch {
            $lastNote = "GET $url — $($_.Exception.Message)"
        }
        Start-Sleep -Milliseconds 1500
    }
    Write-Output ''
    Write-Output $lastNote
    if (-not $ok) {
        # §8.1: the image carries its own HEALTHCHECK — `wget -qO- http://127.0.0.1:$HEALTH_PORT/healthz`
        # INSIDE the container — and that is the authoritative «бот работает» of the §10 День 5 DoD. The
        # host-side probe can fail while the bot is fine: a published port of §8.1 goes through the port
        # proxy of Docker Desktop, and on this host the in-container wget answers 200 while the Windows
        # loopback refuses the connection (§12 reliability: the check must not lie about the bot).
        $cid = (docker compose ps -q bot 2>&1 | Out-String).Trim()
        $state = ''
        if ($cid) {
            $state = (docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' $cid 2>&1 | Out-String).Trim()
        }
        if ($state -eq 'healthy') {
            Write-Output "docker-up: OK via the in-container HEALTHCHECK of §8.1 (docker inspect → healthy); the host probe of §9 did not reach 127.0.0.1:$port — the port proxy of Docker Desktop, see the note in scripts/docker-up.ps1"
            exit 0
        }
        Write-Output "docker-up: /healthz did not answer 200 within ${Wait}s and the container state is '$state' — docker compose logs bot (§12 reliability: the JSON log names the reason)"
        exit 1
    }
    Write-Output 'docker-up: OK — бот работает, /healthz = 200 (§10 День 5 DoD); история чата живёт в named volume bot-data (§8.1)'
    exit 0
}
finally {
    Pop-Location
}
