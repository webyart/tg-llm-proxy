# tg-llm-proxy — Telegram-бота → STRATA llamaServer (prototype, Go 1.27)

Prototype per the plan (`PLAN.md` — local planning doc, not committed, see §10).

Status: **Day 0 done** (§10 Day 0: environment checks), **Day 1 in progress** —
git repo + ignore-файлов + scripts created; Go-каркас (go.mod, `cmd/bot`, `internal/config`)
next.

## Git-репозитория (§10 Day 1)

- Repo root: `C:\Tools\tg-llm-proxy` — standalone repo (`C:\Tools` itself is NOT a repo,
  no parent `.git`, so this folder is the working repo).
- Default branch: `main` (git init -b main).
- Repo-local config: `core.autocrlf=false`, `core.eol=lf` (+ `.gitattributes` → repo always LF
  for Docker/Linux build consistency). Windows: `core.ignorecase=true` — be careful with
  case-sensitive renames (§5 practice: use explicit `git mv`).
- Not committed (§10 .gitignore): `.env` (token!), `data/`, `*.db*`, `bin/`, `.cache/`, `*.log`,
  `PLAN.md`. Committed: `.env.example` (fake token), `.gitignore`, `.dockerignore`,
  `.gitattributes`, `README.md`, `scripts/`.
- Git for Windows installed via winget (`Git.Git`, 2.56) → `C:\Program Files\Git\cmd\git.exe`.
  Already-running PowerShell sessions don't see it in PATH (§11 Windows note):
  run `. .\scripts\Setup.ps1` (dot-source!) or open a new terminal / restart VS Code.
  Also the user PATH (`HKCU\Environment!Path`) was extended with `C:\Program Files\Git\cmd`
  (setx, len 529 → 554) → new processes/VS Code terminals will resolve `git` without Setup.ps1.

## Environment (verified 2026-10-30, §2 env-check green)

| component | value | note |
|---|---|---|
| Go | `go1.27.3 windows/amd64` | pebbled std (plan: Go 1.27) |
| git | `2.56.0.windows.3` | winget `Git.Git` |
| pwsh | `7.6.9` | scripts/ requires ≥7 |
| Docker compose | `v5.6.1` | demon running; ≥ plan "compose v4" |
| C-compiler | none | OK — pure-Go `modernc.org/sqlite` (§2) |
| sqlite3 CLI | absent | expected (§5) → use `scripts/db_inspect.ps1` (Go-tool, Day 3) |
| go proxy | `https://proxy.golang.org,direct` | §3; caches `.cache/gocache`, `.cache/gopath` |
| STRATA network | from host — `host.docker.internal` unreachable | OK in container (Day 4 §11) |

## Layout (§4)

```
tg-llm-proxy/
├── PLAN.md            # planning doc (local, gitignored)
├── cmd/bot/main.go    # entry point — Day 1+ (pending)
├── internal/          # bot/db/context/strata/config — Day 2+ (pending)
├── docker/            # Dockerfile + compose — Day 2+ (pending)
├── scripts/           # env-check.ps1, Setup.ps1 (created) + build/run/... (pending)
├── bin/               # runtime binaries (gitignored)
├── data/              # bot.db (gitignored)
└── .env.example       # template — committed
```

## Quick start (Windows/pwsh)

```powershell
. .\scripts\Setup.ps1          # PATH: git + GOPROXY/GOCACHE/GOPATH (§3, §11)
.\scripts\env-check.ps1        # §2: green before work (Go/git/pwsh/Docker/network)
git status                     # repo state
# later (Day 1+): go build ./cmd/bot -o bin/bot.exe && .\scripts\run.ps1 --foreground
```

## ENV (§7) — see `.env.example` (full table in PLAN.md §7)

Required: `TELEGRAM_BOT_TOKEN`, `STRATA_MODEL` (no default).
Docker/Windows default: `STRATA_BASE_URL=http://host.docker.internal:8000`.
`ALLOWED_USER_IDS` empty → free-for-all (startup warning + log line).
`DB_PATH=./data/bot.db` (bind mount `./data:/app/data` — §11 Day 3).

## Next steps (§10)

- Day 1: `go.mod`, `cmd/bot/main.go` skeleton (config → JSON logs → graceful shutdown),
  config validation unit tests
- Day 2: `go-telegram/bot` v1 + `go mod tidy` (real names!) + Docker image
- Day 3: SQLite (WAL, `bot_state`) + `scripts/db_inspect.ps1` (pure-Go) + `--dry-run`
- Day 4: main loop + DB-safety invariants + crash-restart smoke test
- Day 5-10: idempotency → context → STRATA calls → answers → rate limit → smoke E2E

## Not implemented yet (honest list)

No Telegram calls, no SQLite, no STRATA requests, no /healthz — only env scaffolding +
git is ready. Prototype uses plan's §4 structure; API names will be verified against
Go 1.27 pebbled std + v1.27.0 bot + v1.61.0 sqlite at build time (§11 research items).
