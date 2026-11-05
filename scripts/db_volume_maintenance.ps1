#Requires -Version 7
<#
db_volume_maintenance.ps1 — clean-stop production snapshot, verified maintenance, optional external copy and alerts.

Stops the bot, copies /data/bot.db, restarts through docker-up.ps1 -NoBuild, then runs db_maintenance.ps1 on the
checkpointed file. Backup integrity and schema are verified before success. -ExternalDirectory requests an additional
verified copy; -Encrypt encrypts that external copy with AES-256-GCM. Failures trigger plain-text Telegram alerts unless -NoNotify is set. -Apply affects only backup-file
rotation; message history is never deleted. install_maintenance_schedule.ps1 registers the daily Windows task.

The work directory is an atomic lease. Its lease.json is written before the directory is moved into place and contains
a unique lease ID, UTC start time and PowerShell PID, allowing watch_container.ps1 to distinguish active maintenance
from an abandoned lease. Cleanup removes the directory only when its lease ID still matches this run. A successful run
also writes an atomic backup-verified.json receipt for the newest verified local backup.

Usage:
  .\scripts\db_volume_maintenance.ps1
  .\scripts\db_volume_maintenance.ps1 -Keep 14
  .\scripts\db_volume_maintenance.ps1 -ExternalDirectory E:\tg-llm-proxy-backups
  .\scripts\db_volume_maintenance.ps1 -Keep 7 -Apply

Exit: 0 — the bot was restarted and every requested backup/copy check passed; 1 — any stage failed.
#>

[CmdletBinding()]
param(
    [ValidateRange(1, 1000000)]
    [int]$Keep = 7,
    [string]$ExternalDirectory,
    [switch]$Encrypt,
    [string]$EncryptionKeyPath,
    [switch]$Apply,
    [switch]$Rebuild,
    [switch]$NoNotify
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$work = Join-Path $repoRoot 'zzscratch\volume-maintenance'
$source = Join-Path $work 'source.db'
$leaseStaging = $null
$leaseId = [Guid]::NewGuid().ToString('N')
$stopped = $false
$restarted = $false
$failure = $null

Push-Location $repoRoot
try {
    if (Test-Path -LiteralPath $work) { throw "work directory $work already exists — another run may be in progress or its lease is stale" }
    if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) { throw 'docker is not on PATH' }
    try {
        $leaseStaging = Join-Path $repoRoot "zzscratch\volume-maintenance.$PID.$([Guid]::NewGuid().ToString('N')).tmp"
        [void](New-Item -ItemType Directory -Path $leaseStaging)
        $lease = [ordered]@{
            lease_id = $leaseId
            started_at = [DateTime]::UtcNow.ToString('o')
            pid = $PID
        }
        [System.IO.File]::WriteAllText(
            (Join-Path $leaseStaging 'lease.json'),
            ([pscustomobject]$lease | ConvertTo-Json),
            [System.Text.UTF8Encoding]::new($false)
        )
        try {
            [System.IO.Directory]::Move($leaseStaging, $work)
            $leaseStaging = $null
        }
        catch {
            throw "could not acquire maintenance lease $work — another run may be in progress: $($_.Exception.Message)"
        }

        docker compose stop bot
        if ($LASTEXITCODE -ne 0) { throw 'docker compose stop bot failed' }
        $stopped = $true
        docker compose cp bot:/data/bot.db $source
        if ($LASTEXITCODE -ne 0) { throw 'docker compose cp of the named volume failed' }

        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'docker-up.ps1') -NoBuild
        if ($LASTEXITCODE -ne 0) { throw 'bot restart after snapshot failed' }
        $restarted = $true

        $backupDirectory = Join-Path $repoRoot 'data\backups'
        $maintenanceArgs = @('-Source', $source, '-Directory', $backupDirectory, '-Keep', $Keep)
        if ($ExternalDirectory) { $maintenanceArgs += @('-ExternalDirectory', $ExternalDirectory) }
        if ($Encrypt) { $maintenanceArgs += '-Encrypt' }
        if ($EncryptionKeyPath) { $maintenanceArgs += @('-EncryptionKeyPath', $EncryptionKeyPath) }
        if ($Apply) { $maintenanceArgs += '-Apply' }
        if ($Rebuild) { $maintenanceArgs += '-Rebuild' }
        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_maintenance.ps1') @maintenanceArgs
        if ($LASTEXITCODE -ne 0) { throw 'verified backup, retention, or external copy failed' }

        $newestBackup = Get-ChildItem -LiteralPath $backupDirectory -File -Filter 'bot-*.db' |
            Where-Object { $_.Name -match '^bot-[0-9]{8}-[0-9]{6}\.db$' } |
            Sort-Object -Property LastWriteTimeUtc -Descending |
            Select-Object -First 1
        if (-not $newestBackup) { throw 'verified backup completed but no timestamped local backup was found for its receipt' }
        $verificationPath = Join-Path $repoRoot 'data\monitoring\backup-verified.json'
        $verificationTemp = "$verificationPath.$PID.$([Guid]::NewGuid().ToString('N')).tmp"
        try {
            [void](New-Item -ItemType Directory -Path (Split-Path -Parent $verificationPath) -Force)
            $verification = [ordered]@{
                path = $newestBackup.FullName
                verified_at = [DateTime]::UtcNow.ToString('o')
            }
            [System.IO.File]::WriteAllText(
                $verificationTemp,
                ([pscustomobject]$verification | ConvertTo-Json),
                [System.Text.UTF8Encoding]::new($false)
            )
            [System.IO.File]::Move($verificationTemp, $verificationPath, $true)
        }
        finally {
            if (Test-Path -LiteralPath $verificationTemp) { Remove-Item -LiteralPath $verificationTemp -Force }
        }
        Write-Output 'db_volume_maintenance: OK — checkpointed production snapshot is verified and the bot is running'
    }
    finally {
        if ($stopped -and -not $restarted) {
            & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'docker-up.ps1') -NoBuild
        }
        if (Test-Path -LiteralPath $work) {
            $ownsWork = $false
            try {
                $currentLease = Get-Content -LiteralPath (Join-Path $work 'lease.json') -Raw | ConvertFrom-Json -ErrorAction Stop
                $ownsWork = $currentLease.lease_id -eq $leaseId
            }
            catch {
                Write-Output 'db_volume_maintenance: lease ownership could not be confirmed; leaving the work directory for monitor cleanup'
            }
            if ($ownsWork) { Remove-Item -LiteralPath $work -Recurse -Force }
        }
        if ($leaseStaging -and (Test-Path -LiteralPath $leaseStaging)) { Remove-Item -LiteralPath $leaseStaging -Recurse -Force }
    }
}
catch {
    $failure = $_
    Write-Output "db_volume_maintenance: FAIL — $($_.Exception.Message)"
    if (-not $NoNotify) {
        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'notify-telegram.ps1') -Message "tg-llm-proxy backup failed: $($_.Exception.Message)"
        if ($LASTEXITCODE -ne 0) { Write-Output 'db_volume_maintenance: failure notification was not delivered to every recipient' }
    }
}
finally {
    Pop-Location
}
if ($failure) { exit 1 }
exit 0
