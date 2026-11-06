#Requires -Version 7
<#

Check formatting across the repository while excluding local state and zzscratch/, then vet only release
package roots. Plain vet covers ., ./cmd/... and ./internal/...; the integration-tagged pass covers ./test/...
(test/integration/run_test.go carries //go:build integration). Local scratch packages cannot affect either vet.

Usage:
  . .\scripts\Setup.ps1
  .\scripts\lint.ps1
  .\scripts\lint.ps1 -Fix          # run gofmt -w over our own files first, then check again

Exit: 0 — vet clean (both package sets) and gofmt listed nothing; 1 — the output names what to fix.
#>

[CmdletBinding()]
param([switch]$Fix)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$skip = '[\\/]\.cache[\\/]|[\\/]bin[\\/]|[\\/]data[\\/]|[\\/]zzscratch[\\/]|[\\/]\.git[\\/]'
$packageRoots = @('.', './cmd/...', './internal/...')
$integrationPackageRoots = @('./test/...')
Push-Location $repoRoot
try {
    $ourFiles = Get-ChildItem -LiteralPath $repoRoot -Recurse -File -Filter *.go |
        Where-Object { $_.FullName -notmatch $skip }

    if ($Fix) {
        $ourFiles.FullName | ForEach-Object { gofmt -w $_ }
    }

    $fmtRaw = (gofmt -l . 2>&1 | Out-String) -split "`r?`n" | Where-Object { $_ -and ($_ -notmatch $skip -and $_ -notmatch '^(\.cache|bin|data|zzscratch)[\\/]') }
    $vetPlain = (go vet @packageRoots 2>&1 | Out-String)
    $vetPlainCode = $LASTEXITCODE
    $vetTagged = (go vet -tags integration @integrationPackageRoots 2>&1 | Out-String)
    $vetTaggedCode = $LASTEXITCODE

    $fails = @()
    if ($fmtRaw.Count -gt 0) {
        $fails += "gofmt -l listed $($fmtRaw.Count) file(s) — run scripts/lint.ps1 -Fix :"
        $fails += ($fmtRaw | ForEach-Object { "  $_" })
    }
    if ($vetPlainCode -ne 0) { $fails += "go vet $($packageRoots -join ' ') exit $vetPlainCode : $($vetPlain.Trim())" }
    if ($vetTaggedCode -ne 0) {
        $fails += "go vet -tags integration $($integrationPackageRoots -join ' ') exit $vetTaggedCode : $($vetTagged.Trim())"
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
    Write-Output 'lint: GREEN — gofmt silent; release and integration package roots vet clean'
    exit 0
}
finally {
    Pop-Location
}
