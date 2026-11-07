#Requires -Version 7
<#
test-gateway.ps1 — isolated Compose and HTTP routing acceptance; no production services or .env.
#>

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$caddyFile = Join-Path $repoRoot 'deploy\Caddyfile'
$image = 'caddy:2-alpine@sha256:78bd35025c7ba85961dd149ae670315129954e77abe2383ac47c89106b35ade9'
$suffix = [guid]::NewGuid().ToString('N').Substring(0, 12)
$network = "tg-llm-proxy-gateway-test-$suffix"
$botName = "$network-bot"
$dashboardName = "$network-dashboard"
$gatewayName = "$network-gateway"
$work = Join-Path (Join-Path $repoRoot 'zzscratch') "gateway-$suffix"
$curl = if ($IsWindows) { 'curl.exe' } else { 'curl' }
$failure = $null

function Assert-Equal {
    param($Actual, $Expected, [string]$Message)
    if ([string]$Actual -cne [string]$Expected) {
        throw "$Message (expected '$Expected', got '$Actual')"
    }
}

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Invoke-Docker {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$DockerArgs)
    $output = & docker @DockerArgs 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "docker $($DockerArgs -join ' ') failed: $($output | Out-String)"
    }
    return $output
}

function Get-Route {
    param([string]$Base, [string]$Path, [string]$Method = 'GET')
    $output = & $curl --silent --show-error --fail --max-time 10 --request $Method "$Base$Path" 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "$Method $Path failed: $($output | Out-String)"
    }
    return ($output | Out-String).Trim()
}

try {
    Push-Location $repoRoot
    $composeJson = Invoke-Docker compose --env-file .env.example -f docker-compose.yml -f compose.websearch.yaml -f compose.tts.yaml -f compose.production.yaml config --format json
    $config = ($composeJson | Out-String | ConvertFrom-Json -Depth 100 -AsHashtable)
    $gateway = $config.services.gateway
    Assert-Equal $gateway.image $image 'gateway image is not pinned to the expected digest'
    Assert-Equal $gateway.user '1000:1000' 'gateway must be non-root'
    Assert-True ($gateway.read_only -eq $true) 'gateway root filesystem must be read-only'
    Assert-True ($gateway.cap_drop -contains 'ALL') 'gateway must drop all capabilities'
    Assert-True ($gateway.security_opt -contains 'no-new-privileges:true') 'gateway must set no-new-privileges'
    Assert-Equal $gateway.depends_on.bot.condition 'service_healthy' 'gateway must wait for a healthy bot'
    Assert-True ($gateway.extra_hosts -contains 'host.docker.internal=host-gateway') 'gateway must resolve the host dashboard'
    Assert-Equal $gateway.ports.Count 1 'gateway must publish exactly one port'
    Assert-Equal $gateway.ports[0].host_ip '127.0.0.1' 'gateway must be loopback-only'
    Assert-Equal $gateway.ports[0].published '8085' 'gateway host port must be 8085'
    Assert-Equal $gateway.ports[0].target 8085 'gateway container port must be 8085'
    Assert-True ($gateway.tmpfs -contains '/tmp:mode=1777') 'gateway /tmp must be tmpfs'
    Assert-True ($gateway.tmpfs -contains '/config:uid=1000,gid=1000,mode=0700') 'gateway /config must be writable tmpfs'
    Assert-True ($gateway.tmpfs -contains '/data:uid=1000,gid=1000,mode=0700') 'gateway /data must be writable tmpfs'
    Assert-Equal $gateway.logging.options.'max-size' '10m' 'gateway log size limit changed'
    Assert-Equal $gateway.logging.options.'max-file' '5' 'gateway log file limit changed'
    Assert-Equal $config.services.bot.environment.MINIAPP_PUBLIC_URL 'https://desktop-hvobbn7.tail66517e.ts.net/miniapp' 'production Mini App URL changed'
    $miniAppPort = @($config.services.bot.ports | Where-Object { $_.target -eq 8084 })
    Assert-Equal $miniAppPort.Count 1 'direct Mini App port must remain published'
    Assert-Equal $miniAppPort[0].host_ip '127.0.0.1' 'direct Mini App port must remain loopback-only'

    Invoke-Docker run --rm --volume "${caddyFile}:/etc/caddy/Caddyfile:ro" --entrypoint caddy $image validate --config /etc/caddy/Caddyfile --adapter caddyfile | Out-Null

    [void](New-Item -ItemType Directory -Path $work -Force)
    $botConfig = Join-Path $work 'bot.Caddyfile'
    $dashboardConfig = Join-Path $work 'dashboard.Caddyfile'
    $stub = @'
:PORT {
	respond "{http.request.uri.path}|{http.request.header.X-Forwarded-Prefix}" 200
}
'@
    [System.IO.File]::WriteAllText($botConfig, $stub.Replace('PORT', '8084'))
    [System.IO.File]::WriteAllText($dashboardConfig, $stub.Replace('PORT', '8787'))

    Invoke-Docker network create $network | Out-Null
    Invoke-Docker run --detach --name $botName --network $network --network-alias bot --publish '127.0.0.1:0:8084' --volume "${botConfig}:/etc/caddy/Caddyfile:ro" $image | Out-Null
    Invoke-Docker run --detach --name $dashboardName --network $network --network-alias host.docker.internal --volume "${dashboardConfig}:/etc/caddy/Caddyfile:ro" $image | Out-Null
    Invoke-Docker run --detach --name $gatewayName --network $network --user '1000:1000' --read-only --cap-drop ALL --security-opt no-new-privileges:true --tmpfs '/tmp:mode=1777' --tmpfs '/config:uid=1000,gid=1000,mode=0700' --tmpfs '/data:uid=1000,gid=1000,mode=0700' --publish '127.0.0.1:0:8085' --volume "${caddyFile}:/etc/caddy/Caddyfile:ro" $image | Out-Null

    $gatewayBinding = [string]@(Invoke-Docker port $gatewayName '8085/tcp')[0]
    $botBinding = [string]@(Invoke-Docker port $botName '8084/tcp')[0]
    $gatewayBase = "http://127.0.0.1:$(($gatewayBinding.Trim() -split ':')[-1])"
    $botBase = "http://127.0.0.1:$(($botBinding.Trim() -split ':')[-1])"

    $ready = $false
    for ($attempt = 0; $attempt -lt 50; $attempt++) {
        & $curl --silent --fail --max-time 1 "$gatewayBase/" 2>$null | Out-Null
        if ($LASTEXITCODE -eq 0) {
            $ready = $true
            break
        }
        Start-Sleep -Milliseconds 200
    }
    Assert-True $ready "gateway did not become ready: $(& docker logs $gatewayName 2>&1 | Out-String)"

    Assert-Equal (Get-Route $gatewayBase '/') '/|' 'dashboard root did not reach the dashboard unchanged'
    Assert-Equal (Get-Route $gatewayBase '/dashboard/settings') '/dashboard/settings|' 'dashboard fallback path changed'
    Assert-Equal (Get-Route $gatewayBase '/miniapp/') '/|/miniapp' 'Mini App root prefix handling failed'
    Assert-Equal (Get-Route $gatewayBase '/miniapp/static/app.js') '/static/app.js|/miniapp' 'Mini App static routing failed'
    Assert-Equal (Get-Route $gatewayBase '/miniapp/api/session' 'POST') '/api/session|/miniapp' 'Mini App session routing failed'
    Assert-Equal (Get-Route $gatewayBase '/miniapp/api/history') '/api/history|/miniapp' 'Mini App API routing failed'
    Assert-Equal (Get-Route $botBase '/') '/|' 'direct Mini App root must not require a prefix'
    Assert-Equal (Get-Route $botBase '/api/session' 'POST') '/api/session|' 'direct Mini App API must not require a prefix'

    $redirect = & $curl --silent --show-error --include --max-time 10 "$gatewayBase/miniapp" 2>&1
    Assert-True ($LASTEXITCODE -eq 0) 'bare /miniapp request failed'
    Assert-True (($redirect | Out-String) -match '(?m)^HTTP/\S+ 308') 'bare /miniapp must redirect with 308'
    Assert-True (($redirect | Out-String) -match '(?im)^Location: /miniapp/\s*$') 'bare /miniapp must redirect to /miniapp/'

    Write-Output 'test-gateway: GREEN — Compose hardening and dashboard/Mini App/direct HTTP routes verified'
}
catch {
    $failure = $_
    Write-Output "test-gateway: FAIL — $($_.Exception.Message)"
}
finally {
    Pop-Location
    foreach ($name in @($gatewayName, $botName, $dashboardName)) {
        & docker rm --force $name 2>&1 | Out-Null
    }
    & docker network rm $network 2>&1 | Out-Null
    if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force }
}
if ($failure) { exit 1 }
exit 0
