#Requires -Version 7
<#
test-container-monitor.ps1 — isolated Docker acceptance for failure, suppression, and recovery; no alerts leave the host.
#>

[CmdletBinding()]
param()

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$work = Join-Path $repoRoot 'zzscratch\container-monitor-acceptance'
$state = Join-Path $work 'state.json'
$name = "tg-llm-proxy-monitor-fixture-$([Guid]::NewGuid().ToString('N'))"
$failure = $null

Push-Location $repoRoot
try {
    if (Test-Path -LiteralPath $work) { throw "monitor acceptance directory already exists: $work" }
    [void](New-Item -ItemType Directory -Path $work)
    docker create --name $name tg-llm-proxy-bot:latest | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'docker create fixture failed' }

    $first = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -ContainerId $name -StatePath $state -NoNotify
    if ($LASTEXITCODE -ne 1) { throw 'created fixture must produce monitor exit 1' }
    if (($first | Out-String) -notmatch 'state=created') { throw 'monitor did not report the created fixture state' }
    $second = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -ContainerId $name -StatePath $state -NoNotify
    if ($LASTEXITCODE -ne 1) { throw 'repeated failure must still produce monitor exit 1' }
    if (($second | Out-String) -notmatch 'repeated alert suppressed') { throw 'repeated failure was not suppressed' }

    $recovery = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -StatePath $state -NoNotify
    if ($LASTEXITCODE -ne 0) { throw 'healthy production container must produce monitor exit 0' }
    if (($recovery | Out-String) -notmatch 'recovered state=healthy') { throw 'failure-to-healthy transition was not recognized' }

    $whisperState = Join-Path $work 'whisper-state.json'
    $whisperFailure = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_container.ps1') -WhisperContainerId $name -StatePath $whisperState -NoNotify
    if ($LASTEXITCODE -ne 1) { throw 'created Whisper fixture must produce monitor exit 1' }
    if (($whisperFailure | Out-String) -notmatch 'state=whisper-created') { throw 'Whisper failure was not attributed to its service' }
    Write-Output 'test-container-monitor: GREEN — bot and Whisper failure/suppression/recovery verified; no notifications sent'
}
catch {
    $failure = $_
    Write-Output "test-container-monitor: FAIL — $($_.Exception.Message)"
}
finally {
    docker rm --force $name 2>&1 | Out-Null
    if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force }
    Pop-Location
}
if ($failure) { exit 1 }
exit 0
