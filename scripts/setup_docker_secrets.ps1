#Requires -Version 7
<#
.SYNOPSIS
Create the local Docker secret files used by docker-compose.yml from .env.

.DESCRIPTION
docker-compose.yml mounts two secrets into the bot and passes only their paths
(TELEGRAM_BOT_TOKEN_FILE / STRATA_API_KEY_FILE). The values themselves must live in local files:

    secrets/telegram_bot_token  <- TELEGRAM_BOT_TOKEN from .env
    secrets/strata_api_key      <- STRATA_API_KEY from .env

The script is idempotent: a non-empty existing secret file is kept, never overwritten. New files are
written without a BOM and receive restrictive permissions (Windows ACL: current user + SYSTEM +
Administrators; elsewhere chmod 600). Values are never printed.

The secrets/ directory is gitignored and dockerignored. Rotate a secret by replacing its file (or
deleting it and running this script again), then recreating the bot: the loader reads the file once at
startup and there is no hot reload.

.EXAMPLE
    .\scripts\setup_docker_secrets.ps1
#>

[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$secretsDir = Join-Path $repoRoot 'secrets'
$envPath = Join-Path $repoRoot '.env'

# file name -> .env row
$rows = [ordered]@{
    'telegram_bot_token' = 'TELEGRAM_BOT_TOKEN'
    'strata_api_key'     = 'STRATA_API_KEY'
}

function Read-EnvValue {
    param([string]$Name)

    if (-not (Test-Path -LiteralPath $envPath)) { return '' }
    $value = ''
    foreach ($line in Get-Content -LiteralPath $envPath) {
        if ($line -match "^\s*$([regex]::Escape($Name))\s*=\s*(.*?)\s*$") {
            $value = $Matches[1]
        }
    }
    # A quoted .env value is unquoted by compose; mirror that so the file carries the bare secret.
    if ($value.Length -ge 2) {
        $first = $value.Substring(0, 1)
        $last = $value.Substring($value.Length - 1, 1)
        if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
            $value = $value.Substring(1, $value.Length - 2)
        }
    }
    return $value.Trim()
}

function Set-RestrictedPermissions {
    param([string]$Path)

    if ($IsWindows) {
        $owner = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        # Remove inherited ACEs; keep only the owner, SYSTEM and Administrators. SIDs are used instead
        # of localized group names so the script works on any Windows display language.
        & icacls $Path /inheritance:r '/grant:r' "*${owner}:(F)" '*S-1-5-18:(F)' '*S-1-5-32-544:(F)' | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "icacls could not restrict $Path" }
    }
    else {
        & chmod 600 $Path
        if ($LASTEXITCODE -ne 0) { throw "chmod 600 failed for $Path" }
    }
}

try {
    if (-not (Test-Path -LiteralPath $secretsDir)) {
        New-Item -ItemType Directory -Path $secretsDir | Out-Null
    }
    $gitkeep = Join-Path $secretsDir '.gitkeep'
    if (-not (Test-Path -LiteralPath $gitkeep)) {
        New-Item -ItemType File -Path $gitkeep | Out-Null
    }

    foreach ($file in $rows.Keys) {
        $row = $rows[$file]
        $target = Join-Path $secretsDir $file
        $exists = (Test-Path -LiteralPath $target) -and ((Get-Item -LiteralPath $target).Length -gt 0)

        if ($exists) {
            Write-Output "setup_docker_secrets: keeping existing secrets/$file"
        }
        else {
            $value = Read-EnvValue -Name $row
            if ([string]::IsNullOrWhiteSpace($value)) {
                throw "ENV $row is empty or absent in .env: fill it in (or place the value in secrets/$file) before creating Docker secrets"
            }
            [System.IO.File]::WriteAllText($target, $value, [System.Text.UTF8Encoding]::new($false))
            Write-Output "setup_docker_secrets: created secrets/$file from .env ($row)"
        }
        Set-RestrictedPermissions -Path $target
    }

    Write-Output 'setup_docker_secrets: permissions restricted (owner-only; on Windows also SYSTEM and Administrators)'
    Write-Output 'setup_docker_secrets: secrets/ is local-only, gitignored and dockerignored — never commit or copy these files'
    Write-Output 'setup_docker_secrets: rotate by replacing a file, then `docker compose up -d --force-recreate bot`'
}
catch {
    Write-Error "setup_docker_secrets: $($_.Exception.Message)"
    exit 1
}
