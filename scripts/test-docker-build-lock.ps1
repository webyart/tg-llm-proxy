#Requires -Version 7
<#
test-docker-build-lock.ps1 — cross-process Docker build mutex acceptance without Docker or test execution.
#>

[CmdletBinding()]
param(
    [ValidateSet('hold', 'wait', 'timeout')]
    [string]$Worker,
    [string]$WorkDirectory
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'DockerBuildLock.ps1')
$work = $WorkDirectory

function Write-Marker([string]$Name) {
    [System.IO.File]::WriteAllText((Join-Path $work $Name), [System.DateTimeOffset]::UtcNow.ToString('O'))
}

function Wait-ForMarker([string]$Name) {
    $path = Join-Path $work $Name
    $deadline = [System.DateTimeOffset]::UtcNow.AddSeconds(15)
    while ([System.DateTimeOffset]::UtcNow -lt $deadline) {
        if (Test-Path -LiteralPath $path) { return }
        Start-Sleep -Milliseconds 50
    }
    throw "Timed out waiting for marker $Name"
}

if ($Worker) {
    if (-not $WorkDirectory) { throw '-WorkDirectory is required in worker mode' }
    switch ($Worker) {
        'hold' {
            Invoke-DockerBuildWithLock -RepoRoot $repoRoot -Build {
                Write-Marker 'holder-acquired'
                Wait-ForMarker 'release-holder'
            }
            Write-Marker 'holder-released'
        }
        'wait' {
            Write-Marker 'waiter-started'
            Invoke-DockerBuildWithLock -RepoRoot $repoRoot -Build {
                Write-Marker 'waiter-acquired'
            }
        }
        'timeout' {
            Write-Marker 'timeout-started'
            $timedOut = $false
            try {
                Invoke-DockerBuildWithLock -RepoRoot $repoRoot -Timeout ([TimeSpan]::FromMilliseconds(250)) -Build {
                    Write-Marker 'timeout-acquired'
                }
            }
            catch [System.TimeoutException] {
                $timedOut = $true
                Write-Output $_.Exception.Message
            }
            if (-not $timedOut) { throw 'The contending acquisition did not time out' }
            Write-Marker 'timeout-finished'
        }
    }
    exit 0
}

function Start-LockWorker([string]$Role) {
    $pwsh = (Get-Process -Id $PID).Path
    $startArgs = @{
        FilePath = $pwsh
        ArgumentList = @('-NoLogo', '-NoProfile', '-File', "`"$PSCommandPath`"", '-Worker', $Role, '-WorkDirectory', "`"$work`"")
        RedirectStandardOutput = Join-Path $work "$Role.stdout.log"
        RedirectStandardError = Join-Path $work "$Role.stderr.log"
        PassThru = $true
    }
    if ($IsWindows) { $startArgs['WindowStyle'] = 'Hidden' }
    $process = Start-Process @startArgs
    $process | Add-Member -NotePropertyName LockRole -NotePropertyValue $Role
    return $process
}

function Wait-ForWorker([System.Diagnostics.Process]$Process) {
    if (-not $Process.WaitForExit(30000)) { throw "Worker PID $($Process.Id) did not exit within 30 seconds" }
    if ($Process.ExitCode -ne 0) {
        $stdout = [System.IO.File]::ReadAllText((Join-Path $work "$($Process.LockRole).stdout.log"))
        $stderr = [System.IO.File]::ReadAllText((Join-Path $work "$($Process.LockRole).stderr.log"))
        throw "Worker PID $($Process.Id) exited $($Process.ExitCode): $stdout$stderr"
    }
}

$work = Join-Path $repoRoot ('zzscratch/docker-build-lock-' + [guid]::NewGuid().ToString('N'))
$processes = @()
$failure = $null
try {
    [void](New-Item -ItemType Directory -Path $work)
    $mutexName = Get-DockerBuildMutexName $repoRoot
    if ($mutexName -ne (Get-DockerBuildMutexName (Join-Path $repoRoot 'scripts/..'))) {
        throw 'Equivalent full repository paths produced different mutex names'
    }
    if ($mutexName -eq (Get-DockerBuildMutexName ($repoRoot + '-other'))) {
        throw 'Different repository roots produced the same mutex name'
    }
    if ($mutexName.StartsWith('Global\', [System.StringComparison]::OrdinalIgnoreCase)) {
        throw 'The mutex name uses a Windows-only Global namespace'
    }

    $processes += Start-LockWorker 'hold'
    Wait-ForMarker 'holder-acquired'
    $processes += Start-LockWorker 'wait'
    Wait-ForMarker 'waiter-started'
    Start-Sleep -Milliseconds 400
    if (Test-Path -LiteralPath (Join-Path $work 'waiter-acquired')) {
        throw 'The second process entered before the holder released the mutex'
    }

    $timeoutProcess = Start-LockWorker 'timeout'
    $processes += $timeoutProcess
    Wait-ForMarker 'timeout-finished'
    Wait-ForWorker $timeoutProcess
    if (Test-Path -LiteralPath (Join-Path $work 'timeout-acquired')) {
        throw 'The short-timeout process acquired the held mutex'
    }
    if (Test-Path -LiteralPath (Join-Path $work 'waiter-acquired')) {
        throw 'The waiting process entered while the holder still owned the mutex'
    }
    $timeoutMessage = [System.IO.File]::ReadAllText((Join-Path $work 'timeout.stdout.log'))
    if ($timeoutMessage -notmatch 'Docker image build lock was not acquired within') {
        throw 'The timeout error did not explain the failed acquisition'
    }

    Write-Marker 'release-holder'
    Wait-ForMarker 'holder-released'
    Wait-ForMarker 'waiter-acquired'
    foreach ($process in $processes) { Wait-ForWorker $process }

    $probe = [System.Threading.Mutex]::new($false, $mutexName)
    $probeAcquired = $false
    try {
        try {
            $probeAcquired = $probe.WaitOne(0)
        }
        catch [System.Threading.AbandonedMutexException] {
            $probeAcquired = $true
            throw 'Failed acquisition left the mutex abandoned'
        }
        if (-not $probeAcquired) { throw 'The mutex remained owned after all workers exited' }
    }
    finally {
        try {
            if ($probeAcquired) { $probe.ReleaseMutex() }
        }
        finally {
            $probe.Dispose()
        }
    }
    Write-Output 'test-docker-build-lock: GREEN — second process waited for release; timeout left no abandoned mutex; repository-specific names verified'
}
catch {
    $failure = $_
    Write-Output "test-docker-build-lock: FAIL — $($_.Exception.Message)"
}
finally {
    foreach ($process in $processes) {
        if (-not $process.HasExited) {
            $process.Kill($true)
            [void]$process.WaitForExit(5000)
        }
        $process.Dispose()
    }
    if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force }
}
if ($failure) { exit 1 }
exit 0
