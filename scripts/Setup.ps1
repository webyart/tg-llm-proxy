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
$env:GOCACHE = Join-Path $repoRoot '.cache\gocache'
$env:GOPATH = Join-Path $repoRoot '.cache\gopath'

Write-Output "Setup: git-dirs added to PATH (if existed)"
Write-Output "GOPROXY=$env:GOPROXY"
Write-Output "GOCACHE=$env:GOCACHE"
Write-Output "GOPATH=$env:GOPATH"
if (Get-Command -Name git -ErrorAction SilentlyContinue) {
    Write-Output "git: $(git --version 2>&1 | Select-Object -First 1)"
} else {
    Write-Output "git: NOT FOUND — install Git (winget on Windows, Homebrew on macOS, apt/dnf on Linux) or run scripts/Setup.ps1 after install"
}
