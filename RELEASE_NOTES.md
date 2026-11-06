# tg-llm-proxy v1.29.1

## Mini App mobile layout and localization

- Мобильная вёрстка больше не вызывает горизонтальный скролл: вкладки и кнопки переносятся, selects/forms/model names ограничены viewport, responsive layout для ~360px.
- По умолчанию русский. В header добавлен переключатель RU/EN; выбор сохраняется только в localStorage `miniapp-language`.
- Локализованы вкладки, заголовки, кнопки, settings controls, статусы, ошибки и session messages. Переключение языка не вызывает API и сохраняет уже загруженные страницы.
- Dynamic content по-прежнему рендерится через `textContent`, без HTML injection. Добавлены static tests для переводов, language persistence, responsive wrap/viewport caps и отсутствия fixed-width overflow. Визуальный acceptance на физическом устройстве не выполнялся.

# tg-llm-proxy v1.29.0

## Mini App settings

- Mini App получил Settings: Model, Persona и Stream. `GET /api/settings` возвращает effective/global model, до 50 provider models, persona presets и stream source без custom persona text.
- Разрешены только выбор модели из актуального provider list, model reset, persona preset/custom до 4000 runes, persona reset и stream on/off/reset. Используются те же store-операции, что и Telegram-команды.
- Каждый mutation повторно проверяет bearer session, signed chat/user и current allowlist. JSON strict content type/unknown fields/body cap 16KiB; cookie не используется, browser не обращается к STRATA напрямую.
- Frontend добавил Settings с model choices/reset, persona presets/custom editor/reset и stream controls. Dynamic content создаётся через `textContent`, без HTML injection.
- Добавлены успешные settings/reset, wrong session/allowlist, invalid model/persona, unknown fields/methods/body cap, store effects и no-secrets-in-logs regressions. Обычные reads не вызывают chat completions; provider вызывается только для model list/selection.

# tg-llm-proxy v1.28.0

## TTS monitoring and task resume

- Production TTS включён: `TTS_ENABLED=true`. Реальный Speaches `/v1/audio/speech` возвращает русский MP3, HTTP 200 и корректный `audio/mp3`.
- Mini App получил unauthenticated `GET /healthz` с минимальным `{\"status\":\"ok\"}` без DB/secrets. `watch_container.ps1` при `MINIAPP_ENABLED=true` проверяет его через bot container, включая failure/suppression/recovery и отсутствие автоматического restart.
- `/task resume <id>` возобновляет последнюю failed/cancelled задачу текущего чата с тем же ID, goal/model/plan и сохранёнными `next_step`/results. Completed/active/other-chat запросы отклоняются.
- Resume сбрасывает attempts, claim/lease, approval/continuation/error/finished timestamps и продлевает lifetime. Старые network approvals не replayаются: требуется fresh exact-action confirmation. Меню terminal tasks получило Resume.
- Добавлены health endpoint/method/CSP, monitor disabled/failure/suppression/recovery, store transition, progress preservation и fresh-approval regressions. Живой acceptance подтверждён: русский `/tts` работает, после сбоя на втором шаге `/task resume` привёл к успешному завершению задачи.

# tg-llm-proxy v1.27.0

## Read-only Telegram Mini App

- More → Mini App открывает opt-in read-only companion со Status, History, Documents, Links и Tasks. Нет mutations, model calls или webhook dependency.
- Raw `Telegram.WebApp.initData` проверяется через official sorted data-check string и `WebAppData` HMAC-SHA256, constant-time compare и `auth_date` freshness. `initDataUnsafe` и browser-supplied chat_id не доверяются.
- Одноразовый launch ticket привязан к chat/user и действует 2 минуты. Session — random 256-bit bearer на 15 минут, hashed in memory; каждый request повторно проверяет allowlist, restart/revoke инвалидирует.
- API ограничен 20 items/page с explicit before/next_before_id. Нет model keys, document bodies, task plans/results, approval/continuation tokens или tool arguments в ответах.
- Frontend same-origin без CDN/framework, `textContent` вместо HTML injection, CSP, 16KiB headers/body, global in-flight 8 и strict JSON.
- `MINIAPP_ENABLED=false` по умолчанию. Для включения нужны HTTPS `MINIAPP_PUBLIC_URL`, reverse proxy/tunnel и loopback-only порт 8084 на хосте. Внутри контейнера listener `0.0.0.0`, снаружи только `127.0.0.1`.
- Добавлены official HMAC fixture, auth_date/ticket/session expiry, replay/wrong user/chat, allowlist removal, caps/isolation/pagination/CSP и fake Telegram end-to-end regressions. Внешний HTTPS acceptance выполнен через Tailscale Funnel: local/public 200, unauthorized API 401, запуск в Telegram client подтверждён.

# tg-llm-proxy v1.26.3

## Task list cleanup

- `/task clear` и кнопка «Очистить задачи» удаляют все задачи текущего чата после single-use confirmation. Активные network approvals и scoped grants останавливаются, повторное выполнение невозможно.
- `/task clear confirm` остаётся текстовым fallback. Другие чаты и их задачи не затрагиваются.
- Production tasks #1–#3 находились в terminal `failed`, поэтому Cancel для них скрывался. Теперь список можно полностью очистить, после чего создать новую задачу.
- Store/bot regressions подтверждают обязательное подтверждение, изоляцию чатов и отсутствие удаления до Confirm.

# tg-llm-proxy v1.26.2

## Tool round resilience

- Модель больше не завершает задачу ошибкой при более чем двух tool calls в одном round. Выполняются только первые два вызова, остальные получают `{\"error\":\"tool_round_call_limit\"}` без network/usage side effects и остаются в protocol history для финального ответа.
- System instruction явно ограничивает модель двумя calls per round. Disabled/unknown tools и approvals по-прежнему проверяются до первого side effect.
- Regression подтверждает два реальных calculator call, skipped third call с bounded JSON и успешный финальный ответ вместо `model requested more than two tool calls in one round`.

# tg-llm-proxy v1.26.1

## Task recovery and scoped approvals

- Если restart произошёл после consumed approval, но до завершения шага, задача больше не получает `failed`: старый token и grants инвалидируются, выдаётся новый exact-action approval. Прежнее действие не повторяется автоматически.
- Меню active tasks всегда содержит Cancel; completed/failed/cancelled задачи показывают `Новая задача` через pending input и Back/More.
- Добавлена кнопка `Разрешить для задачи`: область только текущая task/tool-loop, chat/user/task ID, не persistence и не restart. Для `read_url` разрешён только тот же canonical origin; для `web_search` — только тот же tool. Новый origin требует нового подтверждения.
- One-time exact-argument approval и Cancel сохранены. Scoped grants проверяются preflight и непосредственно перед каждым network execution, удаляются при cancel/failure/completion/expiry/restart.
- Production task #1 ранее останавливалась после consumed approval; recovery теперь покрыт fresh-approval и replay-prevention regressions. Добавлены same-origin/tool scope, new-origin, wrong user/chat/task, restart expiration и terminal menu tests.

# tg-llm-proxy v1.26.0

## Opt-in text-to-speech

- `/tts <text>` отправляет текст до 1000 runes в существующий Speaches `/v1/audio/speech` и возвращает MP3 через Telegram `sendAudio`. LLM, shell и произвольный код не используются.
- По умолчанию `TTS_ENABLED=false`. Настраиваются `TTS_MODEL=speaches-ai/piper-ru_RU-ruslan-medium`, `TTS_VOICE=ruslan`, `TTS_MAX_CHARS=1000`, `TTS_TIMEOUT_SECONDS=60`.
- HTTP client ограничивает timeout, redirects запрещены, ответ ограничен 20 МиБ и MP3 content type. Текст, URL и upstream bodies не попадают в ошибки или логи.
- More → Озвучить текст появляется только при включённом TTS и использует pending input с TTL 2 минуты. Pending state очищается после completion/cancel/restart.
- `compose.tts.yaml` предзагружает Whisper и одноразово скачивает Piper через `/v1/models` после healthy sidecar. Production без override остаётся прежним.
- Изолированная acceptance: `speaches-ai/piper-ru_RU-ruslan-medium` скачан, русский текст синтезирован в MP3 (24 428 байт, MPEG audio). Добавлены config/timeout/content-type/oversize/redaction/pending-input и fake Telegram `sendAudio` regressions.

# tg-llm-proxy v1.25.0

## Bounded multi-step tasks

- `/task <goal>` запрашивает у выбранной модели строгий JSON-план из 1–3 шагов по 240 символов. Extra fields, URLs, tool names, code/shell/files, embeddings и agent nesting отклоняются до создания задачи. На чат — одна active task.
- Шаги выполняются последовательно с видимым `1/N` прогрессом и результатом до 4000 runes каждый. Финальный no-tools request объединяет результаты; в историю попадают только исходная цель и финальный ответ.
- `/task` показывает состояние, `/task cancel` отменяет дальнейшие шаги и pending approval. More → Задача предоставляет Refresh/Cancel/Back.
- Миграция 9 добавляет tasks с partial unique active index, due/lease/chat indexes и claim tokens. Состояния: ready/running/awaiting_approval/completed/cancelled/failed.
- Используются только существующие calculator/read_url/web_search и exact-action approvals. Approval resume сохраняется между рестартами; consumed approval не может выполниться повторно. Task expiry — 1 час, approval expiry — 5 минут.
- Финальный completion и один assistant history insert коммитятся одной транзакцией. Добавлены migration/state/claim/recovery, strict plan, cancellation, approval replay prevention и fake-Telegram end-to-end regressions. Живой acceptance ещё предстоит.

# tg-llm-proxy v1.24.0

## Saved links

- `/links add <https-url>` читает явно запрошенную страницу через hardened URL reader и сохраняет canonical URL, title до 200 runes и очищенный текст до 8000 runes. Модель не вызывается, failed fetch не сохраняется, повторный add обновляет существующую запись.
- `/links` выводит до 20 ссылок текущего чата, `/links search <текст>` ищет по title/URL/content с явной областью, `/links delete <id>` удаляет только свою запись через single-use confirmation. На чат — до 50 ссылок.
- `/links summary <id>` резюмирует сохранённый текст через существующий model pipeline без повторной загрузки и без tools/network. Лимиты, model errors и per-chat lock сохранены.
- Documents получил leaf «Ссылки на потом» с list/search/summary/delete/Back. Bare HTTPS URL предлагает Save/Cancel: до подтверждения нет fetch, persistence или model call.
- Миграция 8 добавляет `saved_links` с unique(chat_id,canonical_url) и индексом обновления. Callback data содержит только bounded IDs/random tokens, URL не пишется в callback logs.
- Добавлены migration/CRUD/replacement/isolation/search, cap, no-refetch summary, stale/wrong-user confirmation, bare-URL opt-in и fake-Telegram end-to-end regressions. Живой acceptance ещё предстоит.

# tg-llm-proxy v1.23.0

## Stage E persistent reminders

- `/remind через <N секунд/минут/часов/дней> <текст>` создаёт one-shot reminder, `/remind каждый день HH:MM <текст>` — ежедневное напоминание. Текст ограничен 500 runes, на чат — до 20 active/sending reminders.
- `/reminders` показывает список текущего чата, `/reminders cancel <id>` и `delete <id>` отменяют только свои записи. More → Напоминания предоставляет inline list/refresh/cancel/Back.
- Миграция 7 добавляет `reminders` с due/lease/chat indexes. Scheduler использует один loop, atomic claim с 30-секундным lease, 10-секундный send timeout и повторную проверку allowlist. Модель не вызывается, история и token/query logs не создаются.
- Доставка идёт plain text без Markdown/HTML. Ошибки получают retry 2/4/8/16/32 секунды; после пяти one-shot становится failed, daily пропускает occurrence и переходит к следующему wall-clock запуску.
- `REMINDER_TIMEZONE=UTC` по умолчанию, валидируется `time.LoadLocation`. Daily использует локальные календарные даты и корректно переносит wall-clock через DST; пропущенные occurrences схлопываются до следующего будущего запуска.
- Добавлены migration/CRUD/claim/idempotency, parser/DST, retry/denied-user, restart и fake-Telegram delivery-once regressions. Живой acceptance напоминаний ещё предстоит.

# tg-llm-proxy v1.22.0

## Stage D agent security and operational resilience

- RAG/document/transcript/search/URL contents явно помечены untrusted: они не могут авторизовать новые network actions или копирование истории в tool arguments. Проверка выполняется server-side вне модели.
- Прямой exact HTTPS URL и явное search-намерение пользователя работают автоматически. Другой origin или autonomous network action требует confirmation: random 128-bit token, chat/user/menu message, canonical tool arguments, TTL 5 минут и atomic single use. Cancel/expiry/wrong user/replay не выполняют действие; approval продолжает исходный tool loop.
- Startup recovery сохраняет attempts/next_recovery_at в bot_state: до трёх попыток с backoff 1, 2, 4 минуты, затем suppression без удаления истории. Успех очищает metadata, `/unskip` сбрасывает backoff, повторные restarts не создают duplicate notices.
- Миграция 6 добавляет indexes `events(user_id,kind)`, `events(kind,detail)` и `events(created_at)`; admin event counters объединены conditional aggregation. Данные и upgrade с версии 5 сохранены.
- Compose ceilings: bot 512MiB/1 CPU/128 PIDs, Whisper 4GiB/4 CPU/256 PIDs, SearxNG 1GiB/1 CPU/128 PIDs. Hardening и loopback сохранены.
- Maintenance lease содержит UTC/PID/lease_id и действует 180 минут только с живым процессом; stale lease вызывает alert и восстанавливает monitoring. Backup freshness по умолчанию 30 часов с suppression и verified receipt.
- Добавлены restart-loop, migration-upgrade, stale-lease/backup-age, malicious-RAG exfiltration, approval cancel/expiry/replay/wrong-user и exact-resume regressions. Живой acceptance после deploy ещё предстоит.

# tg-llm-proxy v1.21.0

## Stage C interactive menu and progress feedback

- Плоское меню заменено секционным: Ask/New context, History, Documents, Settings, Status, Help, More. Навигация редактирует исходное сообщение; Back возвращает в предыдущий раздел, текстовые команды сохранены.
- History поддерживает last 5/20, Search, Export MD/JSON и существующее single-use Delete подтверждение. Documents имеет список до 20 документов, Add, RAG export, per-document Delete и Clear-all с expiring confirmation.
- Model предлагает до 12 актуальных моделей по bounded indexes, endpoint check и reset. Persona и Stream управляются inline; Admin виден только администраторам.
- Ask, Search и Custom Persona используют pending input с random cancel token, TTL 2 минуты и привязкой к chat/user/menu message. Документные подтверждения действуют 5 минут и одноразовые; restart безопасно инвалидирует process-local состояние.
- Callback data ограничены 64 байтами и whitelist; model IDs/document names не передаются. Неизвестные и stale actions получают понятный expiry response.
- Обычные requests отправляют Telegram `typing` каждые 4 секунды с отменой по context и без дополнительных сообщений. Streaming tools обновляют placeholder только текстами `Читаю страницу…` и `Ищу информацию…`, без URL/query/arguments.
- Добавлены navigation, admin visibility, pending-input expiry/wrong-user, docs confirmations, heartbeat stop/cancel/privacy и end-to-end pending Ask regressions. Живой acceptance меню ещё предстоит.

# tg-llm-proxy v1.20.0

## Stage B anti-DDoS and resource limits

- `WithNotAsyncHandlers` и bounded admission ограничивают реальную работу: active handlers по `MAX_CONCURRENT_MODEL_REQUESTS`, ещё 32 ожидания, один active + четыре FIFO waiters на чат с queue timeout 2 секунды. Albums используют тот же путь; максимум 32 pending batches.
- Перегруженные updates получают coalesced overload response и advances offset без command/history/model/attachment work. Allowlist проверяется до album timers и persistent mutations; denied/bootstrap replies coalesce с ограниченным state.
- Webhook secret проверяется constant-time до чтения body; invalid secret получает 403 без library logs. In-flight limit — 16, headers — 16 КиБ, body — 1 МиБ.
- Streaming ограничен 100000 runes, каждым tool argument — 16384 runes, восемью dense tool calls и 64 Telegram messages. Ошибки используют `ErrStreamLimit` без retry. Chunks вычисляются один раз на due tick.
- Фактическая model work ограничена `3 × RATE_LIMIT_PER_MINUTE` на пользователя (60/мин по умолчанию). Network tools — 8/мин/user, четыре global in flight; busy/budget JSON идёт модели для synthesis, calculator не расходует network budget.
- RAG retrieval ограничен 4000 runes и первыми 32 терминами без изменения полного prompt. History export пишется инкрементально и останавливается на 5 МиБ, metadata и порядок сохранены.
- Добавлены 1000-update/album floods, FIFO, denied-state, webhook no-body-read/overload, continuous SSE, tool-argument/index caps, model/network budget, export oversize и many-term regressions. Живой acceptance после deploy ещё предстоит.

# tg-llm-proxy v1.19.0

## Stage A security hotfix

- Tool schemas и executor используют единый server-side allowlist: calculator только при `TOOLS_ENABLED=true`, `read_url`/`web_search` — при master flag и собственном opt-in. Все calls в round проверяются до первого side effect; disabled/unknown tools и usage/network activity отклоняются.
- `/models` ограничен существующим `STRATA_TIMEOUT_SECONDS`. `/model CHECK`, `SELECT`, `RESET` работают case-insensitively, control tokens требуют точного совпадения, model ID сохраняет регистр.
- Delete inline confirmations используют random 128-bit token с TTL 5 минут, привязкой к chat/user/message и атомарным consume на Confirm или Cancel. Повторные, старые и чужие callbacks не удаляют историю; production keyboard удаляется после использования.
- Export содержит явные `truncated`, warning и ID/time window; search сообщает область newest 10000 и лимит 20 результатов. RAG export получил отдельную подпись.
- `STRATA_BASE_URL` и `TRANSCRIPTION_BASE_URL` запрещают query/fragment без вывода значения. Panic logs проходят redaction настроенных secrets и ограничены 1024 байтами. URL reader дополнительно блокирует NAT64 `64:ff9b::/96`.
- Добавлены disabled-tool/no-dial, hanging-server, mixed-case, confirmation replay/wrong-user/expiry, 10001-row export/search, redaction и NAT64 regressions. Живой Telegram acceptance этой версии ещё предстоит.

# tg-llm-proxy v1.18.1

## Markdown nesting and native tools patch

- Вложенные inline-code внутри bold/italic восстанавливаются от внешнего токена к внутреннему: служебные `@@TGMD…@@` больше не попадают в Telegram. Добавлен regression-тест для двух вложенных конструкций.
- При доступных tools model request получает отдельное system-правило: только нативное OpenAI `tool_calls`, без текстового/XML `<tool_call>`, `<function=...>` или `<parameter=...>`, и только имена из переданных schemas. Правило добавляется один раз на пользовательский запрос и не сохраняется в истории.
- Парсер текстовых pseudo-tool calls намеренно не добавляется: исполняются только структурированные вызовы протокола. Живой acceptance подтверждён: RAG-ответ без `@@TGMD…@@`, а совмещённый `read_url` + `web_search` запрос завершился финальным ответом без XML.

# tg-llm-proxy v1.18.0

## Performance and build reuse

- Streaming больше не форматирует и не разбивает весь накопленный ответ на каждый token/progress event: полная обработка выполняется только на cadence tick. Rolling multi-message delivery, первая публикация, финальный Markdown и error paths сохранены.
- Benchmark `BenchmarkStreamRefreshNotDue`: с ≈4,23–6,85 мс и ≈5,13 МиБ на событие до ≈4,1 нс и 0 B; это изолированный non-due path, не сетевой benchmark.
- RAG по-прежнему сканирует corpus для IDF и средней длины, но сохраняет только content/name matches, кэширует lowercase-поля и считает tokens без промежуточных slices. Порядок, веса BM25-lite и truncation не изменены.
- Benchmark на 10000 chunks (9999 irrelevant): ≈81,5 → 33,1 мс (−59%), allocated bytes ≈69,0 → 14,7 МиБ (−79%). Это allocated bytes, а не отдельный heap high-water metric.
- Dockerfile имеет общий module-cache stage для builder/test; source copies не сбрасывают кэш установки GCC. `docker-build.ps1` обновляет bot и test одним Compose/BuildKit запросом. Все race/integration проверки сохранены; hosted CI не расширялся.

# tg-llm-proxy v1.17.3

## Tool round completion patch

- После двух tool rounds бот больше не завершается ошибкой `model exceeded the tool round limit`: выполняется один финальный model request с пустым списком tools, чтобы модель собрала ответ из уже полученных результатов.
- Regression-тест проверяет два tool round, третий запрос без tools и доставку финального ответа вместо model error.
- Живой Telegram acceptance подтверждён: `read_url` и `web_search` выполнены, финальный ответ после tool rounds доставлен.
- На скриншоте `Timed out` относится к 90-секундному ожиданию фонового `verify` в интерфейсе, а не к тестам или боту: сам pipeline позже завершился GREEN.

# tg-llm-proxy v1.17.2

## Streaming tools patch

- Модель может отправить текст-преамбулу перед `tool_calls` в streaming mode: это больше не приводит к `tool call followed streamed answer text`. Tool round выполняется, промежуточный текст остаётся в streamed-сообщении до финального ответа.
- Двухраундовый regression-тест подтверждает выполнение calculator после streamed-преамбулы, сохранение assistant/tool messages для второго model request и ровно один usage event.
- Живой acceptance выявил ошибку на включённых `read_url` и `web_search`; полный сценарий подтверждён после исправления финального tool-round запроса в v1.17.3.

# tg-llm-proxy v1.17.1

## Markdown formatting patch

- Подчёркивания внутри идентификаторов (`TELEGRAM_ALLOWED_USERS`, `read_url`, `web_search` и других `snake_case`) больше не интерпретируются как курсив и не исчезают из сообщений Telegram.
- Настоящий `_курсив_`, в том числе рядом с идентификаторами, сохраняется. Добавлены regression-тесты.
- Живой acceptance подтвердил RAG-ответ с источником `debug.log` и длинный стриминг: 3774 output tokens за 1 м 28 с, доставка несколькими сообщениями без итогового пакетного спама.

# tg-llm-proxy v1.17.0

## Web search and RAG ranking release candidate

- Добавлен opt-in tool `web_search` через self-hosted SearxNG. По умолчанию `WEB_SEARCH_ENABLED=false`; для работы также нужен `TOOLS_ENABLED=true` и `WEB_SEARCH_BASE_URL` собственного экземпляра.
- Запрос ограничен `WEB_SEARCH_TIMEOUT_SECONDS=10` (1..30), ответом до 1 МиБ и `WEB_SEARCH_MAX_RESULTS=5` (1..10). Модель получает только `title`, `url` и `snippet`; ссылки выдачи не загружаются. Redirects отклоняются, credentials/query/fragment в адресе provider запрещены, тексты запросов и ответов не логируются.
- `compose.websearch.yaml` запускает закреплённый по tag+digest SearxNG с read-only rootfs, dropped capabilities, `no-new-privileges`, loopback-only портом и JSON API. Провайдер и `livecheck` проверены на реальном поисковом запросе; живой acceptance внутри Telegram ещё не выполнялся.
- `/admin` показывает отдельный `web_search calls`; usage events по-прежнему содержат только имя инструмента.
- RAG retrieval использует IDF-weighted BM25-lite с нормализацией длины, бонусами за coverage, фразу и имя документа. Редкие термины и короткие точные фрагменты получают приоритет; ranking детерминирован, поддерживает Unicode без embeddings, миграций и новых зависимостей.
- Добавлены config/tool/store/admin и ranking regression-тесты. Semantic retrieval не добавлен: STRATA по-прежнему отвечает 404 на `/v1/embeddings`.

# tg-llm-proxy v1.16.0

## URL reader release candidate

- Добавлен opt-in tool `read_url` для чтения текстового содержимого HTTPS-страниц моделью. `URL_READER_ENABLED=false` по умолчанию; shell, файлы и произвольный код не предоставляются.
- URL ограничен 2048 символами и запрещает userinfo. Timeout — около 15 секунд, не более 5 redirects с повторной проверкой, тело — до 1 МиБ; разрешены `text/*`, JSON, XML и HTML, результат — до 8000 runes.
- SSRF-защита отклоняет localhost, private, loopback, link-local, multicast, unspecified, reserved, documentation, CGNAT, benchmark и другие не-global-unicast IP. Каждый `DialContext` самостоятельно выполняет resolve+validate и соединяется напрямую с проверенным IP; proxy отключён.
- `/admin` показывает счётчик `read_url calls` рядом с RAG и calculator usage; usage events по-прежнему содержат только имя инструмента.
- URL и содержимое ответов не логируются. Поведение на реальной сети подтверждено opt-in проверкой под build tag `livecheck`
  (`internal/bot/url_reader_live_test.go`): публичный HTTPS-сайт прочитан, `169.254.169.254`, `http://` и `localhost` отклонены.
  Живой acceptance внутри Telegram при `URL_READER_ENABLED=true` ещё не выполнялся.
- Проверка на физическом NVIDIA GPU выполнена на RTX 5060 Ti (driver 610.88, Docker `nvidia` runtime): CUDA-образ закреплён по digest,
  `WHISPER__INFERENCE_DEVICE=cuda` подтверждён внутри приложения, `PRELOAD_MODELS` загружает модель из существующего volume,
  транскрипция 6-секундного WAV возвращает текст. Прогретый прогон 0,65 с против 2,06 с на CPU; первый прогон после старта контейнера
  занимает ≈33 с из-за JIT-прогрева cuDNN. Production по умолчанию остаётся CPU.

# tg-llm-proxy v1.15.0

## Data management release

- `/docs export` выгружает документы и chunks текущего чата как JSON Telegram document из памяти, с лимитом 5 МиБ.
- `db_prune.ps1 -RAGDays N` добавляет age-based RAG retention; `0` по умолчанию отключает удаление. `-Apply` по-прежнему требует новый проверенный backup и выполняет cascade cleanup chunks.
- Добавлены store/bot/integration и CLI проверки; dry-run не удаляет данные.

# tg-llm-proxy v1.14.0

## RAG tuning release

- `RAG_MAX_DOCUMENTS=20`, `RAG_RESULT_LIMIT=3`, `RAG_MAX_CONTEXT_CHARS=2000` настраивают размер базы и retrieval.
- Ranking учитывает coverage терминов, ограниченную frequency, exact phrase и имя источника. Замена документа разрешена даже при достигнутом лимите.
- Ответ с RAG получает отдельный краткий `Источники:` список без дублей. Semantic retrieval не добавлен: embeddings endpoint недоступен.
- Добавлены config/store/bot regression-тесты; миграций нет.

# tg-llm-proxy v1.13.0

## Extended admin release

- `/admin` дополнительно показывает активные model calls и число чатов, удерживающих per-chat lock. Используются atomic counter и существующий ratelimit.Locks, без текстов чатов.
- Счётчик освобождается после каждого вызова, включая tool rounds и ошибки; BusyChats проверен отдельным concurrency-тестом.

# tg-llm-proxy v1.12.1

## Reliability and observability patch

- `watch_runtime.ps1` учитывает bot transcription failures и Whisper HTTP 5xx/ERROR с отдельным threshold/suppression. Добавлен scratch acceptance для Whisper alerts и пустых логов.
- `/admin` показывает RAG retrieval, calculator calls и текущее число RAG-документов. Usage events содержат только `rag`/`calculator`, без запросов или содержимого документов.
- Store/bot/monitor tests обновлены; длинные streamed ответы и calculator проверяются на production перед тегом.

# tg-llm-proxy v1.12.0

## Operations release

- `watch_container.ps1` проверяет bot и Whisper, с attribution `whisper-<state>` и без автоматического restart. Изолированный acceptance покрывает bot failure/suppression/recovery и Whisper failure без реальных уведомлений.
- Speaches CPU и CUDA images закреплены по digest. Production по-прежнему использует CPU.
- `compose.cuda.yaml` добавляет optional NVIDIA GPU mode: `docker compose -f docker-compose.yml -f compose.cuda.yaml up -d`. Физический GPU acceptance не заявлен.

# tg-llm-proxy v1.11.0

## Safe tools release

- Добавлен OpenAI function calling с одним безопасным `calculator`: +, -, *, /, %, скобки и числа; никаких shell, файлов или сети.
- Tool loop ограничен двумя rounds и двумя calls в round, выражение — 200 символов. Работает в обычном и streaming paths, включая fragmented tool_calls.
- `TOOLS_ENABLED=true` управляет функцией. В SQLite сохраняются только пользовательский вопрос и финальный ответ, без промежуточных tool messages.
- Добавлены model/bot и end-to-end integration проверки.

# tg-llm-proxy v1.10.0

## Local RAG release

- Миграция 5 добавляет `rag_documents` и `rag_chunks`. До 20 текстовых документов на чат, замена по имени, list/delete/clear и cascade.
- Отправка файла с подписью `/docs add` сохраняет текст локально. `/docs`, `/docs delete <id>`, `/docs clear [confirm]` управляют базой.
- Вопросы автоматически получают до 3 релевантных фрагментов (2000 runes) с именами источников. Используется локальный Unicode lexical retrieval без embeddings и внешних зависимостей.
- Добавлены store, bot и end-to-end integration проверки. Production migration требует предварительного проверенного backup.

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
