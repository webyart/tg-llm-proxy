#Requires -Version 7
<#

answers, the rate limit, and the store on in-memory and on a file SQLite in t.TempDir(). The Integration
through scripts/smoke.ps1; the Concurrency level (-race) runs in the container of scripts/test-race.ps1,

Usage:
  . .\scripts\Setup.ps1
  .\scripts\test.ps1
  .\scripts\test.ps1 -VerboseOut     # every PASS line, not only the package summary
  .\scripts\test.ps1 -Pkg internal/bot

Exit: 0 — every package green; 1 — at least one red test.
#>

[CmdletBinding()]
param(
    [string]$Pkg = './...',
    [switch]$VerboseOut,
    [int]$Count = 1
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Push-Location $repoRoot
try {
    # $goArgs, not $args: the latter is the automatic PowerShell variable of the caller's rest arguments.
    $goArgs = @('test', $Pkg, "-count=$Count")
    if ($VerboseOut) { $goArgs += '-v' }
    $out = (& go @goArgs 2>&1 | Out-String)
    Write-Output $out.Trim()
    if ($LASTEXITCODE -ne 0) {
        Write-Output "test: RED  —: -race is NOT used here, it lives in scripts/test-race.ps1"
        exit 1
    }
    Write-Output 'test: GREEN — the Unit + Store levels are green on the host without -race '
    exit 0
}
finally {
    Pop-Location
}
