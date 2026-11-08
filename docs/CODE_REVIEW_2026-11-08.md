# Код-ревью tg-llm-proxy v1.39.0

Дата: 2026-11-08 · Ветка: `main` (clean tree) · Коммит: `38184f6` · Тег: `v1.39.0`
Метод: статическое чтение кода шестью параллельными ревьюерами по зонам ответственности + независимая проверка сборки и тестов.
Файлы не изменялись — только read-only анализ.

## 0. Как проверялось

| Проверка | Команда | Результат |
|---|---|---|
| Сборка | `go build ./...` (go1.27.3 windows/amd64) | OK, 0 ошибок |
| Статический анализ | `go vet ./...` | OK, 0 предупреждений |
| Форматирование | `gofmt -l .` | OK, пусто |
| Юнит-тесты (выборка) | `go test ./internal/config ./internal/ratelimit ./internal/model ./internal/health` | 4/4 `ok` (4.3 s на model) |
| Полный race-прогон | не запускался | В репозитории есть `scripts\test-race.ps1` и CI-job `docker-race` |

Замечание по окружению: `go build` не работает с дефолтным `GOCACHE` (`C:\Users\arti\AppData\Local\go-build` — Access denied под песочницей). Для проверки использовался `GOCACHE` во временном каталоге. Это ограничение среды ревью, не проекта.

## 1. Метрики

| Метрика | Значение |
|---|---|
| Go-файлов | 222 (138 из них — тестовые) |
| Строк Go | ~51 000, из них **~21 300 кода и ~29 800 тестов (≈58 % тестового кода)** |
| `Test`/`Benchmark`/`Fuzz` функций | 734 |
| Прямые зависимости | 2 (`go-telegram/bot v1.27.0`, `modernc.org/sqlite v1.61.0`) |
| Версия схемы БД | 11 миграций |
| Команд бота | 25 |
| ENV-строк | 57 (в `config.Env` и `.env.example` совпадают 1:1) |
| Размер production БД | ~0,8 МБ (домашний/малый масштаб) |
| Мёртвых маркеров (`TODO/FIXME/XXX`) | 0 |
| Комментариев | ~1 400 строк (2,5 %) |

## 2. Архитектура

```
main.go (415 строк)                 wiring: конфиг → store → model → dispatcher → health/webapp → polling/webhook
internal/config    (1 018)          ENV → Config, валидация, fail-fast, deny-by-default allowlist
internal/store     (4 187)          SQLite: WAL, 4 соединения, миграции 1..11, quota, RAG, backup/inspect
internal/model     (1 234)          OpenAI-совместимый клиент: Complete / Stream (SSE) / Transcribe / TTS
internal/bot       (9 606)          Dispatcher (25 команд), middleware-цепочка, транспорт, меню, RAG,
                                    tool-loop с подтверждениями, планировщики напоминаний и задач, recovery
internal/ratelimit (232)            FIFO-локи на чат, лимитер, coalescer ответов
internal/webapp    (2 570 + ~100 КБ статики)  Mini App: initData HMAC, launch tickets, sessions, JSON API
internal/health    (92)             loopback /healthz
cmd/db{backup,restore,rotate,prune,inspect,fixture}   эксплуатационные утилиты
scripts/*.ps1                       verify/test/smoke/backup/monitor (PowerShell 7)
```

Оценка архитектуры — **выше среднего для проекта такого размера**:

- Пакет `bot` не зависит от `go-telegram/bot` в бизнес-логике: `Event`, `Replier`, `AttachmentDownloader`, `OffsetWriter`, `MiniAppTicketIssuer` — интерфейсы, транспорт вынесен в `run.go`/`webhook.go`. Это даёт быстрые unit-тесты без токена и сети.
- `Deps`-инъекция вместо глобалов (кроме двух исключений, см. P2), DI в `Dispatcher`, `webapp.App`, `model.OpenAI`.
- Middleware-цепочка `recovered → allowed → limited → locked → stored` читается сверху вниз и решает ровно пять сквозных задач; порядок осмысленный и документирован.
- Персистентность отделена от домена: SQL только в `internal/store`, никаких `database/sql` выше.
- Тесты — не декоративные: table-driven, с fake-Telegram и fake-STRATA, с проверкой отрицательных исходов (что НЕ произошло: «no model call», «no network dial»).

Главные архитектурные долги:

1. `main.go` — 277-строчная `main()` с ~15 точками `db.Close(); os.Exit(1)` и без `defer`-ов; нет `run() error`, поэтому wiring нетестируем.
2. Логика Mini App частично дублирует бот (`internal/webapp` знает про store и bot-lifecycle), из-за чего появляются обходы лимитов и третьи источники правды.
3. Два `sync.Map` на уровне пакета (`tool_loop.go:99`, `network_scope.go:26`) вместо полей `Dispatcher`.

## 3. Находки: P1 (исправлять в первую очередь)

| # | Файл:строка | Проблема | Влияние | Что делать |
|---|---|---|---|---|
| 1 | `internal/model/stream.go:293` (при `:208-231`) | В watchdog-стриме отправка `beat <- struct{}{}` блокирующая, канал ёмкостью 1. Если таймер простоя сработал одновременно с приходом очередной строки, watchdog уже вышел (`close(stalled); cancelStall(); return`), в буфере лежит один beat — следующая итерация виснет навсегда. | `Stream` не возвращается: утекают горутина, слот семафора модели и per-chat лок. Повторный stall-сценарий может «заклинить» чат до рестарта. Вероятность низкая (узкая гонка), последствия — тяжёлые. | Неблокирующая отправка (`select { case beat <- struct{}{}: default: }`) + проверка `stalled`/`ctx.Err()` в начале каждой итерации цикла сканера. |
| 2 | `internal/store/store.go:162-201`, `main.go:169-178` | Нет проверки «БД новее бинарника»: `ApplySchema` применяет только версии 1..len(MigrationSQL) и молча игнорирует `schema_version` больше N, `main.go` версию только печатает. `VerifyBackup` (`inspect.go:53-86`) версию тоже не проверяет. | Откат образа после деплоя = бот работает против незнакомой схемы: ошибки INSERT в рантайме вместо внятного отказа на старте; `dbrestore` протаскивает такую базу дальше. | Guard после `store.Open`: `if v > len(MigrationSQL) { exit 1 с текстом }`; та же проверка в `VerifyBackup`. |
| 3 | `internal/bot/middleware.go:29-71` + `menu.go:440-448` | Для callback-событий `TelegramMessageID` — id меню-сообщения, которое переиспользуется всеми действиями меню. `stored` пишет его в `messages.telegram_message_id` с UNIQUE(`chat_id`,`telegram_message_id`), поэтому второе модельное действие по тому же меню попадает в duplicate-ветку и молча возвращает `nil`. | «Сводка ссылки» после «Назад» (и любое повторное модельное действие из меню) не даёт ни ответа, ни ошибки — только строка в логе. Пользователь думает, что бот умер. | Для `MenuCallback` не писать `telegram_message_id` (или дедуп по `UpdateID`), оставив уникальный ключ только для настоящих сообщений. |
| 4 | `internal/bot/dispatch.go:99-104,312-339` | Таймаут ожидания per-chat лока — 2 с, а лок держится весь ответ: 30–90 с, со стримингом до 900 с. Второе сообщение/нажатие в том же чате получает `DeadlineExceeded` → `dispatchOverloaded` → **`persistOffset` сдвигает offset** и сообщение теряется безвозвратно (Telegram его уже не отдаст). | Потеря пользовательских сообщений без возможности повторной доставки + ложный текст «очередь переполнена» в логах `the update queue is full`. | Различать «очередь полна» и «чат занят» через `errors.Is`; дать чату бюджет ≥ `TG_REPLY_DEADLINE_SECONDS`; при отказе не двигать offset. |

P0 (дедлок на ровном месте / порча данных) не найдено: `Locks.WithChatBounded` корректно обрабатывает гонку `ready`/`ctx.Done`, `streamSession` действительно однопоточен, `albumMu`/`offsetMu` цикла блокировок не образуют.

## 4. Находки: P2

### 4.1 Ядро бота

| # | Файл:строка | Проблема | Что делать |
|---|---|---|---|
| 1 | `reply.go:38-47`, `stream.go:363` | Длина считается в code points (`len([]rune)`), Telegram считает в UTF-16 code units. 3500 эмодзи = 7000 units → 400 «message is too long», хвост ответа теряется. | Считать `len(utf16.Encode([]rune(s)))` либо резервировать 2× для astral-символов. |
| 2 | `commands.go:860-906` | Запись assistant-строки и доставка идут на `replyCtx` (90 с), который уже израсходован моделью: ответ получен, но не сохраняется/не доставляется. | Писать и доставлять на `context.WithoutCancel(ctx)` с отдельным коротким таймаутом (в коде уже есть `auditCtx` — тот же приём). |
| 3 | `run.go:411-435`, `stream.go:449-459` | `EditStreamMessage` считает отказом «message is not modified» и «message to edit not found»; три таких 400 → `muted`, стрим замолкает навсегда. В `EditMenu` этот случай уже терпят (`menu.go:271`). | Трактовать «not modified» как успех, не инкрементить бюджет отказов на 400-класс. |
| 4 | `run.go:368-384` | 429 ретраится ровно один раз при удержании chat-лока и слота admission; второй 429 = ответ молча потерян, хотя строка в истории есть. | 2–3 попытки, бюджет из остатка ctx, явный лог/notice при сдаче. |
| 5 | `album.go:69-118` | `flushAlbum` удаляет batch из map сразу, а маркер `album_done_*` пишется только после модели. Фото с задержкой > 400 мс создаёт новый batch того же `mediaGroupID` → дубль ответа и повторная загрузка вложений. | Держать in-flight/tombstone до конца обработки, ставить состояние в начале flush. |
| 6 | `tool_loop.go:157-191` | При вытеснении pending-подтверждений (лимиты per-chat/per-user/global) их `streamSession` не помечается как broken — в отличие от stale-пути `:533-537`. Сообщение навсегда остаётся «ответ пишется, N символов». | Вызывать `session.broken(...)` и на вытеснении. |

### 4.2 Слой данных

| # | Файл:строка | Проблема | Что делать |
|---|---|---|---|
| 1 | `history.go:153-169` | `/search` читает 10 000 сообщений целиком и фильтрует подстрокой в Go; объём не ограничен, `ctx.Err()` в цикле не проверяется. | FTS5 (`messages_fts` + триггеры) или лимит по длине + проверка ctx. |
| 2 | `rag.go:321-325` | RAG-поиск сканирует до 10 000 чанков на запрос с двойной токенизацией каждого, под `_txlock=immediate`. | Инвертированный индекс термов (`rag_terms(chunk_id, term, tf)`) или FTS5; убрать `ragSearchChunkScanLimit`. |
| 3 | `history.go:43`, `dsn.go:47` | Экспорт истории идёт в транзакции на runtime-пуле, а `_txlock=immediate` берёт write-lock: записи бота встают в очередь и падают по `busy_timeout=5000`. | Отдельный read-only хендл без `_txlock`, батчи по id, общий лимит байт. |
| 4 | `store.go:172,182-198` | Гонка «проверил версию → применил миграцию» между процессами (бот + `dbprune -apply`): `011_message_user_attribution.sql` — неидемпотентный `ALTER TABLE ... ADD COLUMN`, второй процесс падает и не стартует. | `BEGIN IMMEDIATE` + повторная проверка версии внутри транзакции, для 011 — `pragma_table_info`. |
| 5 | `quota.go:25-33`, `tool_loop.go:237-243`, `usage_daily.go:49-79` | Квота резервируется до вызова модели и не возвращается при ошибке; `/usage` считает «использовано» по messages/events, поэтому «осталось» расходится с фактическим блоком. | `ReleaseDailyQuota` (или резервировать после успеха); `/usage` должен читать `daily_quota_reservations`. |
| 6 | `prune.go:12-16`, `cmd/dbprune/main.go:85-101` | Retention не покрывает `daily_quota_reservations`, `tasks`, `reminders`, `saved_links`; после удаления нет `VACUUM`/`wal_checkpoint(TRUNCATE)` — файл не уменьшается, WAL растёт. | Новые классы в `PruneOptions` + `wal_checkpoint` и `VACUUM` после удаления. |
| 7 | схема БД | Нет индексов под реальные запросы: `messages WHERE created_at < ?` (prune) — full scan; `chats ORDER BY last_active_at DESC` — скан+сортировка. | `idx_messages_created_at(created_at)`, `idx_chats_last_active(last_active_at DESC, chat_id DESC)`. |

### 4.3 Модельный слой и конфиг

| # | Файл:строка | Проблема | Что делать |
|---|---|---|---|
| 1 | `openai.go:338` (поле — `client.go:61`) | `finish_reason` не заполняется в нестримовом пути и нигде не читается: ответы, обрезанные по `length`/`content_filter`, выглядят и сохраняются как полные. | Заполнять и показывать notice при `length`/`content_filter`, писать в `events`. |
| 2 | `stream.go:300-313` | Ошибка провайдера в 200-стриме (`{"error":…}`) не распознаётся, многострочные `data:` склеиваются в невалидный JSON и отбрасываются (`continue`). | Накапливать `data`-строки до пустой, парсить `error`, возвращать `APIError`. |
| 3 | `openai.go:114-121`, `client.go:83` | Насыщение семафора неотличимо от таймаута модели: `ErrNoRoom` нигде не конструируется, пользователь ждёт весь reply-deadline за вызов, которого не было. | Короткий admission-таймаут → `ErrNoRoom` (UX в `commands.go:852` уже готов), метрика queue wait. |
| 4 | `openai.go:213,419-428` | В повторных запросах tool-loop у tool call уходит `"parameters":null` (нет `omitempty`); строгие серверы могут отвергнуть. | Отдельный тип для tool call либо `omitempty`. |
| 5 | `client.go:97,109`, `openai.go:155`, `stream.go:157` | Ретраи только 5xx/сеть, 429 не ретраится, `Retry-After` не читается, backoff фиксированный 250/500 мс без jitter. | Jitter + `Retry-After` + ограниченный ретрай 429. |
| 6 | `config.go:689-691` vs `tool_loop.go:595-597` | `URL_READER_ENABLED=true` при `TOOLS_ENABLED=false` молча игнорируется (у `web_search` такая проверка есть — `web_search.go:30-31`). | Такая же fail-fast валидация. |
| 7 | `web_search.go:38-42` | `WEB_SEARCH_BASE_URL` допускает открытый http для удалённого хоста (у STRATA http разрешён только локально). | HTTPS для нелокального хоста. |

### 4.4 Mini App

| # | Файл:строка | Проблема | Что делать |
|---|---|---|---|
| 1 | `server.go:159,263-269,465` | Нет recover-middleware: паника в хендлере — обрыв без JSON-конверта, а `logMiniAppMutation` запишет мутацию как успешную. | Обёртка с 500 + корректный статус в аудите. |
| 2 | `static/app.js:257-264,1563-1572`, `auth.go:162` | После перезагрузки страницы в групповом чате тикет уже стёрт из `#fragment`, exchange создаёт сессию `scope{ChatID:userID}` — молча показываются данные личного чата вместо группового. | Сохранять chat-scope, возвращать его клиенту. |
| 3 | `auth.go:23,187` | Сессия 15 минут без продления и без re-exchange; длинный сценарий обрывается, вернуться можно только переоткрыв Mini App. | Sliding-TTL/refresh. |
| 4 | `server.go:32-35`, `api.go:848` → `task.go:146` | Создание задачи из Mini App идёт через `Dispatcher.CreateTask` напрямую: нет ни `Limit.Allow`, ни `modelWork` (они живут в middleware команд и `runToolLoop`). До 30 планов модели в минуту против `RATE_LIMIT_PER_MINUTE`. | Проверять оба лимита внутри `CreateTask`/`planTask`. |
| 5 | `server.go:115-135` | Асимметричный fallback `storeTaskLifecycle`: Cancel/Resume/Clear идут в store напрямую (без `clearTaskNetworkScopes`, `removeTaskApproval`, `wakeTasks`), CreateTask возвращает `errLifecycleUnavailable`. | Один путь через Dispatcher для всех lifecycle-операций. |
| 6 | `settings.go:210,414-421,459-461` | Третий источник правды для TTS-скорости: сырой `DELETE FROM bot_state` в обход `bot.TTSSpeed/SetTTSSpeed`, свой список скоростей. | Использовать bot-операции. |

### 4.5 Безопасность

Общий вывод аудита: периметр выстроен честно (SSRF, HMAC initData, подтверждения сетевых действий, отсутствие IDOR проверены по коду). Найденное — не дыры в защите, а расхождения политики и границы доверия.

| # | Файл:строка | Проблема | Что делать |
|---|---|---|---|
| 1 | `webapp/auth.go:183-186`, `webapp/server.go:238-249` | `/api/session` обрабатывается до проверки scope и до rate-limit: любой allowlisted (или с утёкшим initData) может создать ~4096 сессий по 15 минут и выесть глобальный лимит — Mini App становится недоступен всем, включая меню «Ещё» (тикеты в той же карте). | Пер-юзерная квота сессий (3–5) с вытеснением самой старой + rate-limit на `/api/session`; раздельные счётчики тикетов и сессий. |
| 2 | `bot/links.go:41-91`, `webapp/api.go:620-659` | `POST /api/links` тянет произвольный публичный HTTPS независимо от `URL_READER_ENABLED`/`TOOLS_ENABLED`: оператор выключил egress-политику, а она всё равно работает. | Проверять `Cfg.URLReaderEnabled && Cfg.ToolsEnabled` в `fetchSavedLink`. |
| 3 | `bot/miniapp.go:17`, `webapp/api.go:138-286` | Сессия Mini App скоупится на **чат**, а не на пару (чат, пользователь): в групповом чате любой allowlisted участник видит и может удалить историю, документы, ссылки, задачи и напоминания, созданные другими. | Решить явно: либо Mini App только для приватных чатов (`ChatType == "private"`), либо scope = (ChatID, UserID) и фильтрация по `messages.user_id` в группах. |
| 4 | `bot/webhook.go:64` | Проверка `X-Telegram-Bot-Api-Secret-Token` пропускается, если секрет пуст (fail-open), хотя весь остальной проект fail-closed. Сейчас спасает только валидация конфига (`config/webhook.go:47-58`). | Секрет — обязательный параметр конструктора, ошибка при пустом. |
| 5 | `webapp/auth.go:120-177` | initData можно переиграть в пределах 5-минутного окна: нет кэша использованных `query_id`/hash, тикет необязателен, `logout` отзывает только один сеанс. | LRU `query_id` на TTL окна, требовать тикет для всех обменов. |
| 6 | `webapp/server.go:255-262` | Read-эндпоинты поиска (`/api/documents/search`, `/api/history/search`) вне rate-limit, хотя каждый — скан до 10 000 строк/чанков. | Включить GET-поиск в лимитер (например, 10/мин) + кэш. |
| 7 | `bot/markdown.go:16,72` | URL ссылки вставляется в MarkdownV2 без экранирования: `[x](https://a/\))` ломает разметку и роняет сообщение в plain-text фолбэк. | Экранировать `\` и `)` в URL. |

Supply chain: в GitLab CI `.deb` PowerShell ставится из GitHub без проверки хэша/подписи (`.gitlab-ci.yml:27-28`), GitHub Actions прибиты к плавающим тегам, а не SHA (`.github/workflows/verify.yml:19-22`). В `.env` лежат реальные токен и ключ STRATA (в git не попадали — проверено), а `data/backup-encryption.key` хранится рядом с бэкапами, которые он шифрует.

### 4.6 Инфраструктура, CI и тесты

| # | Файл:строка | Проблема | Что делать |
|---|---|---|---|
| 1 | `.github/workflows/verify.yml:26`, `scripts/build.ps1:41`, `test-race.ps1:35` | CI **никогда не собирает production-стадию**: все docker-сборки идут `--target test`, `docker compose config` не проверяется, overlay-файлы не валидируются. Регрессии `USER 65534`, HEALTHCHECK, alpine+CA и overlay'ев проходят незамеченными. | Job: `docker build --target run`, `docker compose -f … config -q`, запуск контейнера + `GET /healthz`, негативный тест пустого токена (ожидать exit 1). |
| 2 | `verify.yml:17`, `scripts/test.ps1:36-39`, `smoke.ps1:69-75,106` | `windows-latest` job негерметичен: `verify.ps1` всё равно вызывает Docker-зависимые `test.ps1`, `test-maintenance.ps1`, `smoke.ps1`, где отсутствие Docker — FAIL, а не SKIP. | Разделить: Windows-этапы через `-Host`, docker-этапы на ubuntu; в smoke отсутствие docker — SKIP. |
| 3 | `docker-compose.yml:135-136`, `README.md:312` | Деплой несамодостаточен: внешняя сеть `edge-telegram` создаётся в другом репозитории (`edge-gateway`), в `scripts/` нет `docker network create`, gateway в compose отсутствует. На чистой машине `docker compose up` падает. | Preflight в `docker-up.ps1` (`network inspect || network create`), gateway — обратно в репозиторий или сабмодулем. |
| 4 | `scripts/db_backup.ps1:81`, `db_restore.ps1:69` | Backup/restore собирают `bin/dbbackup.exe`/`dbrestore.exe` через `go build`: на чистом хосте и в контейнере (в образе только `/bot`) инструментов восстановления нет, задачи Task Scheduler ломаются без Go. | Собирать CLI в CI и публиковать артефактом либо класть в образ. |
| 5 | `docker-compose.yml:24,71`, `data/` | `.env` подаётся через `env_file` (виден в `docker inspect` и env процесса), `required:false` + `restart: unless-stopped` дают вечный restart-loop без токена; `backup-encryption.key` лежит рядом с шифруемыми бэкапами. | Secrets/tmpfs, `on-failure:3`, ключ вне `data/` и вне бэкап-скоупа. |
| 6 | `scripts/`, `.github/` | Покрытие не измеряется вообще: ни `coverprofile`, ни порогов, ни артефактов; `test.ps1:57` гоняет `go test -count=1`. `govulncheck` и JS-тесты есть только в GitLab, который идёт лишь для `main`/web. | `coverprofile` + артефакт + diff-coverage gate; перенести `govulncheck` и `frontendjs` в PR-CI. |
| 7 | тесты | 38 `time.Sleep` с реальными таймерами (`album_test.go:136,169`, `dispatch_test.go:133,208,447`, `resume_test.go:24`, интеграция — 5 с `waitFor`) при `t.Parallel` = 0: главный источник флаки под `-race` на нагруженном раннере. Нет fake-clock. | Инжектируемые часы (`Dispatcher.now` уже есть) вместо sleep'ов; `-shuffle=on`. |
| 8 | `Dockerfile:25,39`, `compose.cuda.yaml:7-13` | `apk add ca-certificates tzdata` без версий → пересборка невоспроизводима; `start-period=5s` может дать ложный unhealthy на медленных миграциях; `deploy.resources.reservations.devices` молча игнорируется Compose < 2.30 (тихий CPU-fallback вместо GPU). | Пиннинг пакетов, `start-period=20s`, `gpus: all` вместо reservations. |
| 9 | `go.mod:3` | В `go.mod` указан `go 1.27.3`, но нет директивы `toolchain` — сборка на другой машине может уйти на другой тулчейн. | Добавить `toolchain go1.27.3`. |

Пробелы в тестах (топ-5): DR-restore не в CI (`-WithVolumeRestore` не передаётся); production-образ/overlay не проверяются; нет coverage и `-shuffle`; lifecycle `main.go` (graceful shutdown, отказ bind/DB-open, пустой токен) не тестируется; `cmd/dbfixture` без тестов.

## 5. Находки: P3 (пакетом)

- **Bot:** плейсхолдеры `@@TGMDn@@` в `markdown.go:30-41` восстанавливаются по всему тексту модели (вывод модели может подставить чужой токен); `__x__` превращается в bold, хотя в MarkdownV2 это underline (`markdown.go:78-81`); ошибки инструментов («unknown tool», «tool disabled») показываются как «модель не ответила» (`tool_loop.go:636,639`); `telegramReplier.users` растёт без вытеснения (`run.go:290-308`); один 5-секундный `menuCtx` на два вызова (`run.go:495-502`); `calculator` не проверяет хвост JSON (`calculator.go:34-44`); offset пишется синхронно под глобальным мьютексом на каждый апдейт и двигается даже для отброшенных (`dispatch.go:380-395`); `err == context.DeadlineExceeded` вместо `errors.Is` (`dispatch.go:335`).
- **Store:** `rag_documents.created_at` перезаписывается при повторной загрузке → такой документ не попадёт под retention (`rag.go:114`); распознавание `ErrTaskActive` по подстроке `uq_tasks_chat_active`, которой SQLite не пишет (`tasks.go:104`); `map[string]**time.Time` в `scanTask` теряет совпадающие непустые значения (`tasks.go:150-159`); `ClearChat` пишет `user_id = chat_id` для групп (`messages.go:401-406`); `AdminStats` — три COUNT + скан events + ~300 запросов pending (`admin.go:31-68`); `ExportRAGDocuments` N+1 и `used += len(chunks)` складывает чанки с байтами (`rag_export.go:29,47`); два формата времени в БД (`datetime('now')` и RFC3339Nano).
- **Model:** стрим склеивает все `choices` без фильтра `Index==0` (`stream.go:322`); не запрашивается `stream_options.include_usage` (`openai.go:228-233`) — usage часто пуст; `http.DefaultClient` без тюнинга транспорта и `CheckRedirect` (`openai.go:87-91`); `StreamInfo.Elapsed` всегда 0 (`stream.go:86`); TTS не умеет `Authorization` и не дочитывает тело ошибки (`tts.go:41-47,120-122`); multipart транскрипции собирается в памяти дважды (`transcribe.go:76-120`); `TranscriptionNoAuth` включается эвристикой «база отличается от STRATA» (`main.go:203`); health-сервер без Write/Idle timeout (`health.go:39-42`); `APIError.Body` заполняется, но нигде не читается (`client.go:87-94`).
- **Webapp:** `/api/session` без rate limit и per-user cap (`server.go:238-249`) — allowlisted-пользователь может выесть глобальный лимит 4096 сессий; статика без ETag/версии, `Cache-Control: no-store` и без gzip (`server.go:194,581-609`); cap тела 16 КиБ меньше допустимой персоны 4000 рун (`server.go:27` vs `settings.go:335`) → 413 на валидном вводе; `maxInFlight=8` общий для API и статики (`server.go:29`); ручная маршрутизация вместо `http.ServeMux` 1.22 (`server.go:214-375`); `/healthz` Mini App не проверяет БД; `/api/history/item` не фильтрует `role` (`api.go:237-239`).
- **Инфраструктура:** `docker compose` (база + 5 overlay) валиден (`config -q` — exit 0), но unhealthy-контейнер не перезапускается автоматически (нет autoheal); `deploy/traefik.yml` не имеет TLS-entrypoint/certresolver, TLS живёт вне репозитория (Tailscale Funnel) и не аудируется; исторические `deploy/Caddyfile`, `deploy/traefik.yml`, `deploy/dynamic.yml`, `scripts/test-gateway.ps1` — мёртвый груз; нет workflow на тег/публикацию образа/SBOM; `start-period=5s` в HEALTHCHECK.
- **Гигиена репозитория:** бинарники **не закоммичены** (проверено `git ls-files` и история — чисто), но локально лежат `bot.exe` 2,8 МБ, `bot.test.exe` 21,5 МБ и `bin/` ~98 МБ; `.cache/` занимает 1,65 ГБ **внутри репозитория** (`Setup.ps1:19-20` пишет GOPATH/GOCACHE в проект); `data/` содержит `bot.db`, 30+ run-логов и `backup-encryption.key`; `zzscratch/` — копии production-БД. `.gitignore` стоит дополнить `*.test`, `*.out`, `*.prof`, `coverage.out`, `*.key`, `*.pem`, `.idea/`.

## 6. Документация: расхождения с кодом

1. README обещает **44** ENV-строки (`README.md:100` и `:523`), фактически их **57**; в таблицу не попали `STREAMING_*` (4), `WEBHOOK_*` (6), `PROXY_FREE_TEXT`, `TELEGRAM_ADMIN_USERS`, `SYSTEM_PROMPT`.
2. `internal/config/config.go:488` всё ещё говорит про «the 29 rows of».
3. `.env.example:8-10` обещает, что плохая строка откатывается на дефолт и «одна опечатка не ослепляет конфигурацию», но `main.go:148-161` завершает процесс при любом `problem` — fallback-сообщения `Num`/`Flag` в проде недостижимы.
4. README:112 называет `STRATA_TIMEOUT_SECONDS` «таймаутом одной попытки», код делит один бюджет на все попытки (`openai.go:4-6,123-158`).
5. Не документировано, что отдельный `TRANSCRIPTION_BASE_URL` автоматически отключает `Authorization` (`main.go:203`).
6. `PLAN.md` (185 КБ) и `RELEASE_NOTES.md` (102 КБ) — в корне; история решений живёт в git-истории этих файлов, а не в ADR.

## 7. Что сделано хорошо (не трогать)

1. **Безопасность транспорта и секретов**: токен нигде не утекает — переопределён `WithErrorsHandler`, все ошибки библиотеки идут через `Sanitize`, включая `bot.New`; `recoveredPanicText` режет панику до 1024 байт по границе UTF-8 и вычищает все секреты.
2. **Идемпотентность и восстановление**: запись `role='user'` до вызова модели + UNIQUE-индекс + startup-recovery с персистентным экспоненциальным backoff, лимитом попыток и проверкой «ответ реально новее вопроса» (`resume.go`). Это редкость даже в коммерческих ботах.
3. **MarkdownV2**: собственное экранирование, отдельная обработка code-fence/inline-code, переоткрытие ``` на каждом чанке, escape-aware резка без висячего `\`.
4. **Стриминг**: промежуточные правки без parse mode, финальная — с экранированием; `partialView`/`brokenView` резервируют место под маркеры, поэтому ни одна правка не превышает лимит сообщения; незавершённый ответ честно помечается.
5. **Подтверждения сетевых действий**: токен привязан к chat/user/message и canonical arguments, TTL 5 минут, однократность, отдельные лимиты per-user/global, валидация снапшота при рестарте.
6. **SSRF-защита `read_url`**: полный запрет не-global-unicast, DNS-resolve и подключение по проверенному IP (анти-rebinding), 5 redirects с повторной проверкой, лимиты 1 МиБ / 8000 рун.
7. **SQLite-дисциплина**: DSN как закрытый whitelist с обратным чтением прагм, `VACUUM INTO` для консистентного бэкапа без остановки бота, атомарные claim'ы задач/напоминаний через `UPDATE ... WHERE ... RETURNING`, расчёт суток строго в UTC полуинтервалом.
8. **Circuit-ломание излишеств**: coalescer'ы для denied/bootstrap/overload, bounded admission, `maxStreamChunks`, `StreamAnswerLimit`, лимиты на tool arguments.
9. **Тестовая культура**: 58 % тестового кода, fake-Telegram/fake-STRATA, negative assertions («no model call», «no network dial»), frontend-тесты без npm-зависимостей.
10. **Dockerfile/CI**: multi-stage, base-образы по digest, non-root, healthcheck с `--proxy off`, `-trimpath -ldflags="-s -w"`, gofmt/vet/race/govulncheck/frontend в CI.

## 8. План исправлений

### Спринт 1 — «надёжность» (3–5 дней)

1. Неблокирующий `beat` + проверки `stalled`/ctx в `model/stream.go` (P1-1).
2. Guard на downgrade схемы + проверка версии в `VerifyBackup` (P1-2).
3. Дедуп callback'ов не по `telegram_message_id` (P1-3).
4. `errors.Is` + отдельный бюджет ожидания чата + не двигать offset при отказе (P1-4).
5. UTF-16-точная длина сообщений; «not modified» = успех.
6. Паник-recovery и корректный аудит в Mini App; rate-limit на `/api/session`.
7. Лимиты модели в `CreateTask` (Mini App не обходит `RATE_LIMIT_PER_MINUTE`).
8. Индексы `messages(created_at)`, `chats(last_active_at)`, `wal_checkpoint` после prune.
9. Webhook: секрет обязательным параметром (fail-closed); `POST /api/links` проверяет `URL_READER_ENABLED && TOOLS_ENABLED`.
10. Решить семантику групп: либо Mini App только для приватных чатов, либо scope (ChatID, UserID) с фильтрацией по `user_id`; сейчас любой allowlisted в группе видит и удаляет чужие данные.

### Спринт 2 — «производительность и данные» (1–2 недели)

11. FTS5 для `/search` и термовый индекс для RAG; вынести `_txlock` в отдельный read-only пул для экспорта.
12. Возврат квоты при неудаче + согласованный `/usage`.
13. Retention для `daily_quota_reservations`/`tasks`/`reminders`/`saved_links` + `VACUUM`.
14. `finish_reason`, распознавание `error` в SSE, `Retry-After`/jitter, `ErrNoRoom`.
15. `include_usage`, фильтр `choices[0]`, настроенный Transport.
16. Приведение README/`.env.example`/сообщений валидатора в соответствие с кодом (57 строк и т.д.).

### Спринт 3 — «инженерия» (2–3 недели)

17. `main.go` → `internal/app` с `run() error`, `defer`-ами и одним местом логирования старта; убрать `PrintMissing`-шум (сейчас на каждом старте печатается «is not set» для всех незаполненных опциональных строк).
18. `slog` вместо `fmt.Println` в стартовом дампе конфигурации.
19. Заменить `sync.Map`-глобалы на поля `Dispatcher`.
20. Убрать `var x = …` / `for i < len(…)` в пользу `:=`/`range` (сейчас стиль неидиоматичен для Go и мешает `golangci-lint`).
21. `golangci-lint` (revive, errcheck, gocritic, staticcheck) в CI как блокирующий шаг + `coverprofile` и diff-coverage gate.
22. CI: job `docker build --target run` + `docker compose config -q` + smoke-запуск контейнера; `govulncheck` и JS-тесты перенести в PR-CI; `-shuffle=on`; restore-drill сделать регулярным (сейчас `-WithVolumeRestore` не передаётся в CI).
23. Деплой: preflight `docker network create edge-telegram` в `docker-up.ps1`; вернуть gateway compose в репозиторий; вынести `backup-encryption.key` из `data/`; `on-failure:3` вместо `unless-stopped`; secrets через файлы/tmpfs.
24. Гигиена: удалить `bot.exe`, `bot.test.exe`, `bin/` (~120 МБ) и перенести `.cache/` (1,65 ГБ) из репозитория; дополнить `.gitignore`; убрать мёртвые `deploy/Caddyfile|traefik.yml|dynamic.yml`; добавить `toolchain go1.27.3`.
25. Перенести историю решений из `PLAN.md` в `docs/adr/`, оставить в корне README + CHANGELOG.

## 9. Дальнейшее развитие

### 9.1 Продукт (то, чего не хватает пользователю)

1. **Полноценный чат в Mini App.** Сейчас Mini App — «read-only панель»: нельзя задать вопрос, ответить на «ожидает ответа», нажать `/skip`/`/unskip`/`/reset` контекста. Это самый заметный разрыв: бот умеет стриминг, Mini App — нет. Дать `POST /api/messages` + SSE-стрим (или long-poll), и Mini App станет полноценным вторым клиентом.
2. **Обратная связь по ответам (👍/👎).** Её нет вовсе (reactions/инлайн-кнопки). Для домашнего бота это главный источник данных для настройки системного промпта, персона-пресетов и RAG-параметров. Плюс таблица `answer_feedback` и `/stats` по ней.
3. **Групповой режим.** Сейчас бот отвечает на всё в чате. Нужны: реакция только на упоминание/reply, лимиты per-user в группе, «молчать, если не спросили», отдельная персона для группы.
4. **Голосовой диалог.** TTS есть, распознавание есть — нет связки «voice-in → voice-out» одним режимом и streaming-TTS.
5. **Память и документы**: embeddings-поиск вместо лексического (README сам это признаёт), гибридный BM25+вектор, `sqlite-vec` или отдельный embedding-sidecar; долговременная память пользователя (сводки) — в `config.go` до сих пор живут legacy-строки `LONGTERM_*`, то есть это уже задумывалось.
6. **i18n бота**: интерфейс Mini App двуязычный, ответы бота — только русские. Локализация сообщений + `/language`.

### 9.2 Архитектура (когда захочется масштаба)

1. **Абстракция провайдера.** Сейчас один OpenAI-совместимый endpoint. Следующий шаг — реестр провайдеров с fallback-цепочкой, per-chat маршрутизацией (уже есть `/model select`) и учётом стоимости по моделям; интерфейс `model.Client` к этому готов.
2. **Postgres-путь.** SQLite+WAL идеален для одного процесса и домашнего масштаба, но `_txlock=immediate`, гонки миграций и read-only экспорт под write-lock — это симптомы. Ввести `store.Repo` интерфейс (сейчас store — набор package-level функций) и вторую реализацию, когда появятся вторая реплика или веб-клиент с нагрузкой.
3. **Метрики и трейсы.** Сейчас `/healthz` отвечает 200 всегда и ничего не проверяет. Нужны: `/readyz` (DB ping, свежесть последнего poll, доступность модели), Prometheus `/metrics` (латентность модели p50/p95, queue wait, ретраи, 429, tool calls, размер очереди, длительность лока, размер БД) и OTel-трейс на апдейт. Без этого «дальнейшее развитие» упирается в слепую отладку.
4. **Надёжность планировщиков.** Startup-recovery обрабатывает до 20 чатов последовательно, каждый — до таймаута модели: до 20 минут на старте и очередь в per-chat локах. Параллелить с bounded-воркерпулом.
5. **Новые возможности Telegram Bot API.** В `go-telegram/bot v1.27.0` уже есть `SendMessageDraft` — стриминг черновика, спроектированный именно под LLM (снимает всю машинерию с edit-троттлингом и 64 чанками в приватных чатах). Плюс реакции как дешёвая обратная связь.
6. **SaaS/мультитенантность** — только если появится второй клиент: per-workspace allowlist, квоты, биллинг. Для домашнего сценария это преждевременно.

### 9.3 Инфраструктура и процесс

1. Релизы: `goreleaser` + подписанные образы + автоматический CHANGELOG (сейчас `buildCommit` подставляется вручную).
2. Обновления зависимостей: Renovate/Dependabot — две прямые зависимости обновлять легко, но и их лучше автоматизировать.
3. `govulncheck` и `gofmt` уже в CI; добавить `golangci-lint`, coverage-gate (например, ≥55 % по `internal/...`) и бенчмарк-регрессии для стриминга.
4. Бэкапы: добавить периодический **restore-drill** в CI (у проекта уже есть репетиция — сделать её регулярной и автоматической).
5. Документация: ADR-папка вместо 185-килобайтного `PLAN.md`; отдельный документ «Runbook» (уже фактически есть в README, стоит вынести).
6. CI как проверка **артефакта**, а не только кода: сборка `run`-стадии, `compose config` по всем overlay, smoke-запуск контейнера, негативный тест пустого токена.
7. Деплой как воспроизводимая единица: gateway и внешние сети — в репозитории (или сабмодуле), preflight-скрипт создания сетей, версия — из git-тега, а не из ручной правки `RELEASE_NOTES`.
8. Секреты: Docker secrets/tmpfs вместо `env_file`, ключ шифрования бэкапов — вне `data/` и вне скоупа бэкапа, ротация при переносе хоста.
9. Тестовая гигиена: инжектируемые часы вместо 38 `time.Sleep`, `-shuffle=on`, регулярный `-race` в PR, а не только для `main`.

## 10. Итоговый вердикт

Проект зрелый: чистая сборка, `vet`/`gofmt` без замечаний, `go mod verify` OK, `docker compose config -q` валиден для всех overlay, 58 % тестового кода, продуманная безопасность (SSRF с DNS-пиннингом, HMAC initData, подтверждения сетевых действий, deny-by-default allowlist), честное восстановление после рестарта и аккуратная работа с Telegram-лимитами. Это не «набросок», а эксплуатируемая система с редкой для ботов дисциплиной.

Основные риски — не в бизнес-логике, а в шести местах:

1. узкая гонка в watchdog'е стрима, способная заклинить чат до рестарта;
2. отсутствие защиты от отката схемы БД;
3. потеря апдейтов при занятом чате: 2-секундный таймаут очереди плюс сдвиг offset;
4. дублирование логики в Mini App — обходы лимитов, теневые пути записи и scope на чат вместо пары (чат, пользователь);
5. CI, который никогда не собирает production-стадию образа и не проверяет overlay-файлы;
6. секреты и ключ шифрования, лежащие рядом с тем, что они защищают.

Все шесть — локальные и чинятся в пределах одного-двух спринтов. Приоритет: сначала «надёжность» (спринт 1), затем производительность данных (FTS/индексы/retention), затем инженерная гигиена (main.go, линтеры, ADR, CI). Функциональное развитие логичнее начинать с чата в Mini App и обратной связи по ответам — они дают максимум пользы при минимуме архитектурных изменений.

## 11. Статус Спринта 1 — выполнен (2026-11-08)

Все 10 пунктов «Спринта 1» реализованы в коде и покрыты тестами. Проверка: `gofmt -l` — пусто, `go vet ./...` — 0 замечаний, `go build ./...` — OK, `go test ./internal/... ./cmd/...` — все пакеты `ok` (bot 25,7 s; store 30,2 s; webapp 95,4 s).

| # | Что сделано | Файлы | Тесты |
|---|---|---|---|
| 1 | Неблокирующий `beatStall` + выход из цикла при сработавшем watchdog | `internal/model/stream.go` | `stream_watchdog_test.go`: регрессия воспроизводит прежний дедлок (старый код висит 4 с и падает, новый возвращает `ErrStreamStalled`) |
| 2 | `GuardSchemaVersion` / `ErrSchemaDowngrade`: отказ при схеме новее бинарника в `ApplySchema`, `store.Open`, `VerifyBackup` | `migrations.go`, `store.go`, `backup.go` | `schema_guard_test.go` — 4 теста, включая повторный `Open` |
| 3 | Дедуп callback'ов: `MenuCallback` больше не пишет `telegram_message_id` | `middleware.go` | `dispatch_queue_test.go`: два модельных действия из одного меню + контрольный тест дедупа реальных сообщений |
| 4 | Бюджет ожидания чата = `TG_REPLY_DEADLINE_SECONDS` (2 s…3 min), `errors.Is`, свой текст «чат занят», отказ не подтверждает offset | `dispatch.go` | 6 тестов: очередь, таймаут, coalescing, лог, отсутствие acknowledge |
| 5 | Длина сообщений в UTF-16 code units (`MessageUnits`, `unitPrefix`, `cutRunesToUnits`); «message is not modified» = успех | `reply.go`, `stream.go`, `run.go`, `menu.go` | `reply_units_test.go`: эмодзи, BMP-регрессия, 400-классификация через fake-транспорт |
| 6 | `recoverPanics` + корректный статус в аудите; per-user rate limit на `/api/session` (6/мин) | `webapp/server.go` | `webapp/hardening_test.go`: 500-конверт, частичный ответ, 429 на 7-м обмене |
| 7 | `CreateTask` (вход Mini App) проверяет `RATE_LIMIT_PER_MINUTE` → `ErrTaskRateLimited` → HTTP 429 | `task.go`, `webapp/api.go` | `task_limits_test.go` |
| 8 | Миграция 012: `messages(created_at)`, `chats(last_active_at)`; `CheckpointWAL` после prune | `migrations/012_*.sql`, `migrations.go`, `prune.go`, `cmd/dbprune` | `indexes_test.go`: индексы, версия 12, усечение `-wal` |
| 9 | Webhook без секрета не создаётся (fail-closed); `/links add` и `POST /api/links` подчиняются `URL_READER_ENABLED && TOOLS_ENABLED` | `webhook.go`, `links.go`, `webapp/api.go` | link/webhook тесты + новые конфиги фикстур |
| 10 | Mini App — только приватные чаты: кнопка не публикуется в группах, тикет не выдаётся (`ErrMiniAppPrivateOnly` / `ErrPrivateOnly`) | `miniapp.go`, `menu.go`, `webapp/auth.go` | `TestMiniAppIsPrivateChatOnly`, `TestGroupTicketIsRefused` |

Попутно обновлены README (схема 12, поведение `/links`, webhook fail-closed, Mini App только для приватных чатов) и фикстуры миграций: они создают БД сразу на нужной версии, а не понижают уже смигрированную — понижение теперь корректно запрещено.

Спринт 1 закрыт. Спринт 2 закрыт полностью (раздел 12), Спринт 3 выполнен в части, не требующей решений владельца (раздел 13).

## 12. Статус Спринта 2 (2026-11-08, частично выполнен)

Сделано и покрыто тестами:

| # | Что сделано | Файлы | Тесты |
|---|---|---|---|
| 12 | `finish_reason` заполняется в `Decode` и в стриме; при `length`/`content_filter` пользователь получает notice, событие пишется в `events` | `model/openai.go`, `bot/commands.go` | `retry_stream_test.go`: `TestDecodeKeepsFinishReason`, `TestDecodePrefersChoiceIndexOfZero` |
| 12 | Ошибка провайдера внутри 200-стрима распознаётся как `APIError`, многострочные `data:` склеиваются по спеке SSE | `model/stream.go` | `TestStreamReportsAProviderErrorInsideA200Stream`, `TestStreamJoinsMultiLineDataEvents` |
| 12 | `Retry-After` (секунды и HTTP-дата), jitter, ретрай 429; ожидание слота ограничено `ErrNoRoom` вместо выжигания бюджета | `model/openai.go`, `model/stream.go`, `model/client.go` | `TestRetryAfterHeaderParsing`, `TestRetryWaitHonoursProviderAndJittersBackoff`, `TestRetryableCoversRateLimitAndTransientProviderErrors`, `TestCompleteRetries429`, `TestCompleteReturnsErrNoRoomInsteadOfBurningTheBudget`, `TestCompleteRetriesAServerErrorAndCountsAttempts` |
| 13 | `stream_options.include_usage`, только `choices[0]`, настроенный Transport с запретом редиректов, `StreamInfo.Elapsed` | `model/openai.go`, `model/stream.go` | `TestStreamRequestsUsageIncluded`, `TestStreamIgnoresChoicesOtherThanTheFirst`, `TestDefaultClientRefusesRedirects`, `TestStreamInfoCarriesElapsed` |
| 10 | `store.ReleaseDailyQuota` + возврат резерва при неудачном вызове модели/инструмента; `/usage` читает те же счётчики, что и enforcement | `store/quota.go`, `bot/tool_loop.go`, `bot/usage.go` | `store/quota_release_test.go`, `bot/quota_refund_test.go` |
| 11 | Retention для `daily_quota_reservations`, терминальных `tasks`/`reminders`, `saved_links` (dry-run и apply), `VACUUM` + `wal_checkpoint` после удаления | `store/prune.go`, `cmd/dbprune/main.go` | `store/prune_classes_test.go`, обновлённые `cmd/dbprune` тесты |
| 14 | README: 57 ENV-строк, семантика `STRATA_TIMEOUT_SECONDS`, поведение `.env.example` приведено к fail-fast; `config.go` больше не говорит про «29 rows» | `README.md`, `.env.example`, `internal/config/config.go` | существующие тесты конфига |
| 9a | **FTS5 для `/search`**: миграция 013 (`messages_fts`, `search_index_meta`), триггеры синхронизации и однократный `rebuild` при открытии; поиск идёт по всей истории, операторы FTS трактуются как текст, последнее слово — префикс | `store/fts.go`, `store/history.go`, `store/migrations/013_*`, `bot/history_manage.go`, `webapp/api.go`, `webapp/static/app.js` | `store/fts_test.go`: синхронизация вставка/правка/удаление, операторы, rebuild старой БД, разбор термов; обновлены тесты окна 10 000 |
| 9b | **Экспорт без длинной транзакции**: чтение по явному диапазону id вместо `BeginTx` на пуле с `_txlock=immediate`, поэтому 10 000 строк больше не держат write-lock бота | `store/history.go` | существующие тесты экспорта |
| 9c | **FTS5-индекс для RAG** (миграция 014): кандидаты берутся из `rag_chunks_fts`, совпадения по имени документа добавляются отдельно, Go-скоринг и его веса не менялись; корпус больше не сканируется целиком | `store/fts.go`, `store/rag.go`, `store/migrations/014_*`, `bot/rag.go`, `webapp/api.go` | обновлены `rag_detailed_test.go`, `rag_ranking_test.go`, `bot/rag_search_test.go`, `webapp/documents_search_test.go` |

Спринт 2 закрыт полностью. Проверка после последнего изменения: `go build ./...` — OK, `go vet ./...` — 0 замечаний, `gofmt -l` — пусто, `go test ./internal/... ./cmd/...` — все пакеты `ok`.

## 13. Статус Спринта 3 (2026-11-08, частично выполнен)

| # | Что сделано | Файлы | Проверка |
|---|---|---|---|
| 17 | `main()` стал тонким: читает конфигурацию, строит один логгер и вызывает `run()`. В `run()` больше нет `os.Exit` — одиннадцать копий `db.Close(); os.Exit(1)` заменены одним `defer db.Close()`, ошибки возвращаются, код выхода принадлежит только `main`. Сохранена строка `bot is polling Telegram, bot_state last_update_id … found …`, по которой `scripts/test-restart.ps1` парсит offset | `main.go` | `main_refactor_test.go`: занятый health-порт → ошибка; после возврата БД закрыта (файл переименовывается); `go test .` — ok |
| 18 | `PrintConfig`/`PrintMissing` удалены: вместо стены «ENV X is not set» на каждый необязательный ряд — одна структурированная запись `configuration loaded` без секретов (только `token_set`/`token_length`) | `main.go` | `TestStartupSummaryNeverPrintsSecretsOrUnsetRows` |
| 19 | Два `sync.Map` уровня пакета (`dispatcherNetworkConfirmations`, `dispatcherNetworkApprovalScopes`), ключом которых был `*Dispatcher`, заменены полями структуры с `sync.Once`-ленивой инициализацией: глобальная карта текла одной записью на каждый диспетчер (в тестах — на каждый тест) и держала состояние подтверждений вне владельца | `dispatch.go`, `tool_loop.go`, `network_scope.go` | существующие network/task/approval-тесты — ok |
| 21 | CI-джоба `golangci-lint` (errcheck, govet, ineffassign, staticcheck, unused) добавлена **advisory** (`allow_failure: true`): репозиторий ни разу не проходил линтер, и включать блокировку до первого чистого отчёта — значит гарантированно покрасить пайплайн | `.gitlab-ci.yml` | локально `golangci-lint` не установлен — джоба не проверена запуском |
| 22 | CI-джоба `coverage`: `-coverprofile` по `./internal/...`, порог 70 % (измерено: bot 79,5, config 94,4, health 86,5, model 88,2, ratelimit 92,3, store 77,7, webapp 86,1); CI-джоба `production-image`: `docker build --target run`, `compose config -q` по базе и всем overlay, запуск контейнера с пустым токеном и проверка кода выхода 1 | `.gitlab-ci.yml` | порог выведен из фактического прогона `go test -cover ./internal/...` |
| 23 | `docker-up.ps1` сам создаёт внешнюю сеть `edge-telegram`, если её нет: на чистой машине `docker compose up` падал с «network not found», а инструкция отправляла в другой репозиторий | `scripts/docker-up.ps1` | разбор скрипта парсером PowerShell — ok |
| 24 | `.gitignore` дополнен `*.test`, `*.out`, `*.prof`, `coverage.*`, `*.key`, `*.pem`, `.idea/` | `.gitignore` | — |
| 24 | **Уточнение к рекомендации про `toolchain`:** строка `toolchain go1.27.3` при `go 1.27.3` избыточна — `go mod tidy` её удаляет, а полная patch-версия в `go` уже задаёт минимальный тулчейн. Точный тулчейн в проекте пинится образом CI, а не `go.mod`; комментарий об этом оставлен в файле | `go.mod` | `go mod tidy -diff` — пусто |
| 22 | **GitHub Actions разделён**: `windows-latest` больше не вызывает `verify.ps1` с Docker-этапами (Docker на windows-раннере нет), а выполняет host-уровень — `lint.ps1`, `test.ps1 -Host`, `test-docker-build-lock.ps1`, `build.ps1`; добавлены Linux-джобы `frontend` (node `--check` + DOM-тесты) и `govulncheck` | `.github/workflows/verify.yml` | структура workflow проверена текстом; запуск — только на GitHub |
| 25 | **ADR вместо локального `PLAN.md`**: `docs/adr/` с пятью записями (SQLite/один writer, long polling, fail-fast конфигурация, FTS5-индексы, Mini App только для приватных чатов), README ссылается на папку | `docs/adr/*`, `README.md` | — |
| 24 | **Кэш Go вне репозитория**: `Setup.ps1` держит `GOPATH`/`GOCACHE` в платформенном cache-каталоге (`%LOCALAPPDATA%\tg-llm-proxy`, `$XDG_CACHE_HOME`, `~/.cache`), с откатом на репозиторный `.cache` — те 1,65 ГБ, которые обходил каждый рекурсивный `gofmt`/secret scan | `scripts/Setup.ps1` | разбор парсером PowerShell — ok; `scripts/lint.ps1` — GREEN |
| 23 | **Секреты файлом вместо ENV**: `TELEGRAM_BOT_TOKEN_FILE`, `STRATA_API_KEY_FILE`, `WEBHOOK_SECRET_TOKEN_FILE` (Docker secrets, systemd credentials, mounted Kubernetes secret). Абсолютный путь, чтение один раз на старте, ≤ 4 КиБ, обрезка завершающего перевода строки; одновременное значение в ENV и в файле — ошибка конфигурации, а не precedence; содержимое никогда не попадает в сообщение об ошибке | `internal/config/secret_file.go`, `config.go`, `.env.example`, `README.md` | `secret_file_test.go`: заполнение, конфликт, относительный путь, нечитаемый/пустой/гигантский файл, NUL-байты, end-to-end через `LoadFrom` |
| 23 | **Политика рестарта зафиксирована как решение, а не как долг:** `unless-stopped` оставлен осознанно (`on-failure:N` перестанет поднимать бота после серии сетевых сбоев, а конфигурационная ошибка и так падает за долю секунды с именем ряда); crash-loop ловит `watch_container.ps1` алертом без автоперезапуска | `docs/adr/0006-restart-policy.md`, комментарий в `docker-compose.yml` | `docker compose config -q` — exit 0 |
| 22 | **`production-image` стал блокирующим**: первый шаг проверяет `docker info`, и на раннере без демона джоба честно сообщает SKIP вместо красного пайплайна; на раннере с Docker сборка `--target run` и проверка пустого токена обязательны. `golangci-lint` оставлен advisory — локально линтера нет, первый отчёт не снят | `.gitlab-ci.yml` | `docker compose config -q` — exit 0; `docker info` на этой машине недоступен (npipe permission denied), поэтому сборка образа локально не проверена |
| 24 | **Исторические `deploy/*` документированы, а не удалены:** `deploy/README.md` объясняет, что это не часть текущего Compose, и почему файлы остаются на месте — bind-mounts остановленного `tg-llm-proxy-gateway-1` ссылаются на эти пути, и перенос сломал бы обещанный rollback | `deploy/README.md`, `README.md` | `docker compose config -q` — exit 0 |
| 24 | **Локальные артефакты:** `bot.test.exe` (20,5 МБ) удалён; `bot.exe` и `bin/` (97,8 МБ) удалить не удалось — файловая система/песочница отвечает Access denied (процессов, держащих файлы, нет). Команда для владельца: `Remove-Item bot.exe,bin -Recurse -Force` | — | `Get-Process` — процессов `*bot*` нет; удаление `bot.test.exe` подтверждено |

Проверка этого раунда: `scripts/lint.ps1` — GREEN (237 Go-файлов, 41 pwsh), `go build ./...` — OK, `go vet ./...` — 0 замечаний, `gofmt -l` — пусто, `go mod tidy -diff` — пусто, `go test ./...` — все 13 пакетов `ok`.

Осталось по Спринту 3 — только решения владельца или Docker-прогон: политика рестарта (`on-failure:3` против `unless-stopped`), судьба исторических `deploy/Caddyfile|traefik.yml|dynamic.yml` (README называет их артефактами отката), удаление локальных `bot.exe`/`bin/`, включение `golangci-lint` и `production-image` в блокирующий режим после первого чистого прогона на раннере.

### 13.1 Инцидент: Mini App показывает «no available server» (2026-11-08)

**Симптом:** Telegram открывает Mini App, вместо интерфейса — тёмная страница с текстом `no available server`; бот при этом отвечает в чате.

**Диагностика:** текст принадлежит Traefik и означает 503, когда у сервиса нет доступных backend'ов. Проверка через сам шлюз: `http://127.0.0.1:8086/miniapp/healthz` → **200**, `http://127.0.0.1:8086/miniapp/` → **200** (index.html), а `http://127.0.0.1:8086/` → **503** (backend `dashboard:8787` недоступен). То есть бот и маршрут `/miniapp` исправны, а Telegram открывал **корень** сайта.

**Причина:** `MINIAPP_PUBLIC_URL` в локальном `.env` был `https://desktop-hvobbn7.tail66517e.ts.net` — без `/miniapp`, под которым шлюз отдаёт Mini App. Контейнер, запущенный без `compose.production.yaml` (где это значение задано правильно), публиковал меню-кнопку на корень; корень маршрутизируется на dashboard, который остановлен, поэтому Traefik отвечал «no available server».

**Исправлено и проверено (2026-11-08, 20:31 +05:00):**

- значение в `.env` приведено к `https://desktop-hvobbn7.tail66517e.ts.net/miniapp`;
- контейнер пересоздан с production-overlay: `docker compose -f docker-compose.yml -f compose.production.yaml up -d --force-recreate bot`, `healthy` через 2 с; в окружении контейнера теперь `MINIAPP_PUBLIC_URL=…/miniapp`, в логе — `Mini App is listening on 0.0.0.0:8084 public URL …/miniapp`;
- Telegram подтверждает новую кнопку: `getChatMenuButton` → `type=web_app`, `url=https://desktop-hvobbn7.tail66517e.ts.net/miniapp`;
- маршрут шлюза: `/miniapp/healthz` → 200, `/miniapp/` → 200, ассеты `app.js`/`style.css` → 200, `/api/status` без сессии → 401 (ожидаемо);
- startup-сводка теперь печатает `miniapp_public_url` и предупреждает, если путь пустой (`main.go`, тест `TestStartupSummaryNamesTheMiniAppURL`);
- в README добавлен раздел «no available server» с порядком проверки.

**Остаётся вне этого репозитория:** корень `/` по-прежнему отдаёт 503, потому что backend `dashboard:8787` — контейнер `tuya-dashboard` остановлен (Exited 143, 22 часа назад). Это отдельный проект; Mini App от него не зависит, пока `MINIAPP_PUBLIC_URL` содержит `/miniapp`. Запуск dashboard — решения владельца.

### 13.2 Деплой Спринтов 1–3 в production (2026-11-08, 20:35–20:40 +05:00)

| Шаг | Результат |
|---|---|
| Бэкап до миграций | `docker compose stop bot` → `docker compose cp bot:/data/bot.db data/backups/pre-sprint-20261108-203534.db` (0,8 МБ); файл сохранён |
| Сборка | `docker compose … build bot` из текущего дерева, `BUILD_COMMIT=38184f6-dirty`; образ создан 15:37 UTC |
| Перезапуск | `up -d --force-recreate bot` с overlay `compose.websearch.yaml`, `compose.tts.yaml`, `compose.production.yaml`; `healthy` через 2 с, `restarts=0` |
| Миграции | в логе `migrations applied 14 schema_version 14` (12 → 14: индексы горячих путей, FTS5 для сообщений и RAG) |
| Паритет конфигурации | 40 переменных окружения до и после деплоя совпадают полностью (`Compare-Object` — пусто). В частности сохранены `URL_READER_ENABLED=true`, `WEB_SEARCH_ENABLED=true`, `MINIAPP_ENABLED=true` |
| Проверка БД (на копии) | `messages 238`, `rag_chunks 3`, `messages_fts rows 238`, `rag_chunks_fts rows 3`, `search_index_meta rows 2`, триггеров 6; `integrity-check` FTS5 для обеих таблиц — **ok**; `MATCH 'и*'` находит 125 строк |
| Шлюз и Telegram | `/miniapp/healthz` → 200, `/miniapp/` → 200, `/miniapp/app.js` → 200, `/miniapp/api/status` без сессии → 401 (ожидаемо); `getChatMenuButton` → `web_app`, `https://…/miniapp` |
| Логи | ни одной строки `level=ERROR`/`panic` после старта; `bot is polling Telegram … found true` |

Следствие для эксплуатации: `/links add` продолжает работать (`URL_READER_ENABLED=true`), приватный Mini App работает в личном чате, webhook выключен, так что обязательный секрет ничего не ломает.

**Релиз:** изменения зафиксированы коммитом `5b4e308` («Harden runtime, add FTS5 search and Mini App guardrails») и аннотированным тегом **`v1.40.0`**; рабочее дерево чистое. Образ пересобран уже из коммита (`build commit 5b4e308` в логе контейнера), контейнер пересоздан, `healthy`, миграции повторно не применялись (`schema_version 14`), `level=ERROR` после старта — 0, `/miniapp/healthz` и `/miniapp/` через шлюз — 200, `getChatMenuButton` — `https://…/miniapp`. Push в `origin`/`gitlab` не выполнялся: это отдельное действие, которое запускает пайплайны.
