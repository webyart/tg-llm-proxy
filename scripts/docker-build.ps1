#Requires -Version 7
<#
docker-build.ps1 — §9 row «scripts/docker-build.ps1»: «docker compose build» (§8.1 «Сборка образа»).

Both targets of the Dockerfile are built: `run` (alpine + ca-certificates + tzdata, USER 65534, the
HEALTHCHECK of §8.1) and the `test` profile image of §9 that carries gcc + musl-dev for `-race` (§3.1).

The second one needs its own compose call: the `test` service lives behind `profiles: ["test"]` of
docker-compose.yml, and `docker compose build` builds only the services of the default profile. Measured
2026-10-31: after a source edit this script rebuilt the bot image and left the test image four hours old, so
the reader inside it printed the sources of the previous day — a check against a stale image is not a check
(§12 reliability). The script does not duplicate the target list; it names the profile (§12: one source of
truth is docker-compose.yml).

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

    # The `test` service of §9 sits behind `profiles: ["test"]`, so the call above never touched it (measured
    # 2026-10-31: the bot image rebuilt, the test image stayed hours old). Skipped when the caller named one
    # service — `-Service bot` means exactly that.
    if (-not $Service) {
        $profileArgs = @('compose', '--profile', 'test', 'build', 'test')
        if ($NoCache) { $profileArgs += '--no-cache' }
        $profileOut = (docker @profileArgs 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output "docker-build: docker $($profileArgs -join ' ') exit $LASTEXITCODE (§9: the -race image of §3.1) — the last lines:"
            Write-Output (($profileOut -split "`r?`n" | Select-Object -Last 25) -join "`n")
            exit 1
        }
    }

    # The age is printed, not only the size: the trap this script exists to avoid is a test image older than
    # the sources it is supposed to contain (§12 reliability).
    docker image ls --format '{{.Repository}}:{{.Tag}}  {{.Size}}  built {{.CreatedSince}}' | Select-String -Pattern 'tg-llm-proxy'
    Write-Output 'docker-build: OK — the images of §8.1 are in the local store (next: scripts/docker-up.ps1, §10 День 5 DoD /healthz = 200)'
    exit 0
}
finally {
    Pop-Location
}
