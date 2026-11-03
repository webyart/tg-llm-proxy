#Requires -Version 7
<#
db_volume_maintenance.ps1 — clean-stop production snapshot, verified maintenance, optional external copy and alerts.

Stops the bot, copies /data/bot.db, restarts through docker-up.ps1 -NoBuild, then runs db_maintenance.ps1 on the
checkpointed file. Backup integrity and schema are verified before success. -ExternalDirectory requests an additional
verified copy; -Encrypt encrypts that external copy with AES-256-GCM. Failures trigger plain-text Telegram alerts unless -NoNotify is set. -Apply affects only backup-file
rotation; message history is never deleted. install_maintenance_schedule.ps1 registers the daily Windows task.

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
$stopped = $false
$restarted = $false
$failure = $null

Push-Location $repoRoot
try {
    if (Test-Path -LiteralPath $work) { throw "work directory $work already exists — another run may be in progress" }
    if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) { throw 'docker is not on PATH' }
    [void](New-Item -ItemType Directory -Path $work)
    try {
        docker compose stop bot
        if ($LASTEXITCODE -ne 0) { throw 'docker compose stop bot failed' }
        $stopped = $true
        docker compose cp bot:/data/bot.db $source
        if ($LASTEXITCODE -ne 0) { throw 'docker compose cp of the named volume failed' }

        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'docker-up.ps1') -NoBuild
        if ($LASTEXITCODE -ne 0) { throw 'bot restart after snapshot failed' }
        $restarted = $true

        $maintenanceArgs = @('-Source', $source, '-Directory', (Join-Path $repoRoot 'data\backups'), '-Keep', $Keep)
        if ($ExternalDirectory) { $maintenanceArgs += @('-ExternalDirectory', $ExternalDirectory) }
        if ($Encrypt) { $maintenanceArgs += '-Encrypt' }
        if ($EncryptionKeyPath) { $maintenanceArgs += @('-EncryptionKeyPath', $EncryptionKeyPath) }
        if ($Apply) { $maintenanceArgs += '-Apply' }
        if ($Rebuild) { $maintenanceArgs += '-Rebuild' }
        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_maintenance.ps1') @maintenanceArgs
        if ($LASTEXITCODE -ne 0) { throw 'verified backup, retention, or external copy failed' }
        Write-Output 'db_volume_maintenance: OK — checkpointed production snapshot is verified and the bot is running'
    }
    finally {
        if ($stopped -and -not $restarted) {
            & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'docker-up.ps1') -NoBuild
        }
        if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force }
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
