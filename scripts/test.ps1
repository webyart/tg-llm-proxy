#Requires -Version 7
<#

Run unit and store tests only in the release package roots, so local scratch packages cannot affect the result.
Integration tests remain a separate tagged suite over ./test/... in scripts/smoke.ps1. The Concurrency level
(-race) runs in the container of scripts/test-race.ps1.

Usage:
  . .\scripts\Setup.ps1
  .\scripts\test.ps1
  .\scripts\test.ps1 -VerboseOut     # every PASS line, not only the package summary
  .\scripts\test.ps1 -Pkg internal/bot

Exit: 0 — every selected package green; 1 — at least one red test.
#>

[CmdletBinding()]
param(
    [string[]]$Pkg = @('.', './cmd/...', './internal/...'),
    [switch]$VerboseOut,
    [int]$Count = 1
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Push-Location $repoRoot
try {
    # $goArgs, not $args: the latter is the automatic PowerShell variable of the caller's rest arguments.
    $goArgs = @('test') + $Pkg + @("-count=$Count")
    if ($VerboseOut) { $goArgs += '-v' }
    $out = (& go @goArgs 2>&1 | Out-String)
    Write-Output $out.Trim()
    if ($LASTEXITCODE -ne 0) {
        Write-Output "test: RED  —: -race is NOT used here, it lives in scripts/test-race.ps1"
        exit 1
    }
    Write-Output "test: GREEN — packages: $($Pkg -join ' '); host run without -race"
    exit 0
}
finally {
    Pop-Location
}
