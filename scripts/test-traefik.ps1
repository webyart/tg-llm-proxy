#Requires -Version 7
<#
test-traefik.ps1 — isolated Traefik Compose and HTTP acceptance; no production services or .env.
#>

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$traefikFile = Join-Path $repoRoot 'deploy/traefik.yml'
$dynamicFile = Join-Path $repoRoot 'deploy/dynamic.yml'
$image = 'traefik:v3.6@sha256:31267173a15b4944e797a76ffd9c419707c8d8b32fe5b610f80cd0cfa05f372d'
$stubImage = 'caddy:2-alpine@sha256:78bd35025c7ba85961dd149ae670315129954e77abe2383ac47c89106b35ade9'
$suffix = [guid]::NewGuid().ToString('N').Substring(0, 12)
$network = "tg-llm-proxy-traefik-test-$suffix"
$botName = "$network-bot"
$upstreamsName = "$network-upstreams"
$gatewayName = "$network-gateway"
$scratchRoot = Join-Path $repoRoot 'zzscratch'
$scratchExisted = Test-Path -LiteralPath $scratchRoot
$work = Join-Path $scratchRoot "traefik-$suffix"
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
    param([string]$Base, [string]$Path, [string]$Method = 'GET', [hashtable]$Headers = @{})
    $curlArgs = @('--silent', '--show-error', '--fail', '--max-time', '10', '--request', $Method)
    foreach ($name in $Headers.Keys) {
        $curlArgs += @('--header', "${name}: $($Headers[$name])")
    }
    $curlArgs += "$Base$Path"
    $output = & $curl @curlArgs 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "$Method $Path failed: $($output | Out-String)"
    }
    return ($output | Out-String).Trim()
}

function Get-Response {
    param([string]$Base, [string]$Path)
    $output = & $curl --silent --show-error --include --max-time 10 "$Base$Path" 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "GET $Path failed: $($output | Out-String)"
    }
    return ($output | Out-String)
}

try {
    Push-Location $repoRoot
    $composeJson = Invoke-Docker compose --env-file .env.example -f docker-compose.yml -f compose.websearch.yaml -f compose.tts.yaml -f compose.production.yaml config --format json
    $config = ($composeJson | Out-String | ConvertFrom-Json -Depth 100 -AsHashtable)
    $gateway = $config.services.gateway
    Assert-Equal $gateway.image $image 'gateway image is not pinned to the expected Traefik digest'
    Assert-Equal $gateway.user '65532:65532' 'gateway must be non-root'
    Assert-True ($gateway.read_only -eq $true) 'gateway root filesystem must be read-only'
    Assert-True ($gateway.cap_drop -contains 'ALL') 'gateway must drop all capabilities'
    Assert-True ($gateway.security_opt -contains 'no-new-privileges:true') 'gateway must set no-new-privileges'
    Assert-Equal $gateway.depends_on.bot.condition 'service_healthy' 'gateway must wait for a healthy bot'
    Assert-True ($gateway.extra_hosts -contains 'host.docker.internal=host-gateway') 'gateway must resolve host upstreams'
    Assert-Equal $gateway.ports.Count 1 'gateway must publish exactly one port'
    Assert-Equal $gateway.ports[0].host_ip '127.0.0.1' 'gateway must be loopback-only'
    Assert-Equal $gateway.ports[0].published '8086' 'gateway host port must be 8086'
    Assert-Equal $gateway.ports[0].target 8080 'gateway container port must be 8080'
    Assert-True ($gateway.tmpfs -contains '/tmp:mode=1777') 'gateway /tmp must be tmpfs'
    Assert-True ([string]$gateway.mem_limit -in @('128MiB', '134217728')) 'gateway memory limit must remain 128 MiB'
    Assert-Equal $gateway.cpus '0.5' 'gateway CPU limit changed'
    Assert-Equal $gateway.pids_limit '64' 'gateway PID limit changed'
    Assert-Equal $gateway.restart 'unless-stopped' 'gateway restart policy changed'
    Assert-Equal $gateway.logging.options.'max-size' '10m' 'gateway log size limit changed'
    Assert-Equal $gateway.logging.options.'max-file' '5' 'gateway log file limit changed'
    Assert-Equal ($gateway.healthcheck.test -join ' ') 'CMD traefik healthcheck --configFile=/etc/traefik/traefik.yml' 'gateway monitor healthcheck changed'
    Assert-Equal @($gateway.volumes).Count 2 'gateway must mount only the two static configuration files'
    Assert-True (@($gateway.volumes | Where-Object { $_.target -eq '/etc/traefik/traefik.yml' -and $_.read_only -eq $true }).Count -eq 1) 'static Traefik config must be read-only'
    Assert-True (@($gateway.volumes | Where-Object { $_.target -eq '/etc/traefik/dynamic.yml' -and $_.read_only -eq $true }).Count -eq 1) 'dynamic Traefik config must be read-only'
    Assert-True (@($gateway.volumes | Where-Object { $_.target -eq '/var/run/docker.sock' }).Count -eq 0) 'Docker socket must not be mounted'
    Assert-Equal $config.services.bot.environment.MINIAPP_PUBLIC_URL 'https://desktop-hvobbn7.tail66517e.ts.net/miniapp' 'production Mini App URL changed'
    $miniAppPort = @($config.services.bot.ports | Where-Object { $_.target -eq 8084 })
    Assert-Equal $miniAppPort.Count 1 'direct Mini App port must remain published'
    Assert-Equal $miniAppPort[0].host_ip '127.0.0.1' 'direct Mini App port must remain loopback-only'

    $staticText = [System.IO.File]::ReadAllText($traefikFile)
    Assert-True ($staticText -match '(?m)^\s*filename: /etc/traefik/dynamic\.yml\s*$') 'static file provider must load dynamic.yml'
    Assert-True ($staticText -notmatch '(?m)^\s*docker:\s*$') 'Docker provider must remain disabled'
    Assert-True ($staticText -match '(?m)^\s*defaultMode: drop\s*$') 'access logs must drop request headers'
    Assert-True ($staticText -match '(?m)^\s*dashboard: false\s*$') 'Traefik dashboard API must remain disabled'

    [void](New-Item -ItemType Directory -Path $work -Force)
    $botConfig = Join-Path $work 'bot.Caddyfile'
    $upstreamsConfig = Join-Path $work 'upstreams.Caddyfile'
    $botStub = @'
:8084 {
	respond "{http.request.uri.path}|{http.request.header.X-Forwarded-Prefix}" 200
}
'@
    $upstreamsStub = @'
:8787 {
	respond "{http.request.uri.path}|{http.request.header.X-Forwarded-Prefix}" 200
}

:8090 {
	handle /healthz {
		respond "direct-healthz" 200
	}
	handle /max/healthz {
		respond "proxied-max-healthz" 200
	}
	handle / {
		respond "not found" 404
	}
	handle {
		respond "{http.request.uri.path}|{http.request.header.X-Forwarded-Prefix}" 200
	}
}
'@
    [System.IO.File]::WriteAllText($botConfig, $botStub)
    [System.IO.File]::WriteAllText($upstreamsConfig, $upstreamsStub)

    Invoke-Docker run --rm --volume "${botConfig}:/etc/caddy/Caddyfile:ro" --entrypoint caddy $stubImage validate --config /etc/caddy/Caddyfile --adapter caddyfile | Out-Null
    Invoke-Docker run --rm --volume "${upstreamsConfig}:/etc/caddy/Caddyfile:ro" --entrypoint caddy $stubImage validate --config /etc/caddy/Caddyfile --adapter caddyfile | Out-Null

    Invoke-Docker network create $network | Out-Null
    Invoke-Docker run --detach --name $botName --network $network --network-alias bot --publish '127.0.0.1:0:8084' --volume "${botConfig}:/etc/caddy/Caddyfile:ro" $stubImage | Out-Null
    Invoke-Docker run --detach --name $upstreamsName --network $network --network-alias host.docker.internal --publish '127.0.0.1:0:8090' --volume "${upstreamsConfig}:/etc/caddy/Caddyfile:ro" $stubImage | Out-Null
    Invoke-Docker run --detach --name $gatewayName --network $network --user '65532:65532' --read-only --cap-drop ALL --security-opt no-new-privileges:true --tmpfs '/tmp:mode=1777' --publish '127.0.0.1:0:8080' --volume "${traefikFile}:/etc/traefik/traefik.yml:ro" --volume "${dynamicFile}:/etc/traefik/dynamic.yml:ro" --health-cmd 'traefik healthcheck --configFile=/etc/traefik/traefik.yml' --health-interval 1s --health-timeout 2s --health-retries 15 --health-start-period 1s $image | Out-Null

    $gatewayBinding = [string]@(Invoke-Docker port $gatewayName '8080/tcp')[0]
    $botBinding = [string]@(Invoke-Docker port $botName '8084/tcp')[0]
    $maxBinding = [string]@(Invoke-Docker port $upstreamsName '8090/tcp')[0]
    $gatewayBase = "http://127.0.0.1:$(($gatewayBinding.Trim() -split ':')[-1])"
    $botBase = "http://127.0.0.1:$(($botBinding.Trim() -split ':')[-1])"
    $maxBase = "http://127.0.0.1:$(($maxBinding.Trim() -split ':')[-1])"

    $ready = $false
    for ($attempt = 0; $attempt -lt 75; $attempt++) {
        & $curl --silent --fail --max-time 1 "$gatewayBase/" 2>$null | Out-Null
        if ($LASTEXITCODE -eq 0) {
            $ready = $true
            break
        }
        Start-Sleep -Milliseconds 200
    }
    Assert-True $ready "Traefik did not load its static configuration: $(& docker logs $gatewayName 2>&1 | Out-String)"

    $healthy = $false
    for ($attempt = 0; $attempt -lt 50; $attempt++) {
        $status = (& docker inspect --format '{{.State.Health.Status}}' $gatewayName 2>$null | Out-String).Trim()
        if ($status -eq 'healthy') {
            $healthy = $true
            break
        }
        Start-Sleep -Milliseconds 200
    }
    Assert-True $healthy "Traefik ping entrypoint did not become healthy: $(& docker logs $gatewayName 2>&1 | Out-String)"
    Invoke-Docker exec $gatewayName traefik healthcheck --configFile=/etc/traefik/traefik.yml | Out-Null

    Assert-Equal (Get-Route $gatewayBase '/') '/|' 'dashboard root did not reach the dashboard unchanged'
    Assert-Equal (Get-Route $gatewayBase '/dashboard/settings') '/dashboard/settings|' 'dashboard fallback path changed'
    Assert-Equal (Get-Route $gatewayBase '/api/rawdata') '/api/rawdata|' 'disabled Traefik API must fall through to the dashboard'
    Assert-Equal (Get-Route $gatewayBase '/maximum') '/maximum|' 'MAX prefix matching must not capture /maximum'
    Assert-Equal (Get-Route $gatewayBase '/miniapp/') '/|/miniapp' 'Mini App root prefix handling failed'
    Assert-Equal (Get-Route $gatewayBase '/miniapp/static/app.js') '/static/app.js|/miniapp' 'Mini App static routing failed'
    Assert-Equal (Get-Route $gatewayBase '/miniapp/api/session' 'POST') '/api/session|/miniapp' 'Mini App session routing failed'
    Assert-Equal (Get-Route $gatewayBase '/miniapp/api/history') '/api/history|/miniapp' 'Mini App API routing failed'
    Assert-Equal (Get-Route $gatewayBase '/miniapp/healthz') '/healthz|/miniapp' 'Mini App health routing failed'
    Assert-Equal (Get-Route $gatewayBase '/max/healthz') 'proxied-max-healthz' 'MAX health path must be proxied without stripping /max'
    Assert-Equal (Get-Route $gatewayBase '/max/api/update' 'POST') '/max/api/update|' 'MAX path must remain unchanged'
    Assert-Equal (Get-Route $botBase '/') '/|' 'direct Mini App root must not require a prefix'
    Assert-Equal (Get-Route $botBase '/healthz') '/healthz|' 'direct Mini App health must remain available'
    Assert-Equal (Get-Route $botBase '/api/session' 'POST') '/api/session|' 'direct Mini App API must not require a prefix'
    Assert-Equal (Get-Route $maxBase '/healthz') 'direct-healthz' 'direct MAX health must remain available'
    Assert-True ((Get-Response $maxBase '/') -match '(?m)^HTTP/\S+ 404') 'direct MAX root fixture must remain 404'

    $redirect = Get-Response $gatewayBase '/miniapp'
    Assert-True ($redirect -match '(?m)^HTTP/\S+ 301') "bare /miniapp must redirect permanently: $redirect"
    Assert-True ($redirect -match '(?im)^Location: /miniapp/\s*$') 'bare /miniapp must redirect to /miniapp/'

    $authorizationSecret = "traefik-authorization-$suffix"
    $cookieSecret = "traefik-cookie-$suffix"
    Assert-Equal (Get-Route $gatewayBase '/miniapp/api/session' 'POST' @{ Authorization = "Bearer $authorizationSecret"; Cookie = "session=$cookieSecret" }) '/api/session|/miniapp' 'Mini App request with sensitive headers failed'
    $logs = (Invoke-Docker logs $gatewayName | Out-String)
    Assert-True ($logs -match 'miniapp@file') 'JSON access logs must contain the Mini App router'
    Assert-True ($logs -notmatch [regex]::Escape($authorizationSecret)) 'access logs leaked an Authorization value'
    Assert-True ($logs -notmatch [regex]::Escape($cookieSecret)) 'access logs leaked a Cookie value'
    Assert-True ($logs -notmatch '(?i)request_(authorization|cookie)') 'access logs must not contain Authorization or Cookie fields'

    Write-Output 'test-traefik: GREEN — static config, hardening, ping, dashboard/Mini App/MAX/direct routes and safe logs verified'
}
catch {
    $failure = $_
    Write-Output "test-traefik: FAIL — $($_.Exception.Message)"
}
finally {
    Pop-Location
    foreach ($name in @($gatewayName, $botName, $upstreamsName)) {
        & docker rm --force $name 2>&1 | Out-Null
    }
    & docker network rm $network 2>&1 | Out-Null
    if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force }
    if (-not $scratchExisted -and (Test-Path -LiteralPath $scratchRoot) -and -not (Get-ChildItem -LiteralPath $scratchRoot -Force)) {
        Remove-Item -LiteralPath $scratchRoot -Force
    }
}
if ($failure) { exit 1 }
exit 0
