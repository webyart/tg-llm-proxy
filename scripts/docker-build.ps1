#Requires -Version 7
<#

Both targets of the Dockerfile are built: `run` (alpine + ca-certificates + tzdata, USER 65534, the

The second one needs its own compose call: the `test` service lives behind `profiles: ["test"]` of
docker-compose.yml, and `docker compose build` builds only the services of the default profile. Measured
2026-10-31: after a source edit this script rebuilt the bot image and left the test image four hours old, so
the reader inside it printed the sources of the previous day — a check against a stale image is not a check
truth is docker-compose.yml).

Usage:
  . .\scripts\Setup.ps1
  .\scripts\docker-build.ps1                  # docker compose build

Exit: 0 — the images are in the local store; 1 — docker missing or the build failed.
#>

[CmdletBinding()]
param(
    [string]$Service,
    [switch]$NoCache
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) {
    Write-Output 'docker-build: docker is not on PATH — start Docker Desktop '
    exit 1
}

Push-Location $repoRoot
try {
    if (-not $env:BUILD_COMMIT) {
        $env:BUILD_COMMIT = 'dev'
        if (Get-Command -Name git -ErrorAction SilentlyContinue) {
            $gitCommit = (git rev-parse --short=12 HEAD 2>$null)
            if ($LASTEXITCODE -eq 0 -and $gitCommit) {
                $env:BUILD_COMMIT = $gitCommit.Trim()
                if ((git status --porcelain 2>$null | Out-String).Trim()) { $env:BUILD_COMMIT += '-dirty' }
            }
        }
    }
    Write-Output "docker-build: BUILD_COMMIT=$env:BUILD_COMMIT"
    $composeArgs = @('compose', 'build')
    if ($NoCache) { $composeArgs += '--no-cache' }
    if ($Service) { $composeArgs += $Service }
    $out = (docker @composeArgs 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0) {
        Write-Output "docker-build: docker $($composeArgs -join ' ') exit $LASTEXITCODE  — the last lines:"
        Write-Output (($out -split "`r?`n" | Select-Object -Last 25) -join "`n")
        exit 1
    }

    # 2026-10-31: the bot image rebuilt, the test image stayed hours old). Skipped when the caller named one
    # service — `-Service bot` means exactly that.
    if (-not $Service) {
        $profileArgs = @('compose', '--profile', 'test', 'build', 'test')
        if ($NoCache) { $profileArgs += '--no-cache' }
        $profileOut = (docker @profileArgs 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output "docker-build: docker $($profileArgs -join ' ') exit $LASTEXITCODE  — the last lines:"
            Write-Output (($profileOut -split "`r?`n" | Select-Object -Last 25) -join "`n")
            exit 1
        }
    }

    # The age is printed, not only the size: the trap this script exists to avoid is a test image older than
    docker image ls --format '{{.Repository}}:{{.Tag}}  {{.Size}}  built {{.CreatedSince}}' | Select-String -Pattern 'tg-llm-proxy'
    Write-Output 'docker-build: OK — the images of are in the local store '
    exit 0
}
finally {
    Pop-Location
}
