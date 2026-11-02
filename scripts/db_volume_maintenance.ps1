#Requires -Version 7
<#
db_volume_maintenance.ps1 — clean-stop snapshot of the production named volume followed by verified maintenance.

Stops the bot, copies /data/bot.db, restarts through docker-up.ps1 -NoBuild, then runs db_maintenance.ps1 on the
checkpointed file. Backup integrity and schema are verified before success. -Apply affects only backup-file rotation;
message history is never deleted. install_maintenance_schedule.ps1 registers a daily dry-run-retention task on Windows.

Usage:
  .\scripts\db_volume_maintenance.ps1
  .\scripts\db_volume_maintenance.ps1 -Keep 14
  .\scripts\db_volume_maintenance.ps1 -Keep 7 -Apply

Exit: 0 — the bot was restarted and the snapshot passed backup + retention checks; 1 — any stage failed.
#>

[CmdletBinding()]
param(
    [ValidateRange(1, 1000000)]
    [int]$Keep = 7,
    [switch]$Apply,
    [switch]$Rebuild
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$work = Join-Path $repoRoot 'zzscratch\volume-maintenance'
$source = Join-Path $work 'source.db'
$stopped = $false
$restarted = $false

Push-Location $repoRoot
try {
    if (Test-Path -LiteralPath $work) {
        Write-Output "db_volume_maintenance: work directory $work already exists — another run may be in progress"
        exit 1
    }
    if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) {
        Write-Output 'db_volume_maintenance: docker is not on PATH'
        exit 1
    }
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
        if ($Apply) { $maintenanceArgs += '-Apply' }
        if ($Rebuild) { $maintenanceArgs += '-Rebuild' }
        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_maintenance.ps1') @maintenanceArgs
        if ($LASTEXITCODE -ne 0) { throw 'verified backup or retention failed' }
        Write-Output 'db_volume_maintenance: OK — checkpointed production snapshot is verified and the bot is running'
        exit 0
    }
    finally {
        if ($stopped -and -not $restarted) {
            & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'docker-up.ps1') -NoBuild
        }
        if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force }
    }
}
finally {
    Pop-Location
}
