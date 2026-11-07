#Requires -Version 7
<#

Both targets of the Dockerfile are built: `run` (alpine + ca-certificates + tzdata, USER 65534) and `test`.
The `test` service lives behind `profiles: ["test"]` of docker-compose.yml, so the default invocation enables
that profile and builds bot and test in one BuildKit request. This shares their common source layers and always
refreshes both images instead of leaving a stale test image after a source edit.

Usage:
  . .\scripts\Setup.ps1
  .\scripts\docker-build.ps1                  # docker compose --profile test build bot test

Exit: 0 — the images are in the local store; 1 — docker missing or the build failed.
#>

[CmdletBinding()]
param(
    [string]$Service,
    [switch]$NoCache
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'DockerBuildLock.ps1')
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
    if ($Service) {
        $composeArgs = @('compose', 'build')
        if ($NoCache) { $composeArgs += '--no-cache' }
        $composeArgs += $Service
    }
    else {
        $composeArgs = @('compose', '--profile', 'test', 'build')
        if ($NoCache) { $composeArgs += '--no-cache' }
        $composeArgs += @('bot', 'test')
    }
    $out = (Invoke-DockerBuildWithLock -RepoRoot $repoRoot -Build {
        docker @composeArgs 2>&1 | Out-String
    })
    $buildExitCode = $LASTEXITCODE
    if ($buildExitCode -ne 0) {
        Write-Output "docker-build: docker $($composeArgs -join ' ') exit $buildExitCode  — the last lines:"
        Write-Output (($out -split "`r?`n" | Select-Object -Last 25) -join "`n")
        exit 1
    }

    # Print image ages as well as sizes so a stale test image is visible.
    docker image ls --format '{{.Repository}}:{{.Tag}}  {{.Size}}  built {{.CreatedSince}}' | Select-String -Pattern 'tg-llm-proxy'
    Write-Output 'docker-build: OK — the images are in the local store '
    exit 0
}
finally {
    Pop-Location
}
