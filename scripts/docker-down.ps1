#Requires -Version 7
<#
docker-down.ps1 — §9 row «scripts/docker-down.ps1»: «docker compose down» (§8.1 «docker down»).

The history of §5 lives in the NAMED volume bot-data, and `compose down` keeps it: the §8.1 check
«docker compose restart сохраняет историю чата и не порождает повторных ответов» only works while the
volume survives. Deleting it is a separate, explicit decision — `bot.db` with every chat message of §5 is
gone with it, so -Volumes asks for -ConfirmVolumes as well (§12 reliability: one typo must not erase data).

Usage:
  . .\scripts\Setup.ps1
  .\scripts\docker-down.ps1                                 # containers stopped, bot-data kept (§8.1)
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
    Write-Output 'docker-down: docker is not on PATH — start Docker Desktop (§2)'
    exit 1
}

Push-Location $repoRoot
try {
    $composeArgs = @('compose', 'down')
    if ($Rmi) { $composeArgs += '--rmi', 'local' }
    if ($Volumes) {
        if (-not $ConfirmVolumes) {
            Write-Output 'docker-down: -Volumes without -ConfirmVolumes does nothing — bot-data of §8.1 holds bot.db with the whole chat history of §5. Add -ConfirmVolumes when the data is really meant to go.'
            exit 1
        }
        $composeArgs += '--volumes'
        Write-Output 'docker-down: removing the named volume bot-data — the chat history of §5 is deleted (§8.1)'
    }
    $out = (docker @composeArgs 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0) {
        Write-Output "docker-down: docker $($composeArgs -join ' ') exit $LASTEXITCODE — the last lines:"
        Write-Output (($out -split "`r?`n" | Select-Object -Last 20) -join "`n")
        exit 1
    }
    ($out -split "`r?`n" | Where-Object { $_ -and $_ -notmatch '^#' } | Select-Object -Last 8)
    docker compose ps
    Write-Output 'docker-down: OK (§8.1) — restart the stack with scripts/docker-up.ps1, history survives while bot-data exists'
    exit 0
}
finally {
    Pop-Location
}
