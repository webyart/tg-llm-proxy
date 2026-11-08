#Requires -Version 7
<#
  its dir не automaticaly added to already-running PowerShell sessions PATH.
  Dot-source это скрипт: `. .\scripts\Setup.ps1` (dot + path!).
#>

# Repo root = parent of scripts/
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

# Git for Windows → PATH (Inno/winget: C:\Program Files\Git\cmd)
$gitDirs = @('C:\Program Files\Git\cmd', 'C:\Program Files\Git\bin')
foreach ($dir in $gitDirs) {
    if ((Test-Path -LiteralPath $dir -PathType 'Any') -and ($env:Path -notlike "*$dir*")) {
        $env:Path = "$dir;" + $env:Path
    }
}

$env:GOPROXY = 'https://proxy.golang.org,direct'
# The Go caches live outside the checkout: a repo-local .cache/ grew to 1.65 GB and made every recursive walk
# (gofmt -l ., the secret scan, git status) pay for it. The platform cache home is the right place, and the
# repository-local path stays as a fallback for an environment without one.
$cacheRoot = ''
if ($env:LOCALAPPDATA) {
    $cacheRoot = Join-Path $env:LOCALAPPDATA 'tg-llm-proxy'
} elseif ($env:XDG_CACHE_HOME) {
    $cacheRoot = Join-Path $env:XDG_CACHE_HOME 'tg-llm-proxy'
} elseif ($HOME) {
    $cacheRoot = Join-Path $HOME '.cache' 'tg-llm-proxy'
}
if (-not $cacheRoot) {
    $cacheRoot = Join-Path $repoRoot '.cache'
}
$env:GOCACHE = Join-Path $cacheRoot 'gocache'
$env:GOPATH = Join-Path $cacheRoot 'gopath'
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOPATH | Out-Null

Write-Output "Setup: git-dirs added to PATH (if existed)"
Write-Output "GOPROXY=$env:GOPROXY"
Write-Output "GOCACHE=$env:GOCACHE"
Write-Output "GOPATH=$env:GOPATH"
if (Get-Command -Name git -ErrorAction SilentlyContinue) {
    Write-Output "git: $(git --version 2>&1 | Select-Object -First 1)"
} else {
    Write-Output "git: NOT FOUND — install Git (winget on Windows, Homebrew on macOS, apt/dnf on Linux) or run scripts/Setup.ps1 after install"
}
