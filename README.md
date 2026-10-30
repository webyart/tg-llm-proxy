# tg-llm-proxy — Telegram-бота → STRATA llamaServer (prototype, Go 1.27)

Prototype per the plan (`PLAN.md` — local planning doc, not committed, see §10).

Status: **Day 0 done** (§10 Day 0: environment checks), **Day 1 done** —
git repo + ignore-файлов + scripts created; `go build` passes with
`cmd/bot/main.go` + `internal/config` (the ENV → Config loader of §7, 19 rows,
validation with the list of problems, §10 Day 1 DoD: echoes the configuration back
and stops cleanly on Ctrl-C with exit code 0; an unset `TELEGRAM_BOT_TOKEN` →
clear error + exit code 1).

## Git-репозитория (§10 Day 1)

- Repo root: `C:\Tools\tg-llm-proxy` — standalone repo (`C:\Tools` itself is NOT a repo,
  no parent `.git`, so this folder is the working repo).
- Default branch: `main` (git init -b main).
- Repo-local config: `core.autocrlf=false`, `core.eol=lf` (+ `.gitattributes` → repo always LF
  for Docker/Linux build consistency). Windows: `core.ignorecase=true` — be careful with
  case-sensitive renames (§5 practice: use explicit `git mv`).
- Not committed (§10 .gitignore): `.env` (token!), `data/`, `*.db*`, `bin/`, `.cache/`, `*.log`,
  `PLAN.md`. Committed: `.env.example` (the §7 table with the secrets left EMPTY — no
  fake token any more), `.gitignore`, `.dockerignore`,
  `.gitattributes`, `README.md`, `scripts/`.
- Git for Windows installed via winget (`Git.Git`, 2.56) → `C:\Program Files\Git\cmd\git.exe`.
  Already-running PowerShell sessions don't see it in PATH (§11 Windows note):
  run `. .\scripts\Setup.ps1` (dot-source!) or open a new terminal / restart VS Code.
  Also the user PATH (`HKCU\Environment!Path`) was extended with `C:\Program Files\Git\cmd`
  (setx, len 529 → 554) → new processes/VS Code terminals will resolve `git` without Setup.ps1.

## Repositoriи — репозitoriyi-и caches for work (verified 2026-10-30)

| kind | location | state |
|---|---|---|
| git (work repo) | `C:\Tools\tg-llm-proxy` + `.git/` | init'd, branch `main`, commits: `ed46cf2` (Day 0 env) + the Day 1 config commit (see `git log`), **no remotes** (`git remote -v` empty) — push per §6 later |
| parent folders | `C:\Tools` — NOT a repo (no parent `.git`) | this project folder IS the work repo (no nested/monorepo ambiguity) |
| authorship | `git config --local` only: `arti` + `arti@localhost` | no global `.gitconfig` on machine → other repos untouched |
| Go module cache | `GOPATH=C:\Users\arti\go` (default, global) | Setup.ps1 switches to project-local `.cache\gopath` (§3) |
| Go build cache | `GOCACHE=%LOCALAPPDATA%\go-build` (default) | Setup.ps1 → `.cache\gocache` (§3) |
| go proxy | `GOPROXY=https://proxy.golang.org,direct` | §3; `go version` fetched OK |
| Docker image build | `dl.build` (Docker 29.2.1, demon running, compose v5.6.1) | §3 Day 2: `docker build --cache-dir .cache/gocache` |
| ignored (not in repo) | `PLAN.md`, `docs/PLAN.md`, `.cache/`, `.env`, `data/`, `bin/`, `*.db*`, `*.log` | proven by `git check-ignore -v` (rules in `.gitignore` lines 2–13) |

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
├── go.mod             # `module bot` + `go 1.27.3` (Go 1.27 modules prototype)
├── main.go            # entry point (package main) — signal.NotifyContext + §7 config
├── internal/config/   # config.go + limits.go — the §7 ENV loader (Day 1: done)
├── docker/            # Dockerfile + compose — Day 2+ (pending)
├── scripts/           # env-check.ps1, Setup.ps1 (created) + build/run/... (pending)
├── bin/               # runtime binaries (gitignored)
├── data/              # bot.db (gitignored)
├── zzscratch/         # local verification scratch (gitignored, never committed)
└── .env.example       # template — committed
```

§4 deviation (honest note): the plan draws the entry point as `cmd/bot/main.go`, but
the Go 1.27 modules prototype of this toolchain puts `package main` at the repo root
next to `go.mod` (`module bot`) and imports the config as `bot/internal/config`.
The layout of `internal/` matches §4; only the entry-point folder differs. Moving
`main.go` into `cmd/bot/` gets decided at Day 2 when `go-telegram/bot` is vendored
(the module graph then becomes visible), not silently now.

## Quick start (Windows/pwsh)

```powershell
. .\scripts\Setup.ps1          # PATH: git + GOPROXY/GOCACHE/GOPATH (§3, §11)
.\scripts\env-check.ps1        # §2: green before work (Go/git/pwsh/Docker/network)
git status                     # repo state
# later (Day 1+): go build -o bin/bot.exe && .\scripts\run.ps1 --foreground
# Day 1 actual: go build -o bin/bot.exe (package main lives at the repo root, §4 note)
```

## ENV (§7) — see `.env.example` (full table in PLAN.md §7)

The loader `internal/config` (imported by `cmd/bot` as `bot/internal/config`) reads
the process ENV once and accepts **exactly the 19 names of the §7 table** — a name
that is not a row of §7 is reported by name and pointed at the table (§16 change 25.1
moves the v1.0 extras `LLM_CHAT_*`, the token budgets and the deep link to the §15 backlog).

Required: `TELEGRAM_BOT_TOKEN` (no default — unset it falls on the start with a clear
error and exit code 1, §7 behaviour, §10 Day 1 DoD). `STRATA_API_KEY` may stay empty
if the endpoint does not need it (§7 table).
Docker/Windows default: `STRATA_BASE_URL=http://host.docker.internal:8000`, stored
**without** the `/v1` suffix — the client joins `{base}/chat/completions` and
`{base}/models` (§7 URL rule, §16 change 8); an old value with `/v1` gets cut by the
loader, so `/v1/v1/chat/completions` is unreachable.
`ALLOWED_USER_IDS` empty → **closed bot, deny by default**: only `/start` and `/whoami`
answer and everything else goes to `events(kind='denied')` (§7, §16 change 13).
`DB_PATH=./data/bot.db` is the local default for `run.ps1`; compose overrides it with
`/data/bot.db` and the named volume `bot-data:/data` is mandatory (§16 change 9, §8.1).
`MAX_MESSAGE_CHUNK` ≤ 4096 SYMBOLS (§12 UX, §16 change 7: кириллица = 2 bytes, the cut
goes by runes); `TG_REPLY_DEADLINE_SECONDS` must exceed `STRATA_TIMEOUT_SECONDS` (§10 Day 3).

## Next steps (§10)

- Day 1: DONE — `go.mod`, `cmd/bot/main.go` (graceful shutdown via `signal.NotifyContext`),
  `internal/config` (ENV → `Config` of §7, validation with the list of problems),
  `.env.example` = the §7 table; config unit tests arrive with the §11 test levels
- Day 2: `go-telegram/bot` v1 + `go mod tidy` (real names!) + Docker image
- Day 3: SQLite (WAL, `bot_state`) + `scripts/db_inspect.ps1` (pure-Go) + `--dry-run`
- Day 4: main loop + DB-safety invariants + crash-restart smoke test
- Day 5-10: idempotency → context → STRATA calls → answers → rate limit → smoke E2E

## Not implemented yet (honest list)

Implemented: the §7 configuration layer only — `internal/config` reads the process
ENV, validates every row of the §7 table with a clear message that names the row and
its bounds, and `cmd/bot` echoes the resolved configuration back (§10 Day 1 DoD).
Not implemented yet: no Telegram calls, no SQLite, no STRATA requests, no `/healthz`
(§16 change 11 — arrives with `internal/health` at Day 2-3), no §11 test levels yet.
The loader keeps secrets out of the output: `TELEGRAM_BOT_TOKEN` and `STRATA_API_KEY`
print only their length (§12 security, §16 change 25).
