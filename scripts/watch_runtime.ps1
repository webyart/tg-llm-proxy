#Requires -Version 7
<#
watch_runtime.ps1 — alert on repeated Telegram/model errors while the container may still be healthy.

Reads at most -WindowMinutes of recent logs. Each category needs -Threshold errors before an alert. Alerts repeat only
after -RepeatAlertHours. -LogPath and -StatePath support isolated acceptance without Docker or production data.
#>

[CmdletBinding()]
param(
    [ValidateRange(1, 60)]
    [int]$WindowMinutes = 10,
    [ValidateRange(2, 100)]
    [int]$Threshold = 3,
    [ValidateRange(1, 168)]
    [int]$RepeatAlertHours = 6,
    [string]$LogPath,
    [string]$StatePath,
    [switch]$NoNotify
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not $StatePath) { $StatePath = Join-Path $repoRoot 'data\monitoring\runtime-alert.json' }
if (-not [System.IO.Path]::IsPathRooted($StatePath)) { $StatePath = Join-Path $repoRoot $StatePath }

$previous = $null
if (Test-Path -LiteralPath $StatePath -PathType Leaf) {
    try { $previous = Get-Content -LiteralPath $StatePath -Raw | ConvertFrom-Json -ErrorAction Stop }
    catch { Write-Output 'watch_runtime: previous state is unreadable; treating this check as new' }
}

function Test-Due([string]$Value) {
    if (-not $Value) { return $true }
    if ($Value -is [DateTime]) { $last = $Value.ToUniversalTime() }
    else { $last = [DateTime]::Parse($Value, [System.Globalization.CultureInfo]::InvariantCulture, [System.Globalization.DateTimeStyles]::RoundtripKind).ToUniversalTime() }
    return ([DateTime]::UtcNow - $last).TotalHours -ge $RepeatAlertHours
}

function Send-Alert([string]$Text) {
    if ($NoNotify) { return $true }
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'notify-telegram.ps1') -Message $Text
    return $LASTEXITCODE -eq 0
}

if ($LogPath) {
    if (-not (Test-Path -LiteralPath $LogPath -PathType Leaf)) {
        Write-Output "watch_runtime: log fixture does not exist: $LogPath"
        exit 1
    }
    $text = Get-Content -LiteralPath $LogPath -Raw
}
else {
    Push-Location $repoRoot
    try {
        $text = docker compose logs --since "${WindowMinutes}m" bot 2>&1 | Out-String
        if ($LASTEXITCODE -ne 0) {
            Write-Output 'watch_runtime: docker logs failed'
            exit 1
        }
    }
    finally {
        Pop-Location
    }
}

$telegramCount = @([regex]::Matches($text, 'the telegram library failed')).Count
$modelCount = @([regex]::Matches($text, 'the model did not answer')).Count
$now = [DateTime]::UtcNow
$state = [ordered]@{
    telegram_alerted_at = if ($previous -and $previous.telegram_alerted_at) { [string]$previous.telegram_alerted_at } else { '' }
    model_alerted_at = if ($previous -and $previous.model_alerted_at) { [string]$previous.model_alerted_at } else { '' }
}

function Save-State {
    $parent = Split-Path -Parent $StatePath
    [void](New-Item -ItemType Directory -Path $parent -Force)
    [pscustomobject]$state | ConvertTo-Json | Set-Content -LiteralPath $StatePath -Encoding utf8NoBOM
}

foreach ($category in @(
    @{ Name = 'telegram'; Count = $telegramCount; Message = "tg-llm-proxy repeated Telegram errors: $telegramCount in the last $WindowMinutes minutes" },
    @{ Name = 'model'; Count = $modelCount; Message = "tg-llm-proxy repeated STRATA errors: $modelCount in the last $WindowMinutes minutes" }
)) {
    if ($category.Count -lt $Threshold) { continue }
    $key = "$($category.Name)_alerted_at"
    if (-not (Test-Due $state[$key])) {
        Write-Output "watch_runtime: $($category.Name) alert suppressed ($($category.Count) errors)"
        continue
    }
    if (-not (Send-Alert $category.Message)) {
        Write-Output "watch_runtime: $($category.Name) alert delivery failed"
        Save-State
        exit 1
    }
    $state[$key] = $now.ToString('o')
    Write-Output "watch_runtime: $($category.Name) alert sent ($($category.Count) errors)"
}
Save-State
Write-Output "watch_runtime: OK — telegram_errors=$telegramCount model_errors=$modelCount threshold=$Threshold window=${WindowMinutes}m"
exit 0
