#Requires -Version 7
<#
notify-telegram.ps1 — send a plain-text operational alert through the existing bot credentials.

Credentials come from process ENV first and .env second. TELEGRAM_ALLOWED_USERS supplies the recipients unless -ChatId
selects one explicitly. The token is never accepted as a command-line argument or printed. No parse mode is used.

Usage:
  .\scripts\notify-telegram.ps1 -Message 'tg-llm-proxy: test notification'
  .\scripts\notify-telegram.ps1 -Message 'backup failed' -ChatId 123456789

Exit: 0 — every selected recipient accepted the message; 1 — configuration or delivery failed.
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Message,
    [ValidatePattern('^-?[0-9]+$')]
    [string]$ChatId,
    [string]$ApiBaseUrl = 'https://api.telegram.org'
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$envPath = Join-Path $repoRoot '.env'
$ProgressPreference = 'SilentlyContinue'

function ReadEnv([string]$Name) {
    $processValue = [System.Environment]::GetEnvironmentVariable($Name)
    if ($processValue) { return $processValue }
    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) { return '' }
    $value = ''
    foreach ($line in Get-Content -LiteralPath $envPath) {
        if ($line -match "^\s*$Name\s*=\s*(.*?)\s*$") { $value = $matches[1] }
    }
    return $value
}

function Protect-Secret([string]$Text, [string]$Secret) {
    if ($Secret) { return $Text.Replace($Secret, '***') }
    return $Text
}

$token = ReadEnv 'TELEGRAM_BOT_TOKEN'
if (-not $token) {
    Write-Output 'notify-telegram: TELEGRAM_BOT_TOKEN is missing from process ENV and .env'
    exit 1
}
$targets = @()
if ($ChatId) {
    $targets = @($ChatId)
}
else {
    $targets = @((ReadEnv 'TELEGRAM_ALLOWED_USERS') -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ })
}
if ($targets.Count -eq 0) {
    Write-Output 'notify-telegram: no recipients; set TELEGRAM_ALLOWED_USERS or pass -ChatId'
    exit 1
}
if ([string]::IsNullOrWhiteSpace($Message)) {
    Write-Output 'notify-telegram: message must not be empty'
    exit 1
}
if ($Message.Length -gt 3500) { $Message = $Message.Substring(0, 3500) }

$failed = $false
foreach ($target in $targets) {
    try {
        $uri = "$($ApiBaseUrl.TrimEnd('/'))/bot$token/sendMessage"
        $response = Invoke-RestMethod -Uri $uri -Method Post -Body @{
            chat_id = $target
            text = $Message
            disable_web_page_preview = 'true'
        } -TimeoutSec 15 -ErrorAction Stop
        if (-not $response.ok) { throw 'Telegram returned a non-OK response' }
        Write-Output "notify-telegram: sent to chat_id=$target"
    }
    catch {
        $failed = $true
        $detail = Protect-Secret $_.Exception.Message $token
        Write-Output "notify-telegram: delivery to chat_id=$target failed — $detail"
    }
}
if ($failed) { exit 1 }
exit 0
