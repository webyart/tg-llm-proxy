#Requires -Version 7
<#
test-notifications.ps1 — loopback acceptance for Telegram alert delivery; no real token or network is used.
#>

[CmdletBinding()]
param()

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$portProbe = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
$portProbe.Start()
$port = ([System.Net.IPEndPoint]$portProbe.LocalEndpoint).Port
$portProbe.Stop()
$job = $null
$oldToken = $env:TELEGRAM_BOT_TOKEN
$oldUsers = $env:TELEGRAM_ALLOWED_USERS

try {
    $job = Start-Job -ScriptBlock {
        param($Port)
        $listener = [System.Net.HttpListener]::new()
        try {
            $listener.Prefixes.Add("http://127.0.0.1:$Port/")
            $listener.Start()
            $context = $listener.GetContext()
            $reader = [System.IO.StreamReader]::new($context.Request.InputStream, $context.Request.ContentEncoding)
            $body = $reader.ReadToEnd()
            $reader.Dispose()
            $payload = [System.Text.Encoding]::UTF8.GetBytes('{"ok":true,"result":{}}')
            $context.Response.ContentType = 'application/json'
            $context.Response.ContentLength64 = $payload.Length
            $context.Response.OutputStream.Write($payload, 0, $payload.Length)
            $context.Response.Close()
            [pscustomobject]@{ Path = $context.Request.Url.AbsolutePath; Body = $body }
        }
        finally {
            $listener.Stop()
            $listener.Close()
        }
    } -ArgumentList $port

    $env:TELEGRAM_BOT_TOKEN = 'loopback-token'
    $env:TELEGRAM_ALLOWED_USERS = '111,222'
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'notify-telegram.ps1') `
        -Message 'backup failed: integrity check' -ChatId 4242 -ApiBaseUrl "http://127.0.0.1:$port"
    if ($LASTEXITCODE -ne 0) { throw 'notify-telegram rejected the loopback response' }
    $finished = Wait-Job -Job $job -Timeout 20
    if (-not $finished -or $job.State -ne 'Completed') { throw "loopback Telegram job state: $($job.State)" }
    $request = Receive-Job -Job $job
    if ($request.Path -ne '/botloopback-token/sendMessage') { throw 'unexpected Telegram request path' }
    $decoded = [System.Net.WebUtility]::UrlDecode($request.Body)
    foreach ($want in @('chat_id=4242', 'text=backup failed: integrity check', 'disable_web_page_preview=true')) {
        if ($decoded -notlike "*$want*") { throw "notification form misses $want" }
    }
    Write-Output 'test-notifications: GREEN — plain-text alert, selected recipient and fake Telegram response verified'
    exit 0
}
catch {
    Write-Output "test-notifications: FAIL — $($_.Exception.Message)"
    exit 1
}
finally {
    $env:TELEGRAM_BOT_TOKEN = $oldToken
    $env:TELEGRAM_ALLOWED_USERS = $oldUsers
    if ($job) { Remove-Job -Job $job -Force }
}
