#Requires -Version 7
<#
docker run --rm tg-llm-proxy-test (CGO_ENABLED=1 go test -race ./...)».

has none. The `test` stage of the Dockerfile installs gcc + musl-dev and runs gofmt + vet + `go test -race`


Usage:
  . .\scripts\Setup.ps1
  .\scripts\test-race.ps1                 # build the test image, then run it
  .\scripts\test-race.ps1 -SkipBuild      # reuse the image that is already in the local store

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
    Write-Output 'test-race: docker is not on PATH  — the race detector cannot run on this host '
    exit 1
}

Push-Location $repoRoot
try {
    if (-not $SkipBuild) {
        $build = (docker build --target test -t tg-llm-proxy-test . 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output "test-race: docker build --target test exit $LASTEXITCODE : the last lines follow"
            Write-Output (($build -split "`r?`n" | Select-Object -Last 25) -join "`n")
            exit 1
        }
        Write-Output "test-race: image $image is built "
    }

    if ($Tagged) {
        $cmd = 'set -e; gofmt -l . > /tmp/f.txt; if [ -s /tmp/f.txt ]; then cat /tmp/f.txt; exit 1; fi; go vet -tags integration ./...; CGO_ENABLED=1 go test -race -tags integration ./... -count=1'
        $run = (docker run --rm $image sh -c $cmd 2>&1 | Out-String)
    }
    else {
        $run = (docker run --rm $image 2>&1 | Out-String)
    }
    Write-Output (($run -split "`r?`n" | Select-Object -Last 40) -join "`n")
    if ($LASTEXITCODE -ne 0) {
        Write-Output "test-race: RED (exit $LASTEXITCODE) — Concurrency level: a race or a red test inside the container"
        exit 1
    }
    Write-Output 'test-race: GREEN — go vet + go test -race inside the container, no data races '
    exit 0
}
finally {
    Pop-Location
}
