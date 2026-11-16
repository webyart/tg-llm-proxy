#!/usr/bin/env bash
# Create the local Docker secret files used by docker-compose.yml from .env.
#
#   secrets/telegram_bot_token  <- TELEGRAM_BOT_TOKEN
#   secrets/strata_api_key      <- STRATA_API_KEY
#   secrets/kimi_api_key        <- MODEL_PROVIDERS api_key of provider "kimi"
#   secrets/deepseek_api_key    <- MODEL_PROVIDERS api_key of provider "deepseek"
#
# Idempotent: a non-empty existing secret file is kept. New files are written with mode 600 and no
# trailing newline. Values are never printed. secrets/ is gitignored and dockerignored; rotate by
# replacing a file, then `docker compose up -d --force-recreate bot`. The provider keys live in separate
# files, so rotating one provider does not touch the Telegram/STRATA secrets or the other provider.
#
# MODEL_PROVIDERS is parsed with python3 (JSON is not safely regex-parseable); a missing python3 is an
# error only when a provider secret file has to be created.
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

# The api_key of one named provider, or an api_key_file already present in MODEL_PROVIDERS. The .env
# catalogue stays the source of truth for local runs; only the key is copied into secrets/.
read_provider_key() {
    local provider="$1"
    if ! command -v python3 >/dev/null 2>&1; then
        printf 'setup_docker_secrets: python3 is required to read MODEL_PROVIDERS when a provider secret is missing\n' >&2
        return 1
    fi
    python3 - "$env_file" "$provider" <<'PY'
import json
import re
import sys

env_path, provider = sys.argv[1], sys.argv[2]
raw = ""
try:
    with open(env_path, encoding="utf-8") as handle:
        for line in handle:
            match = re.match(r"^\s*MODEL_PROVIDERS\s*=\s*(.*?)\s*$", line)
            if match:
                raw = match.group(1)
except FileNotFoundError:
    sys.exit(0)
if not raw:
    sys.exit(0)
if len(raw) >= 2 and raw[0] == raw[-1] and raw[0] in "\"'":
    raw = raw[1:-1]
try:
    providers = json.loads(raw)
except json.JSONDecodeError as error:
    print(f"setup_docker_secrets: MODEL_PROVIDERS is not valid JSON: {error}", file=sys.stderr)
    sys.exit(1)
for entry in providers:
    if entry.get("name") != provider:
        continue
    key = entry.get("api_key") or ""
    if key.strip():
        print(key.strip(), end="")
        sys.exit(0)
    path = entry.get("api_key_file") or ""
    if path:
        try:
            with open(path, encoding="utf-8") as handle:
                print(handle.read().strip(), end="")
        except OSError as error:
            print(f"setup_docker_secrets: api_key_file of provider {provider} cannot be read: {error}", file=sys.stderr)
            sys.exit(1)
    sys.exit(0)
sys.exit(0)
PY
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

for spec in "kimi_api_key:kimi" "deepseek_api_key:deepseek"; do
    file="${spec%%:*}"
    provider="${spec#*:}"
    target="$secrets_dir/$file"

    if [[ -s "$target" ]]; then
        echo "setup_docker_secrets: keeping existing secrets/$file"
    else
        value="$(read_provider_key "$provider")"
        if [[ -z "${value//[[:space:]]/}" ]]; then
            echo "setup_docker_secrets: MODEL_PROVIDERS has no api_key for provider $provider: add it to .env (or place the key in secrets/$file); to run without it, remove that provider from docker-compose.yml's MODEL_PROVIDERS and secrets" >&2
            exit 1
        fi
        printf '%s' "$value" > "$target"
        echo "setup_docker_secrets: created secrets/$file from MODEL_PROVIDERS ($provider, ${#value} bytes)"
    fi
    chmod 600 "$target"
done

echo "setup_docker_secrets: permissions restricted (mode 600)"
echo "setup_docker_secrets: secrets/ is local-only, gitignored and dockerignored — never commit or copy these files"
echo "setup_docker_secrets: rotate by replacing a file, then 'docker compose up -d --force-recreate bot'"
