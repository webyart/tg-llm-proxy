#Requires -Version 7
<#
test-volume-restore.ps1 — rehearse the §9 production-volume restore runbook in an ISOLATED compose project.

The fixed project tg-llm-proxy-restore-rehearsal and its bot-data volume must not exist before the run. The script
restores a verified backup into zzscratch, creates (but never starts) the isolated bot container, copies the file
into its volume, restores ownership 65534:65534, checks read/write access as the runtime user, copies the file back,
and compares integrity/state/table/event counters. The real tg-llm-proxy project and volume are never addressed.
The bot process is never started, so this rehearsal cannot create a second Telegram poller.

Usage:
  .\scripts\test-volume-restore.ps1 -Source .\data\backups\bot-20261101-120000.db

Exit: 0 — the isolated restore and round-trip comparison passed and all rehearsal resources were removed;
1 — validation, Docker, restore, or comparison failed. Cleanup of the isolated project is attempted in either case.
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Source
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$project = 'tg-llm-proxy-restore-rehearsal'
$volume = "${project}_bot-data"
$work = Join-Path $repoRoot 'zzscratch\volume-restore-rehearsal'
$restored = Join-Path $work 'restored.db'
$roundTrip = Join-Path $work 'roundtrip.db'
$composeResourcesMayExist = $false
$failure = $null

function Invoke-Docker([string[]]$Arguments) {
    & docker @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "docker $($Arguments -join ' ') exited $LASTEXITCODE"
    }
}

function Get-DatabaseCounts([string]$Path) {
    $report = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_inspect.ps1') -Path $Path -Counts
    if ($LASTEXITCODE -ne 0) {
        throw "db_inspect failed for $Path"
    }
    return @($report | Where-Object { $_ -match '^(integrity|last_update_id|bot_state_offset_found|table_|events_)' })
}

Push-Location $repoRoot
try {
    if (-not [System.IO.Path]::IsPathRooted($Source)) { $Source = Join-Path $repoRoot $Source }
    $Source = [System.IO.Path]::GetFullPath($Source)
    if (-not (Test-Path -LiteralPath $Source -PathType Leaf)) {
        throw "source backup $Source does not exist"
    }
    if (Test-Path -LiteralPath $work) {
        throw "rehearsal work directory already exists: $work"
    }
    if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) {
        throw 'docker is not on PATH'
    }
    $containers = (& docker compose --project-name $project ps -aq 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'cannot inspect the isolated compose project' }
    if ($containers) { throw "isolated compose project $project already has containers; refusing to remove them" }
    & docker volume inspect $volume 2>$null | Out-Null
    if ($LASTEXITCODE -eq 0) { throw "isolated volume $volume already exists; refusing to remove it" }
    & docker image inspect tg-llm-proxy-bot:latest 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'tg-llm-proxy-bot:latest is missing; run scripts/docker-build.ps1 first' }

    [void](New-Item -ItemType Directory -Path $work)
    & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_restore.ps1') -Source $Source -Dest $restored
    if ($LASTEXITCODE -ne 0) { throw 'scratch restore failed' }
    $before = Get-DatabaseCounts $restored

    $composeResourcesMayExist = $true
    Invoke-Docker @('compose', '--project-name', $project, 'create', 'bot')
    Invoke-Docker @('compose', '--project-name', $project, 'cp', $restored, 'bot:/data/bot.db')
    Invoke-Docker @('compose', '--project-name', $project, 'run', '--rm', '--no-deps', '--user', 'root', '--entrypoint', 'chown', 'bot', '65534:65534', '/data/bot.db')
    Invoke-Docker @('compose', '--project-name', $project, 'run', '--rm', '--no-deps', '--entrypoint', 'sh', 'bot', '-c', 'test -r /data/bot.db && test -w /data/bot.db')
    Invoke-Docker @('compose', '--project-name', $project, 'cp', 'bot:/data/bot.db', $roundTrip)

    $after = Get-DatabaseCounts $roundTrip
    $difference = Compare-Object $before $after
    if ($difference) {
        $difference | Format-Table | Out-String | Write-Output
        throw 'round-trip database counters differ'
    }
    $before | ForEach-Object { Write-Output $_ }
    Write-Output "volume restore rehearsal: OK — runtime access rw, $($before.Count) counters match, isolated volume=$volume"
}
catch {
    $failure = $_
    Write-Output "test-volume-restore: FAIL — $($_.Exception.Message)"
}
finally {
    if ($composeResourcesMayExist) {
        & docker compose --project-name $project down --volumes --remove-orphans 2>&1 | Out-String | Write-Output
        if ($LASTEXITCODE -ne 0 -and -not $failure) {
            $failure = 'isolated compose cleanup failed'
        }
    }
    & docker volume inspect $volume 2>$null | Out-Null
    if ($LASTEXITCODE -eq 0) {
        Write-Output "test-volume-restore: WARNING — isolated volume still exists: $volume"
        if (-not $failure) { $failure = 'isolated volume cleanup failed' }
    }
    if (Test-Path -LiteralPath $work) {
        Remove-Item -LiteralPath $work -Recurse -Force
    }
    Pop-Location
}

if ($failure) { exit 1 }
Write-Output 'test-volume-restore: cleanup OK — isolated containers, network, volume, and scratch files removed; live project untouched'
exit 0
