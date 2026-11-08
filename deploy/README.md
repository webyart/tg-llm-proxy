# `deploy/` — historical gateway configuration, not part of the current Compose project

**Status: inactive.** Nothing in this directory is started by `docker-compose.yml`. The production endpoint is
served by the standalone Traefik in the separate `edge-gateway` project (`127.0.0.1:8086`); see the
"Gateway" section of the root `README.md`.

| File | What it was |
|---|---|
| `Caddyfile` | the Caddy gateway on `127.0.0.1:8085` before Traefik |
| `traefik.yml` | static configuration of the in-Compose Traefik (`:8080`, health `:8081`, no TLS entrypoint) |
| `dynamic.yml` | its routers: `/miniapp` → bot, `/` → dashboard, catch-all through `host.docker.internal` |

The files are kept on purpose: the stopped container `tg-llm-proxy-gateway-1` was created with bind mounts that
point at these paths, so a rollback can restart that exact container. Deleting or moving them would silently
break a rollback that the README promises.

If the old gateway is ever brought back, note what it does **not** have: TLS entrypoint and certresolver (TLS
lived outside this repository), a Docker provider (it uses only the file provider), and any of the Mini App
hardening of the current gateway. Treat it as a rollback artifact, not as a deployment option.

The acceptance script of that generation, `scripts/test-gateway.ps1`, is historical for the same reason: it
validates a Caddy configuration and publishes temporary containers on `:8084`/`:8085`. The current gateway's
acceptance lives in `edge-gateway/scripts/test-traefik.ps1`.
