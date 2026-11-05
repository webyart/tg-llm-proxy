# tg-llm-proxy

Telegram-бот-прокси между пользователями и локальной OpenAI-compatible STRATA.
Основной транспорт — long polling. Webhook и стриминг ответов реализованы как opt-in и по умолчанию выключены.

Поддерживаются Windows, macOS и Linux. Runtime и Docker-образ кроссплатформенные; host-скрипты используют PowerShell 7.

## Возможности

- 22 команды: `/start`, `/help`, `/ping`, `/status`, `/ask`, `/reset`, `/skip`, `/unskip`, `/history`, `/docs`, `/export`, `/search`, `/delete`, `/model`, `/stream`, `/persona`, `/stats`, `/whoami`, `/admin`, `/remind`, `/reminders`, `/links`.
- `/start` и `/help` открывают секционное inline-меню с Back-навигацией; callbacks проходят через тот же Dispatcher и allowlist.
- Альбомы до 10 фото JPEG/PNG, текстовые документы UTF-8 до 64 КиБ, voice, аудиофайлы и video notes через Whisper. Лимиты настраиваются `MEDIA_MAX_MB` и `SPEECH_MAX_SECONDS`.
- История и независимый контекст каждого чата в SQLite/WAL.
- Безопасный OpenAI function calling: локальный calculator, opt-in `read_url` для чтения HTTPS-страниц и opt-in `web_search` через self-hosted SearxNG; без shell, файлов и произвольного кода. Поддерживаются обычные и streaming ответы.
- Allowlist по Telegram user id, rate limiting, per-chat lock и ограничение параллельных запросов к модели.
- Обработка timeout/5xx/пустых ответов, Telegram 429 и восстановление незавершённых вопросов после рестарта.
- Persistent one-shot и ежедневные напоминания с управлением списком и plain-text доставкой без вызова модели.
- «Ссылки на потом»: сохранение страниц, поиск и резюме без повторной загрузки.
- Версионированные миграции через `schema_migrations` (текущая схема — версия 8).
- Backup/restore, ротация копий и age-based retention с dry-run по умолчанию.
- Безопасное восстановление Docker named volume.
- Docker multi-stage build, non-root runtime, healthcheck, build commit и ротация логов 10 MB × 5.
- Unit, integration и race-тесты, fixture maintenance acceptance и `govulncheck`.

## Требования

- Go 1.27.3 или совместимая более новая версия.
- PowerShell 7 (`pwsh`).
- Git.
- Docker Desktop на Windows/macOS или Docker Engine + Compose v2 на Linux.
- Telegram bot token и OpenAI-compatible endpoint STRATA.

macOS/Homebrew:

```bash
brew install go git
brew install --cask powershell docker
```

На Linux установите те же инструменты через пакетный менеджер дистрибутива.
На macOS/Linux вызывайте скрипты через `pwsh -NoLogo -NoProfile -File scripts/<name>.ps1`.
`-UseSystemProxy` читает только реестр Windows; на Unix задайте `HTTPS_PROXY` и `NO_PROXY` вручную.

## Быстрый старт

1. Скопируйте конфигурацию:

```powershell
Copy-Item .env.example .env
```

На macOS/Linux можно использовать `cp .env.example .env`.

2. Заполните в `.env`:

```env
TELEGRAM_BOT_TOKEN=<token from BotFather>
TELEGRAM_ALLOWED_USERS=<numeric Telegram user id>
STRATA_BASE_URL=http://host.docker.internal:8000
STRATA_MODEL=<model id served by STRATA>
STRATA_API_KEY=
```

`TELEGRAM_ALLOWED_USERS` принимает список id через запятую. Пустое значение закрывает бота для всех, кроме `/start` и `/whoami`. `.env` игнорируется Git и не должен попадать в логи или коммиты.

3. Проверьте окружение и модель:

```powershell
. .\scripts\Setup.ps1
.\scripts\env-check.ps1
.\scripts\strata-check.ps1
.\scripts\verify.ps1 -WithVolumeRestore
```

4. Запустите контейнер:

```powershell
.\scripts\docker-up.ps1
docker compose ps
docker compose logs -f bot
```

`docker-up.ps1` собирает образ с правильным build commit и запускает его. `DB_PATH=/data/bot.db` находится в named volume. Не используйте bind-mount Windows-каталога для production SQLite.

Локальный запуск без контейнера:

```powershell
.\scripts\run.ps1                 # в текущей консоли
.\scripts\run.ps1 -Background     # в фоне
.\scripts\stop.ps1 -Id <pid>      # Ctrl+C на Windows, SIGTERM на macOS/Linux
.\scripts\stop.ps1 -Id <pid> -Force
```

`run.ps1 -UseSystemProxy` нужен только для специфической Windows-конфигурации с системным WinINET proxy.

## Конфигурация

Полный список из 44 ENV-строк и их значения по умолчанию находится в `.env.example`. Пустой `TELEGRAM_ADMIN_USERS` закрывает `/admin` для всех.

### Основные параметры

| Параметр | По умолчанию | Назначение |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | обязательный | Telegram token, без значения запуск запрещён |
| `STRATA_BASE_URL` | `http://host.docker.internal:8000` | OpenAI-compatible base URL без обязательного `/v1` |
| `STRATA_MODEL` | `qwen2.5-7b-instruct` | id модели из `GET /v1/models` |
| `STRATA_TRANSCRIPTION_MODEL` | `Systran/faster-whisper-small` | модель для `/audio/transcriptions` |
| `TRANSCRIPTION_BASE_URL` | `STRATA_BASE_URL`; в compose — Whisper | отдельный transcription endpoint |
| `STRATA_API_KEY` | пусто | ключ endpoint, если требуется |
| `STRATA_TIMEOUT_SECONDS` | 60 | таймаут одной попытки модели |
| `TG_REPLY_DEADLINE_SECONDS` | 90 | общий бюджет ответа; должен быть больше timeout |
| `RECOVERY_MAX_AGE_HOURS` | 24 | максимальный возраст unanswered-вопроса для автоматического recovery; `0` отключает лимит |
| `REMINDER_TIMEZONE` | `UTC` | IANA timezone для ежедневных напоминаний, например `Europe/Moscow` |
| `MEDIA_MAX_MB` | 10 | лимит каждого media-файла и альбома целиком |
| `SPEECH_MAX_SECONDS` | 300 | лимит voice/audio/video note |
| `DB_PATH` | `./data/bot.db` | host SQLite; в compose — `/data/bot.db` |
| `TELEGRAM_ALLOWED_USERS` | пусто = deny by default | список user id через запятую |
| `CONTEXT_MESSAGES` | 20 | размер окна истории |
| `MAX_CONTEXT_CHARS` | 8000 | бюджет текста истории |
| `MAX_INPUT_CHARS` | 4000 | ограничение входного текста |
| `RAG_MAX_DOCUMENTS` | 20 | документов на чат; замена по имени не увеличивает usage |
| `RAG_RESULT_LIMIT` | 3 | максимум фрагментов в model request |
| `RAG_MAX_CONTEXT_CHARS` | 2000 | общий бюджет RAG-фрагментов |
| `RATE_LIMIT_PER_MINUTE` | 20 | лимит запросов пользователя к модели |
| `MAX_CONCURRENT_MODEL_REQUESTS` | 2 | параллельные вызовы модели |
| `TOOLS_ENABLED` | true | безопасный calculator через function calling |
| `URL_READER_ENABLED` | false | opt-in `read_url` для чтения текстовых HTTPS-страниц |
| `WEB_SEARCH_ENABLED` | false | opt-in `web_search` через self-hosted SearxNG |
| `WEB_SEARCH_BASE_URL` | пусто | адрес SearxNG; обязателен при `WEB_SEARCH_ENABLED=true` |
| `WEB_SEARCH_TIMEOUT_SECONDS` | 10 | таймаут запроса к SearxNG; 1..30 |
| `WEB_SEARCH_MAX_RESULTS` | 5 | число результатов; 1..10 |
| `MAX_MESSAGE_CHUNK` | 3500 | размер части ответа; максимум 4096 |
| `PARSE_MODE` | `MarkdownV2` | `MarkdownV2`, `HTML` или `plain` |
| `HEALTH_PORT` | 8080 | loopback health endpoint |
| `LOG_LEVEL` | `info` | `error`, `warning`, `info`, `debug` |

`STRATA_BASE_URL` нормализуется к ровно одному `/v1`: chat и models используют этот base. `TRANSCRIPTION_BASE_URL` независимо ведёт в `/audio/transcriptions`. Оба endpoint запрещают userinfo, query и fragment, чтобы credentials не попадали в логи или диагностику. Compose запускает Speaches Whisper без публичного порта и заранее загружает multilingual small-модель. Voice хранится только как текст транскрипции; исходное аудио остаётся в памяти на время запроса.

При `PARSE_MODE=MarkdownV2` сохраняются заголовки, жирный и курсивный текст, `code`, code fences и
http/https-ссылки. Обычные специальные символы экранируются, а небезопасные схемы ссылок не пропускаются.

### Чтение URL (opt-in)

`read_url` позволяет модели прочитать текстовое содержимое страницы. Для включения задайте `URL_READER_ENABLED=true` при `TOOLS_ENABLED=true` и перезапустите бота; по умолчанию tool выключен. Поддерживаются только HTTPS URL без userinfo, длиной до 2048 символов. Запрос ограничен примерно 15 секундами, телом до 1 МиБ и результатом до 8000 runes; разрешены `text/*`, JSON, XML и HTML.

Не более 5 redirects, каждый адрес проверяется повторно. SSRF-защита запрещает localhost, private, loopback, link-local, multicast, unspecified, reserved, documentation, CGNAT, benchmark и другие не-global-unicast IP. Каждое соединение самостоятельно выполняет DNS resolve и проверку, затем подключается непосредственно к проверенному IP без proxy, чтобы исключить DNS rebinding. URL и содержимое не логируются.

Поведение на реальной сети проверяется отдельной opt-in проверкой под build tag `livecheck` — она не входит в стандартный pipeline:

```powershell
go test -count=1 -tags=livecheck -run TestLiveURLReaderRealNetwork ./internal/bot
```

Она читает реальный публичный HTTPS-сайт и подтверждает, что metadata-endpoint `169.254.169.254`, `http://` и `localhost`
отклоняются. Живой acceptance внутри Telegram с `URL_READER_ENABLED=true` выполнен на v1.17.3 и повторно подтверждён на v1.18.1: страница прочитана, финальный streamed-ответ доставлен после tool rounds.

### Веб-поиск (opt-in)

`web_search` даёт модели выдачу self-hosted SearxNG. Tool предлагается модели только при `WEB_SEARCH_ENABLED=true` **и** `TOOLS_ENABLED=true`; по умолчанию выключен. `WEB_SEARCH_BASE_URL` при включённом флаге обязателен: http или https, без userinfo, query и fragment. `WEB_SEARCH_TIMEOUT_SECONDS` (10, 1..30) ограничивает один запрос к SearxNG, `WEB_SEARCH_MAX_RESULTS` (5, 1..10) — число результатов.

Sidecar поднимается отдельным opt-in override-файлом, в `.env` — те же две строки, что включают tool:

```powershell
docker compose -f docker-compose.yml -f compose.websearch.yaml up -d
```

```env
WEB_SEARCH_ENABLED=true
WEB_SEARCH_BASE_URL=http://searxng:8080
```

Бот вызывает только `<base>/search?q=…&format=json` и передаёт модели `title`/`url`/`snippet`; ссылки выдачи не загружаются, поэтому результат поиска не становится шагом для SSRF-перехода. SearxNG опубликован на `127.0.0.1:8083`, внутри compose-сети доступен по имени `searxng`, образ закреплён тегом и digest. JSON API включает settings из override-файла: у образа по умолчанию `format=json` отвечает 403, и ни один `SEARXNG_*` этот ключ не переопределяет. `secret_key` SearxNG генерируется в контейнере при старте и не попадает ни в `.env`, ни в репозиторий.

Override проверен локально: контейнер стартует healthy, `format=json` отвечает выдачей, и opt-in проверка под build tag `livecheck` (`TestLiveWebSearchRealProvider`) выполнила реальный запрос через этот sidecar. Живой acceptance внутри Telegram с `WEB_SEARCH_ENABLED=true` выполнен на v1.17.3 и повторно подтверждён на v1.18.1: поисковый tool вызван, финальный streamed-ответ доставлен после tool rounds.

### Подтверждение сетевых действий

RAG, документы, транскрипты и веб-контент считаются untrusted-данными и не авторизуют новые сетевые действия. Точный HTTPS URL из запроса пользователя и явный поисковый запрос выполняются автоматически. Другой origin или автономный вызов требует inline-подтверждения: оно привязано к chat/user/message и canonical arguments, действует 5 минут и работает однократно. Cancel, expiry и replay не выполняют network action; Confirm продолжает исходный tool loop.

### Сеть Telegram

TLS handshake timeout Telegram transport — 30 секунд вместо стандартных 10; стартовый `getMe` получает до 65 секунд на две попытки маршрута. При ошибке proxy только `getMe`/`getUpdates` автоматически пробуют direct route; `sendMessage` и edit никогда не повторяются через fallback, чтобы не создать дубли. Если прямой маршрут нестабилен, создайте локальный `data/telegram-proxy.env`:

```env
HTTPS_PROXY=http://host.docker.internal:10808
HTTP_PROXY=http://host.docker.internal:10808
NO_PROXY=127.0.0.1,localhost,host.docker.internal,whisper
https_proxy=http://host.docker.internal:10808
http_proxy=http://host.docker.internal:10808
no_proxy=127.0.0.1,localhost,host.docker.internal,whisper
```

Compose подхватывает этот optional файл при каждом запуске, включая scheduled maintenance; он gitignored и не попадает в Docker context. `host.docker.internal` заменяет loopback proxy хоста. `NO_PROXY` обязателен для локальной STRATA, чтобы её трафик и API key не отправлялись через proxy. Docker HEALTHCHECK дополнительно использует `wget --proxy off`, поэтому loopback probe не зависит от proxy. Для host-запуска вне Docker используйте `run.ps1 -UseSystemProxy`. Постоянные timeout означают неисправность сети/VPN, а не ошибку бота.

### Стриминг

```env
STREAMING_ENABLED=false
STREAM_EDIT_INTERVAL_MS=5000
STREAM_IDLE_TIMEOUT_SECONDS=30
STREAM_TOTAL_DEADLINE_SECONDS=900
```

Стриминг переписывает одно сообщение по мере генерации. По умолчанию выключен. `/stream on` включает его только для текущего чата, `/stream off` выключает, `/stream reset` возвращает общий `STREAMING_ENABLED`; override хранится в SQLite и переживает рестарт. `/status` показывает эффективное значение. `TG_REPLY_DEADLINE_SECONDS` не обрезает общий stream deadline.

### Webhook (experimental)

```env
WEBHOOK_ENABLED=true
WEBHOOK_PUBLIC_URL=https://bot.example.com/telegram/webhook
WEBHOOK_SECRET_TOKEN=<random 16–256 ASCII characters>
WEBHOOK_LISTEN_HOST=127.0.0.1
WEBHOOK_PORT=8082
WEBHOOK_PATH=/telegram/webhook
```

Reverse proxy или туннель должен перенаправлять публичный URL на `http://127.0.0.1:8082/telegram/webhook` с тем же path. В compose listener доступен только через loopback host. Не публикуйте его напрямую в интернет.

При старте listener поднимается до `setWebhook`. Secret header проверяется до чтения body, затем метод POST, JSON и размер тела до 1 MiB. In-flight requests ограничены 16, headers — 16 КиБ. Pending updates не удаляются, `getUpdates` не запускается. Для возврата к polling сначала вызовите Telegram `deleteWebhook`.

### Защита от перегрузки

- Active handlers ограничены `MAX_CONCURRENT_MODEL_REQUESTS`; на чат — один active и до четырёх FIFO waiters с queue timeout 2 секунды. Albums используют тот же bounded executor.
- Denied/bootstrap/overload replies coalesce, а отклонённая работа не скачивает вложения, не вызывает модель и не создаёт command/history rows.
- Streaming ограничен 100000 runes, tool argument — 16384 runes, один ответ — 64 Telegram-сообщениями.
- Model work — `3 × RATE_LIMIT_PER_MINUTE` на пользователя; network tools — 8/мин/user и 4 global in flight.
- RAG использует первые 32 термина из 4000 runes запроса. Export сериализуется инкрементально с остановкой на 5 МиБ.
- Compose resource backstops: bot 512MiB/1 CPU/128 PIDs, Whisper 4GiB/4 CPU/256 PIDs, SearxNG 1GiB/1 CPU/128 PIDs.

## Команды бота

| Команда | Назначение |
|---|---|
| `/start` | регистрация чата и список команд |
| `/help` | справка |
| `/ping` | `pong` и uptime |
| `/status` | build, модель, persona, стриминг, pending и suppression |
| `/ask <текст>` | запрос к модели; обычный текст работает так же |
| `/reset` | скрыть старый контекст без удаления истории |
| `/skip` | запретить recovery текущего unanswered-вопроса |
| `/unskip` | снять suppression без немедленного вызова модели |
| `/history [N]` | последние N сообщений, по умолчанию 5 |
| `/remind <время> <текст>` | `через 1 минуту`, `через 2 часа` или `каждый день 09:00`; текст до 500 runes |
| `/reminders` | список активных напоминаний чата; `cancel <id>` или `delete <id>` отменяет свою запись |
| `/links` | сохранённые ссылки; `add <url>`, `search <текст>`, `summary <id>`, `delete <id>` |
| `/docs` | RAG-база; `/docs add`, `export`, `delete <id>` и `clear` |
| `/export [md\|json]` | экспорт последних 10000 сообщений с явным truncation warning и ID/time window |
| `/search <текст>` | до 20 совпадений среди последних 10000 сообщений; область указана в ответе |
| `/delete` | одноразовые Confirm/Cancel с TTL 5 минут, привязкой к пользователю и сообщению; настройки сохраняются |
| `/model [check]` | модель, endpoint и проверка доступности |
| `/model select <id>` | выбрать модель текущего чата из `GET /models` |
| `/model reset` | вернуть модель по умолчанию `STRATA_MODEL` |
| `/stream [on\|off\|reset]` | стриминг текущего чата; без аргумента — состояние |
| `/persona` | inline-меню и текущая роль; `assistant`, `coder`, `writer`, `translator`, `custom <текст>`, `reset` |
| `/stats` | сообщения, токены и ошибки текущего чата |
| `/whoami` | `user_id`, `chat_id` и состояние allowlist |
| `/admin` | агрегаты, usage RAG/calculator/read_url, активные model calls и busy chats; только для `TELEGRAM_ADMIN_USERS` |

## Интерактивное меню

`/start` и `/help` открывают секционное inline-меню: Ask/New context, History, Documents, Settings, Status, Help и More. History поддерживает последние сообщения, поиск, MD/JSON export и удаление; Documents — добавление, export, удаление документов и «Ссылки на потом» с list/search/summary/delete; Settings — Model, Persona и Stream. More содержит напоминания с inline list/refresh/cancel. Admin виден только администраторам.

Ask, Search и Custom Persona принимают следующий текст через pending input с TTL 2 минуты и кнопкой Cancel. Состояние привязано к chat/user/menu message и инвалидируется при рестарте. Все текстовые команды остаются доступными. Обычные запросы показывают Telegram `typing`, а streaming tools — только общие стадии без URL и поисковых запросов.

## Проверка проекта

```powershell
.\scripts\verify.ps1                      # полный pipeline
.\scripts\verify.ps1 -WithVolumeRestore   # + изолированная volume restore rehearsal
.\scripts\verify.ps1 -SkipRace -SkipHealth
.\scripts\test-maintenance.ps1 -WithVolumeRestore
```

Pipeline включает gofmt/vet, PowerShell parser, unit/store, fixture backup/restore/external-copy/rotate/prune/migration, loopback Telegram alerts, fake Telegram + fake STRATA, build, secret leak scan и Docker race. `-WithVolumeRestore` добавляет volume restore и container-monitor acceptance.

Release CI — `.gitlab-ci.yml`: Linux host checks, race и `govulncheck`. Shared GitLab pipeline запускается только для `main` или вручную; `develop` и tags не расходуют shared minutes. Локальный `verify` остаётся обязательным перед push. Для опционального self-hosted CI без Docker socket и production secrets см. `docs/gitlab-runner-wsl.md`. GitHub Actions не требуется.

## SQLite maintenance

Проверка базы:

```powershell
.\scripts\db_inspect.ps1 -Counts
.\scripts\db_inspect.ps1 -Integrity
```

`schema_version=0` означает backup до версионирования; при следующем writable `store.Open` он автоматически становится 8 без изменения прикладных строк.

### Backup и scratch restore

```powershell
.\scripts\db_backup.ps1 -Source .\data\bot.db -Dest .\zzscratch\backup.db
.\scripts\db_restore.ps1 -Source .\zzscratch\backup.db -Dest .\zzscratch\restored.db
.\scripts\db_inspect.ps1 -Path .\zzscratch\restored.db -Counts
```

Backup использует `VACUUM INTO` и не останавливает writer. Restore создаёт только новый файл внутри `zzscratch`; существующие файлы не перезаписываются. Сравнивайте `table_*`, `events_*`, `last_update_id`, `schema_version` и `integrity`, а не hash файла.

### Ротация и scheduler-ready maintenance

```powershell
.\scripts\db_rotate.ps1 -Keep 7
.\scripts\db_rotate.ps1 -Keep 7 -Apply
.\scripts\db_maintenance.ps1 -Keep 7
.\scripts\db_maintenance.ps1 -Keep 7 -Apply
```

Без `-Apply` ротация только печатает план. Удаляются только автоматические имена `bot-YYYYMMDD-HHMMSS.db`, минимум одна копия сохраняется.

### Age-based retention

```powershell
.\scripts\db_prune.ps1
.\scripts\db_prune.ps1 -EventsDays 90 -Apply
.\scripts\db_prune.ps1 -EventsDays 0 -MessagesDays 365
.\scripts\db_prune.ps1 -EventsDays 0 -MessagesDays 365 -Apply
.\scripts\db_prune.ps1 -EventsDays 0 -RAGDays 180 -Apply
```

По умолчанию выбираются только события старше 90 дней; удаление сообщений и RAG-документов отключено. Каждый `-Apply` сначала создаёт и проверяет новый backup, а после удаления повторяет `integrity_check`.

### Backup production Docker volume и расписание

```powershell
.\scripts\db_volume_maintenance.ps1 -Keep 7
.\scripts\install_maintenance_schedule.ps1           # preview
.\scripts\install_maintenance_schedule.ps1 -Register # ежедневно в 04:00
.\scripts\test-volume-restore.ps1 -Source .\data\backups\bot-YYYYMMDD-HHMMSS.db
```

Maintenance чисто останавливает бота, копирует `/data/bot.db`, перезапускает контейнер и проверяет integrity/schema новой копии в `data/backups`. По умолчанию старые backup и история не удаляются; `-Apply` или `-ApplyRetention` включают только ротацию файлов. Ошибка запускает plain-text Telegram-уведомление всем id из `TELEGRAM_ALLOWED_USERS`.

### Внешняя копия backup

```powershell
.\scripts\db_volume_maintenance.ps1 -ExternalDirectory E:\tg-llm-proxy-backups -Encrypt
.\scripts\install_maintenance_schedule.ps1 -ExternalDirectory E:\tg-llm-proxy-backups -EncryptBackups -Register
.\scripts\db_offsite_copy.ps1 -Source .\data\backups\bot-YYYYMMDD-HHMMSS.db -Directory \\NAS\backups\tg-llm-proxy -Encrypt
```

Без `-Encrypt` копия проверяется по SHA-256 и SQLite. С `-Encrypt` на внешний диск/NAS записывается только `.db.enc`: AES-256-GCM, проверка через временную расшифровку в scratch и SQLite integrity/schema. Ключ `data/backup-encryption.key` не покидает компьютер и должен храниться отдельно от backup — без него восстановление невозможно. Каталог должен действительно быть внешним; скрипт не подтверждает физическое расположение. Если ежедневная задача уже существует, сначала удалите её в Task Scheduler: регистратор намеренно не перезаписывает задачи.

### Мониторинг контейнера и уведомления

```powershell
.\scripts\watch_container.ps1
.\scripts\install_monitoring_schedule.ps1           # preview
.\scripts\install_monitoring_schedule.ps1 -Register # каждые 5 минут
.\scripts\notify-telegram.ps1 -Message 'tg-llm-proxy: test'
```

Монитор проверяет bot и Whisper, называет сервис в failure state, отправляет алерт при сбое и восстановлении, повторяет неизменный failure не чаще одного раза в 6 часов и не перезапускает контейнер автоматически. Во время штатного clean-stop backup проверка пропускается только по свежему lease с живым PID; stale lease вызывает alert и восстанавливает проверки. Backup freshness по умолчанию ограничен 30 часами, подтверждённые копии записывают verification receipt. Дополнительно `watch_runtime.ps1` анализирует последние 10 минут логов: три ошибки Telegram, STRATA или Whisper/transcription вызывают алерт, повтор подавляется на 6 часов. Состояния хранятся в `data/monitoring`. Уведомления используют существующие token/allowlist, не принимают token аргументом и не печатают его.

Speaches CPU/CUDA images закреплены по digest. Для NVIDIA GPU после настройки Docker GPU support используйте:

```powershell
docker compose -f docker-compose.yml -f compose.cuda.yaml up -d
```

По умолчанию остаётся CPU. CUDA acceptance выполнена на RTX 5060 Ti: контейнер на CUDA 12.6.3 принимает `WHISPER__INFERENCE_DEVICE=cuda` и
`WHISPER__COMPUTE_TYPE=int8_float16`, модель предзагружается из того же volume, транскрипция 6-секундного WAV возвращает текст, а прогретый
прогон занимает 0,65 с против 2,06 с на CPU (≈3×). Первый прогон после старта контейнера медленно из-за JIT-прогрева ядер cuDNN
(≈33 с на том же аудио), поэтому перед первым голосовым сообщением прогрейте whisper одним запросом. Кроссплатформенность не менялась:
GPU опционален и на macOS/NVIDIA Linux включается тем же override.

Windows Task Scheduler запускает задачи от текущего пользователя в интерактивном сеансе через `conhost.exe --headless` и `pwsh -WindowStyle Hidden`, поэтому окно консоли не появляется. Стандартные ограничения питания также действуют. Существующие задачи не перезаписываются. В действиях сохраняются абсолютные пути к репозиторию и текущему `pwsh.exe` — после их перемещения или обновления пути задачи нужно пересоздать. На macOS/Linux зарегистрируйте maintenance и monitor через cron или systemd; там отдельного консольного окна не создаётся.

Чистая остановка checkpointит WAL. Репетиция использует отдельный compose-проект и не создаёт второй Telegram poller.

### Ручной production restore и rollback

Сначала выполните репетицию и подготовьте проверенный restore-файл. Замена production volume выполняется только после явного решения оператора:

```powershell
docker compose stop bot
docker compose cp bot:/data/bot.db .\zzscratch\pre-restore.db
docker compose cp .\zzscratch\production-restore.db bot:/data/bot.db
docker compose run --rm --no-deps --cap-add CHOWN --user root --entrypoint chown bot 65534:65534 /data/bot.db
docker compose run --rm --no-deps --entrypoint sh bot -c 'test -r /data/bot.db && test -w /data/bot.db'
docker compose cp bot:/data/bot.db .\zzscratch\post-restore.db
.\scripts\db_inspect.ps1 -Path .\zzscratch\post-restore.db -Counts
docker compose up -d
```

Rollback при проблемах:

```powershell
docker compose stop bot
docker compose cp .\zzscratch\pre-restore.db bot:/data/bot.db
docker compose run --rm --no-deps --cap-add CHOWN --user root --entrypoint chown bot 65534:65534 /data/bot.db
docker compose up -d
```

Сохраняйте pre-restore snapshot до принятия результата. Не используйте `docker compose down --volumes`.

## Эксплуатационные ограничения

- Production использует long polling; живой HTTPS webhook acceptance не выполнен, для него нужен внешний HTTPS-хост или туннель с тем же path.
- GitHub Actions на этом аккаунте не запускается: job не стартует с формулировкой «your account is locked due to a billing issue». Основной
  CI выполняется локально через `scripts/verify.ps1`, что и подтверждает каждую версию перед публикацией; workflow в `.github/workflows` остаётся
  запасным и начнёт работать сразу после разблокировки аккаунта.
- Репозиторий опубликован в GitHub (`origin`) и GitLab (`gitlab`), ветки `main` и `develop` синхронны.
- Стриминг реализован и проверен вживую, но остаётся opt-in. Точный 120-секундный 429 и реальный idle-stall покрыты только тестами.
- Startup recovery рассматривает только самый новый visible unanswered-вопрос чата и ограничен `RECOVERY_MAX_AGE_HOURS` (24 часа по умолчанию). Выполняется до трёх попыток с backoff 1, 2, 4 минуты; затем включается suppression без удаления истории, `/unskip` сбрасывает backoff.
- Host-side `/healthz` на Docker Desktop может давать WARN при healthy in-container healthcheck.
- macOS использует те же Unix-ветки, что и проверенный Linux; физический Mac acceptance не выполнялся.
- Содержимое одноразовых фото и документов не сохраняется. `/docs add` явно сохраняет текст для RAG; retrieval lexical, без embeddings. Voice хранится как текст после транскрипции.

## Безопасность

- `.env` и `.env.*` исключены из Git и Docker context; шаблон `.env.example` остаётся tracked. Не помещайте реальные секреты в другие файлы.
- `smoke.ps1` проверяет Telegram token, STRATA key и webhook secret без вывода значений.
- На production Windows ACL проекта должны разрешать запись только владельцу/service account и администраторам. Текущий хост ограничен владельцем, SYSTEM и Administrators; при переносе проекта повторите проверку ACL, особенно для scheduler-скриптов и backup.
- Runtime контейнер имеет read-only rootfs, no capabilities и `no-new-privileges`; `/data` остаётся записываемым volume.
- `strata-check.ps1` не принимает секреты аргументом. Для удалённой STRATA требуется HTTPS; URL с userinfo отклоняются. Startup не печатает содержимое `SYSTEM_PROMPT`.

## Документы

- `.env.example` — все настройки (44) с комментариями.
- `RELEASE_NOTES.md` — состав release candidate и результаты проверок.
- `PLAN.md` — локальный подробный план и история изменений (gitignored).
