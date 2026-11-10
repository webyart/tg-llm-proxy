# tg-llm-proxy

Telegram-бот-прокси между пользователями и локальной OpenAI-compatible STRATA. Основной транспорт — long polling; Mini App и стриминг ответов — opt-in. Здесь хранятся заметки о выпусках: v1.0.0–v1.56.0 разложены по диапазонам в `CHANGELOG/`, новейший выпуск — ниже.

## Новейший выпуск: v1.57.1

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
