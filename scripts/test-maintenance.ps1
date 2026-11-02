#Requires -Version 7
<#

Covers backup → scratch restore → counter comparison, verified external copy without overwrite, maintenance
dry-run/apply rotation, and integrity of every retained generated backup. -WithVolumeRestore additionally runs the
isolated compose-volume and container-monitor rehearsals and therefore requires Docker plus tg-llm-proxy-bot:latest.
All fixture files and containers are removed in finally.

Usage:
  .\scripts\test-maintenance.ps1
  .\scripts\test-maintenance.ps1 -WithVolumeRestore
#>

[CmdletBinding()]
param(
    [switch]$WithVolumeRestore
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$work = Join-Path $repoRoot 'zzscratch\maintenance-acceptance'
$source = Join-Path $work 'source.db'
$backup = Join-Path $work 'manual-backup.db'
$restored = Join-Path $work 'restored.db'
$fixtureExe = Join-Path $repoRoot 'bin\dbfixture.exe'
$failure = $null

function Get-DatabaseCounts([string]$Path) {
    $report = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_inspect.ps1') -Path $Path -Counts
    if ($LASTEXITCODE -ne 0) { throw "db_inspect failed for $Path" }
    return @($report | Where-Object { $_ -match '^(integrity|schema_version|last_update_id|bot_state_offset_found|table_|events_)' })
}

function Assert-SameCounts([string]$ExpectedPath, [string]$ActualPath) {
    $expected = Get-DatabaseCounts $ExpectedPath
    $actual = Get-DatabaseCounts $ActualPath
    $difference = Compare-Object $expected $actual
    if ($difference) {
        $difference | Format-Table | Out-String | Write-Output
        throw "database counters differ: $ExpectedPath versus $ActualPath"
    }
}

Push-Location $repoRoot
try {
    if (Test-Path -LiteralPath $work) { throw "acceptance work directory already exists: $work" }
    [void](New-Item -ItemType Directory -Path $work)

    $needsBuild = -not (Test-Path -LiteralPath $fixtureExe)
    if (-not $needsBuild) {
        $exeStamp = (Get-Item -LiteralPath $fixtureExe).LastWriteTimeUtc
        $sources = @(Get-ChildItem -LiteralPath (Join-Path $repoRoot 'cmd\dbfixture') -File -Filter *.go)
        $sources += Get-ChildItem -LiteralPath (Join-Path $repoRoot 'internal\store') -File -Filter *.go
        foreach ($src in $sources) {
            if ($src.LastWriteTimeUtc -gt $exeStamp) { $needsBuild = $true; break }
        }
    }
    if ($needsBuild) {
        $env:CGO_ENABLED = '0'
        New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'bin') | Out-Null
        $build = (go build -o bin/dbfixture.exe ./cmd/dbfixture 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) { throw "go build ./cmd/dbfixture failed: $build" }
    }
    & $fixtureExe -path $source
    if ($LASTEXITCODE -ne 0) { throw 'dbfixture failed' }

    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_backup.ps1') -Source $source -Dest $backup
    if ($LASTEXITCODE -ne 0) { throw 'backup acceptance failed' }
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_restore.ps1') -Source $backup -Dest $restored
    if ($LASTEXITCODE -ne 0) { throw 'restore acceptance failed' }
    Assert-SameCounts $source $restored
    Write-Output 'maintenance acceptance: backup/restore counters match'

    $external = Join-Path $work 'external-copy'
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_offsite_copy.ps1') -Source $backup -Directory $external
    if ($LASTEXITCODE -ne 0) { throw 'external copy acceptance failed' }
    $externalBackup = Join-Path $external 'manual-backup.db'
    Assert-SameCounts $backup $externalBackup
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_offsite_copy.ps1') -Source $backup -Directory $external
    if ($LASTEXITCODE -eq 0) { throw 'external copy overwrote an existing destination' }
    Write-Output 'maintenance acceptance: external copy matches and existing destinations are never overwritten'

    $legacy = Join-Path $work 'legacy.db'
    & $fixtureExe -path $legacy -pre-version
    if ($LASTEXITCODE -ne 0) { throw 'legacy dbfixture failed' }
    $legacyBefore = Get-DatabaseCounts $legacy
    if ($legacyBefore -notcontains 'schema_version=0') { throw 'legacy fixture must start at schema_version=0' }
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_prune.ps1') -Source $legacy -EventsDays 100000 -Apply -Backup (Join-Path $work 'legacy-pre-migration.db')
    if ($LASTEXITCODE -ne 0) { throw 'pre-versioning adoption failed' }
    $legacyAfter = Get-DatabaseCounts $legacy
    foreach ($want in @('schema_version=4', 'table_schema_migrations=4', 'table_events=2', 'table_messages=4', 'last_update_id=4242', 'integrity=ok')) {
        if ($legacyAfter -notcontains $want) { throw "migration result misses $want" }
    }
    $beforeCore = @($legacyBefore | Where-Object { $_ -notmatch '^(schema_version|table_schema_migrations)=' })
    $afterCore = @($legacyAfter | Where-Object { $_ -notmatch '^(schema_version|table_schema_migrations)=' })
    if (Compare-Object $beforeCore $afterCore) { throw 'migration changed application data' }
    Write-Output 'maintenance acceptance: pre-versioning backup adopted at schema_version=4 without application-data changes'

    $oldNames = @('bot-20000101-000000.db', 'bot-20000102-000000.db')
    foreach ($name in $oldNames + @('bot-manual.db')) {
        Set-Content -LiteralPath (Join-Path $work $name) -Value 'rotation fixture' -NoNewline
    }
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_maintenance.ps1') -Source $source -Directory $work -Keep 2
    if ($LASTEXITCODE -ne 0) { throw 'maintenance dry-run failed' }
    foreach ($name in $oldNames) {
        if (-not (Test-Path -LiteralPath (Join-Path $work $name))) { throw "maintenance dry-run deleted $name" }
    }

    Start-Sleep -Milliseconds 1100
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_maintenance.ps1') -Source $source -Directory $work -Keep 2 -Apply
    if ($LASTEXITCODE -ne 0) { throw 'maintenance apply failed' }
    foreach ($name in $oldNames) {
        if (Test-Path -LiteralPath (Join-Path $work $name)) { throw "expired backup survived: $name" }
    }
    if (-not (Test-Path -LiteralPath (Join-Path $work 'bot-manual.db'))) { throw 'manual backup name was rotated' }

    $generated = @(Get-ChildItem -LiteralPath $work -File -Filter 'bot-*.db' |
        Where-Object { $_.Name -match '^bot-[0-9]{8}-[0-9]{6}\.db$' })
    if ($generated.Count -ne 2) { throw "expected two retained generated backups, got $($generated.Count)" }
    foreach ($file in $generated) { Assert-SameCounts $source $file.FullName }
    Write-Output 'maintenance acceptance: dry-run preserved files, apply removed exactly two old copies, retained backups are valid'

    $beforePrune = Get-DatabaseCounts $source
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_prune.ps1') -Source $source -EventsDays 90
    if ($LASTEXITCODE -ne 0) { throw 'prune dry-run failed' }
    $afterDryRun = Get-DatabaseCounts $source
    if (Compare-Object $beforePrune $afterDryRun) { throw 'prune dry-run changed the fixture' }
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_prune.ps1') -Source $source -EventsDays 90 -Apply -Backup (Join-Path $work 'pre-prune-events.db')
    if ($LASTEXITCODE -ne 0) { throw 'event prune apply failed' }
    $afterEvents = Get-DatabaseCounts $source
    foreach ($want in @('table_events=1', 'table_messages=4', 'integrity=ok')) {
        if ($afterEvents -notcontains $want) { throw "event prune result misses $want" }
    }
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_prune.ps1') -Source $source -EventsDays 0 -MessagesDays 90 -Apply -Backup (Join-Path $work 'pre-prune-messages.db')
    if ($LASTEXITCODE -ne 0) { throw 'message prune apply failed' }
    $afterMessages = Get-DatabaseCounts $source
    foreach ($want in @('table_events=1', 'table_messages=2', 'last_update_id=4242', 'integrity=ok')) {
        if ($afterMessages -notcontains $want) { throw "message prune result misses $want" }
    }
    Write-Output 'maintenance acceptance: prune dry-run was read-only; apply kept verified backups and removed only old rows'

    if ($WithVolumeRestore) {
        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'test-volume-restore.ps1') -Source $backup
        if ($LASTEXITCODE -ne 0) { throw 'isolated volume restore rehearsal failed' }
        & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'test-container-monitor.ps1')
        if ($LASTEXITCODE -ne 0) { throw 'container monitor acceptance failed' }
    }
}
catch {
    $failure = $_
    Write-Output "test-maintenance: FAIL — $($_.Exception.Message)"
}
finally {
    if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force }
    Pop-Location
}

if ($failure) { exit 1 }
Write-Output 'test-maintenance: GREEN — fixture files removed; live database and volume were not used'
exit 0
