#Requires -Version 7
<#
db_offsite_copy.ps1 — copy one verified backup to an explicit external directory and verify the copy.

The destination never overwrites an existing file. SHA-256 must match and the copied database must pass db_inspect.
A partial or invalid destination created by this run is removed. The source is never modified or deleted.
The operator chooses the external directory: a removable drive, UNC share, or cloud-synchronized folder.

Usage:
  .\scripts\db_offsite_copy.ps1 -Source .\data\backups\bot-YYYYMMDD-HHMMSS.db -Directory E:\tg-llm-proxy-backups
  .\scripts\db_offsite_copy.ps1 -Source .\data\backups\bot-YYYYMMDD-HHMMSS.db -Directory \\NAS\backups\tg-llm-proxy

Exit: 0 — the new external copy passed hash and SQLite checks; 1 — any stage failed.
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Source,
    [Parameter(Mandatory)]
    [string]$Directory
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not (Test-Path -LiteralPath $Source -PathType Leaf)) {
    Write-Output "db_offsite_copy: source does not exist: $Source"
    exit 1
}
$sourcePath = (Resolve-Path -LiteralPath $Source).Path
if (-not [System.IO.Path]::IsPathRooted($Directory)) { $Directory = Join-Path $repoRoot $Directory }
$Directory = [System.IO.Path]::GetFullPath($Directory)
$destination = Join-Path $Directory ([System.IO.Path]::GetFileName($sourcePath))
if ($destination.Equals($sourcePath, [System.StringComparison]::OrdinalIgnoreCase)) {
    Write-Output 'db_offsite_copy: source and destination are the same file'
    exit 1
}
if (Test-Path -LiteralPath $destination) {
    Write-Output "db_offsite_copy: destination already exists; backups never overwrite: $destination"
    exit 1
}

$copyAttempted = $false
try {
    [void](New-Item -ItemType Directory -Path $Directory -Force)
    $copyAttempted = $true
    Copy-Item -LiteralPath $sourcePath -Destination $destination -ErrorAction Stop
    $sourceHash = (Get-FileHash -LiteralPath $sourcePath -Algorithm SHA256 -ErrorAction Stop).Hash
    $destinationHash = (Get-FileHash -LiteralPath $destination -Algorithm SHA256 -ErrorAction Stop).Hash
    if ($sourceHash -ne $destinationHash) { throw 'SHA-256 mismatch after copy' }

    $report = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_inspect.ps1') -Path $destination -Counts
    if ($LASTEXITCODE -ne 0) { throw 'db_inspect failed for the external copy' }
    if ($report -notcontains 'integrity=ok') { throw 'external copy did not pass integrity_check' }
    if (-not ($report | Where-Object { $_ -match '^schema_version=[0-9]+$' })) { throw 'external copy has no schema version' }
    Write-Output "db_offsite_copy: OK — $destination; SHA-256 and SQLite integrity/schema verified"
    exit 0
}
catch {
    if ($copyAttempted -and (Test-Path -LiteralPath $destination)) {
        Remove-Item -LiteralPath $destination -Force -ErrorAction SilentlyContinue
    }
    Write-Output "db_offsite_copy: FAIL — $($_.Exception.Message)"
    exit 1
}
