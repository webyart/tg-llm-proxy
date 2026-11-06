#Requires -Version 7

function Test-MaintenancePathWithinRoot([string]$Path, [string]$Root) {
    $comparison = [System.StringComparison]::Ordinal
    if ($IsWindows) { $comparison = [System.StringComparison]::OrdinalIgnoreCase }
    $prefix = $Root + [System.IO.Path]::DirectorySeparatorChar
    return $Path.Equals($Root, $comparison) -or $Path.StartsWith($prefix, $comparison)
}
