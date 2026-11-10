#Requires -Version 7
<#
release-check.ps1 — local pre-tag gate for a v* release.

Runs, on the host and before the tag exists, the checks that must be green for the GitLab release job to
publish anything: gofmt over the tracked tree, go vet (plain and integration-tagged package roots), the full
unit/store test suite, and a clean `git status`. It then prints the tag to create and the exact commands.
Nothing is committed, tagged, pushed, or built — this script only refuses to let a human tag a red tree.

`go test ./...` is intentionally expanded to `. ./cmd/... ./internal/...`: `zzscratch/` is gitignored local
scratch and is not part of the release, and the repository's own scripts/lint.ps1 and scripts/test.ps1 use the
same explicit roots. `go vet -tags integration ./test/...` covers the integration files that the plain vet
pass skips because of their build tag.

Usage:
  .\scripts\release-check.ps1
  .\scripts\release-check.ps1 -SkipTests     # fast fmt/vet/status gate while iterating, not a release gate
  .\scripts\release-check.ps1 -Tag v1.41.0   # release a patch version instead of the default next minor
  .\scripts\release-check.ps1 -SkipStatus    # only for a pre-commit smoke run; never tag from it

Exit: 0 — every check passed and the tag is printed; 1 — at least one check is red or the tag is invalid.
#>

[CmdletBinding()]
param(
    [string]$Tag,
    [switch]$SkipTests,
    [switch]$SkipStatus
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
# Same environment as the other wrappers (Git on PATH, GOPROXY, GOCACHE/GOPATH), but keep its banner out of the
# release-check report.
$setupOutput = . (Join-Path $PSScriptRoot 'Setup.ps1') 2>&1
$packageRoots = @('.', './cmd/...', './internal/...')
$integrationRoots = @('./test/...')
$skipPath = '[\\/]\.cache[\\/]|[\\/]bin[\\/]|[\\/]data[\\/]|[\\/]zzscratch[\\/]|[\\/]\.git[\\/]'
$failures = [System.Collections.Generic.List[string]]::new()

function Add-Failure([string]$Message) {
    $script:failures.Add($Message)
}

function Write-Section([string]$Title) {
    Write-Output "`n=== $Title ==="
}

Push-Location $repoRoot
try {
    if (-not (Get-Command -Name git -ErrorAction SilentlyContinue)) {
        Add-Failure 'git is not on PATH; release-check needs the repository history and tags'
        throw 'git is not available'
    }
    $headSha = (& git rev-parse --short HEAD 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) { throw "git rev-parse HEAD failed: $headSha" }
    $headSubject = (& git log -1 --format=%s 2>&1 | Out-String).Trim()
    $branch = (& git rev-parse --abbrev-ref HEAD 2>&1 | Out-String).Trim()

    Write-Output "release-check — repo: $repoRoot | HEAD: $headSha ($headSubject) | branch: $branch"

    # 1. Working tree. Untracked files count: Docker sends the whole context, so a stray file can change the image.
    Write-Section 'git status'
    $statusOutput = (& git status --porcelain 2>&1 | Out-String).Trim()
    if (-not $statusOutput) {
        Write-Output 'git status: clean'
    }
    elseif ($SkipStatus) {
        Write-Output 'git status: DIRTY — -SkipStatus was given, so this is not a release gate run'
        Write-Output $statusOutput
    }
    else {
        Write-Output $statusOutput
        Add-Failure 'git status is not clean; commit or remove every change before tagging (untracked files included)'
    }

    # 2. Formatting. Same filter as scripts/lint.ps1: .cache/, bin/, data/, zzscratch/ and .git/ are not ours.
    Write-Section 'gofmt'
    $fmtRaw = (& gofmt -l . 2>&1 | Out-String) -split "`r?`n" |
        Where-Object { $_ -and ($_ -notmatch $skipPath) -and ($_ -notmatch '^(\.cache|bin|data|zzscratch)[\\/]') }
    if ($fmtRaw.Count -gt 0) {
        $fmtRaw | ForEach-Object { Write-Output "  $_" }
        Add-Failure "gofmt listed $($fmtRaw.Count) file(s) or unparsable file(s); run scripts/lint.ps1 -Fix"
    }
    else {
        Write-Output 'gofmt: clean'
    }

    # 3. Vet, plain release roots plus the integration-tagged test package.
    Write-Section 'go vet'
    $vetPlain = (& go vet @packageRoots 2>&1 | Out-String)
    $vetPlainCode = $LASTEXITCODE
    $vetTagged = (& go vet -tags integration @integrationRoots 2>&1 | Out-String)
    $vetTaggedCode = $LASTEXITCODE
    if ($vetPlainCode -ne 0) {
        Write-Output $vetPlain.Trim()
        Add-Failure "go vet $($packageRoots -join ' ') exited $vetPlainCode"
    }
    if ($vetTaggedCode -ne 0) {
        Write-Output $vetTagged.Trim()
        Add-Failure "go vet -tags integration $($integrationRoots -join ' ') exited $vetTaggedCode"
    }
    if ($vetPlainCode -eq 0 -and $vetTaggedCode -eq 0) {
        Write-Output "go vet: clean ($($packageRoots -join ' ') and -tags integration $($integrationRoots -join ' '))"
    }

    # 4. Unit/store tests. The integration suite needs Docker and belongs to scripts/smoke.ps1, not here.
    Write-Section 'go test'
    if ($SkipTests) {
        Write-Output 'go test: SKIP — -SkipTests was given; a real release gate must run it'
    }
    else {
        $testArgs = @('test') + $packageRoots + @('-count=1', '-shuffle=on', '-timeout', '20m')
        $watch = [System.Diagnostics.Stopwatch]::StartNew()
        $testOutput = (& go @testArgs 2>&1 | Out-String)
        $testCode = $LASTEXITCODE
        $watch.Stop()
        Write-Output $testOutput.Trim()
        if ($testCode -ne 0) {
            Add-Failure "go test $($packageRoots -join ' ') exited $testCode after $([math]::Round($watch.Elapsed.TotalSeconds, 1))s"
        }
        else {
            Write-Output "go test: GREEN — $([math]::Round($watch.Elapsed.TotalSeconds, 1))s"
        }
    }

    # 5. Tag selection. The latest v* tag is the base; the default is the next minor release.
    Write-Section 'release tag'
    $knownTags = @(& git tag --list 'v*' --sort=-v:refname 2>&1 |
        Where-Object { $_ -match '^v\d+\.\d+\.\d+$' })
    $latestTag = if ($knownTags.Count -gt 0) { $knownTags[0] } else { 'v0.0.0' }
    $latestVersion = [version]($latestTag.TrimStart('v'))

    if ($Tag) {
        if ($Tag -notmatch '^v\d+\.\d+\.\d+$') {
            Add-Failure "tag '$Tag' is not a vMAJOR.MINOR.PATCH tag"
        }
        elseif ((& git rev-parse -q --verify "refs/tags/$Tag" 2>&1 | Out-String).Trim()) {
            Add-Failure "tag $Tag already exists"
        }
        elseif ([version]($Tag.TrimStart('v')) -le $latestVersion) {
            Add-Failure "tag $Tag is not newer than the latest tag $latestTag"
        }
    }
    else {
        $Tag = "v$($latestVersion.Major).$($latestVersion.Minor + 1).0"
    }

    $headTag = (& git describe --tags --exact-match HEAD 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -eq 0 -and $headTag -match '^v\d+\.\d+\.\d+$') {
        Add-Failure "HEAD is already tagged $headTag; commit the release changes first (or pass -Tag for a new version)"
    }
    Write-Output "latest tag: $latestTag"
    Write-Output "tag to create: $Tag"

    if ($failures.Count -gt 0) {
        Write-Output "`nrelease-check: RED — $($failures.Count) check(s) failed"
        foreach ($failure in $failures) { Write-Output "  - $failure" }
        exit 1
    }

    $skipped = @()
    if ($SkipTests) { $skipped += 'tests' }
    if ($SkipStatus) { $skipped += 'git status' }
    if ($skipped.Count -gt 0) {
        Write-Output "`nrelease-check: GREEN (partial) — fmt and vet passed; skipped: $($skipped -join ', ')"
    }
    else {
        Write-Output "`nrelease-check: GREEN — fmt, vet, tests and status are clean"
    }
    Write-Output ''
    Write-Output 'Exact commands:'
    Write-Output '  git push origin HEAD                 # only if the release commit is not on origin yet'
    Write-Output "  git tag -a $Tag -m `"$Tag`""
    Write-Output "  git push origin $Tag"
    Write-Output ''
    Write-Output 'The tag pipeline (GitLab stage release) builds and tags the production image and keeps the checksum,'
    Write-Output 'docker image inspect metadata and coverage.out as artifacts. The GitHub workflow verifies branches only.'
    if ($skipped.Count -gt 0) {
        Write-Output ''
        Write-Output "PARTIAL RUN ($($skipped -join ', ')): do not tag from this result; rerun without the skip switch(es)."
    }
    exit 0
}
catch {
    Write-Output "release-check: RED — $($_.Exception.Message)"
    foreach ($failure in $failures) { Write-Output "  - $failure" }
    exit 1
}
finally {
    Pop-Location
}
