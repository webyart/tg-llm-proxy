#Requires -Version 7
<#
build.ps1 — §9 row «scripts/build.ps1»: «go mod tidy, go build -o bin\bot.exe ./cmd/bot (CGO_ENABLED=0)».

§4 deviation (README «§4 deviation»): package main lives at the repository root, so the build target is `.`
and not `./cmd/bot` — the same target the builder stage of the Dockerfile uses (§8.1), which is why a local
build and an image build can never drift apart (§12 reliability).

CGO_ENABLED=0 is not cosmetic: the driver of §5 is the pure-Go modernc.org/sqlite, so the host binary needs
no C toolchain (§3.1) — and `-race` therefore lives in the container of scripts/test-race.ps1 (§11).

Usage:
  . .\scripts\Setup.ps1             # PATH + project-local Go cache (§3)
  .\scripts\build.ps1               # go mod tidy + bin\bot.exe + the §7 refusal smoke
  .\scripts\build.ps1 -SkipTidy     # the module graph is already resolved
  .\scripts\build.ps1 -SkipNoEnv    # skip the «empty ENV must exit 1» check of §7

Exit: 0 — bin\bot.exe was produced and refuses to start without the §7 ENV; 1 — tidy or build failed.
#>

[CmdletBinding()]
param(
    [switch]$SkipTidy,
    [switch]$SkipNoEnv
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Push-Location $repoRoot
try {
    if (-not $SkipTidy) {
        # §16 change 20: the first tidy of go-telegram/bot needs the GOPROXY of §3 — a red tidy is a
        # network problem, and the message says so instead of pointing at the code.
        $tidyOut = (go mod tidy 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output "go mod tidy exit $LASTEXITCODE — §16 change 20: check GOPROXY (§3, scripts/Setup.ps1)"
            Write-Output $tidyOut.Trim()
            exit 1
        }
    }

    New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'bin') | Out-Null
    $env:CGO_ENABLED = '0'   # §3.1: pure-Go sqlite of §5 — the host has no C compiler and needs none
    $buildOut = (go build -o bin\bot.exe . 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0) {
        Write-Output "go build exit $LASTEXITCODE (§12 reliability: no binary, the reasons above)"
        Write-Output $buildOut.Trim()
        exit 1
    }

    $exe = Join-Path $repoRoot 'bin\bot.exe'
    $sizeKb = [math]::Round((Get-Item -LiteralPath $exe).Length / 1kb)
    Write-Output "build: bin\bot.exe — $sizeKb KiB, CGO_ENABLED=0, target '.' (§4 deviation, §8.1 builder)"

    if (-not $SkipNoEnv) {
        # §7 + §10 День 1 DoD: an empty process ENV must not start a half-configured bot — the binary
        # names the missing §7 rows and leaves with exit code 1. The check runs with the token removed,
        # and the token of §7 is never printed (§12 security).
        $keep = $env:TELEGRAM_BOT_TOKEN
        $env:TELEGRAM_BOT_TOKEN = ''
        try {
            $noEnv = (& $exe 2>&1 | Out-String)
            $noEnvCode = $LASTEXITCODE
        }
        finally {
            $env:TELEGRAM_BOT_TOKEN = $keep
        }
        if ($noEnvCode -ne 1) {
            Write-Output "§7 check FAILED: the binary exited $noEnvCode without TELEGRAM_BOT_TOKEN, want 1"
            Write-Output ($noEnv.Trim())
            exit 1
        }
        Write-Output 'build: §7 check OK — with an empty ENV the binary names the missing rows and exits 1'
    }
    Write-Output 'build: OK'
    exit 0
}
finally {
    Pop-Location
}
