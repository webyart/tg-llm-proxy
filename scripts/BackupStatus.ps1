#Requires -Version 7
<#
BackupStatus.ps1 — the Prometheus textfile of the local backup pipeline.

scripts/db_volume_maintenance.ps1 writes data/backup-status.prom through Write-BackupStatus. The file is
replaced with File.Move(..., overwrite) after the complete payload has been written to a sibling temp file, so
node_exporter's textfile collector never reads half a file. The four metrics are:

  backup_last_success_unixtime   UTC unix time of the last verified local backup
  backup_last_size_bytes         size of that verified backup file
  backup_last_duration_seconds   copy + verification stage of the last successful/attempted run
  backup_last_ok                 1 after a verified backup, 0 when the maintenance run failed

A failed run preserves the previous success timestamp/size/duration and only flips backup_last_ok to 0; a
missing or unreadable previous file yields zeroes. This is a local-only file by the operator's decision — no
credentials, no host names, no paths.
#>

function Read-BackupStatusMetrics {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Path)
    $metrics = @{}
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $metrics }
    foreach ($line in Get-Content -LiteralPath $Path -ErrorAction Stop) {
        if ($line -match '^\s*([A-Za-z_:][A-Za-z0-9_:]*)\s+([-+]?[0-9]+(?:\.[0-9]+)?(?:[eE][-+]?[0-9]+)?)\s*$') {
            $metrics[$matches[1]] = $matches[2]
        }
    }
    return $metrics
}

function Write-BackupStatus {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][bool]$Ok,
        [datetime]$SuccessUtc = [DateTime]::MinValue,
        [long]$SizeBytes = 0,
        [double]$DurationSeconds = 0
    )

    $success = [long]0
    $size = [long]0
    $duration = [double]0
    if ($Ok) {
        if ($SuccessUtc -eq [DateTime]::MinValue) { $SuccessUtc = [DateTime]::UtcNow }
        $success = ([DateTimeOffset]$SuccessUtc.ToUniversalTime()).ToUnixTimeSeconds()
        $size = $SizeBytes
        $duration = if ($DurationSeconds -lt 0) { 0 } else { $DurationSeconds }
    }
    else {
        # Keep the last known-good numbers so the dashboard does not lose the last successful backup just
        # because tonight's run failed before it wrote a new one.
        try {
            $previous = Read-BackupStatusMetrics -Path $Path
            if ($previous.ContainsKey('backup_last_success_unixtime')) { $success = [long]$previous['backup_last_success_unixtime'] }
            if ($previous.ContainsKey('backup_last_size_bytes')) { $size = [long]$previous['backup_last_size_bytes'] }
            if ($previous.ContainsKey('backup_last_duration_seconds')) { $duration = [double]$previous['backup_last_duration_seconds'] }
        }
        catch {
            # The previous file is optional telemetry; never turn its unreadability into a second failure.
        }
    }

    $invariant = [System.Globalization.CultureInfo]::InvariantCulture
    $durationText = $duration.ToString('0.###', $invariant)
    $lines = @(
        '# HELP backup_last_success_unixtime Unix time (seconds) of the last verified local backup.'
        '# TYPE backup_last_success_unixtime gauge'
        "backup_last_success_unixtime $success"
        '# HELP backup_last_size_bytes Size in bytes of the last verified local backup.'
        '# TYPE backup_last_size_bytes gauge'
        "backup_last_size_bytes $size"
        '# HELP backup_last_duration_seconds Duration of the copy and verification stage, in seconds.'
        '# TYPE backup_last_duration_seconds gauge'
        "backup_last_duration_seconds $durationText"
        '# HELP backup_last_ok 1 when the maintenance run produced a verified backup, 0 when it failed.'
        '# TYPE backup_last_ok gauge'
        "backup_last_ok $(if ($Ok) { 1 } else { 0 })"
    )
    $payload = ($lines -join "`n") + "`n"

    $directory = Split-Path -Parent $Path
    if ($directory) { [void](New-Item -ItemType Directory -Path $directory -Force) }
    $temp = "$Path.$PID.$([Guid]::NewGuid().ToString('N')).tmp"
    try {
        [System.IO.File]::WriteAllText($temp, $payload, [System.Text.UTF8Encoding]::new($false))
        [System.IO.File]::Move($temp, $Path, $true)
    }
    finally {
        if (Test-Path -LiteralPath $temp) { Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue }
    }
}
