#Requires -Version 7
<#
test-runtime-monitor.ps1 — threshold and suppression acceptance on scratch logs; no real notifications.
#>

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$work = Join-Path $repoRoot 'zzscratch\runtime-monitor-acceptance'
$log = Join-Path $work 'runtime.log'
$state = Join-Path $work 'state.json'
$failure = $null
try {
    if (Test-Path -LiteralPath $work) { throw "runtime monitor directory already exists: $work" }
    [void](New-Item -ItemType Directory -Path $work)
    [System.IO.File]::WriteAllText($log, "the telegram library failed`nthe telegram library failed`nthe telegram library failed`n")
    $first = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_runtime.ps1') -LogPath $log -StatePath $state -NoNotify
    if ($LASTEXITCODE -ne 0 -or ($first | Out-String) -notmatch 'telegram alert sent') { throw 'threshold did not trigger Telegram alert' }
    $second = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_runtime.ps1') -LogPath $log -StatePath $state -NoNotify
    if ($LASTEXITCODE -ne 0 -or ($second | Out-String) -notmatch 'telegram alert suppressed') { throw 'repeated Telegram alert was not suppressed' }

    [System.IO.File]::WriteAllText($log, "the model did not answer`nthe model did not answer`n")
    $below = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_runtime.ps1') -LogPath $log -StatePath $state -NoNotify
    if ($LASTEXITCODE -ne 0 -or ($below | Out-String) -match 'model alert sent') { throw 'two model errors crossed the threshold' }
    [System.IO.File]::AppendAllText($log, "the model did not answer`n")
    $third = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_runtime.ps1') -LogPath $log -StatePath $state -NoNotify
    if ($LASTEXITCODE -ne 0 -or ($third | Out-String) -notmatch 'model alert sent') { throw 'third model error did not trigger alert' }
    Write-Output 'test-runtime-monitor: GREEN — threshold and suppression verified; no notifications sent'
}
catch {
    $failure = $_
    Write-Output "test-runtime-monitor: FAIL — $($_.Exception.Message)"
}
finally {
    if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force }
}
if ($failure) { exit 1 }
exit 0
