#Requires -Version 7
<#
run.ps1 — §9 row «scripts/run.ps1»: «загрузка .env, сборка, запуск локально».

The .env of §7 is loaded into the PROCESS environment only: no value is ever printed (§12 security — the
token of BotFather stays out of the console and out of the transcript, only the count of rows is echoed).
The build goes through scripts/build.ps1, so `CGO_ENABLED=0` and the §7 refusal check are the same as in
CI of §9. Ctrl-C stops the process through signal.NotifyContext of main.go (§10 День 1 DoD).

Usage:
  . .\scripts\Setup.ps1
  .\scripts\run.ps1                     # build (if needed) + run in this console, Ctrl-C stops it
  .\scripts\run.ps1 -Background         # Start-Process, logs to data\run-<pid>.log (*.log is gitignored)
  .\scripts\run.ps1 -SkipBuild          # reuse bin\bot.exe as it is
  .\scripts\run.ps1 -EnvFile .env.local # another §7 table than .env
  .\scripts\run.ps1 -UseSystemProxy     # export HTTPS_PROXY from the Windows proxy settings first

-UseSystemProxy exists because of what §2 measured on this machine: PowerShell reaches api.telegram.org
through the WinINET proxy of the registry, while the Go process does not see that proxy at all —
`http.Transport` (the nil-Transport default of http.Client) reads only HTTPS_PROXY/HTTP_PROXY of the
PROCESS ENV. Without it `run.ps1` stops at getMe (§10 День 4), with it the same binary polls Telegram.
With the proxy comes NO_PROXY: the loopback and the host of STRATA_BASE_URL of §7 are excluded, because the
model normally answers on this very machine — sending it through the proxy would make the bot need the
internet to talk to its own loopback, and the failure would arrive as events(kind='model_error').
§7 has no row for a proxy and gets none: the variable belongs to the process, not to the configuration.

Exit: the exit code of the bot — 0 after a clean stop by signal, 1 on a configuration or start failure
(§7, §10 День 4: a wrong token stops the start).
#>

[CmdletBinding()]
param(
    [switch]$Background,
    [switch]$SkipBuild,
    [switch]$UseSystemProxy,
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
        Write-Output "run: $EnvFile was not found or empty — the §7 rows are taken from the process ENV as it is (§7: a missing row stops the start with exit 1)"
    }
    else {
        Write-Output "run: $rows ENV row(s) loaded from $(Split-Path -Leaf $EnvFile) (§7 table; values are not printed, §12 security)"
    }
    if (-not $env:DB_PATH) {
        Write-Output 'run: DB_PATH is not in the §7 table of this process — config.LoadFrom takes the local default of §16 change 9 (data/bot.db)'
    }

    if ($UseSystemProxy -and -not $env:HTTPS_PROXY) {
        # The proxy the Windows shell uses is a registry setting (WinINET), and a Go process cannot see it:
        # http.Transport reads HTTPS_PROXY of the ENV only. Copying the first address of ProxyServer into the
        # ENV of this child process is what turns «getMe timed out» into a bot that polls (§10 День 4).
        $inet = $null
        try {
            $inet = Get-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' -ErrorAction SilentlyContinue
        }
        catch {
            $inet = $null
        }
        if ($inet -and $inet.ProxyEnable -eq 1 -and $inet.ProxyServer) {
            $server = (($inet.ProxyServer -split '[;,]') | Select-Object -First 1).Trim()
            if ($server -and $server -notmatch '^[A-Za-z][A-Za-z0-9+.-]*://') { $server = 'http://' + $server }
            $env:HTTPS_PROXY = $server
            Write-Output "run: HTTPS_PROXY=$server copied from the system proxy settings (§2: the Go process has no other way to learn about it; the token is not part of this value)"
        }
        else {
            Write-Output 'run: -UseSystemProxy was asked, but no proxy is enabled in the system settings — the bot keeps the direct route (§10 День 4: getMe needs an answer from api.telegram.org)'
        }
    }

    # The bypass belongs to ANY proxy of the process, not only to the one copied from the registry above: the
    # variable may be exported by the shell or inherited by the service manager, and ProxyFromEnvironment then
    # applies it to EVERY request — including the model of §7, which usually lives on the same machine
    # (http://127.0.0.1:8080/v1 here, i.e. the very port that collided with HEALTH_PORT on 2026-10-31). A
    # request to the bot's own loopback that goes through a proxy needs the proxy and the internet to reach a
    # program next door, and the failure arrives as events(kind='model_error') with a dial error inside
    # (§12 надёжность). §7 has no row for it, exactly as for HTTPS_PROXY: it belongs to the process.
    if ($env:HTTPS_PROXY -or $env:HTTP_PROXY) {
        $bypass = @('127.0.0.1', 'localhost', '::1')
        if ($env:STRATA_BASE_URL) {
            try {
                $strataHost = ([System.Uri]$env:STRATA_BASE_URL).Host
                if ($strataHost -and $bypass -notcontains $strataHost) { $bypass += $strataHost }
            }
            catch {
                Write-Output 'run: STRATA_BASE_URL is not a URL the script can read a host from — only the loopback stays out of the proxy'
            }
        }
        if (-not $env:NO_PROXY) {
            $env:NO_PROXY = ($bypass -join ',')
            Write-Output "run: NO_PROXY=$($env:NO_PROXY) — the model of §7 is reached directly, the proxy is for api.telegram.org (§2)"
        }
        else {
            Write-Output "run: NO_PROXY is already set in the ENV ($($env:NO_PROXY)), the script leaves it alone (§7 table of the process)"
        }
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

    Write-Output 'run: the bot runs in this console — Ctrl-C stops it through signal.NotifyContext (§10 День 1 DoD)'
    & $exe
    Write-Output "run: the bot exited with $LASTEXITCODE (§7/§10 День 4: 1 = configuration, token or start failure)"
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
