#Requires -Version 7
<#
verify.ps1 — the complete local verification pipeline of §9 and §11.

Runs lint, Unit + Store, fixture-based maintenance acceptance, Integration + leak scan, build, and the containerized race detector. Every stage runs
in its own pwsh process because the stage scripts use exit; one red stage therefore does not hide the results
of the others. The final table distinguishes PASS, FAIL, WARN and SKIP; any FAIL makes the pipeline exit 1.

Usage:
  .\scripts\verify.ps1
  .\scripts\verify.ps1 -SkipRace          # no Docker / only the host levels
  .\scripts\verify.ps1 -SkipHealth        # do not probe a bot that was not started
  .\scripts\verify.ps1 -SkipRaceBuild     # reuse the existing tg-llm-proxy-test image
  .\scripts\verify.ps1 -WithVolumeRestore # also rehearse restore in an isolated Docker volume
#>

[CmdletBinding()]
param(
    [switch]$SkipRace,
    [switch]$SkipHealth,
    [switch]$SkipRaceBuild,
    [switch]$WithVolumeRestore
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'Setup.ps1')
$script:results = @()

function Add-Result($name, $status, $seconds, $note) {
    $script:results += , [pscustomobject] [ordered]@{
        Stage   = $name
        Status  = $status
        Seconds = $seconds
        Note    = $note
    }
}

function Invoke-Stage($name, $script, [string[]]$scriptArgs = @()) {
    Write-Output "`n=== $name ==="
    $watch = [System.Diagnostics.Stopwatch]::StartNew()
    $out = (& pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot $script) @scriptArgs 2>&1 | Out-String)
    $code = $LASTEXITCODE
    $watch.Stop()
    Write-Output $out.Trim()

    $status = 'PASS'
    if ($code -ne 0) { $status = 'FAIL' }
    elseif ($out -match 'Summary:.*\| WARN [1-9]\d*') { $status = 'WARN' }
    Add-Result $name $status ([math]::Round($watch.Elapsed.TotalSeconds, 1)) "exit $code"
}

Push-Location $repoRoot
try {
    Invoke-Stage 'lint' 'lint.ps1'
    Invoke-Stage 'unit + store' 'test.ps1'

    $maintenanceArgs = @()
    if ($WithVolumeRestore) { $maintenanceArgs += '-WithVolumeRestore' }
    Invoke-Stage 'maintenance acceptance' 'test-maintenance.ps1' $maintenanceArgs

    $smokeArgs = @()
    if ($SkipHealth) { $smokeArgs += '-SkipHealth' }
    Invoke-Stage 'integration + leak scan' 'smoke.ps1' $smokeArgs

    Invoke-Stage 'build + empty ENV refusal' 'build.ps1'

    if ($SkipRace) {
        Add-Result 'race (tagged)' 'SKIP' 0 '-SkipRace was given; §3.1 requires the Docker test image'
    }
    else {
        $raceArgs = @('-Tagged')
        if ($SkipRaceBuild) { $raceArgs += '-SkipBuild' }
        Invoke-Stage 'race (tagged)' 'test-race.ps1' $raceArgs
    }
}
finally {
    Pop-Location
}

Write-Output "`n=== verification summary ==="
$script:results | Format-Table -AutoSize
$fails = @($script:results | Where-Object { $_.Status -eq 'FAIL' })
$warns = @($script:results | Where-Object { $_.Status -eq 'WARN' })
$skips = @($script:results | Where-Object { $_.Status -eq 'SKIP' })
Write-Output "Summary: total $($script:results.Count) | FAIL $($fails.Count) | WARN $($warns.Count) | SKIP $($skips.Count)"
if ($fails.Count -gt 0) {
    Write-Output "verify: RED — $($fails.Stage -join ', ')"
    exit 1
}
Write-Output 'verify: GREEN — every executed stage passed'
exit 0
