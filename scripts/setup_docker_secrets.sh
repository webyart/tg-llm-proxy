#!/usr/bin/env bash
# Create the local Docker secret files used by docker-compose.yml from .env.
#
#   secrets/telegram_bot_token  <- TELEGRAM_BOT_TOKEN
#   secrets/strata_api_key      <- STRATA_API_KEY
#
# Idempotent: a non-empty existing secret file is kept. New files are written with mode 600 and no
# trailing newline. Values are never printed. secrets/ is gitignored and dockerignored; rotate by
# replacing a file, then `docker compose up -d --force-recreate bot`.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
secrets_dir="$repo_root/secrets"
env_file="$repo_root/.env"

read_env_value() {
    local row="$1" value
    if [[ ! -f "$env_file" ]]; then
        printf ''
        return
    fi
    value="$(grep -m1 -E "^[[:space:]]*${row}[[:space:]]*=" "$env_file" | sed -E 's/^[^=]*=//; s/\r$//' || true)"
    value="$(printf '%s' "$value" | sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//')"
    if [[ ${#value} -ge 2 ]]; then
        local first="${value:0:1}" last="${value: -1}"
        if [[ "$first" == '"' && "$last" == '"' ]] || [[ "$first" == "'" && "$last" == "'" ]]; then
            value="${value:1:${#value}-2}"
        fi
    fi
    printf '%s' "$value"
}

mkdir -p "$secrets_dir"
touch "$secrets_dir/.gitkeep"
umask 077

for spec in "telegram_bot_token:TELEGRAM_BOT_TOKEN" "strata_api_key:STRATA_API_KEY"; do
    file="${spec%%:*}"
    row="${spec#*:}"
    target="$secrets_dir/$file"

    if [[ -s "$target" ]]; then
        echo "setup_docker_secrets: keeping existing secrets/$file"
    else
        value="$(read_env_value "$row")"
        if [[ -z "${value//[[:space:]]/}" ]]; then
            echo "setup_docker_secrets: ENV $row is empty or absent in .env: fill it in (or place the value in secrets/$file) before creating Docker secrets" >&2
            exit 1
        fi
        printf '%s' "$value" > "$target"
        echo "setup_docker_secrets: created secrets/$file from .env ($row)"
    fi
    chmod 600 "$target"
done

echo "setup_docker_secrets: permissions restricted (mode 600)"
echo "setup_docker_secrets: secrets/ is local-only, gitignored and dockerignored — never commit or copy these files"
echo "setup_docker_secrets: rotate by replacing a file, then 'docker compose up -d --force-recreate bot'"
