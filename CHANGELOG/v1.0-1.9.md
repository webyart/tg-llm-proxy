# tg-llm-proxy v1.9.0

## History release

- `/export [md|json]` выгружает до 10000 сообщений как Telegram document из памяти, с лимитом 5 МиБ и без записи на диск.
- `/search <текст>` выполняет Unicode case-insensitive поиск по последним 10000 сообщениям и возвращает до 20 совпадений.
- `/delete` показывает отдельное Confirm/Cancel меню. Только подтверждение удаляет историю текущего чата; model/persona/streaming настройки и другие чаты сохраняются.
- Команды проходят allowlist/lock и не вызывают модель. Добавлены store, bot и end-to-end integration проверки.

# tg-llm-proxy v1.8.0

## Media release

- Альбомы до 10 фото собираются за 400 мс в один model request и один ответ; все offsets и persistent dedup сохраняются. Multimodal JSON поддерживает несколько `image_url` без дублирования первого фото.
- Аудиофайлы и video notes проходят через Whisper вместе с voice. Filename/MIME корректно передаются в multipart, формат проверяется по сигнатуре.
- `MEDIA_MAX_MB=10` ограничивает каждый media-файл и альбом целиком; `SPEECH_MAX_SECONDS=300` ограничивает voice/audio/video notes.
- Усечение документа по `MAX_CONTEXT_CHARS` явно отображается пользователю. Добавлены unit, integration и race-проверки.

# tg-llm-proxy v1.7.0

## Minor release

- Добавлены Telegram voice OGG/Opus до 10 МиБ и 5 минут. Аудио передаётся в `POST /v1/audio/transcriptions` как multipart без записи на диск.
- Compose добавляет Speaches Whisper CPU sidecar без публичного порта, named volume и preload `Systran/faster-whisper-small`. `TRANSCRIPTION_BASE_URL` задаёт отдельный endpoint; STRATA API key на sidecar не пересылается. `NO_PROXY`/`no_proxy` включают `whisper`, чтобы voice не уходил через VPN proxy. `HF_HUB_DISABLE_XET=1` сохраняет загрузку моделей совместимой с read-only rootfs.
- Voice multipart явно содержит `audio/ogg`; transcription возвращает стандартное provider error message в операторский лог без сохранения аудио.
- В SQLite сохраняется только транскрипция, которая затем проходит обычный model/streaming flow. До транскрипции recovery требует повторной отправки voice; после — безопасно использует сохранённый текст.
- Добавлены model, store, bot и end-to-end integration проверки multipart, ошибок, лимитов и recovery.

# tg-llm-proxy v1.6.1

## Reliability patch

- Добавлен `RECOVERY_MAX_AGE_HOURS=24`: startup recovery не вызывает модель для unanswered-вопросов старше 24 часов.
- Просроченный вопрос остаётся в истории и автоматически получает suppression, как при `/skip`; `/status` показывает это состояние. `0` отключает age cap.
- Повреждённый timestamp останавливает recovery с ошибкой вместо скрытого обхода лимита. Добавлены config и startup regression-тесты.

# tg-llm-proxy v1.6.0

## Minor release

- Добавлены фото JPEG/PNG до 10 МиБ и текстовые документы UTF-8 до 64 КиБ. Подпись используется как вопрос; без неё бот просит описать фото или прочитать документ.
- Фото передаются в OpenAI-compatible формате multimodal content, документы — текстом. Обычные сообщения сохраняют прежний string JSON; streaming использует тот же request builder.
- Файлы скачиваются через настроенный Telegram HTTP client/proxy с ограничением чтения, без записи на диск. Содержимое вложений не сохраняется в SQLite и не логируется.
- После прерванного запроса бот не вызывает модель без файла: просит отправить вложение повторно и подавляет повторный recovery. Неподдерживаемые типы получают явный ответ.
- Добавлены unit и end-to-end integration проверки фото, документов, лимитов, невалидного UTF-8, unsupported media и recovery.

# tg-llm-proxy v1.5.0

## Minor release

- Добавлена `/admin`: агрегированные чаты, сообщения, события, ошибки, pending, размер SQLite, модель, uptime и последняя активность.
- Доступ ограничен новым `TELEGRAM_ADMIN_USERS`, отдельным от обычного allowlist. Пустое значение запрещает доступ всем; тексты переписки не показываются.
- Добавлены config, store, bot и integration проверки admin-доступа и 15-й кнопки меню.

# tg-llm-proxy v1.4.6

## Reliability patch

- Мониторинг учитывает не только Docker health: три повторные ошибки Telegram или STRATA за 10 минут вызывают Telegram-уведомление, даже если контейнер healthy.
- Повторные runtime alerts подавляются на 6 часов; единичные сбои не создают уведомлений. Состояние хранится в `data/monitoring/runtime-alert.json`.
- Исправлен flaky restart acceptance: тест ждёт сохранения processed offset после ответа Telegram, а не проверяет его преждевременно.
- Добавлены scratch acceptance для threshold и suppression без реальных уведомлений.

# tg-llm-proxy v1.4.5

## CI quota patch

- GitLab shared CI запускается только для `main` и ручного pipeline через UI; push в `develop` и tags больше не расходуют shared compute minutes.
- Jobs помечены `interruptible`; для автоматической отмены устаревших pipeline включите Auto-cancel redundant pipelines в настройках GitLab.
- CI jobs умеют использовать предустановленные PowerShell/GCC на self-hosted Linux/WSL вместо повторной установки пакетов.
- Добавлен `docs/gitlab-runner-wsl.md`: shell executor без Docker socket и доступа к production secrets; регистрация runner остаётся ручной.

# tg-llm-proxy v1.4.4

## Security patch

- Внешняя backup-копия поддерживает AES-256-GCM: на NAS/cloud/другой диск записывается только `bot-...db.enc`, plaintext SQLite не покидает компьютер.
- Ключ создаётся один раз в `data/backup-encryption.key`, не попадает в Git/Docker context и остаётся под restrictive ACL. Его нужно сохранить отдельно: без него восстановить backup невозможно.
- Перед успехом зашифрованная копия расшифровывается во временный scratch и проходит SQLite integrity/schema. Scratch затем удаляется. Повторная запись существующего файла запрещена.
- `db_volume_maintenance.ps1 -Encrypt` и `install_maintenance_schedule.ps1 -EncryptBackups` включают шифрование. Acceptance покрывает roundtrip, tamper rejection, crypto header и no-overwrite.

# tg-llm-proxy v1.4.3

## Security patch

- Runtime Docker контейнер использует read-only root filesystem, `cap_drop: ALL`, `no-new-privileges` и временный `/tmp`; SQLite остаётся на записываемом named volume.
- `strata-check.ps1` больше не принимает `-Key`: секрет читается только из ENV/`.env` и не остаётся в аргументах процесса/history.
- `STRATA_BASE_URL` запрещает userinfo и требует HTTPS для удалённых hosts; localhost и внутренние Docker hosts могут использовать HTTP.
- Startup логирует только длину `SYSTEM_PROMPT`, не его содержимое. Добавлены config security tests.

# tg-llm-proxy v1.4.2

## Security patch

- `.env.local`, `.env.production` и другие `.env.*` исключены из Git и Docker build context; `.env.example` остаётся tracked.
- Secret leak scan проверяет `TELEGRAM_BOT_TOKEN`, `STRATA_API_KEY` и `WEBHOOK_SECRET_TOKEN` без печати значений.
- Windows ACL проекта ограничены текущим владельцем, SYSTEM и Administrators; широкие inherited права на чтение/изменение секретов, backup и scheduler-скриптов удалены. Доступ владельца, Docker, Git и scheduled monitor проверены.

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
