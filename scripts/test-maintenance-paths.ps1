#Requires -Version 7
<#
test-maintenance-paths.ps1 — host-OS case semantics and directory-boundary checks for maintenance allowed roots.
#>

. (Join-Path $PSScriptRoot 'MaintenancePaths.ps1')

function Assert-PathResult([string]$Path, [string]$Root, [bool]$Expected, [string]$Case) {
    $actual = Test-MaintenancePathWithinRoot $Path $Root
    if ($actual -ne $Expected) {
        throw "${Case}: expected $Expected for path $Path within root $Root, got $actual"
    }
}

$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\zzscratch\maintenance-paths\root'))
$child = Join-Path $root 'child\backup.db'
$caseVariantRoot = $root.ToUpperInvariant()
$caseVariantChild = Join-Path $caseVariantRoot 'child\backup.db'
$outside = "$root-outside\child\backup.db"
$caseVariantOutside = $outside.ToUpperInvariant()

try {
    Assert-PathResult $root $root $true 'exact allowed root'
    Assert-PathResult $child $root $true 'allowed child path'
    Assert-PathResult $caseVariantRoot $root $IsWindows 'case-variant allowed root'
    Assert-PathResult $caseVariantChild $root $IsWindows 'case-variant path outside a case-sensitive root'
    Assert-PathResult $outside $root $false 'directory prefix boundary'
    Assert-PathResult $caseVariantOutside $root $false 'case-variant path outside root'
    Write-Output 'test-maintenance-paths: GREEN — allowed roots, child paths, OS case semantics, and prefix boundaries verified'
    exit 0
}
catch {
    Write-Output "test-maintenance-paths: FAIL — $($_.Exception.Message)"
    exit 1
}
