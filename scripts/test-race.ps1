#Requires -Version 7
<#
test-race.ps1 — §9 row «scripts/test-race.ps1»: «docker build --target test -t tg-llm-proxy-test . +
docker run --rm tg-llm-proxy-test (CGO_ENABLED=1 go test -race ./...)».

Why a container (§3.1, §11): the race detector needs cgo, cgo needs a C toolchain, and this Windows host
has none. The `test` stage of the Dockerfile installs gcc + musl-dev and runs gofmt + vet + `go test -race`
inside the image (§10 День 5), so §10 День 6 DoD «тесты гонок живут в контейнере» is executable at all.

The same thing through compose (§8.1):  docker compose --profile test run --rm test

Usage:
  . .\scripts\Setup.ps1
  .\scripts\test-race.ps1                 # build the test image, then run it
  .\scripts\test-race.ps1 -SkipBuild      # reuse the image that is already in the local store
  .\scripts\test-race.ps1 -Tagged         # also race the //go:build integration tests of §11

Exit: 0 — the race run is clean; 1 — docker is missing, the build failed or a race/test was reported.
#>

[CmdletBinding()]
param(
    [switch]$SkipBuild,
    [switch]$Tagged
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$image = 'tg-llm-proxy-test:latest'

if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) {
    Write-Output 'test-race: docker is not on PATH (§2: start Docker Desktop) — the race detector cannot run on this host (§3.1)'
    exit 1
}

Push-Location $repoRoot
try {
    if (-not $SkipBuild) {
        $build = (docker build --target test -t tg-llm-proxy-test . 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output "test-race: docker build --target test exit $LASTEXITCODE (§8.1): the last lines follow"
            Write-Output (($build -split "`r?`n" | Select-Object -Last 25) -join "`n")
            exit 1
        }
        Write-Output "test-race: image $image is built (§8.1 test stage: gcc + musl-dev, CGO_ENABLED=1)"
    }

    # -tags integration is the §11 Integration level under the race detector: the polling goroutine of §5,
    # the per-chat lock of §12 and the SQLite handle are exactly where a data race would live (§10 День 6).
    if ($Tagged) {
        $cmd = 'set -e; gofmt -l . > /tmp/f.txt; if [ -s /tmp/f.txt ]; then cat /tmp/f.txt; exit 1; fi; go vet -tags integration ./...; CGO_ENABLED=1 go test -race -tags integration ./... -count=1'
        $run = (docker run --rm $image sh -c $cmd 2>&1 | Out-String)
    }
    else {
        $run = (docker run --rm $image 2>&1 | Out-String)
    }
    Write-Output (($run -split "`r?`n" | Select-Object -Last 40) -join "`n")
    if ($LASTEXITCODE -ne 0) {
        Write-Output "test-race: RED (exit $LASTEXITCODE) — §11 Concurrency level: a race or a red test inside the container"
        exit 1
    }
    Write-Output 'test-race: GREEN — go vet + go test -race inside the container, no data races (§11 Concurrency, §3.1)'
    exit 0
}
finally {
    Pop-Location
}
