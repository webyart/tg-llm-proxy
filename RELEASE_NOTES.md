# tg-llm-proxy v1.0.0 — release candidate

Дата: 2026-11-01

Telegram-бот-прокси между Telegram и локальной OpenAI-compatible STRATA. Основной транспорт — long polling.
Production-конфигурация не требует webhook или стриминга: обе возможности opt-in.

## Возможности

- 12 команд: `/start`, `/help`, `/ping`, `/status`, `/ask`, `/reset`, `/skip`, `/unskip`, `/history`, `/model`, `/stats`, `/whoami`.
- Allowlist по Telegram user id, per-chat locks, rate limiting, ограничение параллельных запросов к модели.
- Обработка timeout/5xx/пустых ответов модели, Telegram 429 с `RetryAfter`, маскирование секретов в логах.
- История чатов и offset в SQLite/WAL; защита от повторных ответов и recovery незавершённых вопросов.
- Стриминг с отдельными idle/total бюджетами и ограниченной частотой правок (`STREAMING_ENABLED=false` по умолчанию).
- Версионированные миграции через `schema_migrations`; существующие базы автоматически принимаются с версии 0 на 1.
- Backup/restore через `VACUUM INTO`, read-only integrity/schema checks, ротация backup-файлов и age-based retention.
- Изолированная репетиция восстановления Docker named volume с rollback-инструкцией.
- Docker multi-stage image, non-root runtime, healthcheck, build commit в логах, ротация логов 10 MB × 5.
- Повторяемый pipeline `scripts/verify.ps1`: lint, unit, fixture maintenance, integration, build, leak scan и Docker race.
- CI definitions для GitHub и GitLab; release pipeline `.gitlab-ci.yml` запускает Linux host checks, Docker race и govulncheck.

## Experimental: webhook

Webhook реализован как opt-in: публичный HTTPS URL, secret token, локальный POST listener за reverse proxy/tunnel.
По умолчанию `WEBHOOK_ENABLED=false`. Живой HTTPS acceptance не входит в release gate этой версии. Перед возвратом
с webhook на polling необходимо вызвать Telegram `deleteWebhook`.

## Обновление

1. Остановить бота и сделать backup актуального named volume по README.
2. Обновить образ и конфигурацию. Обязательные новые ENV-строки отсутствуют: текущий файл содержит 29 строк,
   webhook-настройки имеют безопасные значения по умолчанию.
3. Запустить бота. Миграция `schema_version` 0→1 выполняется автоматически и не меняет прикладные строки.
4. Проверить `schema_version=1`, `integrity=ok`, историю и `/status`.

Проверенный release-backup: `data/backups/release-v1.0.0-candidate-20261101-184011.db`.
Его изолированное восстановление подтвердило 11 совпадающих контрольных значений, включая 16 сообщений,
28 событий, `last_update_id=920551691` и `schema_version=1`.

## Проверка

- `pwsh -NoLogo -NoProfile -File scripts/verify.ps1 -WithVolumeRestore` — GREEN, FAIL 0.
- `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` — No vulnerabilities found.
- Production container healthy, build commit `f344afd9cd7f`.
- Fake webhook acceptance: регистрация без drop, отсутствие polling, wrong-secret без model call, валидный POST
  с сохранением offset.

## Известные ограничения

- Внешний release CI выполняется в GitLab; GitHub Actions остаётся дополнительным workflow.
- Живой `/unskip` acceptance выполняется отдельно на тестовом вопросе; production `row=16` не используется как fixture.
- Стриминг выключен по умолчанию; точный 120-секундный 429 и реальный idle-stall покрыты тестами, но не повторены вживую.
- Host-side `/healthz` на этом Docker Desktop может давать WARN при healthy in-container healthcheck.
- Вложения, `/persona`, `/model select`, `/admin` и RAG относятся к следующим версиям.
