#Requires -Version 7
<#
watch_container.ps1 — inspect bot and Whisper health, maintenance leases, backup freshness and repeated runtime errors.

The script never restarts the container. It skips checks only for a fresh maintenance lease whose PID is still active.
A stale, dead or malformed lease is removed and alerted instead of suppressing monitoring forever. State lives outside
Git in data/monitoring/container-alert.json. A changed failure alerts immediately; an unchanged failure repeats only
after -RepeatAlertHours. -ContainerId supports an isolated fixture instead of the compose bot.

Backup freshness uses the atomic data/monitoring/backup-verified.json receipt written after verified volume maintenance.
Before the first receipt exists, timestamped bot-YYYYMMDD-HHMMSS.db files are accepted as legacy verified backups because
that was the existing backup contract. -BackupMaxAgeHours defaults to 30, and backup alerts have independent suppression
state in data/monitoring/backup-alert.json. -BackupDirectory, -BackupVerificationPath and -BackupStatePath support isolated tests.

Usage:
  .\scripts\watch_container.ps1
  .\scripts\watch_container.ps1 -RepeatAlertHours 6 -BackupMaxAgeHours 36 -MaintenanceLeaseMinutes 240
  .\scripts\watch_container.ps1 -ContainerId <id> -StatePath .\zzscratch\monitor-test.json -NoNotify

Exit: 0 — healthy, or fresh active maintenance is in progress; 1 — any health, lease, backup or notification check failed.
#>

[CmdletBinding()]
param(
    [string]$ContainerId,
    [string]$WhisperContainerId,
    [string]$StatePath,
    [ValidateRange(1, 168)]
    [int]$RepeatAlertHours = 6,
    [ValidateRange(1, 10080)]
    [int]$MaintenanceLeaseMinutes = 180,
    [string]$BackupDirectory,
    [ValidateRange(1, 8760)]
    [int]$BackupMaxAgeHours = 30,
    [string]$BackupVerificationPath,
    [string]$BackupStatePath,
    [switch]$NoNotify
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$maintenanceWork = Join-Path $repoRoot 'zzscratch\volume-maintenance'
$maintenanceLease = Join-Path $maintenanceWork 'lease.json'
if (-not $StatePath) { $StatePath = Join-Path $repoRoot 'data\monitoring\container-alert.json' }
if (-not $BackupDirectory) { $BackupDirectory = Join-Path $repoRoot 'data\backups' }
if (-not $BackupVerificationPath) { $BackupVerificationPath = Join-Path $repoRoot 'data\monitoring\backup-verified.json' }
if (-not $BackupStatePath) { $BackupStatePath = Join-Path $repoRoot 'data\monitoring\backup-alert.json' }
foreach ($pathName in @('StatePath', 'BackupDirectory', 'BackupVerificationPath', 'BackupStatePath')) {
    $value = Get-Variable -Name $pathName -ValueOnly
    if (-not [System.IO.Path]::IsPathRooted($value)) { Set-Variable -Name $pathName -Value (Join-Path $repoRoot $value) }
}
$BackupDirectory = [System.IO.Path]::GetFullPath($BackupDirectory)

function ConvertTo-Utc([object]$Value) {
    if ($Value -is [DateTime]) { return $Value.ToUniversalTime() }
    return [DateTime]::Parse([string]$Value, [System.Globalization.CultureInfo]::InvariantCulture, [System.Globalization.DateTimeStyles]::RoundtripKind).ToUniversalTime()
}

function Read-JsonFile([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $null }
    return Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -ErrorAction Stop
}

function Test-WithinRoot([string]$Path, [string]$Root) {
    $prefix = $Root + [System.IO.Path]::DirectorySeparatorChar
    return $Path.Equals($Root, [System.StringComparison]::OrdinalIgnoreCase) -or
        $Path.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)
}

function Test-Due([object]$Value, [datetime]$Now) {
    if (-not $Value) { return $true }
    return ($Now - (ConvertTo-Utc $Value)).TotalHours -ge $RepeatAlertHours
}

function Send-Alert([string]$Text) {
    if ($NoNotify) { return $true }
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'notify-telegram.ps1') -Message $Text
    return $LASTEXITCODE -eq 0
}

$previous = $null
try { $previous = Read-JsonFile $StatePath }
catch { Write-Output 'watch_container: previous state is unreadable; treating this check as new' }

function Save-State([string]$Status, [object]$AlertedAt) {
    $parent = Split-Path -Parent $StatePath
    [void](New-Item -ItemType Directory -Path $parent -Force)
    [pscustomobject]@{ status = $Status; alerted_at = if ($AlertedAt) { [string]$AlertedAt } else { '' } } |
        ConvertTo-Json | Set-Content -LiteralPath $StatePath -Encoding utf8NoBOM
}

$operationFailed = $false
$now = [DateTime]::UtcNow
if (Test-Path -LiteralPath $maintenanceWork -PathType Container) {
    $staleReason = $null
    if (-not (Test-Path -LiteralPath $maintenanceLease -PathType Leaf)) {
        $staleReason = 'lease metadata is missing'
    }
    else {
        try {
            $lease = Read-JsonFile $maintenanceLease
            $leaseStarted = ConvertTo-Utc $lease.started_at
            $leasePid = [int]$lease.pid
            if ($leasePid -le 0) { throw 'lease PID must be positive' }
            if ($leaseStarted -gt $now.AddMinutes(5)) {
                $staleReason = 'lease UTC timestamp is invalid'
            }
            elseif (($now - $leaseStarted).TotalMinutes -ge $MaintenanceLeaseMinutes) {
                $staleReason = "lease exceeded $MaintenanceLeaseMinutes minutes"
            }
            elseif (-not (Get-Process -Id $leasePid -ErrorAction SilentlyContinue)) {
                $staleReason = "lease PID $leasePid is not running"
            }
        }
        catch {
            $staleReason = "lease metadata is unreadable: $($_.Exception.Message)"
        }
    }

    if (-not $staleReason) {
        Write-Output 'watch_container: SKIP — fresh active production backup maintenance lease'
        exit 0
    }

    $operationFailed = $true
    $staleWork = Join-Path $repoRoot "zzscratch\volume-maintenance.stale.$([Guid]::NewGuid().ToString('N')).tmp"
    try {
        if (Test-Path -LiteralPath $maintenanceWork -PathType Container) {
            [System.IO.Directory]::Move($maintenanceWork, $staleWork)
            Remove-Item -LiteralPath $staleWork -Recurse -Force -ErrorAction Stop
        }
        Write-Output "watch_container: stale maintenance lease removed — $staleReason"
    }
    catch {
        $staleReason = "$staleReason; cleanup failed: $($_.Exception.Message)"
        Write-Output "watch_container: FAIL — stale maintenance lease could not be removed — $staleReason"
        if (Test-Path -LiteralPath $staleWork) { Remove-Item -LiteralPath $staleWork -Recurse -Force -ErrorAction SilentlyContinue }
    }
    if (Send-Alert "tg-llm-proxy maintenance lease stale: $staleReason; monitoring resumed") {
        Write-Output 'watch_container: stale maintenance lease alert sent'
    }
    else {
        Write-Output 'watch_container: stale maintenance lease alert delivery failed'
    }
}

$backupPrevious = $null
try { $backupPrevious = Read-JsonFile $BackupStatePath }
catch { Write-Output 'watch_container: previous backup state is unreadable; treating this check as new' }

function Save-BackupState([string]$Status, [object]$AlertedAt) {
    $parent = Split-Path -Parent $BackupStatePath
    [void](New-Item -ItemType Directory -Path $parent -Force)
    [pscustomobject]@{ status = $Status; alerted_at = if ($AlertedAt) { [string]$AlertedAt } else { '' } } |
        ConvertTo-Json | Set-Content -LiteralPath $BackupStatePath -Encoding utf8NoBOM
}

$newestBackup = $null
$backupTime = $null
$backupSource = 'legacy'
try {
    $verification = Read-JsonFile $BackupVerificationPath
    if ($verification -and $verification.path -and $verification.verified_at) {
        $verifiedPath = [string]$verification.path
        if (-not [System.IO.Path]::IsPathRooted($verifiedPath)) { $verifiedPath = Join-Path $BackupDirectory $verifiedPath }
        $verifiedPath = [System.IO.Path]::GetFullPath($verifiedPath)
        if ((Test-WithinRoot $verifiedPath $BackupDirectory) -and
            ((Split-Path -Leaf $verifiedPath) -match '^bot-[0-9]{8}-[0-9]{6}\.db$') -and
            (Test-Path -LiteralPath $verifiedPath -PathType Leaf)) {
            $newestBackup = Get-Item -LiteralPath $verifiedPath
            $backupTime = ConvertTo-Utc $verification.verified_at
            $backupSource = 'receipt'
        }
    }
}
catch {
    Write-Output 'watch_container: backup verification receipt is unreadable; using legacy timestamped backups'
}
if (-not $newestBackup -and (Test-Path -LiteralPath $BackupDirectory -PathType Container)) {
    $newestBackup = Get-ChildItem -LiteralPath $BackupDirectory -File -Filter 'bot-*.db' |
        Where-Object { $_.Name -match '^bot-[0-9]{8}-[0-9]{6}\.db$' } |
        Sort-Object -Property LastWriteTimeUtc -Descending |
        Select-Object -First 1
    if ($newestBackup) { $backupTime = $newestBackup.LastWriteTimeUtc }
}

$backupStatus = 'missing'
$backupAgeHours = $null
if ($newestBackup) {
    $backupAgeHours = ($now - $backupTime).TotalHours
    $backupStatus = if ($backupAgeHours -gt $BackupMaxAgeHours) { 'stale' } else { 'fresh' }
}

if ($backupStatus -eq 'fresh') {
    if ($backupPrevious -and $backupPrevious.status -and $backupPrevious.status -ne 'fresh') {
        if (-not (Send-Alert "tg-llm-proxy local backup recovered: newest verified backup is $($newestBackup.Name)")) {
            Write-Output 'watch_container: backup is fresh, but recovery notification failed'
            $operationFailed = $true
        }
        else {
            Write-Output 'watch_container: backup recovery notification sent'
            Save-BackupState 'fresh' ''
        }
    }
    else {
        Save-BackupState 'fresh' ''
    }
    Write-Output "watch_container: backup OK — newest=$($newestBackup.Name) age_hours=$([Math]::Round($backupAgeHours, 2)) max_age_hours=$BackupMaxAgeHours source=$backupSource"
}
else {
    $operationFailed = $true
    $backupDetail = if ($newestBackup) {
        "newest=$($newestBackup.Name) age_hours=$([Math]::Round($backupAgeHours, 2)) max_age_hours=$BackupMaxAgeHours source=$backupSource"
    }
    else {
        "no verified local bot-YYYYMMDD-HHMMSS.db backup exists in $BackupDirectory"
    }
    $backupAlertedAt = $now.ToString('o')
    $backupAlertDue = $true
    if ($backupPrevious -and $backupPrevious.status -eq $backupStatus -and $backupPrevious.alerted_at) {
        $backupAlertDue = Test-Due $backupPrevious.alerted_at $now
        if (-not $backupAlertDue) { $backupAlertedAt = $backupPrevious.alerted_at }
    }
    if ($backupAlertDue) {
        if (Send-Alert "tg-llm-proxy local backup freshness failure: $backupDetail") {
            Save-BackupState $backupStatus $backupAlertedAt
            Write-Output "watch_container: backup alert sent — status=$backupStatus; $backupDetail"
        }
        else {
            Write-Output "watch_container: backup alert delivery failed — status=$backupStatus; $backupDetail"
        }
    }
    else {
        Save-BackupState $backupStatus $backupAlertedAt
        Write-Output "watch_container: backup alert suppressed — status=$backupStatus; $backupDetail"
    }
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
    if ($operationFailed) { exit 1 }
    exit 0
}

$alertDue = $true
if ($previous -and $previous.status -eq $status -and $previous.alerted_at) {
    $alertDue = Test-Due $previous.alerted_at $now
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
