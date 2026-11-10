# tg-llm-proxy

Telegram-бот-прокси между пользователями и локальной OpenAI-compatible STRATA.
Основной транспорт — long polling. Webhook и стриминг ответов реализованы как opt-in и по умолчанию выключены.

Поддерживаются Windows, macOS и Linux. Runtime и Docker-образ кроссплатформенные; host-скрипты используют PowerShell 7.

## Возможности

- 25 команд: `/start`, `/help`, `/ping`, `/status`, `/ask`, `/reset`, `/skip`, `/unskip`, `/history`, `/docs`, `/export`, `/search`, `/delete`, `/model`, `/stream`, `/persona`, `/stats`, `/whoami`, `/admin`, `/remind`, `/reminders`, `/links`, `/task`, `/tts`, `/usage`.
- `/start` и `/help` открывают секционное inline-меню с Back-навигацией; callbacks проходят через тот же Dispatcher и allowlist.
- Альбомы до 10 фото JPEG/PNG, voice, аудиофайлы и video notes через Whisper. В RAG (`/docs add`) принимаются текстовые документы UTF-8 до 64 КиБ и PDF/DOCX до 20 МиБ. Медиа-лимиты настраиваются `MEDIA_MAX_MB` и `SPEECH_MAX_SECONDS`.
- История и независимый контекст каждого чата в SQLite/WAL.
- Безопасный OpenAI function calling: локальный calculator, opt-in `read_url` для чтения HTTPS-страниц и opt-in `web_search` через self-hosted SearxNG; без shell, файлов и произвольного кода. Поддерживаются обычные и streaming ответы.
- Allowlist по Telegram user id, rate limiting, per-chat lock и ограничение параллельных запросов к модели.
- Обработка timeout/5xx/пустых ответов, Telegram 429 и восстановление незавершённых вопросов после рестарта.
- Persistent one-shot и ежедневные напоминания с управлением списком и plain-text доставкой без вызова модели.
- «Ссылки на потом»: сохранение страниц, поиск и резюме без повторной загрузки.
- Многошаговые read-only задачи с планом, прогрессом и подтверждениями сетевых действий.
- Opt-in `/tts` для русской речи через существующий Speaches sidecar и Telegram `sendAudio`.
- Версионированные миграции через `schema_migrations` (текущая схема — версия 17).
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

Полный список из 68 ENV-строк и их значения по умолчанию находится в `.env.example`; строки `STREAMING_*`, `WEBHOOK_*`, `MINIAPP_*`, `HEALTH_*` и `RAG_EMBEDDINGS_*` описаны в одноимённых разделах ниже. Пустой `TELEGRAM_ADMIN_USERS` закрывает `/admin` для всех.

### Основные параметры

| Параметр | По умолчанию | Назначение |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | обязательный | Telegram token, без значения запуск запрещён |
| `STRATA_BASE_URL` | `http://host.docker.internal:8000` | OpenAI-compatible base URL без обязательного `/v1` |
| `STRATA_MODEL` | `qwen2.5-7b-instruct` | id модели из `GET /v1/models` |
| `STRATA_FALLBACK_MODEL` | пусто = fallback выключен | резервная модель после retryable-ошибки основной |
| `STRATA_FALLBACK_API_KEY` | пусто | ключ резервного эндпоинта, если он не совпадает с основным (иначе `STRATA_API_KEY`) |
| `STRATA_FALLBACK_BASE_URL` | `STRATA_BASE_URL` | отдельный endpoint fallback; absolute http(s) без userinfo |
| `STRATA_FALLBACK_TIMEOUT_SECONDS` | 120 | таймаут одной попытки резерва: модель может загружаться по требованию |
| `STRATA_TRANSCRIPTION_MODEL` | `Systran/faster-whisper-small` | модель для `/audio/transcriptions` |
| `TRANSCRIPTION_BASE_URL` | `STRATA_BASE_URL`; в compose — Whisper | отдельный transcription endpoint |
| `STRATA_API_KEY` | пусто | ключ endpoint, если требуется |
| `STRATA_TIMEOUT_SECONDS` | 60 | общий бюджет всех попыток одной модели (не таймаут одной попытки) |
| `TG_REPLY_DEADLINE_SECONDS` | 90 | общий бюджет ответа; должен быть больше timeout |
| `RECOVERY_MAX_AGE_HOURS` | 24 | максимальный возраст unanswered-вопроса для автоматического recovery; `0` отключает лимит |
| `REMINDER_TIMEZONE` | `UTC` | IANA timezone для напоминаний, например `Europe/Moscow` |
| `TTS_ENABLED` | false | opt-in `/tts` через существующий Speaches |
| `TTS_MODEL` | `speaches-ai/piper-ru_RU-ruslan-medium` | русская Piper-модель |
| `TTS_VOICE` | `ruslan` | voice для `/v1/audio/speech` |
| `TTS_MAX_CHARS` | 1000 | максимум runes текста; 1..10000 |
| `TTS_TIMEOUT_SECONDS` | 60 | timeout синтеза; 1..300 |
| `USER_DAILY_MODEL_LIMIT` | 0 = без ограничений | дневной лимит model calls пользователя по UTC |
| `USER_DAILY_NETWORK_TOOL_LIMIT` | 0 = без ограничений | дневной лимит read_url/web_search пользователя по UTC |
| `USER_LIMITS_JSON` | `{}` | per-user overrides: `{"id":{"model":N,"network_tools":N}}` |
| `MINIAPP_ENABLED` | false | opt-in Telegram Mini App |
| `MINIAPP_TELEGRAM_FALLBACK` | true | глобальное значение по умолчанию: ответ, досчитанный после закрытия Mini App, отправляется в Telegram; для конкретного чата переопределяется `/miniapp` и настройкой Mini App |
| `MINIAPP_LISTEN_HOST` | `127.0.0.1`; в compose — `0.0.0.0` | listener Mini App |
| `MINIAPP_PORT` | 8084 | loopback port Mini App |
| `MINIAPP_PUBLIC_URL` | пусто | обязательный HTTPS URL при `MINIAPP_ENABLED=true` |
| `MEDIA_MAX_MB` | 10 | лимит каждого media-файла и альбома целиком |
| `SPEECH_MAX_SECONDS` | 300 | лимит voice/audio/video note |
| `DB_PATH` | `./data/bot.db` | host SQLite; в compose — `/data/bot.db` |
| `TELEGRAM_ALLOWED_USERS` | пусто = deny by default | список user id через запятую |
| `CONTEXT_MESSAGES` | 20 | строк истории, из которых собираются ходы окна |
| `MAX_CONTEXT_CHARS` | 8000 | бюджет текста истории: ход входит целиком или не входит; вопрос нового хода сохраняется всегда |
| `MAX_INPUT_CHARS` | 4000 | ограничение входного текста |
| `RAG_MAX_DOCUMENTS` | 20 | документов на чат; замена по имени не увеличивает usage |
| `RAG_RESULT_LIMIT` | 3 | максимум фрагментов в model request |
| `RAG_MAX_CONTEXT_CHARS` | 2000 | общий бюджет RAG-фрагментов |
| `RAG_EMBEDDINGS_ENABLED` | false | opt-in гибридный RAG: embeddings + FTS5 |
| `RAG_EMBEDDINGS_BASE_URL` | `http://host.docker.internal:1234/v1` | OpenAI-compatible base URL, `/v1` включён |
| `RAG_EMBEDDINGS_MODEL` | `text-embedding-nomic-embed-text-v1.5` | id embedding-модели; ключ сохранённых векторов |
| `RAG_EMBEDDINGS_TIMEOUT_SECONDS` | 10 | timeout одного запроса; 1..120 |
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

### Резервная модель (fallback, opt-in)

Если endpoint отдаёт **ровно одну** модель и её имя не совпадает с `STRATA_MODEL`, бот на старте принимает имя загруженной модели (в лог уходит `WARN` с обоими именами). Это нужно одномодельной STRATA: она может отвечать загруженной моделью на любой запрос или отклонить имя, которого больше нет. При недоступном списке или нескольких моделях конфигурация не меняется — угадывать в неоднозначном случае бот не будет.

`STRATA_FALLBACK_MODEL` включает один повторный запрос на резервную модель. Fallback срабатывает только на retryable-ошибку основной: 5xx, 429, transient error внутри 200-стрима или сетевой сбой; 400/401/404 и ошибки разбора возвращаются как есть. `STRATA_FALLBACK_BASE_URL` задаёт отдельный endpoint, пустое значение означает тот же `STRATA_BASE_URL`; непустой URL обязан быть absolute http(s) без userinfo, а та же модель на том же endpoint отклоняется как ошибка конфигурации. `STRATA_FALLBACK_TIMEOUT_SECONDS` (по умолчанию 120) — таймаут одной попытки резерва: локальный сервер вроде LM Studio загружает модель на первый запрос (27B холодной занял ~61 с), и общий таймаут основной модели его бы обрезал. Частично доставленный стрим не переигрывается: если хотя бы один `delta` уже ушёл в чат, второй запрос не делается и возвращается исходная ошибка, поэтому ответ не дублируется. Список `/model` по-прежнему строится по основной `GET /models`, а явно выбранная fallback-модель работает как обычная per-chat модель и не сохраняется как fallback.

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

### Документы RAG: текст, PDF и DOCX

`/docs add` и `POST /api/documents` используют один вход `bot.ExtractDocumentText`, поэтому набор форматов и лимиты совпадают:

| Формат | Лимит файла | Что сохраняется |
|---|---|---|
| UTF-8 текст (`.txt`, `.md`, `.json`, `.csv`, `.xml`, `.log`, код и т. п.) | 64 КиБ (`DocumentMaxBytes`) | файл как есть, после прежней проверки UTF-8/NUL/control |
| PDF (`.pdf`, `application/pdf`) | 20 МиБ (`DocumentBinaryMaxBytes`) | текстовый слой; до 200 000 runes (`DocumentTextMaxRunes`) |
| DOCX (`.docx`, `application/vnd.openxmlformats-officedocument.wordprocessingml.document`) | 20 МиБ | текст `word/document.xml`; до 200 000 runes |
| `.doc` (старый binary) | — | не поддерживается: «сохрани как .docx или .pdf» |

20 МиБ — это ровно предел Telegram Bot API `getFile`, поэтому Mini App не примет файл, который бот всё равно не смог бы скачать. `MEDIA_MAX_MB` к PDF/DOCX не применяется; для `.txt` по-прежнему 64 КиБ, а не 10 МиБ. Тело `POST /api/documents` ограничено 20 МиБ + 1 МиБ multipart-overhead.

PDF разбирается чистым Go-пакетом `github.com/ledongthuc/pdf` (MIT, без cgo); DOCX — стандартными `archive/zip` + `encoding/xml`, без отдельной зависимости. PDF без текстового слоя (скан) и PDF с паролем отклоняются с понятной ошибкой: OCR в боте нет, пароль не подбирается. Извлечённый текст нормализуется (валидный UTF-8, без NUL/control кроме `\n\t`, пустые строки схлопываются, края обрезаются) и при превышении 200 000 runes обрезается с маркером. Дальше текст идёт в прежние chunking/`RAG_MAX_DOCUMENTS` и best-effort embeddings.

Ожидание по ресурсам: извлечение синхронное и ограничено входом в 20 МиБ; в памяти парсер держит объекты PDF и извлечённый текст, а FlateDecode-поток может временно распаковаться в несколько раз больше сжатого размера. У DOCX чтение `word/document.xml` ограничено 16 МиБ, а собранный текст — 200 000 runes, поэтому zip-bomb не раздувает память. На 20 МиБ PDF рассчитывайте на единицы секунд; Mini App ограничивает число одновременных запросов, Telegram обрабатывает чат последовательно.

### Гибридный RAG: embeddings + FTS5 (opt-in)

Лексический поиск не находит перефразировки. Гибридный слой считает эмбеддинг запроса и смешивает его с существующим BM25-lite score:

```
final = (1 - w) * lexicalNorm + w * cosine(query, chunkVector),  w = 0.4
```

`lexicalNorm` — прежний score, нормированный к лучшему score запроса; `cosine` — косинус между вектором запроса и вектором фрагмента. Фрагмент без вектора этой модели даёт cosine 0, а фрагмент, который не задел ни один термин запроса, всё равно оценивается, если попал в 64 ближайших по косинусу кандидата чата — именно так находится перефразировка. Если векторов нет вообще, результат совпадает с прежним лексическим ранжированием. Порог `0.15` отсекает слабые vector-only совпадения. Вес `0.4` — константа скорера (`store.DefaultEmbeddingWeight`), а не строка `.env`: качество ранжирования проверяется тестом, а не настраивается на проде.

Сервер — OpenAI-compatible; по умолчанию локальный `host.docker.internal:1234/v1`. В compose `extra_hosts: host.docker.internal:host-gateway` уже есть, а ручному контейнеру нужен флаг `--add-host host.docker.internal:host-gateway`. Модель по умолчанию — `text-embedding-nomic-embed-text-v1.5`, 768 измерений.

```env
RAG_EMBEDDINGS_ENABLED=true
RAG_EMBEDDINGS_BASE_URL=http://host.docker.internal:1234/v1
RAG_EMBEDDINGS_MODEL=text-embedding-nomic-embed-text-v1.5
RAG_EMBEDDINGS_TIMEOUT_SECONDS=10
```

Новый документ эмбеддится при `/docs add`. Уже сохранённые фрагменты заполняет backfill — только на копии базы, не на живом volume:

```powershell
go run ./cmd/ragembed -path .\data\ragembed-smoke.db -base-url http://127.0.0.1:1234/v1
# или из test-образа, если сервер виден только через host-gateway:
docker run --rm --add-host host.docker.internal:host-gateway -v "${PWD}/data:/data" tg-llm-proxy-test:latest `
  go run ./cmd/ragembed -path /data/ragembed-smoke.db -base-url http://host.docker.internal:1234/v1
```

Флаги: `-path`, `-base-url`, `-model`, `-chat` (0 = все чаты с фрагментами), `-batch` (не больше 64). Команда идемпотентна: фрагменты с вектором выбранной модели пропускаются, прерванный прогон продолжается с места остановки. Exit code ≠ 0 только если не удалось заэмбеддить ни одного фрагмента. Метрики: `rag_embeddings_total{outcome="ok|error"}` и `rag_embedding_dimensions`.

Если сервер эмбеддингов недоступен, поиск молча возвращается к лексическому ранжированию: запрос пишет одно предупреждение в лог и не падает, а документ после неудачного `/docs add` остаётся сохранённым и находимым по словам.

### Подтверждение сетевых действий

RAG, документы, транскрипты и веб-контент считаются untrusted-данными и не авторизуют новые сетевые действия. Точный HTTPS URL из запроса пользователя выполняется автоматически. Web search выполняется автоматически только при явном search intent и nonempty query, буквально присутствующем в исходном тексте пользователя без учёта регистра; расширенный или переформулированный моделью query требует подтверждения. Другой origin или автономный вызов также требует inline-подтверждения: оно привязано к chat/user/message и canonical arguments, действует 5 минут и работает однократно. `Разрешить для задачи` дополнительно разрешает только текущую задачу/continuation: для `read_url` — тот же canonical origin, для `web_search` — тот же tool; новый origin требует нового подтверждения. Cancel, expiry, restart, Mini App lifecycle и replay не выполняют network action; Confirm продолжает исходный tool loop.

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

Если во время стриминга в чат успевает прийти следующий вопрос, готовый ответ отправляется отдельным сообщением (ниже вопроса), а placeholder удаляется — иначе правка оставила бы ответ над вопросом, на который он отвечает. `STREAM_DRAFTS_ENABLED=true` включает приватным чатам Bot API draft-превью вместо правок; по умолчанию выключено, потому что черновик закреплён на позиции старта ответа и при следующем вопросе визуально оказывается выше него.

### Озвучка текста (opt-in)

```env
TTS_ENABLED=true
TTS_MODEL=speaches-ai/piper-ru_RU-ruslan-medium
TTS_VOICE=ruslan
TTS_MAX_CHARS=1000
TTS_TIMEOUT_SECONDS=60
```

Запускайте `docker compose -f docker-compose.yml -f compose.tts.yaml up -d`: override предзагружает Whisper и одноразово скачивает Piper через `/v1/models`. `/tts <текст>` возвращает MP3 через `sendAudio`, без вызова LLM. `/tts last` так же без вызова модели озвучивает последний ответ ассистента текущего чата. `/tts speed` показывает текущую скорость, а `/tts speed 0.75|1|1.25|1.5` сохраняет её для чата в `bot_state`; выбранное значение используется при запросах к Speaches, по умолчанию — `1`. More → Озвучить текст появляется только при включённом TTS. Изолированная и production acceptance подтвердили скачивание модели и русскую речь в Telegram.

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

При старте listener поднимается до `setWebhook`. Secret header проверяется до чтения body, затем метод POST, JSON и размер тела до 1 MiB. Пустой `WEBHOOK_SECRET_TOKEN` не создаёт listener вообще (fail-closed): без секрета эндпоинт принимал бы поддельные update'ы от кого угодно. In-flight requests ограничены 16, headers — 16 КиБ. Pending updates не удаляются, `getUpdates` не запускается. Для возврата к polling сначала вызовите Telegram `deleteWebhook`.

### Защита от перегрузки

- Active handlers ограничены `MAX_CONCURRENT_MODEL_REQUESTS`; на чат — один active и до четырёх FIFO waiters с queue timeout 2 секунды. Albums используют тот же bounded executor.
- Denied/bootstrap/overload replies coalesce, а отклонённая работа не скачивает вложения, не вызывает модель и не создаёт command/history rows.
- Streaming ограничен 100000 runes, tool argument — 16384 runes, один ответ — 64 Telegram-сообщениями.
- Model work — `3 × RATE_LIMIT_PER_MINUTE` на пользователя; network tools — 8/мин/user и 4 global in flight.
- RAG использует до 32 значимых RU/EN терминов из 4000 runes запроса, Unicode token boundaries и sentence-aware chunks. Кандидаты отбирает FTS5-индекс (`rag_chunks_fts`) по всей базе чата, поэтому корпус больше не сканируется целиком; при числе совпадений выше лимита `/docs search` и automatic RAG context явно сообщают об усечении. С `RAG_EMBEDDINGS_ENABLED=true` к лексическим кандидатам добавляются ближайшие по косинусу фрагменты и score смешивается по формуле `(1-w)*lexical + w*cosine` (w = 0.4); без векторов результат прежний. Экспорт истории читает явный диапазон id без длинной транзакции, поэтому не блокирует записи бота, и сериализуется инкрементально с остановкой на 5 МиБ.
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
| `/remind <время> <текст>` | `через 1 минуту`, `через 2 часа` или `каждый день 09:00` по `REMINDER_TIMEZONE`; текст до 500 runes |
| `/reminders` | список и inline-меню активных напоминаний; `cancel <id>` или `delete <id>` отменяет активную запись; в меню есть создание и до 5 недоставленных с Retry/Delete |
| `/links` | сохранённые ссылки; `add <url>`, `search <текст>`, `summary <id>`, `delete <id>`; `add` читает страницу, поэтому работает только при `URL_READER_ENABLED=true` и `TOOLS_ENABLED=true` |
| `/task <цель>` | read-only задача из 1–3 шагов с прогрессом; без аргумента — статус, `/task cancel` — отмена, `/task resume <id>` — возобновление только последней failed/cancelled текущего чата с сохранением прогресса, `/task clear` — очистка списка с подтверждением |
| `/tts <текст>` | opt-in озвучка через Speaches и `sendAudio`; `/tts last` — последний ответ ассистента, `/tts speed [0.75\|1\|1.25\|1.5]` — скорость чата; по умолчанию выключено |
| `/docs` | RAG-база; `/docs add` (текст UTF-8, PDF, DOCX), `search <query>`, `export`, `delete <id>` и `clear`; поиск показывает до 3 фрагментов с document id и score |
| `/export [md\|json]` | экспорт последних 10000 сообщений с явным truncation warning и ID/time window |
| `/search <текст>` | до 20 совпадений по всей истории чата через FTS5-индекс; префиксный поиск по последнему слову, операторы FTS трактуются как текст |
| `/delete` | одноразовые Confirm/Cancel с TTL 5 минут, привязкой к пользователю и сообщению; настройки сохраняются |
| `/model [check]` | модель, endpoint и проверка доступности |
| `/model select <id>` | выбрать модель текущего чата из `GET /models` |
| `/model reset` | вернуть модель по умолчанию `STRATA_MODEL` |
| `/stream [on\|off\|reset]` | стриминг текущего чата; без аргумента — состояние |
| `/persona` | inline-меню и текущая роль; `assistant`, `coder`, `writer`, `translator`, `custom <текст>`, `reset` |
| `/stats` | сообщения, токены и ошибки текущего чата |
| `/usage` | дневные вызовы и tokens по моделям, использование tools и остаток лимитов без текстов запросов |
| `/whoami` | `user_id`, `chat_id` и состояние allowlist |
| `/admin` | агрегаты, usage RAG/calculator/read_url, активные model calls и busy chats; только для `TELEGRAM_ADMIN_USERS` |

## Интерактивное меню

`/start` и `/help` открывают секционное inline-меню: Ask/New context, History, Documents, Settings, Status, Help и More. History поддерживает последние сообщения, поиск, MD/JSON export и удаление; Documents — добавление, поиск через pending input, export, удаление документов и «Ссылки на потом» с list/search/summary/delete; Settings — Model, Persona и Stream. More содержит напоминания, многошаговые задачи, opt-in озвучку текста и `/usage` с дневной статистикой. Admin виден только администраторам.

Все навигационные и информационные callback-кнопки редактируют исходное сообщение меню, включая Stats, Ping, History, Reset, подтверждения и Back. Обычные текстовые команды по-прежнему отправляют ответы отдельно. При ошибке редактирования бот не создаёт дублирующее меню снизу. Отдельные сообщения остаются только для export-документов, TTS-аудио и результатов выполнения запросов/задач.

При включённом Mini App нижняя кнопка «Меню» рядом с полем ввода публикуется как `MenuButtonWebApp` и сразу открывает companion. При выключенном Mini App публикуется обычная кнопка команд.

Меню напоминаний создаёт одноразовые записи через 10 минут, 1 час или завтра в 09:00 по `REMINDER_TIMEZONE`. Раздел «Недоставленные» показывает до 5 последних failed текущего чата: Retry снова активирует запись с нулевым `attempts`, Delete удаляет только failed текущего чата. Для одноразового напоминания после пятой ошибки доставки бот пытается отправить финальное уведомление (best-effort). Atomic claims, backoff и расчёт daily-времени с учётом DST не изменились.

Ask, Search, Custom Persona и создание напоминания принимают следующий текст через pending input с TTL 2 минуты и кнопкой Cancel. Состояние привязано к chat/user/menu message и инвалидируется при рестарте. Все текстовые команды остаются доступными. Обычные запросы показывают Telegram `typing`, а streaming tools — только общие стадии без URL и поисковых запросов.

## Telegram Mini App (opt-in)

Mini App доступен **только в приватных чатах**: его сессия привязана к чату, поэтому в группе кнопка не публикуется, а тикет не выдаётся (`ErrPrivateOnly`).

More → Mini App открывает companion со Status, History, Documents, Links, Tasks, Reminders, Usage и Settings. Для включения задайте `MINIAPP_ENABLED=true` и HTTPS `MINIAPP_PUBLIC_URL`; внутри контейнера listener `0.0.0.0`, host-порт бота остаётся loopback-only `127.0.0.1:8084`.

`MINIAPP_TELEGRAM_FALLBACK` — глобальное значение по умолчанию. Для конкретного чата его переопределяют команда `/miniapp on|off|reset` в Telegram и переключатель «Mini App» в Settings: строка `chat_miniapp_fallback` (миграция 17) побеждает env, а `reset` возвращает env. Тот же выбор отдаёт `GET /api/settings` (`miniapp_fallback: {enabled, source}`) и меняют `PUT`/`DELETE /api/settings/miniapp-fallback`.

### Голосовой ввод в чате Mini App

В composer вкладки «Чат» рядом с «Отправить» есть кнопка микрофона (44 px, доступная подпись, `aria-pressed` во время записи). Запись идёт через `MediaRecorder` с первым поддерживаемым MIME: `audio/webm;codecs=opus`, затем `audio/ogg;codecs=opus`, `audio/ogg`, `audio/mp4`. Пока идёт запись, под полем показаны секунды и кнопки «Остановить запись»/«Отменить запись»; отмена останавливает дорожки и ничего не отправляет. После остановки клип уходит на `POST /api/transcribe` (`multipart/form-data`, одна часть `file`), а полученный текст вставляется в поле ввода (обрезанный по краям, не более 2000 символов) и поле получает фокус. Автоотправки нет: текст можно отредактировать и отправить обычным сообщением.

`POST /api/transcribe` использует тот же `model.Transcriber`, что и голосовые в Telegram, и ту же проверку: allow-list аудио MIME/расширений и сигнатуры контейнера, лимит `MEDIA_MAX_MB`, пустое/слишком короткое аудио, allowlist пользователя и per-minute бюджет. Ответ — `200 {"text":"…"}`; ошибки: `400 invalid_body` (нет части `file`, лишняя часть, query-параметры), `400 unsupported_audio`, `400 audio_too_short`, `401` без bearer, `405` для не-POST, `413 body_too_large`, `415 content_type`, `429 rate_limited` (общий лимит mutations 30/мин), `500 transcription_failed`. Тело запроса ограничено `MEDIA_MAX_MB` + 64 КиБ multipart-overhead; аудио не сохраняется ни в `messages`, ни в RAG, ни в audit (audit содержит только operation/chat_id). Успешная транскрипция, как и другие mutations, пишет `miniapp_mutation` с `operation=transcribe`.

Браузерная поддержка: нужен `MediaRecorder` и `navigator.mediaDevices.getUserMedia` (HTTPS или localhost). В iOS Safari `MediaRecorder` может отсутствовать (старые версии WebView): кнопка остаётся видимой, но показывает локализованное сообщение «Браузер не поддерживает запись звука…», и голосовое можно отправить в Telegram — там путь транскрипции прежний. Отказ в доступе к микрофону, слишком короткая запись и обрыв сети также показываются локализованно (RU/EN).

Production Telegram запускается всеми override-файлами одной командой:

```powershell
docker compose -f docker-compose.yml -f compose.websearch.yaml -f compose.tts.yaml -f compose.production.yaml up -d
```

`compose.production.yaml` сохраняет `MINIAPP_PUBLIC_URL=https://desktop-hvobbn7.tail66517e.ts.net/miniapp`. Gateway больше не входит в этот Compose-проект: он находится в независимом проекте `C:\Kimi code\edge-gateway` с Compose project name `edge-gateway` и по-прежнему публикует только `127.0.0.1:8086:8080`.

Telegram `bot` одновременно подключён к обычной Compose-сети `default` и внешней `edge-telegram`, где имеет alias `telegram-miniapp`. Whisper и SearxNG остаются только на `default` и не подключаются к edge-сетям; их исходящий доступ для загрузки моделей и поисковых запросов сохранён. Перед запуском создайте `edge-telegram` по инструкции `edge-gateway/README.md`.

Маршруты независимого Traefik:

- `/miniapp` и `/miniapp/*` → `http://telegram-miniapp:8084`. Middleware `stripPrefix` снимает `/miniapp`, а `X-Forwarded-Prefix: /miniapp` сохраняет публичный префикс. Static, `/miniapp/api/session` и остальные API используют тот же backend.
- `/max` и `/max/*` → `http://max-bot:8090` через external-сеть `edge-max`. Path передаётся без изменений, кроме `/max/healthz`, который переписывается в `/healthz`.
- `/` и все остальные пути → dashboard `http://dashboard:8787` через external-сеть `edge-dashboard` как fallback без изменения path.

Health и прямые listeners не меняются: bot health остаётся на `127.0.0.1:8080/healthz`, Mini App без gateway по-прежнему принимает запросы без префикса на `127.0.0.1:8084`, а прямой max на `127.0.0.1:8090` отвечает `/healthz` = 200; ответ 404 на его корне `/` допустим и не означает сбой gateway.

Traefik использует только static file provider: `/etc/traefik/traefik.yml` и `/etc/traefik/dynamic.yml` монтируются read-only. Docker provider и Docker socket не используются. Встроенные Traefik API/dashboard не доступны на production entrypoint: локальный dashboard с Basic Auth работает на `127.0.0.1:8089`, а Prometheus/Grafana описаны в `edge-gateway/README.md`; маршрут `/` относится к приложению dashboard на порту 8787. У standalone gateway нет зависимости от Telegram `bot` или `host-gateway`: backends разрешаются через network aliases.

Контейнер gateway работает non-root с read-only rootfs, `cap_drop: ALL`, `no-new-privileges` и tmpfs; заданы memory/CPU/PID limits и ротация Docker logs. Auth headers, cookies, Telegram `initData`, bearer sessions, токены и ключи не записываются в access/error logs. Логи не должны использоваться для вывода секретов при диагностике маршрутов.

### Запуск и миграция gateway

Создание трёх external-сетей, порядок пересоздания backends, cutover без остановки текущего процесса миграции и rollback описаны в `C:\Kimi code\edge-gateway\README.md`. Acceptance gateway находится там же: `C:\Kimi code\edge-gateway\scripts\test-traefik.ps1`. Он использует только временные контейнеры и сети, не читает `.env` и не пересоздаёт production services.

`deploy/Caddyfile` и `scripts/test-gateway.ps1` сохранены только как исторические rollback-артефакты прежнего Caddy на `127.0.0.1:8085`. Прежние `deploy/traefik.yml` и `deploy/dynamic.yml` также оставлены для обратного запуска остановленного `tg-llm-proxy-gateway-1`: его bind-mounts ссылаются на эти файлы. Ни Caddy, ни старый Traefik не входят в текущий Compose и не являются активным gateway; production endpoint обслуживает standalone Traefik на `127.0.0.1:8086`. Статус каждого файла и то, чего у старого gateway нет (TLS, Docker provider, hardening), описаны в `deploy/README.md`.

Прямой запуск Mini App без любого gateway сохраняет прежнее поведение: reverse proxy/tunnel направляет публичный URL без `/miniapp` на `127.0.0.1:8084`, и префикс приложению не требуется.

Raw `initData` проверяется через Telegram HMAC и `auth_date`, session — на 15 минут. Inline launch ticket привязан к chat/user на 2 минуты и остаётся одноразовым. Direct launch через нижнюю кнопку работает без ticket только с private scope `ChatID=UserID`, полученным из подписанного `user.id`; client-supplied `chat_id` не используется. Доступ и allowlist проверяются при каждом запросе. Нет model keys, task plans/results или tool arguments. Настройки Model/Persona/Stream/Mini App fallback работают через те же store-операции и проверку allowlist, без прямых обращений браузера к STRATA. Интерфейс по умолчанию русский, есть переключатель RU/EN и адаптивная мобильная вёрстка без горизонтального скролла. `watch_container.ps1` дополнительно проверяет `/healthz` при включённом Mini App.

History поддерживает поиск через `GET /api/history/search?q=...`, полный просмотр через `/api/history/item`, MD/JSON export с bearer fetch/Blob, удаление отдельных user/assistant сообщений и очистку с подтверждением. Links поддерживает добавление HTTPS-страниц через общий Dispatcher lifecycle, поиск, полный просмотр и delete/clear. Reminders поддерживает создание once/daily, Cancel active и Retry/Delete failed; Tasks — создание безопасного плана через общий Dispatcher lifecycle, Cancel/Resume/Delete/Clear. Settings при включённом TTS позволяет выбрать скорость 0.75/1/1.25/1.5 через `/api/settings/tts-speed` или вернуть 1. Bearer mutations ограничены 30/min/user, History export — 5/min/user с HTTP 429 и `Retry-After`.

Mini App использует современную responsive-навигацию, карточки, badges, inline creation forms с валидацией, loading/notices, focus-visible и reduced-motion transitions. Управление с клавиатуры поддерживает ArrowLeft/ArrowRight/Home/End; разрушительные действия требуют inline Confirm/Cancel.

Documents поддерживает загрузку текстового файла, PDF и DOCX через `POST /api/documents` (`multipart/form-data`, одна часть `file`, необязательный `name`) с теми же типами и лимитами, что `/docs add`: UTF-8 текст без NUL до 64 КиБ, PDF/DOCX до 20 МиБ (предел Bot API `getFile`), `RAG_MAX_DOCUMENTS` на чат; chat/user берутся из сессии, тело запроса ограничено `DocumentBinaryMaxBytes` + 1 МиБ multipart-overhead, ответ `201 {"id","name","chunks","embedded"}` без текста документа. Для PDF/DOCX сохраняется извлечённый и нормализованный текст, а не binary-байты; скан без текстового слоя и `.doc` возвращают `400 document_no_text`/`unsupported_document`. Дальше Documents поддерживает поиск через `GET /api/documents/search?q=...` с corpus truncation warning, удаление отдельного документа вместе с RAG-фрагментами через `POST /api/documents/{id}/delete` и очистку базы текущего чата через `POST /api/documents/clear`. Links позволяет удалить отдельную ссылку через `POST /api/links/{id}/delete` или очистить весь список текущего чата через `POST /api/links/clear`. Tasks показывает для failed sanitized error и expiry. Cancel отменяет active, Resume возобновляет последнюю failed/cancelled через общий TaskLifecycle; network approvals остаются только в Telegram. Отдельная terminal задача удаляется через `POST /api/tasks/{id}/delete`; active нужно сначала отменить. Для любого непустого списка доступна общая очистка через `POST /api/tasks/clear`. Reminders показывает active/failed и поддерживает Cancel active, Retry/Delete failed. Все удаления и очистки требуют отдельного inline Confirm/Cancel и строгого пустого POST body без query params. Loading/mutations показывают индикатор и блокируют повторные действия. Успешные mutations создают best-effort `miniapp_mutation` audit только с operation/chat_id/numeric id/count, без текстов. Вкладка Usage получает данные через `GET /api/usage` и показывает за UTC-сутки model calls и tokens по моделям, вызовы calculator/RAG/read_url/web_search, дневные limits/remaining; для unlimited `remaining` равен `null`. Вкладка не содержит текстов запросов, целей задач и event detail.

### «no available server» в Mini App

Этот текст — ответ Traefik, а не бота: роутер отдал 503, потому что у его сервиса нет доступных backend'ов. Проверяйте по порядку:

1. `curl -i http://127.0.0.1:8086/miniapp/healthz` — **200** значит, что шлюз видит контейнер бота; **503** значит, что active health check сервиса `miniapp` (`/healthz`, каждые 10 с) не проходит: контейнер не в сети `edge-telegram`, listener не на `0.0.0.0:8084` или `MINIAPP_ENABLED=false`. Прямой probe `http://127.0.0.1:8084/healthz` при этом может отвечать 200 — это проверка через публикацию порта, а не из сети шлюза.
2. `MINIAPP_PUBLIC_URL` обязан содержать публичный префикс, под которым шлюз отдаёт Mini App: с Traefik это `https://host/miniapp`. Если путь пустой (`https://host`), Telegram открывает корень сайта, а `/` маршрутизируется на dashboard — при остановленном dashboard это и есть страница «no available server». При старте бот пишет `miniapp_public_url` и предупреждает `MINIAPP_PUBLIC_URL has no path`.
3. После правки `MINIAPP_PUBLIC_URL` контейнер нужно **пересоздать**: меню-кнопку (`setChatMenuButton`) и inline-кнопки бот публикует при старте, поэтому старый процесс продолжает отдавать старый URL. Уже отправленные в чат кнопки содержат URL на момент отправки — открывайте Mini App из обновлённой нижней кнопки меню.

```powershell
docker compose -f docker-compose.yml -f compose.production.yaml up -d --force-recreate bot
```

## Проверка проекта

```powershell
.\scripts\verify.ps1                      # полный pipeline
.\scripts\verify.ps1 -WithVolumeRestore   # + изолированная volume restore rehearsal
.\scripts\verify.ps1 -SkipRace -SkipHealth
.\scripts\test-maintenance.ps1 -WithVolumeRestore
..\edge-gateway\scripts\test-traefik.ps1   # изолированные Compose и HTTP gateway regressions
```

Pipeline включает gofmt/vet, PowerShell parser, unit/store, fixture backup/restore/external-copy/rotate/prune/migration, loopback Telegram alerts, fake Telegram + fake STRATA, build, secret leak scan и Docker race. `-WithVolumeRestore` добавляет volume restore и container-monitor acceptance. Явные package roots исключают `zzscratch`; `go mod tidy -diff` проверяет module files без изменений. Docker base images и CI images/tools закреплены по digest/версии.

`scripts/restore-drill.sh` — та же rehearsal без Docker: fixture-база во временном каталоге, backup, restore в новый каталог (замена восстановленному volume), сравнение `integrity`, `schema_version`, `table_*` и `events_*` с оригиналом, проверка read-write и идемпотентности повторного restore. В GitLab CI job `restore-drill` выполняет его на Linux в образе `golang:1.27-bookworm` и блокирует pipeline; `--keep` оставляет временный каталог для разбора. Windows-сценарий `.\scripts\test-maintenance.ps1 -WithVolumeRestore` с изолированным Docker volume и container monitor остаётся для ручного запуска на self-hosted Windows runner.

### Unit и integration без Windows test listeners

Начиная с v1.37.2 unit/store и integration Go tests по умолчанию выполняются в Linux test image `tg-llm-proxy-test:latest`. Это единый стандарт для разработчика и CI: вызывайте обёртки без `-Host`, а runner должен предоставлять рабочий Docker. Первый запуск собирает test stage из текущих исходников:

```powershell
.\scripts\test.ps1                       # unit/store в Docker, включая сборку test image
.\scripts\smoke.ps1 -SkipHealth          # integration в Docker + остальные smoke-проверки
```

`-SkipBuild` у обоих скриптов пропускает сборку и использует существующий `tg-llm-proxy-test:latest`. Применяйте его только для повторного прогона того же кода; после изменения исходников, `go.mod` или Dockerfile сначала пересоберите образ без `-SkipBuild`. Общий `verify.ps1` использует тот же Docker default для unit/store и integration.

На Windows штатный путь не запускает `go test` на хосте. Все `httptest` listeners fake Telegram/fake STRATA находятся внутри контейнера, поэтому host-процессы `*.test.exe` не создают listeners и не вызывают Windows Firewall prompts. `-Host` — явный fallback для сред без Docker:

```powershell
.\scripts\test.ps1 -Host
.\scripts\smoke.ps1 -Host -SkipHealth
```

Host fallback сохраняет прежние `go test` и на Windows может снова вызвать firewall prompt; он не является стандартом для разработки или CI. Прямой host `go test ./...` также не заменяет штатные обёртки.

Существующий race path не изменён: `scripts\test-race.ps1` по-прежнему собирает Docker target `test` и запускает `go test -race` в контейнере; `-Tagged` включает integration, `-SkipBuild` повторно использует race image. В `verify.ps1` для этого по-прежнему предусмотрен `-SkipRaceBuild`.

`docker-build.ps1`, `test.ps1` и `test-race.ps1` сериализуют только Docker image builds кроссплатформенным named mutex, уникальным для полного repo root. Ожидание ограничено 30 минутами; `go test` и `-SkipBuild` lock не используют. Лёгкий двухпроцессный acceptance запускается через `.\scripts\test-docker-build-lock.ps1` и не требует Docker.

GitLab `frontend` job дополнительно выполняет `node --check internal/webapp/static/app.js` и `node --test internal/webapp/frontendjs/*.test.js` в pinned Node 22 image. Это 11 executable DOM/fetch-сценариев фактического `app.js` без npm-зависимостей. При локальном Node 22 команды можно запустить теми же аргументами; на host без Node используется контейнерный вариант.

Release CI — `.gitlab-ci.yml`: Docker unit/integration checks, race и `govulncheck`. Shared GitLab pipeline запускается только для `main` или вручную; `develop` и tags не расходуют shared minutes. Локальный `verify` остаётся обязательным перед push; CI не должен молча переключаться на `-Host`. Для опционального self-hosted CI без production secrets см. `docs/gitlab-runner-wsl.md`; для стандартного pipeline нужен Docker. GitHub Actions не требуется.

## SQLite maintenance

Проверка базы:

```powershell
.\scripts\db_inspect.ps1 -Counts
.\scripts\db_inspect.ps1 -Integrity
```

`schema_version=0` означает backup до версионирования; при следующем writable `store.Open` он автоматически становится 17 без изменения прикладных строк.

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

Монитор проверяет bot и Whisper, называет сервис в failure state, отправляет алерт при сбое и восстановлении, повторяет неизменный failure не чаще одного раза в 6 часов и не перезапускает контейнер автоматически. Mini App probe выполняется прямым `docker exec` по уже проверенному container ID без дополнительной зависимости от Compose в среде Task Scheduler, имеет timeout 5 секунд и до трёх попыток с паузой 2 секунды: failure устанавливается только после третьей последовательной ошибки, а лог содержит exit codes и сокращённую диагностику без секретов. Во время штатного clean-stop backup проверка пропускается только по свежему lease с живым PID; stale lease вызывает alert и восстанавливает проверки. Backup freshness по умолчанию ограничен 30 часами, подтверждённые копии записывают verification receipt. Дополнительно `watch_runtime.ps1` анализирует последние 10 минут логов: три ошибки Telegram, STRATA или Whisper/transcription вызывают алерт, повтор подавляется на 6 часов. Состояния хранятся в `data/monitoring`. Уведомления используют существующие token/allowlist, не принимают token аргументом и не печатают его.

Метрики и готовность бота отдаются на loopback: `GET http://127.0.0.1:8081/metrics` и `GET http://127.0.0.1:8081/readyz` (проверяет БД и доступность модели). Prometheus из `edge-gateway` скрейпит `bot:8081` по общей docker-сети, держит дашборд `Bot overview` и отправляет алерты (down, ошибки модели, p95, очередь, рост БД, смена схемы) в Telegram через Alertmanager; настройка и список правил — в `C:\Kimi code\edge-gateway\README.md`.

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
- Содержимое одноразовых фото и документов не сохраняется. `/docs add` явно сохраняет текст для RAG (для PDF/DOCX — извлечённый текст, binary-байты не сохраняются); retrieval лексический (FTS5 + BM25-lite), гибридный слой embeddings включается отдельно через `RAG_EMBEDDINGS_ENABLED=true` и по умолчанию выключен. Voice хранится как текст после транскрипции.

## Безопасность

- `.env` и `.env.*` исключены из Git и Docker context; шаблон `.env.example` остаётся tracked. Не помещайте реальные секреты в другие файлы.
- Секреты можно подавать файлом вместо ENV: `TELEGRAM_BOT_TOKEN_FILE`, `STRATA_API_KEY_FILE`, `WEBHOOK_SECRET_TOKEN_FILE` (Docker secrets, systemd credentials, mounted Kubernetes secret). Путь обязан быть абсолютным, файл читается один раз на старте (до 4 КиБ, завершающий перевод строки срезается), а одновременное значение в ENV и в файле отклоняется как ошибка конфигурации — precedence не угадывается.
- `smoke.ps1` проверяет Telegram token, STRATA key и webhook secret без вывода значений.
- На production Windows ACL проекта должны разрешать запись только владельцу/service account и администраторам. Текущий хост ограничен владельцем, SYSTEM и Administrators; при переносе проекта повторите проверку ACL, особенно для scheduler-скриптов и backup.
- Runtime контейнер имеет read-only rootfs, no capabilities и `no-new-privileges`; `/data` остаётся записываемым volume.
- `strata-check.ps1` не принимает секреты аргументом. Для удалённой STRATA требуется HTTPS; URL с userinfo отклоняются. Startup не печатает содержимое `SYSTEM_PROMPT`.

## Документы

- `.env.example` — все настройки (67) с комментариями.
- `RELEASE_NOTES.md` — состав release candidate и результаты проверок.
- `docs/adr/` — Architecture Decision Records: SQLite/один writer, long polling, fail-fast конфигурация, FTS5-индексы, Mini App только для приватных чатов, политика рестарта.
- `deploy/README.md` — статус исторических конфигураций gateway в `deploy/` (не часть текущего Compose).
- `docs/CODE_REVIEW_2026-11-08.md` — код-ревью v1.39.0 и статус трёх спринтов.
- `PLAN.md` — локальный подробный план и история изменений (gitignored).
