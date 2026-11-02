#Requires -Version 7
<#



Usage:
  .\scripts\build.ps1 -SkipTidy     # the module graph is already resolved

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
        # network problem, and the message says so instead of pointing at the code.
        $tidyOut = (go mod tidy 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) {
            Write-Output "go mod tidy exit $LASTEXITCODE —: check GOPROXY "
            Write-Output $tidyOut.Trim()
            exit 1
        }
    }

    New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'bin') | Out-Null
    $commit = 'dev'
    if (Get-Command -Name git -ErrorAction SilentlyContinue) {
        $gitCommit = (git rev-parse --short=12 HEAD 2>$null)
        if ($LASTEXITCODE -eq 0 -and $gitCommit) {
            $commit = $gitCommit.Trim()
            if ((git status --porcelain 2>$null | Out-String).Trim()) { $commit += '-dirty' }
        }
    }
    $env:CGO_ENABLED = '0'   #: pure-Go sqlite of — the host has no C compiler and needs none
    $buildOut = (go build -ldflags "-X main.buildCommit=$commit" -o bin/bot.exe . 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0) {
        Write-Output "go build exit $LASTEXITCODE "
        Write-Output $buildOut.Trim()
        exit 1
    }

    $exe = Join-Path $repoRoot 'bin\bot.exe'
    $sizeKb = [math]::Round((Get-Item -LiteralPath $exe).Length / 1kb)
    Write-Output "build: bin\bot.exe — $sizeKb KiB, CGO_ENABLED=0, target '.' "

    if (-not $SkipNoEnv) {
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
            Write-Output " check FAILED: the binary exited $noEnvCode without TELEGRAM_BOT_TOKEN, want 1"
            Write-Output ($noEnv.Trim())
            exit 1
        }
        Write-Output 'build: check OK — with an empty ENV the binary names the missing rows and exits 1'
    }
    Write-Output 'build: OK'
    exit 0
}
finally {
    Pop-Location
}
