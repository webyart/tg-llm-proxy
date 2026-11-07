#Requires -Version 7
<#

Run unit and store tests only in the release package roots, so local scratch packages cannot affect the result.
By default the tg-llm-proxy-test:latest image is rebuilt from the Dockerfile test target and Go runs only in
that container. -Host is the explicit fallback for a host Go installation; -SkipBuild reuses the local image.
Integration tests remain a separate tagged suite over ./test/... in scripts/smoke.ps1. The Concurrency level
(-race) runs in the container of scripts/test-race.ps1.

Usage:
  . .\scripts\Setup.ps1
  .\scripts\test.ps1
  .\scripts\test.ps1 -SkipBuild         # reuse tg-llm-proxy-test:latest
  .\scripts\test.ps1 -Host              # explicit host Go fallback
  .\scripts\test.ps1 -VerboseOut        # every PASS line, not only the package summary
  .\scripts\test.ps1 -Pkg internal/bot

Exit: 0 — every selected package green; 1 — at least one red test or a Docker setup failure.
#>

[CmdletBinding()]
param(
    [string[]]$Pkg = @('.', './cmd/...', './internal/...'),
    [switch]$VerboseOut,
    [int]$Count = 1,
    # $Host is a read-only PowerShell automatic variable, so expose -Host through an alias.
    [Alias('Host')]
    [switch]$HostTests,
    [switch]$SkipBuild
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'DockerBuildLock.ps1')
$image = 'tg-llm-proxy-test:latest'

if (-not $HostTests -and -not (Get-Command -Name docker -ErrorAction SilentlyContinue)) {
    Write-Output 'test: docker is not on PATH — use -Host for the explicit host Go fallback'
    exit 1
}

Push-Location $repoRoot
try {
    if (-not $HostTests -and -not $SkipBuild) {
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
    $goArgs = @('test') + $Pkg + @("-count=$Count")
    if ($VerboseOut) { $goArgs += '-v' }
    if ($HostTests) {
        $out = (& go @goArgs 2>&1 | Out-String)
    }
    else {
        # Keep every package in its own argument; no shell evaluates the caller's -Pkg values.
        $dockerArgs = @('run', '--rm', $image, 'go') + $goArgs
        $out = (& docker @dockerArgs 2>&1 | Out-String)
    }
    Write-Output $out.Trim()
    if ($LASTEXITCODE -ne 0) {
        Write-Output "test: RED  —: -race is NOT used here, it lives in scripts/test-race.ps1"
        exit 1
    }
    $mode = if ($HostTests) { 'host' } else { 'container' }
    Write-Output "test: GREEN — packages: $($Pkg -join ' '); $mode run without -race"
    exit 0
}
finally {
    Pop-Location
}
