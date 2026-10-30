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
├── internal/store/    # schema of §5 via go:embed, Open/PRAGMA, messages + bot_state (Day 2: done)
├── internal/model/    # client.go + openai.go — the STRATA client of §10 День 3 (retries, budget)
├── internal/health/   # /healthz of §9 (Day 5: done)
├── internal/testutil/ # the httptest fake STRATA of §11 (serves /v1/..., §16 change 26)
├── Dockerfile         # multi-stage (§8.1: golang builder → alpine + ca-certificates, USER 65534) — Day 5: done
├── docker-compose.yml # bot + named volume bot-data:/data + the `test` profile for -race (§8.1)
├── scripts/           # env-check.ps1, Setup.ps1, strata-check.ps1 (§10 День 3) + build/run/... (pending)
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
.\scripts\strata-check.ps1     # §10 День 3: is STRATA ready? (§6 /model check + a chat probe)
go test ./...                  # §11: config, store, model (against the httptest fake)
# later (Day 4+): go build -o bin/bot.exe && .\scripts\run.ps1 --foreground
```

## ENV (§7) — see `.env.example` (full table in PLAN.md §7)

The loader `internal/config` (imported by `cmd/bot` as `bot/internal/config`) reads
the process ENV once and accepts **exactly the 19 names of the §7 table** — a name
that is not a row of §7 is reported by name and pointed at the table (§16 change 25.1
moves the v1.0 extras `LLM_CHAT_*`, the token budgets and the deep link to the §15 backlog).

Required: `TELEGRAM_BOT_TOKEN` (no default — unset it falls on the start with a clear
error and exit code 1, §7 behaviour, §10 Day 1 DoD). `STRATA_API_KEY` may stay empty
if the endpoint does not need it (§7 table).
Docker/Windows default: `STRATA_BASE_URL=http://host.docker.internal:8000`. The loader normalizes
it with `config.NormalizeBase` to **exactly one** `/v1` and joins `{base}/v1/chat/completions` and
`{base}/v1/models` (§7 URL rule, §16 change 8, §16 change 26: the real endpoint answers 200 on
`/v1/chat/completions` and 404 without the prefix, so a written `/v1` is kept — a trailing `/v1/v1/`
is still collapsed, and `scripts/strata-check.ps1` repeats the same rule).
`ALLOWED_USER_IDS` empty → **closed bot, deny by default**: only `/start` and `/whoami`
answer and everything else goes to `events(kind='denied')` (§7, §16 change 13).
`DB_PATH=./data/bot.db` is the local default for `run.ps1`; compose overrides it with
`/data/bot.db` and the named volume `bot-data:/data` is mandatory (§16 change 9, §8.1).
`MAX_MESSAGE_CHUNK` ≤ 4096 SYMBOLS (§12 UX, §16 change 7: кириллица = 2 bytes, the cut
goes by runes); `TG_REPLY_DEADLINE_SECONDS` must exceed `STRATA_TIMEOUT_SECONDS` (§10 Day 3).

## Progress (§10)

- Day 0: DONE — env-check green, git repo, `.env` from `.env.example`, Docker image builds
- Day 1: DONE — `go.mod`, `main.go` (graceful shutdown via `signal.NotifyContext`),
  `internal/config` (ENV → `Config` of §7, validation with the list of problems),
  `.env.example` = the §7 table, unit tests of the loader (§11 level «юнит»)
- Day 2: DONE — `internal/store`: `migrations/001_init.sql` through `go:embed`, `store.Open` with
  the reference DSN of §5 (validated shorts, not `_pragma=`), the repositories of chats / messages /
  events / `bot_state`, tests on a file DB and in-memory (§16 change 17)
- Day 3: DONE — `internal/model` (`Client` of §4 + `openai.go`: 2 попытки, экспоненциальная
  задержка, только сеть и 5xx, один бюджет времени §7), `internal/testutil` (httptest-фейк STRATA
  of §11), `scripts/strata-check.ps1`, интеграционный тест §10 День 3 DoD: успех / timeout / 500 /
  пустой ответ / 500→успех после ретрая — ровно одна строка `assistant` в БД
- Day 5 (partly): DONE — `Dockerfile` multi-stage (§8.1), `docker-compose.yml` with the named
  volume `bot-data:/data` and the `test` profile for `-race`, `internal/health` (`/healthz` of §9)
- Day 4 (next): `go-telegram/bot` v1.27.0 + long polling `b.Start(ctx)`, dispatcher and the command
  registry of §6, middleware (allowlist, rate limit, per-chat lock, panic recovery). The library is
  **not** in `go.mod` yet: the first `go mod tidy` needs network (§16 change 20)
- Day 6: integration tests with `bot.WithServerURL` (fake Telegram API) + `-race` in the container,
  `smoke.ps1`, the remaining PowerShell scripts (`build`, `test`, `test-race`, `lint`, `run`)
- Day 7: `slog` JSON logs, `/stats`, README walkthrough, manual demo

## Not implemented yet (honest list)

Implemented: the §7 configuration loader, the SQLite layer of §5, the STRATA client of §10 День 3,
`/healthz` of §9 and the Docker image. Not implemented yet: no Telegram calls at all (no polling, no
dispatcher, no commands of §6), so the bot does not answer anyone yet; no rate limit, no per-chat
lock, no idempotency of §10 День 6, no `slog` logs and no `/stats`. The model client never touches
SQLite — the write invariant of §10 День 3 («assistant ровно один раз») is enforced by the caller,
and that caller arrives with День 4. Secrets stay out of the output: `TELEGRAM_BOT_TOKEN` and
`STRATA_API_KEY` print only their length, the fake STRATA records the Authorization header but no
test prints it (§12 security, §16 change 25).
