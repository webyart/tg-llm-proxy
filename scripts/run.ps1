#Requires -Version 7
<#
run.ps1 — §9 row «scripts/run.ps1»: «загрузка .env, сборка, запуск локально».

The .env of §7 is loaded into the PROCESS environment only: no value is ever printed (§12 security — the
token of BotFather stays out of the console and out of the transcript, only the count of rows is echoed).
The build goes through scripts/build.ps1, so `CGO_ENABLED=0` and the §7.1 refusal check are the same as in
CI of §9. Ctrl-C stops the process through signal.NotifyContext of main.go (§10.2 Day 1 DoD).

Usage:
  . .\scripts\Setup.ps1
  .\scripts\run.ps1                     # build (if needed) + run in this console, Ctrl-C stops it
  .\scripts\run.ps1 -Background         # Start-Process, logs to data\run-<pid>.log (*.log is gitignored)
  .\scripts\run.ps1 -SkipBuild          # reuse bin\bot.exe as it is
  .\scripts\run.ps1 -EnvFile .env.local # another §7 table than .env

Exit: the exit code of the bot — 0 after a clean stop by signal, 1 on a configuration or start failure
(§7.1, §10 День 4: a wrong token stops the start).
#>

[CmdletBinding()]
param(
    [switch]$Background,
    [switch]$SkipBuild,
    [string]$EnvFile
)

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not $EnvFile) { $EnvFile = Join-Path $repoRoot '.env' }
else { $EnvFile = (Resolve-Path -LiteralPath $EnvFile).Path }

function LoadEnv([string]$path) {
    if (-not (Test-Path -LiteralPath $path)) { return 0 }
    $loaded = 0
    foreach ($line in Get-Content -LiteralPath $path) {
        if ($line -match '^\s*([A-Z][A-Z0-9_]*)\s*=\s*(.*?)\s*$') {
            $name = $matches[1]
            $value = $matches[2]
            if ($value.Length -ge 2 -and (($value[0] -eq '"' -and $value[-1] -eq '"') -or ($value[0] -eq "'" -and $value[-1] -eq "'"))) {
                $value = $value.Substring(1, $value.Length - 2)   # the quotes of §7 are not part of the value
            }
            Set-Item -Path ("Env:" + $name) -Value $value
            $loaded += 1
        }
    }
    return $loaded
}

Push-Location $repoRoot
try {
    $rows = LoadEnv $EnvFile
    if ($rows -eq 0) {
        Write-Output "run: $EnvFile was not found or empty — the §7 rows are taken from the process ENV as it is (§7.1: a missing row stops the start with exit 1)"
    }
    else {
        Write-Output "run: $rows ENV row(s) loaded from $(Split-Path -Leaf $EnvFile) (§7 table; values are not printed, §12 security)"
    }
    if (-not $env:DB_PATH) {
        Write-Output 'run: DB_PATH is not in the §7 table of this process — config.LoadFrom takes the local default of §16 change 9 (data/bot.db)'
    }

    $exe = Join-Path $repoRoot 'bin\bot.exe'
    if (-not $SkipBuild -or -not (Test-Path -LiteralPath $exe)) {
        & (Join-Path $PSScriptRoot 'build.ps1')
        if ($LASTEXITCODE -ne 0) {
            Write-Output "run: the build failed, nothing was started (§9 scripts/build.ps1)"
            exit 1
        }
    }

    if ($Background) {
        New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'data') | Out-Null
        $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
        $outLog = Join-Path $repoRoot "data\run-$stamp.out.log"
        $errLog = Join-Path $repoRoot "data\run-$stamp.err.log"
        # §12: slog writes JSON to stdout, so the log files of a detached run are the only trace; *.log is
        # gitignored (§10 День 1) and the token never enters them (§12 security, Sanitize of §6).
        $proc = Start-Process -FilePath $exe -WorkingDirectory $repoRoot -WindowStyle Hidden -PassThru `
            -RedirectStandardOutput $outLog -RedirectStandardError $errLog
        Write-Output "run: PID $($proc.Id), logs: $outLog / $errLog — stop it with Stop-Process -Id $($proc.Id) (or taskkill /PID $($proc.Id) for the kill case of §10 День 4)"
        exit 0
    }

    Write-Output 'run: the bot runs in this console — Ctrl-C stops it through signal.NotifyContext (§10.2 Day 1 DoD)'
    & $exe
    Write-Output "run: the bot exited with $LASTEXITCODE (§7.1/§10 День 4: 1 = configuration, token or start failure)"
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
