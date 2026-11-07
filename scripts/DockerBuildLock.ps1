#Requires -Version 7

function Get-DockerBuildMutexName([string]$RepoRoot) {
    $fullRoot = [System.IO.Path]::GetFullPath($RepoRoot)
    $fullRoot = [System.IO.Path]::TrimEndingDirectorySeparator($fullRoot)
    if ($IsWindows) { $fullRoot = $fullRoot.ToUpperInvariant() }
    $hash = [System.Security.Cryptography.SHA256]::HashData([System.Text.Encoding]::UTF8.GetBytes($fullRoot))
    return 'tg-llm-proxy-docker-build-' + [System.Convert]::ToHexString($hash).ToLowerInvariant()
}

function Invoke-DockerBuildWithLock {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$RepoRoot,
        [Parameter(Mandatory)][scriptblock]$Build,
        [TimeSpan]$Timeout = [TimeSpan]::FromMinutes(30)
    )

    if ($Timeout -lt [TimeSpan]::Zero) { throw 'Docker image build lock timeout cannot be negative.' }
    $fullRoot = [System.IO.Path]::GetFullPath($RepoRoot)
    $mutex = [System.Threading.Mutex]::new($false, (Get-DockerBuildMutexName $fullRoot))
    $acquired = $false
    try {
        try {
            $acquired = $mutex.WaitOne($Timeout)
        }
        catch [System.Threading.AbandonedMutexException] {
            $acquired = $true
        }
        if (-not $acquired) {
            $timeoutText = if ($Timeout -eq [TimeSpan]::FromMinutes(30)) { '30 minutes' } else { "$($Timeout.TotalSeconds) seconds" }
            throw [System.TimeoutException]::new("Docker image build lock was not acquired within $timeoutText for repository '$fullRoot'; another build is still running. Retry after it finishes.")
        }
        & $Build
    }
    finally {
        try {
            if ($acquired) { $mutex.ReleaseMutex() }
        }
        finally {
            $mutex.Dispose()
        }
    }
}
