# tg-llm-proxy

Telegram-бот-прокси между пользователями и локальной OpenAI-compatible STRATA.
Основной транспорт — long polling. Webhook и стриминг ответов реализованы как opt-in и по умолчанию выключены.

Поддерживаются Windows, macOS и Linux. Runtime и Docker-образ кроссплатформенные; host-скрипты используют PowerShell 7.

## Возможности

- 12 команд: `/start`, `/help`, `/ping`, `/status`, `/ask`, `/reset`, `/skip`, `/unskip`, `/history`, `/model`, `/stats`, `/whoami`.
- `/start` и `/help` открывают inline-меню с 12 кнопками команд; callback проходит через тот же Dispatcher.
- История и независимый контекст каждого чата в SQLite/WAL.
- Allowlist по Telegram user id, rate limiting, per-chat lock и ограничение параллельных запросов к модели.
- Обработка timeout/5xx/пустых ответов, Telegram 429 и восстановление незавершённых вопросов после рестарта.
- Версионированные миграции через `schema_migrations` (текущая схема — версия 2).
- Backup/restore, ротация копий и age-based retention с dry-run по умолчанию.
- Версионированные миграции и безопасное восстановление Docker named volume.
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

Полный список из 29 ENV-строк и их значения по умолчанию находится в `.env.example`.

### Основные параметры

| Параметр | По умолчанию | Назначение |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | обязательный | Telegram token, без значения запуск запрещён |
| `STRATA_BASE_URL` | `http://host.docker.internal:8000` | OpenAI-compatible base URL без обязательного `/v1` |
| `STRATA_MODEL` | `qwen2.5-7b-instruct` | id модели из `GET /v1/models` |
| `STRATA_API_KEY` | пусто | ключ endpoint, если требуется |
| `STRATA_TIMEOUT_SECONDS` | 60 | таймаут одной попытки модели |
| `TG_REPLY_DEADLINE_SECONDS` | 90 | общий бюджет ответа; должен быть больше timeout |
| `DB_PATH` | `./data/bot.db` | host SQLite; в compose — `/data/bot.db` |
| `TELEGRAM_ALLOWED_USERS` | пусто = deny by default | список user id через запятую |
| `CONTEXT_MESSAGES` | 20 | размер окна истории |
| `MAX_CONTEXT_CHARS` | 8000 | бюджет текста истории |
| `MAX_INPUT_CHARS` | 4000 | ограничение входного текста |
| `RATE_LIMIT_PER_MINUTE` | 20 | лимит запросов пользователя к модели |
| `MAX_CONCURRENT_MODEL_REQUESTS` | 2 | параллельные вызовы модели |
| `MAX_MESSAGE_CHUNK` | 3500 | размер части ответа; максимум 4096 |
| `PARSE_MODE` | `MarkdownV2` | `MarkdownV2`, `HTML` или `plain` |
| `HEALTH_PORT` | 8080 | loopback health endpoint |
| `LOG_LEVEL` | `info` | `error`, `warning`, `info`, `debug` |

`STRATA_BASE_URL` нормализуется к ровно одному `/v1`: запросы идут в `{base}/chat/completions` и `{base}/models`.

При `PARSE_MODE=MarkdownV2` сохраняются заголовки, жирный и курсивный текст, `code`, code fences и
http/https-ссылки. Обычные специальные символы экранируются, а небезопасные схемы ссылок не пропускаются.

### Сеть Telegram

TLS handshake timeout Telegram transport — 30 секунд вместо стандартных 10; long-poll бюджет и повторы библиотеки сохраняются. Если прямой маршрут нестабилен, в `.env` можно задать стандартные proxy-переменные процесса:

```env
HTTPS_PROXY=http://host.docker.internal:10808
NO_PROXY=127.0.0.1,localhost,host.docker.internal
```

`host.docker.internal` заменяет loopback proxy хоста при запуске в Docker. `NO_PROXY` обязателен для локальной STRATA, чтобы её трафик и API key не отправлялись через proxy. Постоянные timeout после 30 секунд означают неисправность сети/VPN, а не ошибку форматирования бота.

### Стриминг

```env
STREAMING_ENABLED=false
STREAM_EDIT_INTERVAL_MS=5000
STREAM_IDLE_TIMEOUT_SECONDS=30
STREAM_TOTAL_DEADLINE_SECONDS=300
```

Стриминг переписывает одно сообщение по мере генерации. По умолчанию выключен. `TG_REPLY_DEADLINE_SECONDS` не обрезает общий stream deadline.

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

При старте listener поднимается до `setWebhook`. Проверяются secret header, метод POST и размер тела до 1 MiB. Pending updates не удаляются, `getUpdates` не запускается. Для возврата к polling сначала вызовите Telegram `deleteWebhook`.

## Команды бота

| Команда | Назначение |
|---|---|
| `/start` | регистрация чата и список команд |
| `/help` | справка |
| `/ping` | `pong` и uptime |
| `/status` | build, модель, стриминг, pending и suppression |
| `/ask <текст>` | запрос к модели; обычный текст работает так же |
| `/reset` | скрыть старый контекст без удаления истории |
| `/skip` | запретить recovery текущего unanswered-вопроса |
| `/unskip` | снять suppression без немедленного вызова модели |
| `/history [N]` | последние N сообщений, по умолчанию 5 |
| `/model [check]` | модель, endpoint и проверка доступности |
| `/model select <id>` | выбрать модель текущего чата из `GET /models` |
| `/model reset` | вернуть модель по умолчанию `STRATA_MODEL` |
| `/stats` | сообщения, токены и ошибки текущего чата |
| `/whoami` | `user_id`, `chat_id` и состояние allowlist |

## Проверка проекта

```powershell
.\scripts\verify.ps1                      # полный pipeline
.\scripts\verify.ps1 -WithVolumeRestore   # + изолированная volume restore rehearsal
.\scripts\verify.ps1 -SkipRace -SkipHealth
.\scripts\test-maintenance.ps1 -WithVolumeRestore
```

Pipeline включает gofmt/vet, PowerShell parser, unit/store, fixture backup/restore/external-copy/rotate/prune/migration, loopback Telegram alerts, fake Telegram + fake STRATA, build, secret leak scan и Docker race. `-WithVolumeRestore` добавляет volume restore и container-monitor acceptance.

Release CI — `.gitlab-ci.yml`: Linux host checks, race непосредственно в `golang:1.27-alpine` и `govulncheck`. Docker-in-Docker не требуется. `.github/workflows/verify.yml` сохранён как дополнительный workflow.

## SQLite maintenance

Проверка базы:

```powershell
.\scripts\db_inspect.ps1 -Counts
.\scripts\db_inspect.ps1 -Integrity
```

`schema_version=0` означает backup до версионирования; при следующем writable `store.Open` он автоматически становится 2 без изменения прикладных строк.

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
```

По умолчанию выбираются только события старше 90 дней; удаление сообщений отключено. Каждый `-Apply` сначала создаёт и проверяет новый backup, а после удаления повторяет `integrity_check`.

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
.\scripts\db_volume_maintenance.ps1 -ExternalDirectory E:\tg-llm-proxy-backups
.\scripts\install_maintenance_schedule.ps1 -ExternalDirectory E:\tg-llm-proxy-backups -Register
.\scripts\db_offsite_copy.ps1 -Source .\data\backups\bot-YYYYMMDD-HHMMSS.db -Directory \\NAS\backups\tg-llm-proxy
```

Внешняя копия создаётся после локального backup, не перезаписывает файлы и проверяется по SHA-256 и SQLite integrity/schema. Каталог должен действительно находиться на другом диске, NAS или в синхронизируемом облачном хранилище — скрипт не может подтвердить физическое расположение. Если ежедневная задача уже существует, сначала удалите её в Task Scheduler: регистратор намеренно не перезаписывает задачи.

### Мониторинг контейнера и уведомления

```powershell
.\scripts\watch_container.ps1
.\scripts\install_monitoring_schedule.ps1           # preview
.\scripts\install_monitoring_schedule.ps1 -Register # каждые 5 минут
.\scripts\notify-telegram.ps1 -Message 'tg-llm-proxy: test'
```

Монитор отправляет алерт при сбое и восстановлении, повторяет неизменный failure не чаще одного раза в 6 часов и не перезапускает контейнер автоматически. Во время штатного clean-stop backup проверка пропускается. Состояние подавления хранится в `data/monitoring/container-alert.json`. Уведомления используют существующие token/allowlist, не принимают token аргументом и не печатают его.

Windows Task Scheduler запускает задачи от текущего пользователя в интерактивном сеансе через `conhost.exe --headless` и `pwsh -WindowStyle Hidden`, поэтому окно консоли не появляется. Стандартные ограничения питания также действуют. Существующие задачи не перезаписываются. В действиях сохраняются абсолютные пути к репозиторию и текущему `pwsh.exe` — после их перемещения или обновления пути задачи нужно пересоздать. На macOS/Linux зарегистрируйте maintenance и monitor через cron или systemd; там отдельного консольного окна не создаётся.

Чистая остановка checkpointит WAL. Репетиция использует отдельный compose-проект и не создаёт второй Telegram poller.

### Ручной production restore и rollback

Сначала выполните репетицию и подготовьте проверенный restore-файл. Замена production volume выполняется только после явного решения оператора:

```powershell
docker compose stop bot
docker compose cp bot:/data/bot.db .\zzscratch\pre-restore.db
docker compose cp .\zzscratch\production-restore.db bot:/data/bot.db
docker compose run --rm --no-deps --user root --entrypoint chown bot 65534:65534 /data/bot.db
docker compose run --rm --no-deps --entrypoint sh bot -c 'test -r /data/bot.db && test -w /data/bot.db'
docker compose cp bot:/data/bot.db .\zzscratch\post-restore.db
.\scripts\db_inspect.ps1 -Path .\zzscratch\post-restore.db -Counts
docker compose up -d
```

Rollback при проблемах:

```powershell
docker compose stop bot
docker compose cp .\zzscratch\pre-restore.db bot:/data/bot.db
docker compose run --rm --no-deps --user root --entrypoint chown bot 65534:65534 /data/bot.db
docker compose up -d
```

Сохраняйте pre-restore snapshot до принятия результата. Не используйте `docker compose down --volumes`.

## Эксплуатационные ограничения

- Production использует long polling; живой HTTPS webhook acceptance не выполнен.
- Стриминг реализован и проверен вживую, но остаётся opt-in. Точный 120-секундный 429 и реальный idle-stall покрыты только тестами.
- Startup recovery не имеет age cap и рассматривает только самый новый visible unanswered-вопрос чата.
- Host-side `/healthz` на Docker Desktop может давать WARN при healthy in-container healthcheck.
- macOS использует те же Unix-ветки, что и проверенный Linux; физический Mac acceptance не выполнялся.
- Вложения, `/persona`, `/admin` и RAG относятся к следующим версиям.

## Документы

- `.env.example` — все 29 настроек с комментариями.
- `RELEASE_NOTES.md` — состав release candidate и результаты проверок.
- `PLAN.md` — локальный подробный план и история изменений (gitignored).
