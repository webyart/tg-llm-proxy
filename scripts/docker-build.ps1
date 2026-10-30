#Requires -Version 7
<#
docker-build.ps1 — §9 row «scripts/docker-build.ps1»: «docker compose build» (§8.1 «Сборка образа»).

Both targets of the Dockerfile are built: `run` (alpine + ca-certificates + tzdata, USER 65534, the
HEALTHCHECK of §8.1) and the `test` profile image of §9 that carries gcc + musl-dev for `-race` (§3.1).
compose decides that from docker-compose.yml, so the script does not duplicate the target list (§12
reliability: one source of truth).

Usage:
  . .\scripts\Setup.ps1
  .\scripts\docker-build.ps1                  # docker compose build
  .\scripts\docker-build.ps1 -Service bot     # only the bot service (§8.1)
  .\scripts\docker-build.ps1 -NoCache         # after a Dockerfile edit (§8.1)

Exit: 0 — the images are in the local store; 1 — docker missing or the build failed.
#>

[CmdletBinding()]
param(
    [string]$Service,
    [switch]$NoCache
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) {
    Write-Output 'docker-build: docker is not on PATH — start Docker Desktop (§2, §10 День 0)'
    exit 1
}

Push-Location $repoRoot
try {
    $composeArgs = @('compose', 'build')
    if ($NoCache) { $composeArgs += '--no-cache' }
    if ($Service) { $composeArgs += $Service }
    $out = (docker @composeArgs 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0) {
        Write-Output "docker-build: docker $($composeArgs -join ' ') exit $LASTEXITCODE (§8.1) — the last lines:"
        Write-Output (($out -split "`r?`n" | Select-Object -Last 25) -join "`n")
        exit 1
    }
    docker image ls --format '{{.Repository}}:{{.Tag}}  {{.Size}}' | Select-String -Pattern 'tg-llm-proxy'
    Write-Output 'docker-build: OK — the images of §8.1 are in the local store (next: scripts/docker-up.ps1, §10 День 5 DoD /healthz = 200)'
    exit 0
}
finally {
    Pop-Location
}
