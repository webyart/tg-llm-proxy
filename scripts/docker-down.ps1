#Requires -Version 7
<#

«docker compose restart сохраняет историю чата и не порождает повторных ответов» only works while the

Usage:
  . .\scripts\Setup.ps1
  .\scripts\docker-down.ps1 -Volumes -ConfirmVolumes        # also removes the named volume — data gone
  .\scripts\docker-down.ps1 -Rmi                            # and removes the images compose built

Exit: 0 — compose stopped the services; 1 — docker missing or compose failed.
#>

[CmdletBinding()]
param(
    [switch]$Volumes,
    [switch]$ConfirmVolumes,
    [switch]$Rmi
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not (Get-Command -Name docker -ErrorAction SilentlyContinue)) {
    Write-Output 'docker-down: docker is not on PATH — start Docker Desktop '
    exit 1
}

Push-Location $repoRoot
try {
    $composeArgs = @('compose', 'down')
    if ($Rmi) { $composeArgs += '--rmi', 'local' }
    if ($Volumes) {
        if (-not $ConfirmVolumes) {
            Write-Output 'docker-down: -Volumes without -ConfirmVolumes does nothing — bot-data of holds bot.db with the whole chat history of. Add -ConfirmVolumes when the data is really meant to go.'
            exit 1
        }
        $composeArgs += '--volumes'
        Write-Output 'docker-down: removing the named volume bot-data — the chat history of is deleted '
    }
    $out = (docker @composeArgs 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0) {
        Write-Output "docker-down: docker $($composeArgs -join ' ') exit $LASTEXITCODE — the last lines:"
        Write-Output (($out -split "`r?`n" | Select-Object -Last 20) -join "`n")
        exit 1
    }
    ($out -split "`r?`n" | Where-Object { $_ -and $_ -notmatch '^#' } | Select-Object -Last 8)
    docker compose ps
    Write-Output 'docker-down: OK  — restart the stack with scripts/docker-up.ps1, history survives while bot-data exists'
    exit 0
}
finally {
    Pop-Location
}
