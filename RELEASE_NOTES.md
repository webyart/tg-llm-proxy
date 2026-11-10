# tg-llm-proxy

Telegram-бот-прокси между пользователями и локальной OpenAI-compatible STRATA. Основной транспорт — long polling; Mini App и стриминг ответов — opt-in. Здесь хранятся заметки о выпусках: v1.0.0–v1.56.0 разложены по диапазонам в `CHANGELOG/`, новейший выпуск — ниже.

## Новейший выпуск: v1.57.0

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
