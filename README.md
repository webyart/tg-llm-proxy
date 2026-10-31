# tg-llm-proxy — Telegram-бота → STRATA llamaServer (prototype, Go 1.27)

Prototype per the plan (`PLAN.md` — local planning doc, not committed, see §10).

Status: **Day 0 done** (§10 Day 0: environment checks), **Day 1 done** —
git repo + ignore-файлов + scripts created; `go build` passes with
`main.go` (package main, see «§4 deviation») + `internal/config` (the ENV → Config loader of §7, 19 rows,
validation with the list of problems, §10 Day 1 DoD: echoes the configuration back
and stops cleanly on Ctrl-C with exit code 0; an unset `TELEGRAM_BOT_TOKEN` →
clear error + exit code 1). **Day 4 done** — `internal/bot` (long polling of §5 over
`go-telegram/bot` v1.27.0, the dispatcher + command registry + middleware of §6) is wired into
`main.go`: the process now polls Telegram, answers the §6 commands, and stops on Ctrl-C/SIGTERM
with the offset of §5 written before `db.Close` checkpoints the WAL. Reaching `api.telegram.org` from a
Windows process needs the proxy of this machine handed to the process ENV — `go http.Transport` ignores the
WinINET registry, so `scripts\run.ps1 -UseSystemProxy` exports `HTTPS_PROXY` and only then does `getMe`
answer (2026-10-31; without the flag the start stops at `getMe` with exit 1). The §11 Integration level runs
against the fake Telegram of `internal/testutil/telegram.go` regardless (§16 change 16: `bot.WithServerURL`) —
it is what makes the §10 DoD cases repeatable, not a substitute for the missing network.

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
| sqlite3 CLI | absent | expected (§5) → `scripts/db_inspect.ps1` exists and is that Go-tool (`cmd/dbinspect`, read-only `mode=ro`) |
| go proxy | `https://proxy.golang.org,direct` | §3; caches `.cache/gocache`, `.cache/gopath` |
| STRATA (the model of §7) | `http://127.0.0.1:8080/v1`, answers now | measured 2026-10-31: `GET /v1/models` → 200 (serves `qwen3.8-flash-next-iq3_s`, and `.env` names exactly that id in `STRATA_MODEL` — the served id changed that day, the previous `qwen3.8-flash-next-nvfp4-nvfp4-nvidia` still answered `POST` 200 although `/v1/models` no longer listed it), `POST /v1/chat/completions` → 200 in 0.7 s, tokens 59/35 (`scripts/strata-check.ps1`); a container reaches the same service as `host.docker.internal:8080` (§8.1) |

## Layout (§4)

```
tg-llm-proxy/
├── PLAN.md            # planning doc (local, gitignored)
├── go.mod             # `module bot` + `go 1.27.3` (Go 1.27 modules prototype)
├── main.go            # entry point (package main) — signal.NotifyContext + §7 config
├── cmd/dbinspect/     # the second package main: the read-only viewer of §5 behind scripts/db_inspect.ps1
│                      # (not in the image of §8.1 — a maintenance reader has no business in a bot container)
├── internal/config/   # config.go + limits.go — the §7 ENV loader (Day 1: done)
├── internal/store/    # schema of §5 via go:embed, Open/PRAGMA, messages + bot_state (Day 2: done) +
│                      # inspect.go — the read-only side (mode=ro, no _journal_mode/_txlock) of §9
├── internal/model/    # client.go + openai.go — the STRATA client of §10 День 3 (retries, budget)
├── internal/ratelimit/ # rate limiter + per-chat lock of §12 (§10 День 4 middleware)
├── internal/bot/      # §6 + §10 День 4: registry (commands.go), normalization + middleware
│                      # (dispatch.go, middleware.go), context builder (context.go), answers with the
│                      # 429/flood sleep + chunking (reply.go), the library adapter run.go
│                      # (New/getMe, WithServerURL, WithInitialOffset, workers, errors handler)
├── internal/health/   # /healthz of §9 (Day 5: done)
├── internal/testutil/ # httptest fakes of §11: strata.go (the /v1 endpoint) + telegram.go
│                      # (getMe/getUpdates/sendMessage/setMyCommands + the offset semantics + 429)
├── test/integration/  # //go:build integration — §11 «бот ↔ фейковый STRATA ↔ фейковый Telegram»
├── Dockerfile         # multi-stage (§8.1: golang builder → alpine + ca-certificates, USER 65534) — Day 5: done
├── docker-compose.yml # bot + named volume bot-data:/data + 127.0.0.1 healthz publish + `test` profile for -race (§8.1)
├── scripts/           # Setup, env-check, strata-check, build, test, test-race, lint, run, smoke,
│                      # docker-build, docker-up, docker-down, db_inspect (§9: every script the plan names)
├── bin/               # runtime binaries (gitignored)
├── data/              # bot.db (gitignored)
├── zzscratch/         # local verification scratch (gitignored, never committed)
└── .env.example       # template — committed
```

§4 deviation (honest note, updated after День 4): the plan draws the entry point as `cmd/bot/main.go`, but
the Go 1.27 modules prototype of this toolchain puts `package main` at the repo root
next to `go.mod` (`module bot`) and imports the config as `bot/internal/config`.
The layout of `internal/` matches §4; only the entry-point folder differs. `go-telegram/bot` v1.27.0 arrived
in `go.mod` at День 4 and the root entry point survived it — `go build .` of the Dockerfile builder stage
(§8.1) and `go build -o bin\bot.exe .` of scripts/build.ps1 use the same target, so the host and the image
cannot drift apart, and moving the file now would buy nothing but a diff. `cmd/` does exist for the second
entry point (`cmd/dbinspect`): a second `package main` cannot sit next to the root one.

## Quick start (Windows/pwsh)

```powershell
. .\scripts\Setup.ps1          # PATH: git + GOPROXY/GOCACHE/GOPATH (§3, §11)
.\scripts\env-check.ps1        # §2: green before work (Go/git/pwsh/Docker/network)
git status                     # repo state
.\scripts\strata-check.ps1     # §10 День 3: is STRATA ready? (§6 /model check + a chat probe)
.\scripts\lint.ps1             # §9: gofmt + go vet (also with -tags integration)
.\scripts\test.ps1             # §11 Unit + Store levels (no -race on this host, §3.1)
.\scripts\smoke.ps1 -Unit      # §11 Integration: fake Telegram + fake STRATA, then the leak scan of §12
.\scripts\build.ps1            # §9: go mod tidy + bin\bot.exe + the §7.1 «empty ENV must exit 1» check
.\scripts\run.ps1              # §9: loads .env (values never printed), builds, runs — Ctrl-C stops it
.\scripts\db_inspect.ps1       # §9: reads DB_PATH of §7 through mode=ro — chats/messages/events/bot_state
.\scripts\test-restart.ps1     # §10 День 4 at process level: hard kill between two instances, the §5 offset must survive
# container of §8.1: .\scripts\docker-build.ps1; .\scripts\docker-up.ps1 (waits for /healthz = 200);
# .\scripts\test-race.ps1 (the -race run lives in the image, §3.1); .\scripts\docker-down.ps1
```

## Пошаговая инструкция (§10 День 7)

The whole path from an empty folder to a bot that answers, in the order the steps actually depend on each
other. Every line is a command from the repository root in pwsh; `.` before `Setup.ps1` is not decoration —
without the dot the PATH of §3 dies with the child process.

1. `. .\scripts\Setup.ps1` — git on PATH + `GOPROXY`/`GOCACHE`/`GOPATH` of §3 for this session.
2. `.\scripts\env-check.ps1` — §2: Go ≥ 1.27, pwsh ≥ 7, git, Docker daemon, network. Red here means the
   later steps cannot be trusted, fix it first (§10 День 0 DoD).
3. `Copy-Item .env.example .env` and fill the §7 table: `TELEGRAM_BOT_TOKEN` from BotFather,
   `TELEGRAM_ALLOWED_USERS` = your own numeric `user_id` (id@username in BotFather is NOT it — §14.1;
   §16 change 29 made this the row's only spelling, the old `ALLOWED_USER_IDS` is refused by name), the STRATA
   URL/model/key. Nothing else has to be touched: every remaining row of the 19 has the default of §7.
   `.env` is gitignored and never printed (§12 security).
4. `.\scripts\strata-check.ps1` — is the model reachable at all (§6 `/model` check + one chat request)?
   A red here with the rest green means STRATA, not the code (§13 риск «сервер недоступен»).
5. `.\scripts\lint.ps1`, `.\scripts\test.ps1`, `.\scripts\smoke.ps1 -Unit` — the three green levels of §11
   that do not need a network: Unit + Store on the host, Integration against the fake Telegram and the fake
   STRATA, then the leak scan of §12 over the whole source tree.
6. `.\scripts\test-race.ps1 -Tagged` — §11 Concurrency inside the §8.1 test image (§3.1: the host has no
   C toolchain, so `-race` physically cannot run here).
7. Local run: `.\scripts\run.ps1` — loads `.env` into the process ENV, builds `bin\bot.exe`
   (`CGO_ENABLED=0`), prints the §7 configuration back, starts polling, stops on Ctrl-C with exit 0.
   Measured 2026-10-31 on THIS machine: the host reaches `api.telegram.org` only through the WinINET proxy
   (`127.0.0.1:10808` in `HKCU\…\Internet Settings`), which Go's `http.Transport` does not read — so the start
   ends at `getMe` with exit 1 unless the proxy is handed to the process: `.\scripts\run.ps1 -UseSystemProxy`
   exports `HTTPS_PROXY` from those registry settings for the child process only and the bot polls normally.
   `scripts/env-check.ps1` checks exactly this (a TCP connect from the process + `HTTPS_PROXY`), because a
   check that asks PowerShell reports «network OK» while the bot dies at `getMe` (§10 День 4 «без сети/токена
   бот не стартует» stays true; only the diagnosis changed). A `HEALTH_PORT` of §7 held by another process
   stops the start too — on this machine the local STRATA sits on 8080, so `.env` carries `HEALTH_PORT=8081`;
   the bind happens before the process claims that `/healthz` answers (§12 reliability).
8. Container run (the way that has a network here): `.\scripts\docker-build.ps1`, then
   `.\scripts\docker-up.ps1` — compose starts `tg-llm-proxy-bot` with `DB_PATH=/data/bot.db` in the named
   volume `bot-data` (§8.1) and waits for the health check; `docker compose logs -f bot` for the JSON log
   of §12; `.\scripts\docker-down.ps1` to stop (add `-Volumes -ConfirmVolumes` only when the history of §5
   is really meant to go).
9. Talk to the bot with the §6 commands (the manual check-list below). The first message from a new user is
   `/start` — it registers the chat of §5; while `TELEGRAM_ALLOWED_USERS` does not contain that `user_id`,
   `/whoami` still answers and prints the id to add (§16 change 13).
10. Read what the bot stored, without a Telegram account: `.\scripts\db_inspect.ps1` — it takes `DB_PATH` of
    §7 (from `.env`), builds `bin\dbinspect.exe` when the sources are newer and prints `bot_state` (the offset
    that must survive a restart), the events by kind (`denied`/`flood`/`model_error`/`limit`), the chats with
    their message counts, and the newest messages and events. `-Chat 4242` narrows it to one chat, `-Limit 100`
    widens it, `-Integrity` runs `PRAGMA integrity_check` of §15 (exit 2 when the file is not sound). The read
    goes through `mode=ro` (the maintenance DSN of §5): it can look at the database of a RUNNING bot, cannot
    write a row to it and cannot re-mode its journal (`internal/store/inspect.go`, `TestReadOnlyCannotWrite`).
    A container keeps its database in the named volume, so read it there:
    `docker compose cp bot:/data/bot.db .\zzscratch\bot.db` and then
    `.\scripts\db_inspect.ps1 -Path .\zzscratch\bot.db`.
11. `.\scripts\docker-down.ps1` + `git status` before a commit: `bin/`, `data/`, `*.db*`, `.env`,
    `zzscratch/` are all ignored (§10 День 1); `scripts/smoke.ps1` has already proven no token is in a file.

## Ручной чек-лист демо (§11 «Manual», реальный Telegram + реальный STRATA)

Перед обходом удобно поставить `LOG_LEVEL=debug` в `.env` и рестартнуть бота: тогда на каждый апдейт в JSON-логе
появляется строка «the update arrived» с `update_id` и длиной текста (`internal/bot/run.go`; самого текста в логе
нет — §12 security), и щелчок в Telegram становится сопоставим с конкретной строкой лога. После обхода row
возвращается к `info`: `main_test.go` фиксирует, что на `info` этой строки нет, а ERROR-строки остаются, — то
есть переключение описано тестами, а не догадкой.

- [ ] `/start` in a private chat → the greeting + the command list; a second `/start` changes nothing
      (`EnsureChat` of §5 is idempotent).
- [ ] `/whoami` → the numeric `user_id`, `chat_id` and the line «в allowlist: да/нет»; when it says «нет»,
      the answer names the ENV row to write the id into (`TELEGRAM_ALLOWED_USERS`, §16 change 29) — restart
      after writing, and the refusal of §7 must disappear.
- [ ] `/help`, `/ping` (the `pong <uptime>` grows), `/model`, `/model check` (the real `GET /models` of §14).
- [ ] 10 messages of `/ask …` in one chat → 10 pairs of rows in `messages` (§5), the answer footer
      `⏱ latency · N→M токенов · модель … · попыток K`, and `/stats` counts the same numbers.
- [ ] two `/ask` in a row from two different chats → both answers arrive, the context of each chat stays
      its own (the per-chat lock and the context builder of §12).
- [ ] `/ask` with a Russian answer longer than `MAX_MESSAGE_CHUNK` → several messages, each ≤ 4096 symbols,
      nothing cut mid-word, no dangling `\` at the end of a chunk (§12 UX, §16 change 7).
- [ ] `docker compose restart` in the middle of a conversation → no repeated answers, no duplicated rows
      (§5 `bot_state` + `uq_messages_chat_tg_id`; §10 День 6 proved both in `test/integration`).
- [ ] stop STRATA (or point `STRATA_BASE_URL` at a dead port) → `/ask` answers the «модель недоступна»
      notice of §12, `events(kind='model_error')` grows, `/ping` still works; restart STRATA → `/ask`
      recovers without a bot restart (§13 риск «STRATA лежит»).
- [ ] flood: `/ask` ~30 times in a few seconds → Telegram's 429 is logged as `retry_after`, the chat waits
      and the answer still arrives (§10 День 4, `events(kind='flood')` grows).
- [ ] a stranger writes `/ask` → the refusal of §7 and `events(kind='denied')`; `/start` and `/whoami`
      stay open (§16 change 13).
- [ ] Ctrl-C / `docker compose down` during a long `/ask` → the process exits, WAL of §5 is checkpointed,
      the next start continues from the stored offset (§12.1.4).

### Часовой пояс: `created_at` — это UTC (§5, §13)

`messages.created_at` and `chats.last_active_at` are written by SQLite as `datetime('now')`, i.e. **UTC**
without a timezone marker, and `store` returns the text exactly as it is stored — `/history` and `/stats`
therefore show UTC too. The container has `tzdata` (§8.1) so that a reader can convert deliberately
(`docker compose exec bot date`), and the JSON log of §12 carries its own RFC3339Nano timestamp, which is
the machine-readable half of the same story: a log line and a DB row can be lined up only in UTC.

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
`TELEGRAM_ALLOWED_USERS` empty → **closed bot, deny by default**: only `/start` and `/whoami`
answer and everything else goes to `events(kind='denied')` (§7, §16 change 13).
**§16 change 29 renamed the row.** The §7 table of the plan named it `ALLOWED_USER_IDS`, the `.env` of this
machine carried `TELEGRAM_ALLOWED_USERS`, and the name fell between the two lists the loader reports (the 19
rows + the v1.0 legacy rows), so the real `user_id` configured nothing and nothing was printed about it —
§12 reliability's worst failure: a setting that looks applied and is not. `TELEGRAM_ALLOWED_USERS` is now
**the** row of §7 (still 19 names), `UserIDs` reads that one name, and the old `ALLOWED_USER_IDS` left the
loader: it is not merged and not quietly ignored — `Renamed` refuses it by name and the start stops with exit
code 1 pointing at the new spelling, because an old file must not turn into a silently closed bot.
`/whoami` prints the numeric `user_id` plus «в allowlist: да/нет» and the name to write it under
(`bot.AllowlistHint`, §12 UX); a malformed entry is named under that same name (§16 change 28, which first
accepted the Telegram-style spelling, is superseded). Verified 2026-10-30: `docker compose config` shows the
one name, the start echoes `TELEGRAM_ALLOWED_USERS 1 entries`, and `PrintMissing` no longer claims a row that
is in fact set.
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
  volume `bot-data:/data` and the `test` profile for `-race`, `internal/health` (`/healthz` of §9),
  the §9 scripts (`build`, `test`, `test-race`, `lint`, `run`, `docker-build`, `docker-up`,
  `docker-down`); the healthz port is published on `127.0.0.1` only (§12 security: not on a public iface).
  Verified on this host with a 15-second live probe: `docker compose up -d` → container `Up (healthy)`
  (the in-container `wget` of the §8.1 HEALTHCHECK answers 200) and `/data/bot.db` + `-wal`/`-shm` appear
  in the named volume. Caveat measured here: the HOST-side probe of §9
  («GET http://localhost:8080/healthz») is refused through the port proxy of Docker Desktop even while the
  container is healthy, so `docker-up.ps1`/`smoke.ps1` accept the `docker inspect` health state and treat
  the host probe as WARN (§12 reliability: the check must not lie about the bot).
- Day 4: DONE — `internal/bot` over `go-telegram/bot` v1.27.0: the registry of §6 (`commands.go`),
  normalization `@BotName`/case + the middleware chain (allowlist with `/start`+`/whoami` always allowed,
  rate limit, per-chat lock, panic recovery — `dispatch.go`, `middleware.go`), the context builder of §7
  (`context.go`), MarkdownV2/HTML escaping then the rune-wise cut, the `⏱ latency, tokens` footer,
  `RetryAfter` sleep per chat + `events(kind='flood')` (`reply.go`), and the adapter `run.go`
  (`bot.New` + `getMe`, `WithServerURL` for tests, `WithInitialOffset` from `bot_state`,
  `WithHTTPClient(pollTimeout)`, `WithWorkers`, `WithErrorsHandler`, `setMyCommands` at start, `Sanitize`
  of every library error). `main.go` wires config → store → model client → bot → healthz and stops
  gracefully: the poller is awaited before `db.Close` (§5 WAL), a dead poller exits 1 instead of a
  healthy-but-deaf container (§12). The DoD cases «denied user → events kind='denied'»,
  «/ask@BotName works», «restart does not repeat answers» are green as integration tests (§11).
  **Network split measured on this machine**: from the Windows host `api.telegram.org` is unreachable, so a
  local `scripts/run.ps1` stops at `getMe` with exit 1 (§10 День 4: «без сети/токена бот не стартует»), and
  the URL inside that error is already sanitized — `https://api.telegram.org/bot***/getMe`, no token in the
  log (§12 security, checked by `scripts/smoke.ps1`). Corrected 2026-10-31: what is unreachable from the host
  is the *process*, not the machine — the host has a WinINET proxy in the registry that `http.Transport` does
  not read, and `scripts/run.ps1 -UseSystemProxy` hands it to the child as `HTTPS_PROXY`, after which the same
  `bin\bot.exe` polls Telegram from Windows and answers real chats (step 7 above). Inside the container the same token works: a
  15-second `docker compose up -d` probe logged `bot is polling Telegram, bot_state last_update_id 0
  found false` with the container `Up (healthy)`. The interactive part of the §11 manual checklist (real
  answers to the §6 commands in a real chat) now only needs the bot to be reachable: `.env` lists the real
  `user_id` under the row `TELEGRAM_ALLOWED_USERS` (§16 change 29 — the row's only spelling), so the refusal
  of §7 and the `events(kind='denied')` row are reserved for strangers.
- Day 6: DONE — `internal/testutil/telegram.go` (the fake Bot API: `getMe`, `getUpdates` with the
  real offset semantics, `sendMessage`, `setMyCommands`, scripted 429 with `retry_after`, wrong-token
  counter, `Replay`), `test/integration/run_test.go` (`//go:build integration`, 8 cases),
  `scripts/smoke.ps1`, and `scripts/test-race.ps1 -Tagged` green: `go vet` + `gofmt` + `go test -race
  -tags integration ./... -count=2` inside the §8.1 test image — no data races (§11 Concurrency,
  §10 День 6 DoD). The race detector earned its keep once: the integration harness had been polling a
  plain `bytes.Buffer` of the run log while the library answers every update in its own goroutine
  (`ProcessUpdate` → `go r(ctx, b, upd)` in v1.27.0) — the sink is now `syncLog` (mutex-guarded), which is
  a test-only fix, production hands slog an `os.Stdout`.
  Idempotency of §10 День 6 is proved on both levels: `TestOffsetSurvivesRestart` (the offset of §5 means
  the update never comes back) and `TestRedeliveredUpdateIsAnsweredOnce` (the crash *before* `bot_state`
  was written: the same update arrives again, `store.Append` hits `uq_messages_chat_tg_id`, the answer is
  silence — no second `sendMessage`, no second model call, one extra log line).
- Day 7 (partly): DONE — the JSON log of §10 День 7 got the fields the plan names: every answered `/ask`
  writes `chat_id`, `user_id`, `model`, `latency_ms`, `attempts`, `tokens_in`, `tokens_out` through the
  `slog` JSON handler of `main.go` (an unknown usage stays `null`, §14 риск «usage полей нет»), and
  `TestAskLogsTheFieldsOfDay7` / `TestAskLogsUnknownTokensAsNull` read those very JSON lines; `/stats` of §6
  is part of the registry and of the integration suite. The README now carries the пошаговая инструкция, the
  §11 manual check-list, the UTC note about `created_at` (§13) and the §15 v2 backlog.
  **§16 changes 28 → 29** (found in the real `.env`): the allowlist was written as `TELEGRAM_ALLOWED_USERS`,
  the loader knew only the plan's name `ALLOWED_USER_IDS`, and the row fell between the two lists the loader
  reports (§7 rows + the v1.0 legacy rows) — so the real `user_id` configured nothing and nothing was printed
  about it. Change 28 first accepted the second spelling as an alias; change 29 reversed the direction the
  same day, because the name the operator actually writes is the Telegram-style one: `TELEGRAM_ALLOWED_USERS`
  is now the row of §7 (`Env` still holds 19 names, `UserIDs`/`RowIsSet` read exactly that one), and
  `ALLOWED_USER_IDS` is gone from the loader — `Renamed` refuses it by name and the start exits 1 pointing at
  the new spelling rather than ignoring it (§7: the loader reads exactly the names of the table). `/whoami`,
  the refusal texts and `bot.AllowlistHint` name the single row (§12 UX). Covered by
  `TestRowOfTheAllowlistIsTelegramsName`, `TestOldNameOfTheRowIsRefusedByName`,
  `TestAllowlistCountsADuplicateOnce`, `TestAllowlistNamesTheRowOfABadEntry` and `TestAllowlistRowOpensTheBot`
  (the last one asserts the user reaches the model and `events(kind='denied')` stays empty).
  Still open: the manual demo itself (the checklist above) and the last §1 criterion it proves — both need
  `api.telegram.org` from the process, which this host filtered for the Windows binary until 2026-10-31 (see
  the next paragraph).
  **The live demo was walked on 2026-10-31**: `.\scripts\run.ps1 -UseSystemProxy` on the Windows host, a real
  chat, real STRATA. `/ping` answered, `/ask 2+2` answered `4`, and the assistant row of §5 carries
  `model`, `latency_ms` 1567 and tokens 62/25 — the §10 День 7 fields read back with
  `scripts\db_inspect.ps1` while the bot was still running. The walk exposed two defects, and both are fixed
  and covered: a `HEALTH_PORT` held by the local STRATA made the process promise `/healthz` and die later, so
  the bind now happens before the promise (`health.Listen`, `TestListenNamesTheTakenPortAtOnce`, `.env` →
  8081); and the «carries no text» log line dereferenced `upd.Message.Chat.ID` before the nil check, so one
  `callback_query`/`my_chat_member`/`edited_message` update panicked inside the library and killed the process
  — silently, because getUpdates has already deleted the update and `bot_state` only moves after `Dispatch`.
  `telegramHandler` now has its own recover, and `TestUpdatesWithoutMessageSurviveTheHandler` fails with that
  exact nil dereference against the old code.
  A STRATA outage in the same walk (07:17 local, `dial tcp 127.0.0.1:8080: connectex … actively refused`)
  showed the third gap: the user was answered and `events(kind='model_error')` held the reason, while the §12
  log held only «the update arrived» lines — the failure was invisible to whoever reads the log. A failed
  model call is an ERROR line with `chat_id` and the sanitized reason now
  (`TestModelFailureIsLoggedWithItsReason`), and `cmd/dbinspect` no longer clips `events.detail` at 60 runes,
  which is exactly where that reason used to disappear.
  **The kill case of §10 День 4 is now proven at process level too.** `scripts/test-restart.ps1` (GREEN on
  2026-10-31: 19 checks, 0 FAIL, 0 WARN) starts the bot, waits for `/healthz` = 200 on `HEALTH_PORT` of §7,
  kills it with `taskkill /F` — not a polite stop, §10 День 4 asks about a death the program did not choose —
  and reads the §5 numbers again through `cmd/dbinspect -counts` (a new `key=value` mode of the same read-only
  viewer, so a script compares numbers instead of parsing a table): `journal_mode=wal`, `integrity=ok` and
  `last_update_id` unchanged while **no bot was running** to checkpoint the WAL for the reader, then a second
  instance that resumes on exactly that offset. Two more scans cover the failures of the same day: no panic
  text in the logs of either run (the update-without-`message` case above) and no value of
  `TELEGRAM_BOT_TOKEN` in them (§12 security). What the script deliberately does not check is named in its own
  header: repeated answers are the unique index of §5 plus the integration tests, and the SIGTERM case of
  §12.1.4 belongs to `docker compose stop` of §8.1, because Windows has no signal for a detached process (§3.1).

## Not implemented yet (honest list)

Implemented: the §7 configuration loader, the SQLite layer of §5, the STRATA client of §10 День 3,
the dispatcher/registry/middleware and the Telegram adapter of §10 День 4, the §11 Integration level with
the fake Telegram + fake STRATA and the `-race` run of §11 Concurrency (both green in the container of
§8.1), `/healthz` of §9, the Docker image of §8.1, the §9 scripts and the read-only viewer of §5
(`cmd/dbinspect` + `scripts/db_inspect.ps1`, so the history is readable without a Telegram account and
without `sqlite3` CLI, plus the `-counts` key=value mode a script can compare) and `scripts/test-restart.ps1`
— the §10 День 4 kill/restart case at the level of a real process, GREEN on 2026-10-31 with 19 checks:
`taskkill /F` between two instances, `journal_mode=wal` / `integrity=ok` / `last_update_id` unchanged read
while no bot is running, the second instance resuming on that very offset, no panic text and no token value in
the logs. The repeated-answer half of that case stays with `uq_messages_chat_tg_id` of §5 and the integration
tests `TestOffsetSurvivesRestart` and `TestRedeliveredUpdateIsAnsweredOnce`; the SIGTERM half of §12.1.4 stays
with `docker compose stop` of §8.1, because Windows has no signal for a detached process (§3.1).
Not implemented yet: the manual demo of §11 (the §10 День 7 checklist: `/ping` and `/ask` were walked in a real chat
on 2026-10-31, the rest of the list — `/help`, `/model`, `/forget`, a denied user, a restart without repeated
answers — has not been walked by a human yet). Secrets stay
out of the output: `TELEGRAM_BOT_TOKEN` and `STRATA_API_KEY` print only their length, every library error
goes through `bot.Sanitize` (a live run shows `https://api.telegram.org/bot***/getMe`), the fakes record
the Authorization header but no test prints it, and `scripts/smoke.ps1` greps the whole source tree for
the token value before a commit (§12 security, §16 change 25).

## Бэклог v2 (§15, «запись v2-бэклога» of §10 День 7)

Not a todo list for v1 — these are the things §15 deliberately left out of the prototype, plus what this
implementation made concrete enough to name.

- Стриминг ответов: правка одного сообщения раз в ~1 с вместо одного `sendMessage` (the chunking of
  `internal/bot/reply.go` already owns the size logic a stream would reuse).
- Вебхуки + HTTPS (`StartWebhook` of the library exists) вместо long polling; `bot_state` offset then
  becomes the idempotency key of the incoming side instead of the poll cursor.
- Команды `/image`, `/voice`, вложения и файлы (§15) — the registry of §6 takes a new entry without
  touching the dispatcher, that is what `UsesModel`/`AlwaysAllowed` are for.
- Пресеты системного промпта (`/persona`) и выбор модели (`/model select`); инструменты/функции модели и
  RAG поверх локальных документов.
- `/admin`: сводка по всем чатам, очередь и приоритеты запросов (v1 counts one chat, §6 `/stats`).
- Версионированные миграции (`schema_version` рядом с `bot_state`), бэкап SQLite, `PRAGMA integrity_check`
  по расписанию; v1 applies `001_init.sql` through `go:embed` and nothing else (§5).
- CI (gofmt/vet/test/`-race`) and publishing the image to a registry — today the §9 scripts are the pipeline.
- `Config.Get()` — overrides per chat / per `user_id` (§16 change 25.1 kept the loader at the 19 rows of §7;
  the first consumer would have been a per-chat `PARSE_MODE`, which v1 does not need).
- Time budgets and TTLs as their own module: the limits of §7 live next to the loader in
  `internal/config/limits.go`; the TTL of the `bot_state` rows and a per-request budget bookkeeping move to v2.
- The v1.0 field names (`LLM_CHAT_*`, the token budgets, the deep links, `PORT`, `DB_DBPATH`, …) stay out of
  the loader until a consumer exists — §16 change 25.1 says an ENV row without a reader is a liability.

