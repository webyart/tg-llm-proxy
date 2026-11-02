#Requires -Version 7
<#


     github.com/go-telegram/bot, modernc.org/sqlite, …) and the gitignored scratch of zzscratch/, so the
     raw output is red on a clean tree. The exclusions below are exactly the .gitignore/.dockerignore rows
     (test/integration/run_test.go carries //go:build integration). A second vet with -tags integration

Usage:
  . .\scripts\Setup.ps1
  .\scripts\lint.ps1
  .\scripts\lint.ps1 -Fix          # run gofmt -w over our own files first, then check again

Exit: 0 — vet clean (both tag sets) and gofmt listed nothing; 1 — the output names what to fix.
#>

[CmdletBinding()]
param([switch]$Fix)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$skip = '[\\/]\.cache[\\/]|[\\/]bin[\\/]|[\\/]data[\\/]|[\\/]zzscratch[\\/]|[\\/]\.git[\\/]'
Push-Location $repoRoot
try {
    $ourFiles = Get-ChildItem -LiteralPath $repoRoot -Recurse -File -Filter *.go |
        Where-Object { $_.FullName -notmatch $skip }

    if ($Fix) {
        $ourFiles.FullName | ForEach-Object { gofmt -w $_ }
    }

    $fmtRaw = (gofmt -l . 2>&1 | Out-String) -split "`r?`n" | Where-Object { $_ -and ($_ -notmatch $skip -and $_ -notmatch '^(\.cache|bin|data|zzscratch)[\\/]') }
    $vetPlain = (go vet ./... 2>&1 | Out-String)
    $vetPlainCode = $LASTEXITCODE
    $vetTagged = (go vet -tags integration ./... 2>&1 | Out-String)
    $vetTaggedCode = $LASTEXITCODE

    $fails = @()
    if ($fmtRaw.Count -gt 0) {
        $fails += "gofmt -l listed $($fmtRaw.Count) file(s) — run scripts/lint.ps1 -Fix :"
        $fails += ($fmtRaw | ForEach-Object { "  $_" })
    }
    if ($vetPlainCode -ne 0) { $fails += "go vet ./... exit $vetPlainCode : $($vetPlain.Trim())" }
    if ($vetTaggedCode -ne 0) {
        $fails += "go vet -tags integration ./... exit $vetTaggedCode : $($vetTagged.Trim())"
    }

    $scriptFiles = @(Get-ChildItem -LiteralPath (Join-Path $repoRoot 'scripts') -File -Filter *.ps1)
    foreach ($script in $scriptFiles) {
        $parseErrors = $null
        [void][System.Management.Automation.Language.Parser]::ParseFile($script.FullName, [ref]$null, [ref]$parseErrors)
        foreach ($parseError in $parseErrors) {
            $fails += "$($script.Name): $($parseError.Message)"
        }
    }

    Write-Output "lint — repo: $repoRoot | our .go files: $($ourFiles.Count), pwsh files: $($scriptFiles.Count) "
    foreach ($line in $fails) { Write-Output $line }
    if ($fails.Count -gt 0) {
        Write-Output 'lint: RED '
        exit 1
    }
    Write-Output 'lint: GREEN — gofmt silent, go vet clean with and without the integration tag '
    exit 0
}
finally {
    Pop-Location
}
