# tg-llm-proxy v1.4.1

## Patch release

- `/persona` теперь открывает inline-меню: `assistant`, `coder`, `writer`, `translator`, `custom` и `reset`.
- Persona callbacks принимают только зарегистрированные действия, проходят существующий allowlist и Dispatcher; `custom` показывает синтаксис ввода роли.
- Добавлены unit и end-to-end integration проверки клавиатуры, callback и сохранения выбора в SQLite.

# tg-llm-proxy v1.4.0

## Minor release

- Добавлена `/persona` с пресетами `assistant`, `coder`, `writer`, `translator`, поддержкой `/persona custom <текст>` и `/persona reset`.
- Persona выбирается независимо для каждого чата и применяется как system prompt со следующего запроса. Настройка сохраняется после рестарта и не удаляется при `/model reset` или `/stream reset`.
- `/status` показывает effective persona; inline-меню содержит 14 команд.
- Добавлена append-only миграция `schema_version=4` с таблицей `chat_personas`; существующие сообщения, модель, streaming и offset не меняются.
- Store, bot и integration тесты покрывают пресеты, custom, изоляцию чатов, reset и публикацию команды.

# tg-llm-proxy v1.3.1

## Hotfix

- Исправлена доставка ответов при Telegram `can't parse entities`: обычное сообщение и финальный streaming edit один раз повторяются как readable plain text на том же сообщении.
- Fallback срабатывает только на подтверждённую ошибку форматирования; сетевые сбои не вызывают потенциально дублирующую повторную отправку.
- Неуспешная финализация streaming больше не считается успешной доставкой. Добавлены integration regression-тесты для обычных и streamed ответов.
- Исправлено разбиение длинного Markdown: каждая часть fenced code block получает собственные открывающий и закрывающий fences; Telegram больше не получает незакрытый блок на границе сообщений.
- Длинный streaming больше не отправляет хвост пакетом в конце: новые части появляются по мере генерации, а существующие сообщения форматируются по ID при завершении. Короткие соседние сегменты упаковываются без лишнего дробления.
- Из Telegram-сообщений, логов, ошибок, Go/PowerShell-кода и комментариев удалены служебные ссылки на разделы плана; пользовательские формулировки очищены без изменения истории `PLAN.md`.

# tg-llm-proxy v1.3.0

## Minor release

- Добавлены `/stream`, `/stream on`, `/stream off` и `/stream reset`: стриминг можно включить только для тестового чата, сохранив общий `STREAMING_ENABLED=false` для остальных.
- `/status` показывает эффективную настройку и наличие override; inline-меню содержит 13 команд. Выбор стриминга независим от `/model reset` и сохраняется после рестарта.
- Добавлена append-only миграция `schema_version=3` с таблицей `chat_streaming`; существующие сообщения, настройки модели и offset не меняются.
- Unit, integration и race-тесты покрывают изоляцию чатов, reset, оба пути ответа и публикацию команды.

## Maintenance и сетевые исправления

- Добавлен `db_volume_maintenance.ps1`: clean-stop snapshot production named volume, перезапуск бота и проверка integrity/schema backup.
- Добавлен preview/registration Windows Task Scheduler: ежедневно в 04:00, без автоматического удаления старых копий. На macOS/Linux используется cron/systemd.
- Контрольный запуск через Task Scheduler завершился с кодом 0; backup `bot-20261102-065649.db` содержит `schema_version=2`, `integrity=ok`, 28 сообщений и 71 событие. Бот возвращён в healthy.
- Ошибки backup и контейнера отправляют plain-text Telegram-уведомления всем разрешённым id. Monitor запускается каждые 5 минут, подавляет повторы на 6 часов, сообщает о восстановлении и не перезапускает контейнер сам.
- Добавлена опциональная внешняя копия без overwrite с SHA-256 и SQLite integrity/schema проверкой. Каталог другого диска/NAS/cloud задаёт оператор.
- Loopback Telegram, external-copy и изолированный container-monitor acceptance добавлены в verification pipeline. Живой monitor task завершился с кодом 0; тестовое уведомление доставлено обоим разрешённым получателям.
- Сквозной production maintenance создал `bot-20261102-075400.db` и проверенную внешнюю scratch-копию. Полный `verify.ps1 -WithVolumeRestore`: 7 стадий, FAIL 0; единственный WARN — известный host-side health probe при healthy container.
- Windows maintenance и container monitor переведены на `conhost.exe --headless` + `pwsh -WindowStyle Hidden`: периодическое окно консоли больше не создаётся. Оба пересозданных task запущены вручную через Task Scheduler и завершились кодом 0; бот healthy.
- Telegram transport использует TLS handshake timeout 30 секунд вместо стандартных 10 для устойчивости к кратким сетевым провалам. Стартовый `getMe` также увеличен с 5 до 30 секунд, чтобы не перезапускать контейнер до начала polling. Long polling и custom HTTP clients не меняются; добавлены unit-тесты transport и инструкция по `HTTPS_PROXY`/`NO_PROXY` для Docker.
- Для нестабильного direct route compose поддерживает optional non-secret `data/telegram-proxy.env`; файл сохраняется при scheduled restart, а `NO_PROXY` исключает локальную STRATA.
- При proxy network error `getMe`/`getUpdates` пробуют direct fallback с replay тела запроса. `sendMessage`/edit fallback не используют, поэтому дубли ответов не создаются. Добавлены unit-тесты обоих случаев.
- Docker HEALTHCHECK использует `wget --proxy off`: loopback probe больше не получает ложный 503 через Telegram proxy.
- Default `STREAM_TOTAL_DEADLINE_SECONDS` увеличен с 300 до 900 секунд после живого timeout длинного ответа. Явные ENV-переопределения сохраняют приоритет.

# tg-llm-proxy v1.2.1

## Hotfix

- Исправлена отправка текста inline-меню: `/start` и `/help` теперь проходят MarkdownV2 escaping перед
  `SendMessage`, поэтому `!`, `+` и остальные зарезервированные символы не вызывают `can't parse entities`.
- Добавлены regression assertions на фактический `/start` и на `a+b` через `SendMenu`.

# tg-llm-proxy v1.2.0

## Minor release

- `/start` и `/help` теперь открывают обычное inline-меню Telegram с 12 кнопками в две колонки.
- Нажатие кнопки вызывает `answerCallbackQuery` и существующую команду через тот же Dispatcher и middleware chain.
- Callback data ограничены зарегистрированными командами; произвольные данные игнорируются.
- Добавлены unit/integration тесты inline keyboard и обработки callback.

# tg-llm-proxy v1.1.0

## Minor release

- Добавлены `/model select <id>` и `/model reset`: выбор модели отдельно для каждого чата с сохранением в SQLite.
- `/model`, `/status` и `/ask` используют override чата; `/model select` принимает только id из `GET /models`.
- Добавлена миграция `schema_version=2` с таблицей `chat_settings`; существующая история не меняется.
- Добавлены unit/integration тесты выбора, сброса и передачи модели в completion request.

# tg-llm-proxy v1.0.1

## Patch release

- Исправлено форматирование ответов Telegram: `PARSE_MODE=MarkdownV2` больше не превращает всю разметку
  модели в обычный текст. Сохраняются заголовки, жирный/курсивный текст, `code`, code fences и http/https-ссылки.
- Обычные специальные символы продолжают экранироваться; небезопасные схемы ссылок не пропускаются.
- Добавлены unit-тесты форматирования и безопасности; полный pipeline и race-тесты проходят.
- Живой Telegram acceptance подтвердил заголовок, жирный/курсивный текст и корректную ссылку.

# tg-llm-proxy v1.0.0

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
- Кроссплатформенный runtime и host tooling: Windows, macOS и Linux; SIGTERM/Ctrl+C, restart tests, portable Go builds и Docker.
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
- GitLab pipeline: host-checks, docker-race и govulncheck — GREEN.
- Живой `/unskip` acceptance на новом `row=17`: suppression снят и снова установлен финальным `/skip`, `integrity=ok`.
- Fake webhook acceptance: регистрация без drop, отсутствие polling, wrong-secret без model call, валидный POST
  с сохранением offset.

## Известные ограничения

- Внешний release CI выполняется в GitLab; GitHub Actions остаётся дополнительным workflow.
- Стриминг выключен по умолчанию; точный 120-секундный 429 и реальный idle-stall покрыты тестами, но не повторены вживую.
- Host-side `/healthz` на этом Docker Desktop может давать WARN при healthy in-container healthcheck.
- macOS использует проверенные Unix-ветки Linux; физический Mac acceptance не выполнялся.
- Вложения, `/persona`, `/model select`, `/admin` и RAG относятся к следующим версиям.
