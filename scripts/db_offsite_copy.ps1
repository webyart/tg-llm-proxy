#Requires -Version 7
<#
db_offsite_copy.ps1 — copy one verified backup to an explicit external directory and verify the copy.

The destination never overwrites an existing file. Plain copies use SHA-256; -Encrypt uses AES-256-GCM and verifies a temporary decrypted scratch. A partial or invalid destination created by this run is removed. The source is never modified or deleted.
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
    [string]$Directory,
    [switch]$Encrypt,
    [string]$KeyPath
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not (Test-Path -LiteralPath $Source -PathType Leaf)) {
    Write-Output "db_offsite_copy: source does not exist: $Source"
    exit 1
}
$sourcePath = (Resolve-Path -LiteralPath $Source).Path
if (-not [System.IO.Path]::IsPathRooted($Directory)) { $Directory = Join-Path $repoRoot $Directory }
$Directory = [System.IO.Path]::GetFullPath($Directory)
$destinationName = [System.IO.Path]::GetFileName($sourcePath)
if ($Encrypt) { $destinationName += '.enc' }
$destination = Join-Path $Directory $destinationName
if ($destination.Equals($sourcePath, [System.StringComparison]::OrdinalIgnoreCase)) {
    Write-Output 'db_offsite_copy: source and destination are the same file'
    exit 1
}
if (Test-Path -LiteralPath $destination) {
    Write-Output "db_offsite_copy: destination already exists; backups never overwrite: $destination"
    exit 1
}
if (-not $KeyPath) { $KeyPath = Join-Path $repoRoot 'data\backup-encryption.key' }
if (-not [System.IO.Path]::IsPathRooted($KeyPath)) { $KeyPath = Join-Path $repoRoot $KeyPath }
$KeyPath = [System.IO.Path]::GetFullPath($KeyPath)

$copyAttempted = $false
$decryptWork = $null
try {
    [void](New-Item -ItemType Directory -Path $Directory -Force)
    $copyAttempted = $true
    if ($Encrypt) {
        . (Join-Path $PSScriptRoot 'BackupCrypto.ps1')
        Protect-BackupFile -Source $sourcePath -Destination $destination -KeyPath $KeyPath
        $decryptWork = Join-Path $repoRoot ("zzscratch\offsite-decrypt-" + [Guid]::NewGuid().ToString('N'))
        [void](New-Item -ItemType Directory -Path $decryptWork)
        $inspectPath = Join-Path $decryptWork 'decrypted.db'
        Expand-BackupFile -Source $destination -Destination $inspectPath -KeyPath $KeyPath
    }
    else {
        Copy-Item -LiteralPath $sourcePath -Destination $destination -ErrorAction Stop
        $sourceHash = (Get-FileHash -LiteralPath $sourcePath -Algorithm SHA256 -ErrorAction Stop).Hash
        $destinationHash = (Get-FileHash -LiteralPath $destination -Algorithm SHA256 -ErrorAction Stop).Hash
        if ($sourceHash -ne $destinationHash) { throw 'SHA-256 mismatch after copy' }
        $inspectPath = $destination
    }

    $report = & pwsh -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'db_inspect.ps1') -Path $inspectPath -Counts
    if ($LASTEXITCODE -ne 0) { throw 'db_inspect failed for the external copy' }
    if ($report -notcontains 'integrity=ok') { throw 'external copy did not pass integrity_check' }
    if (-not ($report | Where-Object { $_ -match '^schema_version=[0-9]+$' })) { throw 'external copy has no schema version' }
    Write-Output "db_offsite_copy: OK — $destination; external copy verified"
    exit 0
}
catch {
    if ($copyAttempted -and (Test-Path -LiteralPath $destination)) {
        Remove-Item -LiteralPath $destination -Force -ErrorAction SilentlyContinue
    }
    Write-Output "db_offsite_copy: FAIL — $($_.Exception.Message)"
    exit 1
}
finally {
    if ($decryptWork -and (Test-Path -LiteralPath $decryptWork)) {
        Remove-Item -LiteralPath $decryptWork -Recurse -Force
    }
}
