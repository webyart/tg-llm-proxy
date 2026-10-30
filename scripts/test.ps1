#Requires -Version 7
<#
test.ps1 — §9 row «scripts/test.ps1»: «go test ./... -count=1 — без -race (на хосте нет cgo, §3.1)».

This is the Unit + Store levels of §11: the config loader, the context builder, the rune-wise cut of the
answers, the rate limit, and the store on in-memory and on a file SQLite in t.TempDir(). The Integration
level (fake Telegram + fake STRATA behind bot.WithServerURL, §11) is build-tagged `integration` and runs
through scripts/smoke.ps1; the Concurrency level (-race) runs in the container of scripts/test-race.ps1,
because the host has no C compiler (§3.1) — that is why neither of them is folded in here.

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
    # -count=1 of §9: the test cache must not pretend that a green run happened now (§16 lint discipline).
    # $goArgs, not $args: the latter is the automatic PowerShell variable of the caller's rest arguments.
    $goArgs = @('test', $Pkg, "-count=$Count")
    if ($VerboseOut) { $goArgs += '-v' }
    $out = (& go @goArgs 2>&1 | Out-String)
    Write-Output $out.Trim()
    if ($LASTEXITCODE -ne 0) {
        Write-Output "test: RED (§11 Unit/Store levels, exit $LASTEXITCODE) — §3.1: -race is NOT used here, it lives in scripts/test-race.ps1"
        exit 1
    }
    Write-Output 'test: GREEN — the §11 Unit + Store levels are green on the host without -race (§3.1)'
    exit 0
}
finally {
    Pop-Location
}
