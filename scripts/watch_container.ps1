#Requires -Version 7
<#
watch_container.ps1 — inspect bot and Whisper health and repeated runtime errors, notify on failure/recovery, and suppress repeated alerts.

The script never restarts the container. It skips checks while db_volume_maintenance.ps1 owns its work directory.
State lives outside Git in data/monitoring/container-alert.json. A changed failure alerts immediately; an unchanged
failure repeats only after -RepeatAlertHours. -ContainerId supports an isolated fixture instead of the compose bot.

Usage:
  .\scripts\watch_container.ps1
  .\scripts\watch_container.ps1 -RepeatAlertHours 6
  .\scripts\watch_container.ps1 -ContainerId <id> -StatePath .\zzscratch\monitor-test.json -NoNotify

Exit: 0 — healthy or maintenance is in progress; 1 — unhealthy, Docker unavailable, or notification failed.
#>

[CmdletBinding()]
param(
    [string]$ContainerId,
    [string]$WhisperContainerId,
    [string]$StatePath,
    [ValidateRange(1, 168)]
    [int]$RepeatAlertHours = 6,
    [switch]$NoNotify
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$maintenanceWork = Join-Path $repoRoot 'zzscratch\volume-maintenance'
if (-not $StatePath) { $StatePath = Join-Path $repoRoot 'data\monitoring\container-alert.json' }
if (-not [System.IO.Path]::IsPathRooted($StatePath)) { $StatePath = Join-Path $repoRoot $StatePath }
if (Test-Path -LiteralPath $maintenanceWork -PathType Container) {
    Write-Output 'watch_container: SKIP — production backup maintenance is in progress'
    exit 0
}

$previous = $null
if (Test-Path -LiteralPath $StatePath -PathType Leaf) {
    try { $previous = Get-Content -LiteralPath $StatePath -Raw | ConvertFrom-Json -ErrorAction Stop }
    catch { Write-Output 'watch_container: previous state is unreadable; treating this check as new' }
}

function Save-State([string]$Status, [string]$AlertedAt) {
    $parent = Split-Path -Parent $StatePath
    [void](New-Item -ItemType Directory -Path $parent -Force)
    [pscustomobject]@{ status = $Status; alerted_at = $AlertedAt } |
        ConvertTo-Json | Set-Content -LiteralPath $StatePath -Encoding utf8NoBOM
}

function Send-Alert([string]$Text) {
    if ($NoNotify) { return $true }
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'notify-telegram.ps1') -Message $Text
    return $LASTEXITCODE -eq 0
}

$status = ''
if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) {
    $status = 'docker-cli-missing'
}
else {
    Push-Location $repoRoot
    try {
        $cid = $ContainerId
        if (-not $cid) {
            $cid = (docker compose ps -q bot 2>&1 | Out-String).Trim()
            if ($LASTEXITCODE -ne 0) { $status = 'docker-compose-unavailable' }
            elseif (-not $cid) { $status = 'container-missing' }
        }
        if ($cid -and -not $status) {
            $status = (docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' $cid 2>&1 | Out-String).Trim()
            if ($LASTEXITCODE -ne 0) { $status = 'container-inspect-failed' }
        }
        if (-not $ContainerId -and $status -in @('healthy', 'running')) {
            $whisperId = $WhisperContainerId
            if (-not $whisperId) {
                $whisperId = (docker compose ps -q whisper 2>&1 | Out-String).Trim()
                if ($LASTEXITCODE -ne 0) { $status = 'whisper-compose-unavailable' }
                elseif (-not $whisperId) { $status = 'whisper-missing' }
            }
            if ($whisperId -and $status -in @('healthy', 'running')) {
                $whisperStatus = (docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' $whisperId 2>&1 | Out-String).Trim()
                if ($LASTEXITCODE -ne 0) { $status = 'whisper-inspect-failed' }
                elseif ($whisperStatus -notin @('healthy', 'running')) { $status = "whisper-$whisperStatus" }
            }
        }
    }
    finally {
        Pop-Location
    }
}
if (-not $status) { $status = 'unknown' }
$healthy = $status -in @('healthy', 'running')
$now = [DateTime]::UtcNow

if ($healthy) {
    $recovered = $previous -and $previous.status -notin @('healthy', 'running')
    if ($recovered) {
        if (-not (Send-Alert "tg-llm-proxy container recovered: state=$status")) {
            Write-Output 'watch_container: container is healthy, but recovery notification failed'
            exit 1
        }
        Write-Output "watch_container: recovered state=$status; notification sent"
    }
    else {
        Write-Output "watch_container: OK — state=$status"
    }
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'watch_runtime.ps1') -RepeatAlertHours $RepeatAlertHours -NoNotify:$NoNotify
    if ($LASTEXITCODE -ne 0) { exit 1 }
    Save-State $status ''
    exit 0
}

$alertDue = $true
if ($previous -and $previous.status -eq $status -and $previous.alerted_at) {
    if ($previous.alerted_at -is [DateTime]) {
        $lastAlert = $previous.alerted_at.ToUniversalTime()
    }
    else {
        $lastAlert = [DateTime]::Parse([string]$previous.alerted_at, [System.Globalization.CultureInfo]::InvariantCulture, [System.Globalization.DateTimeStyles]::RoundtripKind).ToUniversalTime()
    }
    $alertDue = ($now - $lastAlert).TotalHours -ge $RepeatAlertHours
}
if ($alertDue) {
    if (-not (Send-Alert "tg-llm-proxy container failure: state=$status; inspect Docker logs")) {
        Write-Output "watch_container: FAIL — state=$status and failure notification was not delivered"
        exit 1
    }
    Save-State $status $now.ToString('o')
    Write-Output "watch_container: FAIL — state=$status; failure notification sent"
}
else {
    Save-State $status $previous.alerted_at
    Write-Output "watch_container: FAIL — state=$status; repeated alert suppressed until $RepeatAlertHours hours after the previous alert"
}
exit 1
