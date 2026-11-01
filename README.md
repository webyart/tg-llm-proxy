# tg-llm-proxy — Telegram-бота → STRATA llamaServer (prototype, Go 1.27)

Prototype per the plan (`PLAN.md` — local planning doc, not committed, see §10).

Status: **Day 0 done** (§10 Day 0: environment checks), **Day 1 done** —
git repo + ignore-файлов + scripts created; `go build` passes with
`main.go` (package main, see «§4 deviation») + `internal/config` (the ENV → Config loader of §7, 29 rows,
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
| git (work repo) | `C:\Tools\tg-llm-proxy` + `.git/` | init'd, branch `main`, commits: `ed46cf2` (Day 0 env) + the Day 1 config commit (see `git log`), remotes: `origin` = GitHub, `gitlab` = GitLab (release CI) |
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
├── internal/model/    # client.go + openai.go + stream.go — the STRATA client of §10 День 3 (retries, budget)
│                      # + the Streamer of §16 change 34 (SSE, idle/total budgets, reasoning_content kept apart)
├── internal/ratelimit/ # rate limiter + per-chat lock of §12 (§10 День 4 middleware)
├── internal/bot/      # §6 + §10 День 4: registry (commands.go), normalization + middleware
│                      # (dispatch.go, middleware.go), context builder (context.go), answers with the
│                      # 429/flood sleep + chunking (reply.go), the library adapter run.go
│                      # (New/getMe, WithServerURL, WithInitialOffset, workers, errors handler),
│                      # the streaming session of §16 change 34 (stream.go: placeholder → cadenced edits →
│                      # final edit + the tail as the ordinary messages; run.go is the StreamSender)
├── internal/health/   # /healthz of §9 (Day 5: done)
├── internal/testutil/ # httptest fakes of §11: strata.go (the /v1 endpoint) + telegram.go
│                      # (getMe/getUpdates/sendMessage/setMyCommands + the offset semantics + 429)
├── test/integration/  # //go:build integration — §11 «бот ↔ фейковый STRATA ↔ фейковый Telegram»
├── Dockerfile         # multi-stage (§8.1: golang builder → alpine + ca-certificates, USER 65534) — Day 5: done
├── docker-compose.yml # bot + named volume bot-data:/data + 127.0.0.1 healthz publish + `test` profile for -race (§8.1)
├── scripts/           # Setup, env-check, strata-check, build, test, test-race, lint, run, stop, smoke,
│                      # docker-build, docker-up, docker-down, db_inspect (§9: every script the plan names,
│                      # plus stop.ps1 — the graceful stop of §10 День 1 for a detached instance)
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
cannot drift apart, and moving the file now would buy nothing but a diff. `cmd/` holds the maintenance entry
points (`cmd/dbinspect`, `cmd/dbbackup`, `cmd/dbrestore`, `cmd/dbrotate`, `cmd/dbprune`, `cmd/dbfixture`):
another `package main` cannot sit next to the root one.

## Supported platforms (Windows, macOS, Linux)

The Go runtime and Docker image are platform-independent. Host maintenance uses PowerShell 7 (`pwsh`), Go 1.27.3,
Git and Docker. Docker Desktop is recommended on Windows/macOS; Docker Engine with Compose v2 works on Linux.
The database stays in the named Docker volume, not a platform-specific bind mount.

macOS with Homebrew:

```bash
brew install go git
brew install --cask powershell docker
```

Linux: install the same tools with the distribution package manager; Go must be 1.27.3 or a compatible newer
release. On macOS/Linux invoke scripts with `pwsh -NoLogo -NoProfile -File scripts/<name>.ps1`. `-UseSystemProxy`
reads only the Windows registry; on Unix export `HTTPS_PROXY` and `NO_PROXY` in the shell instead.

Background process lifecycle is portable: `stop.ps1` sends Ctrl+C on Windows and SIGTERM on macOS/Linux;
`-Force` uses the hard-kill path. `run.ps1`, health/port probes, DB maintenance, restart tests and `verify.ps1`
have already passed both Windows and clean Linux `golang:1.27-bookworm` acceptance. macOS uses the same Unix
branches, but no physical Mac acceptance has been performed.

## Quick start (Windows, macOS, Linux — pwsh 7)

```powershell
. .\scripts\Setup.ps1          # PATH: git + GOPROXY/GOCACHE/GOPATH (§3, §11)
.\scripts\env-check.ps1        # §2: green before work (Go/git/pwsh/Docker/network)
git status                     # repo state
.\scripts\strata-check.ps1     # §10 День 3: is STRATA ready? (§6 /model check + a chat probe)
.\scripts\verify.ps1           # lint, unit, fixture maintenance, integration, build, leak scan and tagged -race
                               # -SkipRace = host-only; -SkipHealth = no probe; -WithVolumeRestore = Docker rehearsal
.\scripts\lint.ps1             # §9: gofmt + go vet (also with -tags integration)
.\scripts\test.ps1             # §11 Unit + Store levels (no -race on this host, §3.1)
.\scripts\smoke.ps1 -Unit      # §11 Integration: fake Telegram + fake STRATA, then the leak scan of §12
.\scripts\build.ps1            # §9: go mod tidy + bin\bot.exe + the §7 «empty ENV must exit 1» check
.\scripts\run.ps1              # §9: loads .env (values never printed), builds, runs — Ctrl-C stops it
.\scripts\stop.ps1 -Id <pid>   # §10 День 1: Ctrl+C to a detached instance through its console — the graceful stop
                               # (signal.NotifyContext → pollerWait → db.Close → the WAL checkpoint of §5);
                               # -Force is the hard kill of §10 День 4
.\scripts\db_inspect.ps1       # §9: reads DB_PATH of §7 through mode=ro — chats/messages/events/bot_state
.\scripts\db_backup.ps1        # §9: online VACUUM INTO backup + read-only integrity/schema verification
.\scripts\db_restore.ps1 -Source .\data\backups\<backup>.db
                               # §9: verified restore to a NEW file under zzscratch only; live DB is untouched
.\scripts\db_rotate.ps1 -Keep 7
                               # §9: dry-run retention plan; deletion requires the explicit -Apply flag
.\scripts\db_maintenance.ps1 -Keep 7
                               # §9: scheduler-friendly verified backup + retention; -Apply permits deletion
.\scripts\db_prune.ps1        # §9: dry-run age retention; events 90d, messages disabled; -Apply backs up first
.\scripts\test-maintenance.ps1
                               # §9: fixture backup/restore/rotate/prune acceptance; no live DB access
.\scripts\test-volume-restore.ps1 -Source .\data\backups\<backup>.db
                               # §9: isolated volume-restore rehearsal; no second poller, live project untouched
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
   URL/model/key. Nothing else has to be touched: every remaining row of the 29 has the default of §7.
   `.env` is gitignored and never printed (§12 security).
4. `.\scripts\strata-check.ps1` — is the model reachable at all (§6 `/model` check + one chat request)?
   A red here with the rest green means STRATA, not the code (§13 риск «сервер недоступен»).
5. `.\scripts\verify.ps1` — the complete local pipeline of §9/§11: lint, Unit + Store, Integration with the
   fake Telegram and fake STRATA, the §12 leak scan, the build with its empty-ENV refusal, and the tagged
   `-race` run inside Docker. Every stage is reported even when an earlier one is red; `-SkipRace` runs only
   the host levels, `-SkipHealth` marks the probe of a deliberately stopped bot SKIP rather than probing it.
6. The same levels remain available separately: `lint.ps1`, `test.ps1`, `smoke.ps1 -Unit`, `build.ps1` and
   `test-race.ps1 -Tagged`. On this host `-race` physically requires the container of §3.1, not the Windows Go tool.
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
   The same held port is also how a **second** start is refused: `run.ps1` looks for a listener on
   `HEALTH_PORT` before it even builds, names the PID that owns it and exits 1, because Telegram serves one
   `getUpdates` consumer per token (§3.1, the same invariant `test-restart.ps1` checks under «one poller»);
   `-AllowExisting` opts out. The reason is a trap measured 2026-10-31: starting in the background on top of a
   running bot left the newcomer dead with «bind: Only one usage of each socket address» in its own log, while
   the OLD instance — whose `.env` had been edited under it to another model port — kept answering `/healthz`
   200 and kept polling, so every probe reported a healthy bot that no longer matched the file. A process
   carries the ENV of **its own** start: after editing `.env` the instance has to be restarted. The graceful way
   is `.\scripts\stop.ps1 -Id …`: it posts Ctrl+C to the console of the instance (a hidden instance still owns
   a console), so the process runs its own stop path — `signal.NotifyContext`, `pollerWait`, `db.Close` and the
   WAL checkpoint of §5. Measured 2026-10-31 on the live instance: «stopped by signal true» → «poller stopped
   true» → «stopped by signal (the graceful shutdown of §10 День 1)», the process was gone 0.9 s after the
   event, and `bot.db-wal` of 2 364 912 bytes plus `bot.db-shm` disappeared, leaving only `bot.db` of 73 728.
   `Stop-Process -Id …` is the hard kill of §10 День 4 — survivable through the offset of §5 and
   `uq_messages_chat_tg_id`, which `scripts/test-restart.ps1 -StopExisting` asserts (19 checks, 0 FAIL, walked
   again that same day).
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
    The report also names the handle it read through — the `open mode` line, and the `open_mode`/`wal_side_file`
    keys of `-Counts` — because a WAL file read through the second open (`mode=ro&immutable=1`) answers
    `journal_mode=delete`, which is a fact about the handle and not about the file (§12 reliability). A container
    keeps its database in the named volume, so read the volume as it is:
    `docker run --rm -v tg-llm-proxy_bot-data:/data:ro tg-llm-proxy-test sh -c "go run ./cmd/dbinspect -path /data/bot.db -counts"`.
    That `:ro` mount is exactly the shape the second open exists for: after `docker compose stop` the volume holds
    `bot.db` alone (the WAL of §5 checkpointed), and a plain `mode=ro` answers SQLITE_CANTOPEN on it because the
    `-shm` cannot be created. `docker compose cp bot:/data/bot.db .\zzscratch\bot.db` and then
    `.\scripts\db_inspect.ps1 -Path .\zzscratch\bot.db` still works too.
11. `.\scripts\docker-down.ps1` + `git status` before a commit: `bin/`, `data/`, `*.db*`, `.env`,
    `zzscratch/` are all ignored (§10 День 1); `scripts/smoke.ps1` has already proven no token is in a file.

## Ручной чек-лист демо (§11 «Manual», реальный Telegram + реальный STRATA)

Перед обходом удобно поставить `LOG_LEVEL=debug` в `.env` и рестартнуть бота: тогда на каждый апдейт в JSON-логе
появляется строка «the update arrived» с `update_id` и длиной текста (`internal/bot/run.go`; самого текста в логе
нет — §12 security), и щелчок в Telegram становится сопоставим с конкретной строкой лога. После обхода row
возвращается к `info`: `main_test.go` фиксирует, что на `info` этой строки нет, а ERROR-строки остаются, — то
есть переключение описано тестами, а не догадкой.

- [x] `/start` in a private chat → the greeting + the command list; a second `/start` changes nothing
      (`EnsureChat` of §5 is idempotent). **Walked 2026-10-31** with two real accounts: two chats in §5,
      `events(detail='start')` ×2.
- [x] `/whoami` → the numeric `user_id`, `chat_id` and the line «в allowlist: да/нет»; when it says «нет»,
      the answer names the ENV row to write the id into (`TELEGRAM_ALLOWED_USERS`, §16 change 29) — restart
      after writing, and the refusal of §7 must disappear. **Walked 2026-10-31 весь круг:** the second
      account first got the refusal (`events(kind='denied')` = 1), its id went into the §7 row, the bot
      restarted on the same offset of §5, and its `/ask` answered — no new `denied` row appeared.
- [x] `/skip` → suppress startup recovery of ONLY the newest visible unanswered question, keep its history,
  and leave a newer question recoverable; `/status` must show the explicit suppression. No model call is allowed.
  **Walked on real Telegram:** `/skip` + `/status` confirmed suppression of `row=16`, `bot_state` stored
  `resume_skip_666244095=16`, history stayed intact, and the next `docker compose restart` made no recovery call.
- [ ] `/unskip` → remove only this chat's `resume_skip_` marker, keep history, make the same row eligible for a
  later startup recovery, and make no immediate model call. Unit and fake-Telegram integration are green; the real
  Telegram walk is pending. Do not use the production `row=16` suppression merely as a test fixture.
- [x] `/status` → build commit (`short-sha`, plus `-dirty` for uncommitted sources), model and uptime, the
      streaming flag and its three settings, and whether the newest visible question of THIS chat still waits for
      an answer. The command is allowlist-only and must not call the model.
      **Walked on real Telegram:** the response matched the running configuration (`STREAMING_ENABLED=false`) and
      correctly surfaced the newest pending question of the chat (`row=16`). Build identity was additionally read
      from BOTH the host binary and the container startup log: `3a1cec659893-dirty`.
- [x] `/help`, `/ping` (the `pong <uptime>` grows), `/model`, `/model check` (the real `GET /models` of §14).
      **Walked 2026-10-31:** `help` ×3, `ping` ×64, `model` ×6 in `events.detail`; `strata-check.ps1` on the
      same machine answered `GET /v1/models` 200 and `POST /v1/chat/completions` 200 in 2.18 s.
- [x] 10 messages of `/ask …` in one chat → 10 pairs of rows in `messages` (§5), the answer footer
      `⏱ latency · N→M токенов · модель … · попыток K`, and `/stats` counts the same numbers.
      **Walked 2026-10-31:** 30 `ask` commands → the pairs of §5 (41 rows across two chats), the same
      numbers the viewer prints as `tokens` and `ms` (`db_inspect -Chat`), `stats` ×3.
- [x] two `/ask` in a row from two different chats → both answers arrive, the context of each chat stays
      its own (the per-chat lock and the context builder of §12). **Walked 2026-10-31:** «запомни: моё
      любимое число 42» is recalled in its own chat (`tokens_in` 1400) and unknown in the other («не знаю»,
      `tokens_in` 149), and «О чём мы говорили?» lists only the topics of its own chat.
- [x] `/ask` with a Russian answer longer than `MAX_MESSAGE_CHUNK` → several messages, each ≤ 4096 symbols,
      nothing cut mid-word, no dangling `\` at the end of a chunk (§12 UX, §16 change 7). **Walked
      2026-10-31:** the WAL answer is one row of §5 of 6063 chars (`length(content)`), so ≥ 2 pieces at
      `MAX_MESSAGE_CHUNK=3500` (a second answer: 4334 chars → 2 pieces), and a human saw several messages
      with no visible mid-word cut.
- [x] `STREAMING_ENABLED=true` (§15, §16 changes 34–35) → ONE placeholder, cadenced edits, final MarkdownV2 and
      the tail as ordinary messages; exactly one assistant row of §5 with the whole `content` and no reasoning.
      **Walked on real Telegram + STRATA:** the short answer took 9.037 s (5 edits, no delivery failures). The first
      long runs exposed a real bug: a 900-second stream was cut by the outer 90-second reply context and mislabeled
      as its own total budget; that is fixed with regression tests. A 257.272-second answer (6042 output tokens)
      then generated successfully but sustained one-second edits reached `429 retry_after=120`; the cadence default
      is now 5000 ms and `FloodSleepMax=120 s`. The final fresh answer took **104.872 s**, 2470 output tokens,
      **17 edits, 0 delivery failures and no new 429**; it produced one assistant row (`id=13`), advanced the offset
      to 920551685, and a human confirmed the complete response, continuation and correct formatting. A real idle
      stall remains with `internal/model/stream_test.go`; the SSE shapes and fallback also stay covered by
      `internal/bot/stream_test.go` and `test/integration/TestStreamedAnswerEditsOneMessage`.
- [x] `docker compose restart` in the middle of a conversation → no repeated answers, no duplicated rows
      (§5 `bot_state` + `uq_messages_chat_tg_id`; §10 День 6 proved both in `test/integration`).
      **Walked 2026-10-31 on the host** as three `taskkill /F` between instances: the offset of §5 was read
      back unchanged each time (920551619 → 920551634 → 920551657) and nothing was answered twice; the
      container half was walked the same day with `docker compose stop` (§8.1, exit 0 in 0.5 s).
- [x] point `STRATA_BASE_URL` at a dead port → `/ask` answers `Модель не ответила: <причина> …` and
      `events(kind='model_error')` grows, while `/ping`, `/model` and `/healthz` keep answering — the reliability
      line of the plan is «один сбой STRATA ≠ падение бота» (PLAN «Надёжность»); when the endpoint answers
      again, `/ask` must recover **without** restarting the bot. (The STRATA of §14 lives on another machine
      behind the local proxy of §14 item 7, so stopping that service is not ours to do — which is exactly why
      §11 offers the dead port.) **Walked 2026-10-31 in three phases, and the recovery phase ran on one and the
      same process.** Phase 1 — nothing listening on 127.0.0.1:8099: `/ping` answered `pong 1m17s`, `/healthz`
      stayed 200, `/model check` answered `STRATA не отвечает: Get "http://127.0.0.1:8099/v1/models": dial tcp
      127.0.0.1:8099: connectex: …`, `/ask 2+2` answered `Модель не ответила: Post … (after 2 attempts,
      §10 День 3)`; `events(kind='model_error')` grew 1 → 3, and the row of §5 stayed a lone `user` row — the
      table still shows `id=43 «3+3»` with no answer next to it, which is the «a failed call writes no assistant
      row» invariant of §10 День 3 seen in a live chat. Phase 2 — a scratch endpoint (gitignored `zzscratch/`,
      deleted after the walk) stood on that port and answered `/v1/models` at once but `/v1/chat/completions`
      after 25 s, while `STRATA_TIMEOUT_SECONDS=15`: the plan's exact phrase came out — «Модель не ответила за
      15 с, попробуй ещё раз (§7 STRATA_TIMEOUT_SECONDS)» — and `events(kind='timeout')` appeared with it
      (free TEXT of §5 is why no migration was needed), while `/model check` in the same minute answered
      `Модели (1): stub-model`, i.e. the readiness of §14.2 honestly says «the endpoint is alive» at the moment
      the inference hangs. Phase 3 — the same endpoint started answering at once, and the SAME bot process
      (PID 11664, no restart happened) answered `/ask 5+5`: `latency_ms` 1, exactly one assistant row, 11→12
      tokens, `model=stub-model`. The timeout branch was met on the real service too: a ~6000-symbol WAL
      generation did not fit into `STRATA_TIMEOUT_SECONDS=60` and produced the same phrase with 60 in it — so a
      long answer needs a bigger pair of budgets, and §7 keeps `TG_REPLY_DEADLINE_SECONDS` > `STRATA_TIMEOUT_SECONDS`.
      The two remaining notices of that §12 branch stay walked by `test/integration` only — `model_error/empty_answer`
      (an empty 200) and `model_error/no_room` (a saturated `MAX_CONCURRENT_MODEL_REQUESTS`) need an endpoint that
      lies or a pool of zero slots, and that is what the fakes of §11 are for.
- [ ] flood: `/ask` ~30 times in a few seconds → Telegram's 429 is logged as `retry_after`, the chat waits
      and the answer still arrives (§10 День 4, `events(kind='flood')` grows). Ordinary sequential sends did not
      reach the threshold (~70 pieces of `/history 50`, zero flood rows), but the live streamed walk DID reach it:
      sustained one-second edits produced `retry_after=120`. The old `FloodSleepMax=30` retried too early, received
      another 429 and lost the tail; the cap is now 120 s and the default edit cadence is five seconds. The final
      104.872-second run with those settings completed with zero delivery failures and no new 429; the exact long
      RetryAfter sleep remains open because the new run did not trigger it. `run_test.go` holds the scripted 429
      path: sleep, one retry, `events(kind='flood')`, and no token in the log. The other guard WAS walked:
      `RATE_LIMIT_PER_MINUTE=5` gave five answers and five refusals (`events(kind='limit')` = 5); `/ping` does not
      use the model and therefore is not limited (`middleware.limited` protects the model, not SQLite).
- [x] a stranger writes `/ask` → the refusal of §7 and `events(kind='denied')`; `/start` and `/whoami`
      stay open (§16 change 13). **Walked 2026-10-31:** the second account's `start` and `whoami` produced
      `command` rows while its `ask` produced the `denied` row and the refusal text.
- [x] Ctrl-C / `docker compose down` during a long `/ask` → the process exits, WAL of §5 is checkpointed,
      the next start continues from the stored offset (the graceful stop of §10 День 1, the checkpoint of §5).
      **Measured 2026-10-31 end to end on the real Telegram + the real STRATA** (`zzscratch/container-walk.ps1`,
      the report lands in `data/container-walk-report.txt`). The signal was `docker compose stop bot` (§8.1)
      aimed 10 s into a live generation; the host half of the box had already been measured with
      `scripts/stop.ps1` (an idle process left 0.9 s after `CTRL_C_EVENT`, §3.1). The numbers:
      `docker inspect` gave **exit 0 in 0.6 s** (`FinishedAt 2026-10-31T14:28:08Z`) — the graceful path of
      `main.go`, not the SIGKILL at the end of the stop grace; the log of the cancelled update is
      `ERROR bot_state last_update_id was not stored update_id=920551680 error="context canceled"` plus
      `ERROR dispatch failed command="ask" error="context canceled: the budget of STRATA_TIMEOUT_SECONDS is spent"`,
      then the three stop lines of §10 День 1; the volume held `bot.db` **36864 B alone** — no `-wal`, no `-shm`,
      so the checkpoint of §5 ran inside those 0.6 s; read through the copy of §9, the question of the interrupted
      update was still there (`messages id=5`, `tg_id=448`, `role='user'`, no answer row after it) and
      `last_update_id` had stayed **920551679** while the interrupted update is **920551680** — exactly the window
      §5 was built for: `stored` wrote `role='user'` before the model call (`middleware.go`), `persistOffset`
      never ran (`dispatch.go`). Two traps of the script itself, both fixed in it: listing `/data` needs the
      volume mounted (`docker run -v tg-llm-proxy_bot-data:/data`), otherwise it prints `total 0` and measures
      nothing; and the arrival must be detected by the **row of the question**, not by `events_command` — the
      first attempt latched onto a `/ping`, so the signal hit an idle poller and the real `/ask` came afterwards
      and was answered normally in 4.2 s.
      **What the run refuted is the half of the story this README used to take for granted.** The restart was
      healthy and polling: `WithInitialOffset(920551679)` makes the library ask `offset = lastUpdateID + 1`
      (`get_updates.go` of go-telegram/bot v1.27.0), so the bot demanded 920551680 — and Telegram returned empty
      batches for 9.5 minutes. Those minutes cannot hide a broken poller: `WithErrorsHandler` (`internal/bot/run.go`)
      logs every failed request as `the telegram library failed` on level `error`, and not one such line appeared.
      So the update was **not** handed back, the guard of §2 п.7 never ran, and the question stayed in §5 with no
      answer. Real Telegram drops a batch whose `getUpdates` response was delivered successfully; it re-delivers
      only what never got through (the connection broke mid-poll). The offset of §5 therefore prevents duplicate
      answers, but on its own it does not recover a question cut inside the handler.
      **Closed the same day.** At the start of the process `store.UnansweredQuestions` names the chats whose newest
      VISIBLE row of §5 is a `role='user'` row, and `Dispatcher.ResumeInterruptedQuestions` (`internal/bot/resume.go`,
      started by `main.go` beside the polling goroutine so a slow STRATA cannot delay the first `getUpdates` of §5)
      pushes that stored question back through the same middleware chain: `stored` of §2 п.7 hits
      `uq_messages_chat_tg_id`, the guard sees the row of that question still the newest, and the `/ask` handler of §6
      answers it. Nothing was written twice — the guard decides, the replay only supplies the update Telegram will not
      send. The check is sound precisely because `commands.go` writes the assistant row before sending, so «no answer
      row» means «nothing was ever sent»; the opposite window (row written, send lost) stays silent on purpose, as
      §13 риск «двойная доставка» prefers losing to duplicating. Two conditions the query enforces, both learned from
      §2 п.7: the row must carry a `telegram_message_id` (without it no UNIQUE guard of §5 would make the replay
      idempotent), and only the NEWEST visible row of the chat may be answered (a chat the user asked twice must not
      get its first answer a second time). A scan that finds one says so in the log of §12 — `the stored question of
      §5 has no answer of §6, the start of the process answers it` — and closes with two counters, `answered` and
      `pending`, whose difference the live walk below is about. `TestUnansweredQuestions`
      (`internal/store/messages_test.go`) and the five `TestStartup…` cases of `internal/bot/resume_test.go` hold it:
      the question cut mid-generation, a chat that had already caught up, a chat the user asked twice, the scan
      racing a real redelivery of the same update (the per-chat lock of §12 puts one of them second, the guard
      silences it — one answer, two rows of §5, no second model call), and a replay whose model never answered.
      **The live end was walked twice the same afternoon — at 15:55:28 and at 16:12:03 UTC — and the first of those
      two runs is what found the bug described below.** For the better part of an hour before them the recreated
      container had sat in
      `restart: unless-stopped` on `bot.New: … getMe … context deadline exceeded` (exit 1) while `example.com`
      answered 200 and the registry of Docker Hub its usual 401: only the Telegram side of the route was down, and
      `bot.New` of §10 День 4 runs before the scan of §15, so there was nothing of §15 to watch. The first container that
      got through printed `the stored question of §5 has no answer of §6, the start of the process answers it`
      (`chat=666244095 row=6 tg_id=449 stored_at 14:57:47` — the same chat the read-only `docker cp` copy of §9 had
      named through `zzscratch/resumescan`, which had likewise left the older `row=5 tg_id=448` of the same text
      alone: the newest-visible-row rule of §2 п.7 working on real data), then the guard of §2 п.7
      (`the question of this update has no answer of §6 yet, answering it`), then `the model did not answer` at
      15:56:28 when the whole `STRATA_TIMEOUT_SECONDS 60s` was spent — and, the bug, `the start of the process
      answered 1 stored question(s) …`. The second container, on the fixed counting, printed the same three lines at
      16:12:03 and then `the replay of the start of the process left the stored question without an answer of §6,
      the next start tries it again` (WARN, `newest_row=6 newest_role=user`) plus the summary that names both
      counters: `the start of the process of §15 answered 0 stored question(s) of §5 that had no answer of §6, left
      1 still without an answer (the next start of the process tries them again)`. The volume after both runs
      (`db_inspect.ps1` of §9 over a second copy): `command` still five rows, `timeout` now three (14:58:47 the run
      that was interrupted, 15:56:28 and 16:13:03 the two replays), `last_update_id` still 920551681, and `messages`
      still ending at `row=6` — no second question row, no assistant row for an answer that never happened, no
      second `ask` event, the offset unmoved. The replay supplies the update Telegram will not send; §5 decides the
      rest, exactly as the §11 tests say.
      What the first of those runs exposed was a lie in the log, and it was the counting: the process claimed an
      answer for a question nobody had answered, because the `/ask` handler of §6 finishes a STRATA failure itself —
      the events row of §10 День 3 and the notice to the user — and returns nil to the chain, so the chain cannot
      see the failure. `ResumeInterruptedQuestions` therefore takes both counters from §5, through
      `store.LastVisibleMessage`, the same read the guard of §2 п.7 decides with: an `assistant` row with `id >` the
      question row is the only proof that the user was finally answered, and the poller running beside this loop must
      not be able to fake that proof with a NEW question of the same chat.
      `TestStartupCountsAQuestionTheModelCouldNotAnswer` holds it — a failed replay leaves the question in §5, one
      `timeout` event, one notice to the user, `answered=0 pending=1`.
      And here is why this particular question keeps failing, measured through `zzscratch/asktime` against the same
      STRATA, the question read out of the §9 copy, a long budget: **1m55.99s, 6034 runes, 140 → 7248 tokens**. It
      asks for «не менее 20 пунктов, каждый с пояснением в 3 предложения, плюс в конце вывод на 10 предложений», so
      `STRATA_TIMEOUT_SECONDS 60` of §7 was never going to cover it: the replay of §15 works, the budget of the model
      does not fit the answer, and that is the same wall the §15 «стриминг ответов» item runs into from the other
      side. At that measurement `row=6` was the newest unanswered row and each start tried it again. No age cap on
      a replayed question is deliberately in v1 — §12 promises an answer to a question the user never got, and an
      age cap would break that promise silently — but the cost was one model call per pending question per start.
      **Decision kept: `STRATA_TIMEOUT_SECONDS` stays 60 and the scan gets no age cap.** The §15 stream now gives
      long answers their own budget. After the later live questions and the assistant row `id=13`, older `row=6` and
      `row=9` are no longer selected: the scan intentionally considers only the newest visible row of a chat, so it
      does not resurrect questions the user has already moved past. They remain in history; no silent deletion or TTL
      was introduced. `MAX_CONCURRENT_MODEL_REQUESTS` stays occupied for the whole stream (§7, §12).
      The guard itself stays as fixed on 2026-10-31 — it is what makes any redelivery that *does* happen safe, and
      `TestRedeliveredUnansweredQuestionIsAnswered` / `TestRedeliveredAnsweredQuestionStaysSilentWhenTheChatMovedOn`
      hold both directions of it (§10 День 6 note below).

Две ловушки обхода 2026-10-31, обе про команды §6: очистка контекста называется `/reset` (`store.ClearChat`
прячет контекст, строки §5 остаются — `/history` их всё ещё видит), а не `/forget`; и с
`PROXY_FREE_TEXT=true` неизвестная команда не получает отказа, а уходит модели — посланный `/forget` дал
галлюцинацию «Context cleared. How can I help?» и контекст при этом очищен не был.

### Что именно считает `/stats` (исправлено 2026-10-31, §6)

Прогон §11 про модель показал три места, где числа `/stats` и `events` говорили не то, что о них думают. Все три
исправлены и закрыты тестами; ниже — что именно теперь значит каждая строка.

- **«Модель: …» — модель последнего ответа этого чата.** Раньше запрос брал `MAX(model)`, то есть лексикографически
  наибольшее имя среди всех строк чата: чат, который сначала обслуживал `qwen3.8-flash-next-nvfp4-nvfp4-nvidia`,
  а позже `qwen3.8-flash-next-iq3_s`, показывал первое имя вечно (`n` сортируется позже `i`), и оператор читал это
  как «сейчас мне отвечает вот эта модель». Живое расхождение было видно глазами: `.env` говорит `iq3_s`, зритель §5
  по строкам чата печатает `iq3_s`, а `/stats` отвечал `…nvfp4-nvfp4-nvidia`. `TestStatsModelIsTheLastAnswer`
  раскладывает имена так, чтобы хронологический и лексикографический ответы не совпадали;
  `TestStatsModelEmptyBeforeAnyAnswer` — что пока модель ни на что не ответила, строки нет вовсе (§10 День 3).
- **«Ошибок модели» и «Таймаутов модели» — два разных числа,** потому что §5 хранит два разных `kind`. До правки
  таймаут не попадал ни в один счётчик: сразу после ответа «Модель не ответила за 15 с, попробуй ещё раз» `/stats`
  продолжал показывать «Ошибок модели: 3» со вчерашним значением, что читается как «ничего не случилось». Строка
  про таймауты появляется только когда они есть; `TestStatsCountsTimeoutsApartFromErrors` держит и разделение
  счётчиков, и то, что `kind` которого `/stats` не считает (`flood`), чтению не мешает.
- **`events.detail` строки `timeout` называет тот бюджет, который истёк, и тот же номер видит пользователь:**
  `STRATA_TIMEOUT_SECONDS 15s spent` или `TG_REPLY_DEADLINE_SECONDS 30s spent`. Раньше событие писало `budget 30s`
  (бюджет ответа), когда человеку говорили «за 15 с» — одно событие и два разных числа в двух местах, которые
  оператор сравнивает. Текст пользователю теперь цитирует имя того же бюджета.
- **Аудит упавшего вызова пишется на отвязанном контексте** (`auditBudget` = 3 с, `context.WithoutCancel`): строка
  `timeout` выжила в этом прогоне только потому, что бюджет модели (15 с) меньше бюджета ответа (30 с) — при
  обратном соотношении истёкший `replyCtx` унёс бы её молча, ровно в том случае, ради которого §12 обещает
  «один сбой STRATA ≠ падение бота». Ошибку записи `logEvent` пишет в лог, а не прячет.

Отдельно про бюджет длинных ответов: **реальная STRATA не уложила ~6000-символьный ответ в
`STRATA_TIMEOUT_SECONDS=60`** — тот же текст «Модель не ответила за 60 с …» случился в живом чате сам собой. Для
длинных генераций пару бюджетов надо поднимать (например 120/150), помня инвариант §7:
`TG_REPLY_DEADLINE_SECONDS` строго больше `STRATA_TIMEOUT_SECONDS`.

### Часовой пояс: `created_at` — это UTC (§5, §13)

`messages.created_at` and `chats.last_active_at` are written by SQLite as `datetime('now')`, i.e. **UTC**
without a timezone marker, and `store` returns the text exactly as it is stored — `/history` and `/stats`
therefore show UTC too. The container has `tzdata` (§8.1) so that a reader can convert deliberately
(`docker compose exec bot date`), and the JSON log of §12 carries its own RFC3339Nano timestamp, which is
the machine-readable half of the same story: a log line and a DB row can be lined up only in UTC.

## ENV (§7) — see `.env.example` (full table in PLAN.md §7)

The loader `internal/config` (imported by `cmd/bot` as `bot/internal/config`) reads
the process ENV once and accepts **exactly the rows of the §7 table — 29 names since
§16 change 34 added the four streaming rows** — a name
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
**the** row of §7 (still 29 names), `UserIDs` reads that one name, and the old `ALLOWED_USER_IDS` left the
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
The four rows of §16 change 34 (`STREAMING_ENABLED`, `STREAM_EDIT_INTERVAL_MS`,
`STREAM_IDLE_TIMEOUT_SECONDS`, `STREAM_TOTAL_DEADLINE_SECONDS`) belong to the budgets, not to the looks: the flag
defaults to `false` because a streamed answer rewrites ONE message and therefore changes how every answer of
§12 UX looks, so an operator turns it on deliberately. `STREAM_EDIT_INTERVAL_MS` defaults to **5000** after the
live Telegram walk: a sustained one-second cadence reached `429 retry_after=120`. The streamed model call gets
`STREAM_TOTAL_DEADLINE_SECONDS`, with `TG_REPLY_DEADLINE_SECONDS` left around it for the context read, SQLite
and delivery; the outer 90-second reply context no longer cuts a 300/900-second stream and misreports its budget.
The loader refuses two combinations with a message that names both rows and their numbers:
`STREAM_TOTAL_DEADLINE_SECONDS` ≤ `STREAM_IDLE_TIMEOUT_SECONDS` (a stall detector may not outlive the answer it
watches), and with the flag on `TG_REPLY_DEADLINE_SECONDS` ≤ `STREAM_IDLE_TIMEOUT_SECONDS` — the answer must
START arriving inside the reply budget, which is the promise «сообщение не зависает» of §12 UX. Verified by the
loader tests of §11 (`internal/config`); the streaming behaviour itself by `internal/bot/stream_test.go`.

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
  counter, `Replay`), `test/integration/run_test.go` (`//go:build integration`, 10 cases),
  `scripts/smoke.ps1`, and `scripts/test-race.ps1 -Tagged` green: `go vet` + `gofmt` + `go test -race
  -tags integration ./... -count=2` inside the §8.1 test image — no data races (§11 Concurrency,
  §10 День 6 DoD). The race detector earned its keep once: the integration harness had been polling a
  plain `bytes.Buffer` of the run log while the library answers every update in its own goroutine
  (`ProcessUpdate` → `go r(ctx, b, upd)` in v1.27.0) — the sink is now `syncLog` (mutex-guarded), which is
  a test-only fix, production hands slog an `os.Stdout`.
  Idempotency of §10 День 6 is proved on both levels: `TestOffsetSurvivesRestart` (the offset of §5 means
  the update is not replayed) and `TestRedeliveredUpdateIsAnsweredOnce` (the crash *before* `bot_state`
  was written: the fake of §11 replays the same update, `store.Append` hits `uq_messages_chat_tg_id`, the answer is
  silence — no second `sendMessage`, no second model call, one extra log line).
  Corrected 2026-10-31, while the §11 item «Ctrl-C / `docker compose down` при длинном `/ask`» was being
  prepared: «a row of this update exists» and «this question was answered» are two different facts, because
  `stored` writes `role='user'` BEFORE the model call (`middleware.go`) while the offset of §5 is
  acknowledged only after the handler (`dispatch.go` → `persistOffset`). A process stopped in the middle of a
  generation leaves the question stored and unanswered; whenever that update comes back — the fake of §11 replays
  it, and the real Bot API re-delivers a batch only when its response never arrived, which the live container walk
  of 2026-10-31 did **not** observe after a stop inside the handler (§11 manual, 9.5 minutes of empty batches) —
  the guard read that redelivery as «already answered» and stayed silent: the user got nothing, and nothing in
  `events` of §5 said why either. The guard now asks the database about **this** update: `store.RowIDByTelegramMessageID`
  locates the row it left in §5, `store.LastVisibleMessage` names the newest row inside the same window
  `RecentByChat` glues (so the rows a `/reset` of §6 hid do not decide the question), and the question is still
  waiting exactly when those two are the same row — the answer of §6 is always the next row of the chat
  (`commands.go` writes it before sending, §10 День 3 «ровно одна запись assistant»). Comparing the ROLE alone
  would be a new bug: when the user asked twice, the chat ends with the row of the second question and the
  answer the first one already got looks missing, so the old question would be answered a second time — the
  §13 risk «повторные ответы» this middleware exists to prevent. Proven by running the suite against that
  naive variant: `TestRedeliveredAnsweredQuestionStaysSilentWhenTheChatMovedOn` fails with «the redelivered
  answered question must not reach the model again, got 3 calls». Three tests hold the line:
  `TestDuplicateUpdateAnswersOnce` (the answered case stays silent),
  `TestRedeliveredUnansweredQuestionIsAnswered` (the interrupted case reaches the model exactly once more; it
  fails on the old guard with «the redelivered question must reach the model … got 1 calls») and
  `TestRedeliveredAnsweredQuestionStaysSilentWhenTheChatMovedOn` (the two-questions case). One window stays
  open by design: the answer is stored before it is sent, so a stop between those two writes loses the
  message while §5 keeps the answer — `/history` of §6 shows it, and the guard stays silent.
  Closed the same day for the direction that used to be lost: since the live walk showed that no redelivery is
  coming (`§11 manual`), `store.UnansweredQuestions` + `Dispatcher.ResumeInterruptedQuestions`
  (`internal/bot/resume.go`, started by `main.go` beside the polling goroutine) feed the stored question back
  into the SAME chain, so this guard — not a second copy of the answering code — decides. `TestStartup…` in
  `internal/bot/resume_test.go` holds the five directions of it: the interrupted question is answered once at
  the start of a new process, an answered chat stays silent, a twice-asked chat is answered only for its newest
  question, the scan racing a real redelivery of the same update still produces exactly one answer — the
  per-chat lock of §12 puts one of them second, and the guard silences it — and a replay whose model never
  answered leaves §5 exactly as it found it and reports `pending=1` rather than pretending otherwise, which is
  the case the live container of §8.1 caught the first version lying about (§11 note above).
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
  is now the row of §7 (`Env` still holds 29 names, `UserIDs`/`RowIsSet` read exactly that one), and
  `ALLOWED_USER_IDS` is gone from the loader — `Renamed` refuses it by name and the start exits 1 pointing at
  the new spelling rather than ignoring it (§7: the loader reads exactly the names of the table). `/whoami`,
  the refusal texts and `bot.AllowlistHint` name the single row (§12 UX). Covered by
  `TestRowOfTheAllowlistIsTelegramsName`, `TestOldNameOfTheRowIsRefusedByName`,
  `TestAllowlistCountsADuplicateOnce`, `TestAllowlistNamesTheRowOfABadEntry` and `TestAllowlistRowOpensTheBot`
  (the last one asserts the user reaches the model and `events(kind='denied')` stays empty).
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
  header: repeated answers are the unique index of §5 plus the integration tests, and the SIGTERM case (the
  graceful stop of §10 День 1) belongs to `docker compose stop` of §8.1, because Windows has no signal for a
  detached process (§3.1).
  **The container path of §8.1 was walked the same day, and with it that SIGTERM half.**
  `.\scripts\docker-build.ps1` + `.\scripts\docker-up.ps1`: the container reached `healthy`, and its log said
  «bot is polling Telegram, bot_state last_update_id 0 found false» — inside the container Telegram needs no
  proxy at all (the §2 workaround is an affair of the Windows host), and `host.docker.internal:8080` of §8.1
  answers 401 to a `wget` sent without the key, which is the proof that the network path exists.
  `docker compose stop` then gave the graceful death: **exit 0 in 0.5 s** (Docker's default 10 s stop grace was
  never needed — `docker-compose.yml` sets no `stop_grace_period` of its own), `restart=0` under
  `restart: unless-stopped`, and the last three lines of the log being
  «stopped by signal true», «poller stopped true» (§5: `bot_state` lands before `db.Close` checkpoints the WAL)
  and «stopped by signal» — the line the graceful-shutdown requirement of §10 День 1 produces. After that the
  volume held `bot.db` alone — no `-wal`, no `-shm`,
  which is the checkpoint of §5 — and the next start resumed on the state that volume carried (a volume that has
  never answered anyone carries no offset row, and the §9 viewer says so: «no poll offset stored yet»).
  Two defects fell out of the walk, and both are fixed (§16 change 31):
  **(1)** the viewer of §9 could not read such a volume at all: `mode=ro` on a WAL file whose `-shm` cannot be
  created answers SQLITE_CANTOPEN (14) although the file is plainly readable. `internal/store` now has a second
  open — `store.OpenReadOnlySnapshot` over `mode=ro&immutable=1` — and it offers it ONLY when no `-wal` sits next
  to the file: with no `-wal` the main file IS the whole state of §5, while with a `-wal` an immutable read would
  hide committed pages, and that case is refused with its own message. Every number names the handle it came
  through (`open_mode=mode_ro_immutable`, `wal_side_file=absent`, the `open mode` line of the human report),
  because through that handle `PRAGMA journal_mode` answers `delete` even for a WAL file — measured in the
  container and asserted by `TestImmutableReaderSeesTheSameNumbersAndCallsJournalModeDelete`.
  **(2)** `scripts/docker-build.ps1` promised both Dockerfile targets and built one: the `test` service of §9
  hides behind `profiles: ["test"]`, which `docker compose build` skips, so after a source edit the `-race` image
  stayed four hours old and the reader inside it printed yesterday's code. The script builds the profile too and
  prints the age of both images.

## Backup and verified scratch restore (§9)

For a host-side database, the online backup does not require stopping the bot:

```powershell
.\scripts\db_backup.ps1 -Source .\data\bot.db -Dest .\zzscratch\backup.db
.\scripts\db_inspect.ps1 -Path .\zzscratch\backup.db -Counts
.\scripts\db_restore.ps1 -Source .\zzscratch\backup.db -Dest .\zzscratch\restored.db
.\scripts\db_inspect.ps1 -Path .\zzscratch\restored.db -Counts
```

`db_backup` and `db_restore` both reject existing destinations. Restore first verifies the source read-only
(`PRAGMA integrity_check` plus the required §5 tables), materializes a new file through SQLite `VACUUM INTO`, and
verifies that file again. Its PowerShell wrapper accepts destinations only below `zzscratch`; it cannot overwrite
`data/bot.db` or the Docker volume. Compare `table_*`, `events_*`, `last_update_id`, and `integrity` in the two
`-Counts` reports, not file hashes: `VACUUM INTO` preserves the data but may change the physical page layout.
The same report includes `schema_version`. Pre-versioning databases and backups report 0; on the next writable
`store.Open`, migration 1 adopts them transactionally into `schema_migrations` without changing application rows.
Migration history is append-only: deployed SQL entries must not be edited.

Retention is explicit and dry-run first:

```powershell
.\scripts\db_rotate.ps1 -Keep 7          # print the plan; delete nothing
.\scripts\db_rotate.ps1 -Keep 7 -Apply   # delete only the expired files listed by that plan
```

Rotation considers only regular files named `bot-YYYYMMDD-HHMMSS.db` in `data/backups` (or an explicitly supplied
scratch directory), always retains at least one copy, and ignores manual names such as `backup.db`. `-Apply` is an
irreversible deletion of the listed old backups.

For a scheduler-ready backup plus retention step, use:

```powershell
.\scripts\db_maintenance.ps1 -Keep 7          # creates a backup; retention is still dry-run
.\scripts\db_maintenance.ps1 -Keep 7 -Apply   # creates a backup and deletes the listed expired copies
```

In Task Scheduler select program `pwsh`, set the repository as «Start in», and use arguments such as
`-NoLogo -NoProfile -File "C:\Kimi code\tg-llm-proxy\scripts\db_maintenance.ps1" -Keep 7`. Test the command manually
first; add `-Apply` only after reviewing the dry-run. The project does not register or modify any scheduled task.

Age-based deletion of rows is a separate operation from backup-file rotation:

```powershell
.\scripts\db_prune.ps1                                      # dry-run: events older than 90 days, no messages
.\scripts\db_prune.ps1 -EventsDays 90 -Apply                # verified pre-prune backup, then delete old events
.\scripts\db_prune.ps1 -EventsDays 0 -MessagesDays 365      # dry-run explicit message retention
.\scripts\db_prune.ps1 -EventsDays 0 -MessagesDays 365 -Apply
```

Message deletion is disabled unless `-MessagesDays` is explicitly positive. Every `-Apply` creates a new backup and
verifies it before opening the source for writing, then runs `integrity_check` afterwards. No scheduler invokes
`db_prune`; production history deletion remains an explicit operator decision.

For the current Docker named volume, take the intermediate file only after a clean stop so the WAL is checkpointed:

```powershell
docker compose stop bot
docker compose cp bot:/data/bot.db .\zzscratch\volume-bot.db
docker compose up -d
.\scripts\db_backup.ps1 -Source .\zzscratch\volume-bot.db -Dest .\zzscratch\volume-backup.db
```

### Production volume restore and rollback (manual runbook)

Production replacement is intentionally not automated and requires the operator's explicit decision. First rehearse
the exact backup in the isolated compose project; this never starts a second Telegram poller and removes its own
project, volume, and scratch files afterwards:

```powershell
.\scripts\test-volume-restore.ps1 -Source .\data\backups\bot-YYYYMMDD-HHMMSS.db
.\scripts\db_restore.ps1 -Source .\data\backups\bot-YYYYMMDD-HHMMSS.db -Dest .\zzscratch\production-restore.db
```

For the real restore, choose new host-side snapshot names and do not delete the pre-restore snapshot until the bot
has been accepted. The following commands are the manual runbook, not an operation performed by `db_restore`:

```powershell
docker compose stop bot
docker compose cp bot:/data/bot.db .\zzscratch\pre-restore.db
docker compose cp .\zzscratch\production-restore.db bot:/data/bot.db
docker compose run --rm --no-deps --user root --entrypoint chown bot 65534:65534 /data/bot.db
docker compose run --rm --no-deps --entrypoint sh bot -c 'test -r /data/bot.db && test -w /data/bot.db'
docker compose cp bot:/data/bot.db .\zzscratch\post-restore.db
.\scripts\db_inspect.ps1 -Path .\zzscratch\post-restore.db -Counts
docker compose up -d
docker compose ps
```

The clean stop checkpoints the WAL before `pre-restore.db` is copied. `chown` is required because a host-side copy
can otherwise arrive root-owned while the runtime runs as uid/gid 65534. Compare the post-restore counts with the
verified backup before `up -d`. If anything fails, roll back while the bot is stopped, then repeat the permission
check and inspect the log before accepting:

```powershell
docker compose stop bot
docker compose cp .\zzscratch\pre-restore.db bot:/data/bot.db
docker compose run --rm --no-deps --user root --entrypoint chown bot 65534:65534 /data/bot.db
docker compose up -d
```

Never use `docker compose down --volumes` for this runbook: it deletes the very history the restore is meant to save.

## Optional webhook transport (§15)

Long polling remains the default. Webhook mode is an opt-in for an existing HTTPS reverse proxy or tunnel:

```env
WEBHOOK_ENABLED=true
WEBHOOK_PUBLIC_URL=https://bot.example.com/telegram/webhook
WEBHOOK_SECRET_TOKEN=<a-random-token-with-16-to-256-ascii-characters>
WEBHOOK_LISTEN_HOST=127.0.0.1
WEBHOOK_PORT=8082
WEBHOOK_PATH=/telegram/webhook
```

The proxy forwards `https://bot.example.com/telegram/webhook` to `http://127.0.0.1:8082/telegram/webhook`; the two
paths must match exactly. The secret travels only in `X-Telegram-Bot-Api-Secret-Token` and is logged by length.
The listener accepts POST only, limits bodies to 1 MiB, and does not expose TLS itself. In compose it listens on
`0.0.0.0:8082`, while the published host endpoint stays `127.0.0.1:8082` (`WEBHOOK_HOST_PORT` changes the host side).
Do not publish the listener directly to the internet.

Startup binds the local listener before `setWebhook`, registers only `message` updates without dropping pending
updates, and never starts `getUpdates` in webhook mode. Switching back to polling requires Telegram `deleteWebhook`
first; the project does not delete a webhook automatically. The fake integration proves registration, secret
rejection, POST delivery and offset persistence; live HTTPS acceptance needs a real public URL and tunnel.

## Current limits (honest list)

The v1 path is implemented: configuration, SQLite, the STRATA client, commands and middleware, integration and
race tests, Docker, health, read-only DB inspection, process-level kill/restart, and graceful stop both in Docker
and on Windows (`scripts/stop.ps1` delivers Ctrl+C to a hidden console). `scripts/verify.ps1` is the single entry
point for the automated levels; the manual checklist above remains the authority for real Telegram behaviour.

- Webhook transport is implemented as opt-in but has no live HTTPS acceptance yet; production remains on polling.
- Streaming is implemented but stays opt-in (`STREAMING_ENABLED=false`). Its live checks and the budgets measured
  there are recorded in the checklist and §15 below; fakes alone cannot prove Telegram's edit/flood limits.
- Ordinary sequential sends did not reproduce Telegram 429, but sustained one-second stream edits did produce
  `retry_after=120`. The default edit cadence is now five seconds and `FloodSleepMax` is 120 seconds.
- Startup recovery still has no age cap and handles the newest unanswered visible question of a chat per start.
  A stored assistant row is not replayed when only Telegram delivery failed, because a second send may duplicate it.
  `/skip` explicitly suppresses exactly the current question row in `bot_state` without deleting history; a newer
  question does not inherit that suppression, `/status` shows it, and `/unskip` removes the marker without an
  immediate model call. Measured on `row=16` («2000+ слов»): even a
  900-second stream ended with 4225 runes delivered, 97 edits and zero delivery failures, but no finished answer
  and therefore no assistant row. The operator chose to leave it pending rather than occupy the GPU for 3600 seconds,
  then applied `/skip`: the row remains pending in history, but the next restart made no recovery call.
- Backups and scratch restores use SQLite `VACUUM INTO`, read-only integrity/schema checks, and no-overwrite
  destinations. Backup-file rotation, backup-before-delete age retention, scheduler-ready maintenance, and the
  isolated volume-restore rehearsal are implemented. Task Scheduler registration and production history deletion
  stay explicit operator actions; production replacement still requires the manual runbook above and approval.
- `/reset` (not `/forget`) clears context without deleting history. Secrets print only their length, library errors
  go through `bot.Sanitize`, and `scripts/smoke.ps1` scans the tree for the token value (§12 security).

## Бэклог v2 (§15, «запись v2-бэклога» of §10 День 7)

Not a todo list for v1 — these are the things §15 deliberately left out of the prototype, plus what this
implementation made concrete enough to name.

- Стриминг ответов: правка одного сообщения по настраиваемой cadence вместо одного `sendMessage`. **Сделано досрочно —
  §16 change 34** (2026-10-31): четыре строки §7 (`STREAMING_ENABLED=false` по умолчанию,
  `STREAM_EDIT_INTERVAL_MS=5000`, `STREAM_IDLE_TIMEOUT_SECONDS=30`, `STREAM_TOTAL_DEADLINE_SECONDS=300`),
  `model.Streamer` рядом с `model.Client`, `internal/model/stream.go` (SSE, два бюджета, `reasoning_content`
  отдельно) и `internal/bot/stream.go` (сессия ответа), `run.go` как `StreamSender`, `chunksOf` из `reply.go`
  для обеих дорожек. Замер 2026-10-31 через `zzscratch/asktime` — тот же STRATA и тот же вопрос, который
  догоняет §15: **1m55.99s, 6034 runes, 140 → 7248 tokens**. В `STRATA_TIMEOUT_SECONDS 60` такой ответ не
  влезает ни при каком числе повторов (§10 День 3: кончился бюджет — повторов уже нет), поэтому у потока свои
  два бюджета: пауза между SSE-событиями и потолок всего ответа. **Ручной §11 пройден** (чек-лист выше):
  104.872 s, 17 edits, zero delivery failures, полный ответ и разметка подтверждены человеком. Старые `row=6`/`row=9`
  не догоняются после более нового ответа: startup scan намеренно смотрит только на самую новую visible row чата,
  чтобы не воскрешать перекрытые вопросы; возрастного TTL или автоматического удаления по-прежнему нет.
- Живой HTTPS acceptance webhook и автоматический `deleteWebhook` при возврате к polling; сам opt-in transport,
  `setWebhook`, secret validation и fake POST integration уже реализованы (§16 change 52).
- Команды `/image`, `/voice`, вложения и файлы (§15) — the registry of §6 takes a new entry without
  touching the dispatcher, that is what `UsesModel`/`AlwaysAllowed` are for.
- Пресеты системного промпта (`/persona`) и выбор модели (`/model select`); инструменты/функции модели и
  RAG поверх локальных документов.
- `/admin`: сводка по всем чатам, очередь и приоритеты запросов (v1 counts one chat, §6 `/stats`).
- Автоматическая регистрация расписания; версионированные миграции уже используют `schema_migrations`,
  ручной backup, проверенный scratch-restore, dry-run/apply retention и scheduler-ready maintenance реализованы (§9).
- Публикация образа в registry; `.github/workflows/verify.yml` и `.gitlab-ci.yml` описывают host checks и Docker
  race, а локальный `scripts/verify.ps1` остаётся тем же pipeline. Release CI использует GitLab.
- `Config.Get()` — overrides per chat / per `user_id` (§16 change 25.1 kept the loader at the 29 rows of §7;
  the first consumer would have been a per-chat `PARSE_MODE`, which v1 does not need).
- Time budgets and TTLs as their own module: the limits of §7 live next to the loader in
  `internal/config/limits.go`; the TTL of the `bot_state` rows and a per-request budget bookkeeping move to v2.
- The v1.0 field names (`LLM_CHAT_*`, the token budgets, the deep links, `PORT`, `DB_DBPATH`, …) stay out of
  the loader until a consumer exists — §16 change 25.1 says an ENV row without a reader is a liability.

