# tg-llm-proxy

Telegram-бот-прокси между пользователями и локальной OpenAI-compatible STRATA. Основной транспорт — long polling; Mini App и стриминг ответов — opt-in. Здесь хранятся заметки о выпусках: v1.0.0–v1.56.0 разложены по диапазонам в `CHANGELOG/`, новейший выпуск — ниже.

## Новейший выпуск: v1.62.2

# tg-llm-proxy v1.62.2

## Regression fix: task creation, stored XML and streamed markup

- Task planning now uses the task budgets too (`TASK_MODEL_TIMEOUT_SECONDS` / `TASK_STEP_DEADLINE_SECONDS`), so creating a long task no longer fails with `the budget of STRATA_TIMEOUT_SECONDS is spent`.
- Raw tool-call markup is stripped before it is written to the message store, before the Mini App history/API returns a row, and before stored rows enter the model context; old answers that contain the markup no longer reappear in the chat or poison the next turn.
- A streamed textual tool call is held back by a stream guard and parsed after the stream ends, so XML never flashes in the Mini App; ordinary text (including a literal `<`) still streams normally.
- An invented textual tool (`web_extractor`, etc.) is dropped instead of failing the whole answer: the model's remaining text is returned, or a localized «не удалось вызвать инструмент» when nothing is left.

## Предыдущий выпуск: v1.62.1

# tg-llm-proxy v1.62.1

## Textual tool calls are executed instead of being shown

- Some local-model answers write the tool call as text (`<tool_call><function=web_search><parameter=query>…`) instead of the native `tool_calls` field. The tool loop now parses that documented markup, turns it into real calls and runs them through the SAME allowlist, normalisation and approval path, so the raw XML never reaches the chat.
- If the markup cannot be parsed, the model gets one corrective instruction to use native calls; after that the markup is stripped and, when nothing remains, the chat shows a localized «не удалось вызвать инструмент» instead of XML. The shared answer finalizer and task step/synthesis also strip any leftover markup.
- Tests cover the exact screenshot shape (two web_search calls), text around the calls, malformed/unclosed markup with the corrective retry, execution of a parsed calculator call and the finalizer guard.

## Предыдущий выпуск: v1.62.0

# tg-llm-proxy v1.62.0

## Task steps get their own model and step budgets

- A long task that searches the web no longer dies with `the budget of STRATA_TIMEOUT_SECONDS is spent`: every task step (and the final synthesis) asks the model client for `TASK_MODEL_TIMEOUT_SECONDS` (default 240 s) instead of the chat's `STRATA_TIMEOUT_SECONDS` (60 s), and the whole step, including several tool rounds, is bounded by `TASK_STEP_DEADLINE_SECONDS` (default 300 s). The task lease follows the step deadline (+30 s).
- Private chats are unchanged: the chat answer still uses `STRATA_TIMEOUT_SECONDS` + `TG_REPLY_DEADLINE_SECONDS`. The override lives in `model.Request.Timeout` and is still capped by the caller's context deadline.
- Config: two new rows (75 total), validation `TASK_MODEL_TIMEOUT_SECONDS < TASK_STEP_DEADLINE_SECONDS`, startup summary prints both; `.env`/README updated. A failed task can be resumed after the restart (`/task resume <id>` or the «Возобновить» button).

## Предыдущий выпуск: v1.61.2

# tg-llm-proxy v1.61.2

## English reminder grammar and 44 px secondary controls

- `/remind` now parses English as well as Russian in any chat: `in N minute(s)/hour(s)/day(s)` and `every day HH:MM` / `daily at HH:MM` / `every day at HH:MM`, case-insensitive, with the existing timezone and text-extraction rules. Rejections are explicit (`in 0 minutes`, `in 2 weeks`, `every day 25:00`, `daily 09:00`); the RU grammar and its tests are unchanged.
- Every interactive Mini App control now has a 44×44 px touch target: header menu toggle, generic/secondary buttons, logout/refresh, pagination, language select, inputs/selects/textarea, chat Reset/Skip, menu-panel tabs, document upload, standalone item links and settings/task action rows. Inline links inside prose remain exempt (WCAG 2.5.5 inline exception) and are documented in the regression test. Layout was checked statically at 320/360 px; no horizontal overflow path.

## Предыдущий выпуск: v1.61.1

# tg-llm-proxy v1.61.1

## Security headers, accessibility and the last RU strings

- Mini App responses now carry `Cross-Origin-Opener-Policy: same-origin` and `Cross-Origin-Resource-Policy: same-origin` always, and `Strict-Transport-Security: max-age=31536000; includeSubDomains` only when the request is TLS or the proxy forwards `X-Forwarded-Proto: https` (never on plain HTTP). Covered by plain-HTTP/forwarded-HTTP/forwarded-HTTPS/TLS tests.
- Accessibility: tabs and chat feedback buttons are at least 44×44 px, and `#content` lost its extra `aria-live` so the chat log is the single live region (no double announcements). Layout on 320 px is intact.
- i18n: the last user-facing RU strings moved to the catalog — dispatch overloaded/busy/question-not-stored notices, middleware panic/too-often and access refusals, the `/ask` network-approval prompt with its buttons and control replies, and the recovery notice. Catalog is 462 keys per language with parity; RU remains the default and Mini App/prompts/logs stay language-neutral.

## Предыдущий выпуск: v1.61.0

# tg-llm-proxy v1.61.0

## Private metrics, complete RU/EN command surface and the Postgres ADR

- Metrics no longer carry chat/user labels: `queue_depth` is an aggregate gauge and `busy_chats` was added, so the registry cannot grow with chats or leak who is talking. A test dumps `/metrics` while a chat is queued and asserts no `chat_id`/`user_id` labels.
- `/metrics` is isolated: a separate listener binds only the bot's static `edge-monitoring` address (Prometheus still scrapes `bot:8081`), the Docker HEALTHCHECK uses a separate `HEALTH_PROBE_PORT=8080` listener that serves only `/healthz`/`/readyz`, and the host publication `127.0.0.1:8081` now returns 404 for `/metrics`. Containers on `default`/`edge-telegram` cannot reach the metrics socket (verified with curl exit 7). The gateway dashboard panel became "Queue depth (total)".
- i18n long tail: `/docs`, `/links`, `/remind(s)`, `/task`, `/tts`, `/admin`, all inline menus/`menu:*`, streaming placeholders, persona preset names and refusal texts now render RU or EN by chat language; callbacks and preset IDs stayed byte-stable. The catalog grew to 442 keys per language with a parity test; `docs/I18N_STATUS.md` lists the intentionally language-neutral surfaces (model prompts, logs, export content, Mini App).
- `docs/adr/0007-store-repo-postgres.md` records the Postgres/`store.Repo` decision: SQLite remains the default, the repository interface and Postgres start only after a recorded trigger (second writer/HA), with five phases, acceptance gates and a "do not start until" list; the roadmap row links to it.

## Предыдущий выпуск: v1.60.0

# tg-llm-proxy v1.60.0

## Secrets, an optional Whisper and sleep-free tests

- Docker secrets: the bot token and the STRATA key are mounted as files and passed through `TELEGRAM_BOT_TOKEN_FILE` / `STRATA_API_KEY_FILE`; `docker compose config` and `docker inspect` no longer contain the values. `scripts/setup_docker_secrets.ps1` (and a bash twin) create them idempotently from `.env` with restrictive ACLs, and `secrets/` is git- and docker-ignored. Plain ENV remains a fallback for non-compose runs; rotation is a container recreate plus a BotFather token roll.
- Whisper is no longer a hard dependency: the bot starts and answers text even when the transcriber is down, voice returns the existing localized `transcription_unavailable`/`transcription_failed` message, and the `WhisperDown` alert stays. The voice-unavailable path gained a test that proves the chat model is not called.
- Tests: 43 of 50 `time.Sleep` calls were replaced by `testutil.Eventually`/`Consistently` (bounded polling, failure details, no production seams); the 7 remaining sleeps are deliberate timing windows and are marked as such. The shuffled suite runs 16/16 packages green and is slightly faster; integration 35 PASS; JS 48/48.

## Предыдущий выпуск: v1.59.0

# tg-llm-proxy v1.59.0

## Group mode and a bilingual command interface

- Groups: `GROUPS_ENABLED=true` lets the bot answer in groups/supergroups only when addressed — an `@BotName` mention in the text/caption, a reply to one of the bot's messages, or `/cmd@BotName`. The mention is stripped before storing and before the model call; the sender must be in `TELEGRAM_ALLOWED_USERS` (the allowlist names users, not chats), per-user limits still apply, and an ignored group update calls no model and consumes no daily quota. `GROUPS_ENABLED=false` (default) ignores groups entirely; private chats are byte-identical to before. The bot id/username come from `getMe`, with reply/command forms still working if it is unavailable.
- Language: `DEFAULT_LANGUAGE=ru|en` for new chats and `/language ru|en|reset` per chat (migration **19**, `chat_language`). A new `internal/i18n` catalog (RU/EN, key parity test, fallback to RU) drives `/start`, `/help`, `/status`, `/model`, `/stream`, `/miniapp`, `/persona`, `/language`, `/reset`, `/skip`, `/unskip`, `/whoami`, `/stats`, `/usage`, `/feedback`, `/history`, `/export`, `/search`, `/delete` and the answer wrappers (timeout, overloaded, finish_reason, latency, sources, truncation). Model answers are never translated; the remaining RU surfaces are listed in `docs/I18N_STATUS.md`.
- Startup summary prints `default_language` and `groups_enabled`; the config has 71 rows. The integration group-attribution test now enables group mode and mentions the bot.

## Предыдущий выпуск: v1.58.0

# tg-llm-proxy v1.58.0

## Data reliability, Mini App admission and recovery hygiene

- Migrations: the schema version is re-read inside `BEGIN IMMEDIATE` before every pending migration, the legacy 011 `ALTER TABLE` is guarded by `pragma_table_info`, and concurrent `Open` calls (bot + dbprune/ragembed) no longer fail on a duplicate column or a half-applied version. The FTS rebuild and its marker now commit in one transaction with `INSERT OR IGNORE`; a marker failure on a valid index no longer blocks startup.
- Embeddings (schema **18**): `rag_chunk_embeddings` is rebuilt with `PRIMARY KEY (chunk_id, model)` in a data-preserving migration, the model and its dimension are registered on write, a wrong-length vector is rejected with `expects N dimensions, got M`, a model change keeps the old rows instead of overwriting them, and search filters by the current model (falling back to lexical when it is absent). The migration was rehearsed on a copy of the live database: 148 messages, 1165 events, 68 chunks and 68 embeddings intact.
- Backup verification: `VerifyBackup` now checks `PRAGMA foreign_key_check`, the FTS integrity of both indexes, required tables/triggers/indexes; `dbinspect -counts` emits a SHA-256 `content_hash`, and the restore drill compares it in addition to the 35 counters.
- Mini App: one active stream per user (second → `429 stream_limit`, localized), uploads/transcribe have their own 2-slot semaphore, and `AnswerForMiniApp` uses the same bounded per-chat queue and execution slot as Telegram. A shared finalizer now appends the `finish_reason` warning, latency, sources and document-truncation marker in BOTH clients; the stored row stays raw content.
- Bot: recovery attempts are counted only when the handler actually runs (a limiter refusal no longer burns attempts and abandons the question), a superseded unanswered question gets one «остался без ответа» notice per question, album offsets follow the same stored-question ack rule, dispatcher approval/document-confirmation state moved into the dispatcher (no package-level leak), `/links add` reserves the shared daily network quota, and the turn/user maps are bounded.

## Предыдущий выпуск: v1.57.1

# tg-llm-proxy v1.57.1

## Hotfix: security, delivery reliability, local backup and monitoring

- Mini App: `Permissions-Policy` now allows same-origin microphone (`microphone=(self)`) — the v1.56 voice feature was blocked in Chromium clients; `auditResponseWriter` implements `Unwrap`, so per-request upload/transcribe deadlines actually reach the connection; `/api/session` reads its body with a 5-second deadline and a byte cap, so slow unauthenticated requests cannot park every global slot; six server error codes (`task_active`, `link_limit`, `reminder_limit`, `url_reader_disabled`, `lifecycle_unavailable`, `internal_error`) are localized, and a frontend test now fails when a new `writeError` code has no translation.
- Telegram delivery: the update offset is acknowledged only after the user question was actually stored (or the command needs no storage); a failed append sends «Не удалось сохранить вопрос, попробуйте ещё раз» and leaves the update for redelivery instead of silently losing it. The streaming placeholder is now deleted via the optional `DeleteMessage` capability, with an «(ответ ниже)» edit fallback, so a newer question no longer leaves a truncated duplicate above the final answer.
- Local backup (still on this PC by decision): maintenance stops the bot with `stop_grace_period: 60s`, copies the whole `/data` snapshot (db + wal + shm), verifies integrity/schema and counters, publishes `bot-*.db` atomically via `VACUUM INTO` and rotates `pre-prune-*`; retention now supports all seven classes and runs while the bot is stopped; scheduled tasks use `StartWhenAvailable`, `WakeToRun` and allow battery start. `data/backup-status.prom` feeds monitoring; the local-only RPO remains ~24 h and off-host/encryption is a separate release to an external host.
- Monitoring and gateway: seven scrape targets (bot, traefik, prometheus, alertmanager, grafana, node_exporter, blackbox whisper), node_exporter with host and backup textfile metrics, `Watchdog` + `absent()` guards, `DiskWillFillIn4Hours`, `BackupStale`, `WhisperDown`, `TelegramSendFailing`; fallback info no longer repeats, schema changes go to a blackhole, the dashboard gained disk-free and backup-age panels, the internet-facing catch-all root now requires BasicAuth (401 anonymous), and edge-gateway got CI (compose config, promtool check/test, dashboard JSON, port contract). `HEALTH_PORT` is canonically 8081. One labelled test alert was delivered to Telegram (counter 1→2, 0 failures).

## Предыдущий выпуск: v1.57.0

# tg-llm-proxy v1.57.0

## An ordered reserve chain, and the roadmap tech debt

- `STRATA_FALLBACKS` (row 69) is a JSON array of up to eight reserves `{model, base_url?, api_key?, timeout_seconds?}`. The request walks the list in order after a retryable primary failure and stops at the first answer; a non-retryable error is not replayed. A distinct `base_url` without `api_key` is called unauthenticated, a missing `base_url` uses the primary endpoint, and the legacy `STRATA_FALLBACK_MODEL` stays a one-entry chain (both rows at once is a configuration error). Production runs `kimi-k3.1-pro` → local `qwen/qwen3.8-27b`; a dead-primary drill answered from the first reserve in 5 s.
- A stream still never replays after text reached the caller; the chain stops there and returns the error. `/model` keeps the primary catalogue and only falls through when the listing itself fails. Startup prints `fallback_models` in order.
- `internal/bot/parity_test.go` (v1.56.0) stays green with the new config surface.
- Tech debt: `-shuffle=on` in CI and the local scripts (two shuffled runs green: bot seeds 1794277491112246690 / 1794277532726559674, webapp 1794277491112181525 / 1794277532726549209), `renovate.json` with grouped weekly updates and no automerge, `RELEASE_NOTES.md` split into `CHANGELOG/v1.0-1.9 … v1.50-1.56` (92 release headings before and after), and the open decisions checklist refreshed in `docs/ROADMAP.md`.

## Старые выпуски

Заметки о выпусках v1.0.0–v1.56.0 перенесены без изменений в `CHANGELOG/`. Имя файла — `CHANGELOG/v<первый>-<последний>.md`; диапазон включает все релизы между границами.

- [v1.50–v1.56](CHANGELOG/v1.50-1.56.md) — v1.56.0 … v1.50.0
- [v1.40–v1.49](CHANGELOG/v1.40-1.49.md) — v1.49.3 … v1.40.0
- [v1.30–v1.39](CHANGELOG/v1.30-1.39.md) — v1.39.0 … v1.30.0
- [v1.20–v1.29](CHANGELOG/v1.20-1.29.md) — v1.29.1 … v1.20.0
- [v1.10–v1.19](CHANGELOG/v1.10-1.19.md) — v1.19.0 … v1.10.0
- [v1.0–v1.9](CHANGELOG/v1.0-1.9.md) — v1.9.0 … v1.0.0
