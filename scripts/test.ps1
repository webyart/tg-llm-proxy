#Requires -Version 7
<#

Run unit and store tests only in the release package roots, so local scratch packages cannot affect the result.
The tg-llm-proxy-test:latest image is rebuilt from the Dockerfile test target and Go runs ONLY in that container.
There is no host fallback: `webapp.test` must never execute on this machine. Integration tests remain a separate
tagged suite over ./test/... in scripts/smoke.ps1. The Concurrency level (-race) runs in the container of
scripts/test-race.ps1.

Usage:
  . .\scripts\Setup.ps1
  .\scripts\test.ps1
  .\scripts\test.ps1 -SkipBuild         # reuse tg-llm-proxy-test:latest
  .\scripts\test.ps1 -VerboseOut        # every PASS line, not only the package summary
  .\scripts\test.ps1 -Pkg internal/bot

Exit: 0 — every selected package green; 1 — at least one red test or a Docker setup failure.
#>

[CmdletBinding()]
param(
    [string[]]$Pkg = @('.', './cmd/...', './internal/...'),
    [switch]$VerboseOut,
    [int]$Count = 1,
    [switch]$SkipBuild
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'DockerBuildLock.ps1')
$image = 'tg-llm-proxy-test:latest'

if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) {
    Write-Output 'test: docker is not on PATH — Go tests run only in the test container; install/start Docker'
    exit 1
}

Push-Location $repoRoot
try {
    if (-not $SkipBuild) {
        $build = (Invoke-DockerBuildWithLock -RepoRoot $repoRoot -Build {
            docker build --target test -t $image . 2>&1 | Out-String
        })
        $buildExitCode = $LASTEXITCODE
        if ($buildExitCode -ne 0) {
            Write-Output "test: docker build --target test exit $buildExitCode : the last lines follow"
            Write-Output (($build -split "`r?`n" | Select-Object -Last 25) -join "`n")
            exit 1
        }
        Write-Output "test: image $image is built"
    }

    # $goArgs, not $args: the latter is the automatic PowerShell variable of the caller's rest arguments.
    # -shuffle=on randomizes test order; the seed is printed on failure, rerun with -shuffle=<seed>.
    $goArgs = @('test') + $Pkg + @("-count=$Count", '-shuffle=on')
    if ($VerboseOut) { $goArgs += '-v' }
    # Keep every package in its own argument; no shell evaluates the caller's -Pkg values.
    $dockerArgs = @('run', '--rm', $image, 'go') + $goArgs
    $out = (& docker @dockerArgs 2>&1 | Out-String)
    Write-Output $out.Trim()
    if ($LASTEXITCODE -ne 0) {
        Write-Output "test: RED  —: -race is NOT used here, it lives in scripts/test-race.ps1"
        exit 1
    }
    Write-Output "test: GREEN — packages: $($Pkg -join ' '); container run without -race, shuffle=on"
    exit 0
}
finally {
    Pop-Location
}
