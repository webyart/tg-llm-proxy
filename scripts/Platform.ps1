#Requires -Version 7

function Get-PortOwnerIds([int]$Port) {
    if ($IsWindows) {
        return @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue |
            Select-Object -ExpandProperty OwningProcess -Unique | Sort-Object)
    }
    $client = [System.Net.Sockets.TcpClient]::new()
    try {
        $pending = $client.ConnectAsync('127.0.0.1', $Port)
        if (-not $pending.Wait(250)) { return @() }
        if ($client.Connected) { return @(0) }
    }
    catch {
        return @()
    }
    finally {
        $client.Dispose()
    }
    return @()
}
