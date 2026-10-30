#Requires -Version 7
<#
lint.ps1 — §9 row «scripts/lint.ps1»: «go vet ./..., gofmt -l . (fail, если есть файлы)» and §16 «lint».

Two details the one-line version of §9 gets wrong in this repository:

  1. `gofmt -l .` walks the project-local Go cache of §3 (.cache/gopath — the sources of
     github.com/go-telegram/bot, modernc.org/sqlite, …) and the gitignored scratch of zzscratch/, so the
     raw output is red on a clean tree. The exclusions below are exactly the .gitignore/.dockerignore rows
     of §10 День 1 — the lint speaks about OUR sources, not about the module cache.
  2. `go vet ./...` skips the build-tagged files, i.e. the whole Integration level of §11
     (test/integration/run_test.go carries //go:build integration). A second vet with -tags integration
     keeps that file compilable (§12 reliability), the same rule the test stage of the Dockerfile follows.

Usage:
  . .\scripts\Setup.ps1
  .\scripts\lint.ps1
  .\scripts\lint.ps1 -Fix          # run gofmt -w over our own files first, then check again

Exit: 0 — vet clean (both tag sets) and gofmt listed nothing; 1 — the output names what to fix.
#>

[CmdletBinding()]
param([switch]$Fix)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
# .gitignore of §10 День 1 + §3: the caches and the scratch are not our sources.
$skip = '\\\.cache\\|\\bin\\|\\data\\|\\zzscratch\\|\\\.git\\'
Push-Location $repoRoot
try {
    $ourFiles = Get-ChildItem -LiteralPath $repoRoot -Recurse -File -Filter *.go |
        Where-Object { $_.FullName -notmatch $skip }

    if ($Fix) {
        # gofmt -w is safe: the tool rewrites only formatting, never semantics (§16 lint).
        $ourFiles.FullName | ForEach-Object { gofmt -w $_ }
    }

    $fmtRaw = (gofmt -l . 2>&1 | Out-String) -split "`r?`n" | Where-Object { $_ -and ($_ -notmatch $skip -and $_ -notmatch '^(\.cache|bin|data|zzscratch)[\\/]') }
    $vetPlain = (go vet ./... 2>&1 | Out-String)
    $vetPlainCode = $LASTEXITCODE
    $vetTagged = (go vet -tags integration ./... 2>&1 | Out-String)
    $vetTaggedCode = $LASTEXITCODE

    $fails = @()
    if ($fmtRaw.Count -gt 0) {
        $fails += "gofmt -l listed $($fmtRaw.Count) file(s) — run scripts/lint.ps1 -Fix (§16 lint):"
        $fails += ($fmtRaw | ForEach-Object { "  $_" })
    }
    if ($vetPlainCode -ne 0) { $fails += "go vet ./... exit $vetPlainCode (§9 scripts/lint.ps1): $($vetPlain.Trim())" }
    if ($vetTaggedCode -ne 0) {
        $fails += "go vet -tags integration ./... exit $vetTaggedCode (§11 Integration level must compile): $($vetTagged.Trim())"
    }

    Write-Output "lint — repo: $repoRoot | our .go files: $($ourFiles.Count) (caches and scratch excluded, §3/§10 День 1)"
    foreach ($line in $fails) { Write-Output $line }
    if ($fails.Count -gt 0) {
        Write-Output 'lint: RED (§9)'
        exit 1
    }
    Write-Output 'lint: GREEN — gofmt silent, go vet clean with and without the integration tag (§9, §16)'
    exit 0
}
finally {
    Pop-Location
}
