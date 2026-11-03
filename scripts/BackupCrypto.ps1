#Requires -Version 7

function Get-BackupEncryptionKey([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        $parent = Split-Path -Parent $Path
        [void](New-Item -ItemType Directory -Path $parent -Force)
        $newKey = New-Object byte[] 32
        [System.Security.Cryptography.RandomNumberGenerator]::Fill($newKey)
        [System.IO.File]::WriteAllBytes($Path, $newKey)
        if (-not $IsWindows) { & chmod 600 $Path }
    }
    $key = [System.IO.File]::ReadAllBytes($Path)
    if ($key.Length -ne 32) { throw "backup crypto: key must contain exactly 32 bytes: $Path" }
    return $key
}

function Protect-BackupFile {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Source,
        [Parameter(Mandatory)][string]$Destination,
        [Parameter(Mandatory)][string]$KeyPath
    )
    $key = Get-BackupEncryptionKey $KeyPath
    $plain = [System.IO.File]::ReadAllBytes($Source)
    $nonce = New-Object byte[] 12
    [System.Security.Cryptography.RandomNumberGenerator]::Fill($nonce)
    $cipher = New-Object byte[] $plain.Length
    $tag = New-Object byte[] 16
    $aes = [System.Security.Cryptography.AesGcm]::new($key)
    try {
        $aes.Encrypt($nonce, $plain, $cipher, $tag)
    }
    finally {
        $aes.Dispose()
        [System.Array]::Clear($plain, 0, $plain.Length)
    }
    $magic = [System.Text.Encoding]::ASCII.GetBytes('TGDBENC1')
    $payload = New-Object byte[] ($magic.Length + $nonce.Length + $tag.Length + $cipher.Length)
    $offset = 0
    [System.Buffer]::BlockCopy($magic, 0, $payload, $offset, $magic.Length)
    $offset += $magic.Length
    [System.Buffer]::BlockCopy($nonce, 0, $payload, $offset, $nonce.Length)
    $offset += $nonce.Length
    [System.Buffer]::BlockCopy($tag, 0, $payload, $offset, $tag.Length)
    $offset += $tag.Length
    [System.Buffer]::BlockCopy($cipher, 0, $payload, $offset, $cipher.Length)
    [System.IO.File]::WriteAllBytes($Destination, $payload)
}

function Expand-BackupFile {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Source,
        [Parameter(Mandatory)][string]$Destination,
        [Parameter(Mandatory)][string]$KeyPath
    )
    $key = Get-BackupEncryptionKey $KeyPath
    $payload = [System.IO.File]::ReadAllBytes($Source)
    $magicLength = 8
    if ($payload.Length -lt $magicLength + 12 + 16) { throw 'backup crypto: encrypted payload is truncated' }
    $magic = [System.Text.Encoding]::ASCII.GetString($payload, 0, $magicLength)
    if ($magic -ne 'TGDBENC1') { throw 'backup crypto: payload has no encryption header' }
    $nonce = $payload[$magicLength..($magicLength + 11)]
    $tagStart = $magicLength + 12
    $tag = $payload[$tagStart..($tagStart + 15)]
    $cipherStart = $tagStart + 16
    $cipher = $payload[$cipherStart..($payload.Length - 1)]
    $plain = New-Object byte[] $cipher.Length
    $aes = [System.Security.Cryptography.AesGcm]::new($key)
    try {
        $aes.Decrypt($nonce, $cipher, $tag, $plain)
    }
    finally {
        $aes.Dispose()
        [System.Array]::Clear($cipher, 0, $cipher.Length)
    }
    [System.IO.File]::WriteAllBytes($Destination, $plain)
    [System.Array]::Clear($plain, 0, $plain.Length)
}
