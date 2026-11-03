#Requires -Version 7
<#
test-backup-crypto.ps1 — AES-GCM roundtrip and tamper rejection on scratch data only.
#>

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$work = Join-Path $repoRoot 'zzscratch\backup-crypto-acceptance'
. (Join-Path $PSScriptRoot 'BackupCrypto.ps1')
$failure = $null
try {
    if (Test-Path -LiteralPath $work) { throw "crypto acceptance directory already exists: $work" }
    [void](New-Item -ItemType Directory -Path $work)
    $keyPath = Join-Path $work 'key.bin'
    $source = Join-Path $work 'source.bin'
    $encrypted = Join-Path $work 'encrypted.bin'
    $decrypted = Join-Path $work 'decrypted.bin'
    [System.IO.File]::WriteAllText($source, 'backup plaintext with user data')
    Protect-BackupFile -Source $source -Destination $encrypted -KeyPath $keyPath
    Expand-BackupFile -Source $encrypted -Destination $decrypted -KeyPath $keyPath
    if ([System.IO.File]::ReadAllText($decrypted) -ne 'backup plaintext with user data') { throw 'roundtrip mismatch' }
    if ([System.IO.File]::ReadAllText($encrypted) -eq 'backup plaintext with user data') { throw 'plaintext leaked into encrypted payload' }

    $payload = [System.IO.File]::ReadAllBytes($encrypted)
    $payload[$payload.Length - 1] = $payload[$payload.Length - 1] -bxor 1
    [System.IO.File]::WriteAllBytes($encrypted, $payload)
    $tamperRejected = $false
    try {
        Expand-BackupFile -Source $encrypted -Destination (Join-Path $work 'tampered.bin') -KeyPath $keyPath
    }
    catch {
        $tamperRejected = $true
    }
    if (-not $tamperRejected) { throw 'tampered payload was accepted' }
    Write-Output 'test-backup-crypto: GREEN — roundtrip, plaintext absence, and tamper rejection verified'
}
catch {
    $failure = $_
    Write-Output "test-backup-crypto: FAIL — $($_.Exception.Message)"
}
finally {
    if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force }
}
if ($failure) { exit 1 }
exit 0
