# ROADMAP — tg-llm-proxy после v1.40.0

Состояние на 2026-11-08: релиз **v1.40.0** (`2c8f06e`, тег, GitHub Release) задеплоен в production, схема БД **14**,
все 13 пакетов тестов зелёные. Закрыты три спринта из `docs/CODE_REVIEW_2026-11-08.md`: надёжность, данные/модель,
инженерия. Ниже — что делать дальше, в порядке, в котором это выгодно делать.

## Принцип приоритизации

1. **Сначала видимость, потом фичи.** Деплой уже в production, но единственный сигнал о деградации — `/healthz=200` и
   жалоба пользователя. Пока нет метрик, каждая следующая фича проверяется глазами.
2. **Сначала дешёвая польза.** Обратная связь по ответам (день работы) даёт данные, без которых улучшение RAG и
   промптов нечем измерять.
3. **Инфраструктурные изменения — только под конкретный триггер.** Postgres и мультитенантность не делаем «на всякий
   случай»: у них есть критерии перехода (ниже).

## Спринт 4 — наблюдаемость и эксплуатация (≈2 недели) — рекомендуется первым

| Задача | Зачем | Объём |
|---|---|---|
| `/readyz` в `internal/health` | Отличать «процесс жив» от «бот работает»: ping БД, свежесть последнего успешного poll, доступность модели | 1–2 дня |
| Prometheus `/metrics` | Латентность модели p50/p95, queue wait, ретраи, 429, tool calls, глубина очереди, время удержания per-chat lock, доставка в Telegram, размер БД и таблиц, расход квоты | 3–4 дня |
| Дашборд Grafana + алерты в `edge-gateway` | Prometheus и Grafana уже есть, но метрик бота в них нет; алерты: error rate, p95 latency, очередь, рост БД, возраст бэкапа | 2 дня |
| Restore-drill в CI ✅ | `verify.ps1 -WithVolumeRestore` существует, но в пайплайн не передаётся; добавить в ночной прогон | 1 день |
| Release automation ✅ | Тег → сборка образа с digest, SBOM, публикация; сейчас образ собирается локально вручную | 2 дня |
| Блокирующий `golangci-lint` ⏳ | Джоба advisory: снять флаг после первого чистого отчёта на раннере | 0.5 дня |
| Стоимость и токены | Дашборд tokens/day по моделям и пользователям на основе `usage` | 1 день |

**CI-часть сделана (2026-11-08).** `restore-drill` в `.gitlab-ci.yml` (stage `test`) гоняет
`scripts/test-maintenance.ps1 -WithVolumeRestore` и пропускается только при отсутствии Docker-демона;
`release` (stage `release`, только теги `v*`) собирает production-образ по тегу, сохраняет checksum,
`docker image inspect` и `coverage.out` как артефакты; `scripts/release-check.ps1` — локальный гейт
(gofmt/vet/tests/чистый `git status`) перед созданием тега. SBOM не генерируется: офлайн-инструмента в
runner-образе нет, решение и его причина зафиксированы комментарием в `.gitlab-ci.yml`. Публикация в
registry остаётся открытой (нужен токен с нужным scope). `golangci-lint` остаётся advisory до первого
чистого отчёта именно на раннере.

**DoD:** алерт приходит в Telegram при деградации; дашборд показывает латентность и ошибки; образ публикуется из тега

СТАТУС 2026-11-09: **выполнено.** `/readyz` и `/metrics` работают, Prometheus скрапит `bot:8081` (`up=1`), шесть алертов (down, error share, p95, queue, DB growth, schema change) с promtool-тестами загружены, дашборд `Bot overview` провижинится, Alertmanager доставляет firing/resolved в личный чат администратора (проверено метрикой `alertmanager_notifications_total{integration="telegram"}`), Grafana и все сервисы healthy. Код — в `edge-gateway` (коммиты `baefd61`, `ff691c7`, `59e40ca`).
без ручной сборки.

## Спринт 5 — Mini App как полноценный второй клиент (≈2–3 недели)

СТАТУС 2026-11-10: **основной сценарий готов.** `POST /api/messages` (SSE: `stage`/`delta`/`approval`/`done`/`error`),
`POST /api/chat/approval`, `POST /api/context/reset`, `POST /api/pending/skip`, `GET /api/whoami`, вкладка «Чат» в UI.
Mini App отвечает через ТОТ ЖЕ `Dispatcher`, что и Telegram (`main.go` вызывает `SetChatBackend(dispatcher)`): общий
per-chat lock, общий per-minute бюджет и общая дневная квота. Сетевые инструменты (`read_url`, `web_search`) доступны
и в Mini App: модель запрашивает действие, SSE-событие `approval` показывает карточку, `POST /api/chat/approval`
возвращает решение и тот же поток продолжается; `calculator` работает без подтверждения.
Осталось: ничего. Загрузка голоса закрыта в v1.56.0 (`POST /api/transcribe` + тот же
`model.Transcriber`, что и у голосовых в Telegram). Загрузка документов закрыта в v1.50.0 (`POST /api/documents` + общий
ingest-helper с `/docs add`), PDF/DOCX для обоих путей добавлены в v1.51.0, подтверждения сетевых действий — в v1.52.0,
матрица паритета — в `internal/bot/parity_test.go` (v1.56.0).

Спринт начинался с read-only панели: задать вопрос, ответить на «ожидает ответа», нажать `/skip`/`/unskip`/`/reset`
было нельзя, хотя бот умеет стриминг.

| Задача | Зачем | Объём |
|---|---|---|
| `POST /api/messages` + SSE-стрим | Чат в Mini App; переиспользовать `Dispatcher` (лимиты, RAG, tools, подтверждения), а не писать второй путь | 5–7 дней |
| Индикатор «ожидает ответа», `/skip`, `/unskip`, `/reset`, `/whoami` | Паритет с ботом для основного сценария | 2 дня |
| Подтверждения сетевых действий в Mini App | ✅ v1.52.0: SSE `approval`, одноразовый токен, `POST /api/chat/approval`, тот же поток продолжается после решения | 3 дня |
| Загрузка документов и голоса | `/docs add` и транскрипция сейчас только в боте | ✅ документы — v1.50.0 (`POST /api/documents`, общий `bot.IngestRAGDocument`), PDF/DOCX — v1.51.0 (`bot.ExtractDocumentText`); ✅ голос — v1.56.0 (`POST /api/transcribe`, общий `model.Transcriber` и та же валидация) |
| Матрица паритета «бот vs Mini App» в тестах | Чтобы третья итерация не забыла половину команд | ✅ v1.56.0: `internal/bot/parity_test.go` сверяет `registry()` с таблицей «команда → `/api/...` или причина telegram-only»; 5 исключений: `/start`, `/help`, `/ping`, `/tts`, `/admin` |

**DoD:** в Mini App можно вести диалог со стримингом, ставить задачу и загрузить документ; паритет зафиксирован тестом.

### Матрица паритета (источник истины — `registry()`)

| Команда | Мини-апп |
|---|---|
| `/status` | `GET /api/status` |
| `/model` | `GET /api/settings`, `PUT/DELETE /api/settings/model` |
| `/stream`, `/miniapp` | `PUT/DELETE /api/settings/stream`, `/api/settings/miniapp-fallback` |
| `/persona` | `PUT/DELETE /api/settings/persona` |
| `/ask` | `POST /api/messages` (SSE) |
| `/reset`, `/skip`, `/unskip` | `POST /api/context/reset`, `POST /api/pending/skip` |
| `/history`, `/search`, `/export`, `/delete` | `GET /api/history`, `/api/history/search`, `/api/history/export`, `POST /api/history/clear` |
| `/docs` | `GET/POST /api/documents`, `/api/documents/search`, `POST /api/documents/{id}/delete` |
| `/links` | `GET/POST /api/links`, `/api/links/search`, `/api/links/item` |
| `/task`, `/remind`, `/reminders` | `GET/POST /api/tasks`, `/api/tasks/{id}`, `/api/tasks/cancel`, `GET/POST /api/reminders`, `/api/reminders/{id}` |
| `/feedback`, `/fb`, `/usage`, `/stats` | `POST /api/history/{id}/feedback`, `GET /api/usage` |
| `/whoami` | `GET /api/whoami` |
| `/start`, `/help`, `/ping`, `/tts`, `/admin` | Telegram-only: сессия, справка, диагностика, синтез аудио клиентом, админ-метрики |

## Что уже сделано по спринтам

- Спринт 4: `/readyz` (ping БД + доступность модели с кэшем 30 с) и `/metrics` на loopback-листенере, метрики
  модели/очередей/апдейтов/инструментов/БД, CI `restore-drill` (manual, требует Windows-раннер) и `release`
  (тег → образ + артефакты), `scripts/release-check.ps1`.
- Спринт 5: чат в Mini App (см. статус выше); загрузка документов в Mini App закрыта в v1.50.0, PDF/DOCX
  добавлены в v1.51.0, подтверждения сетевых действий — в v1.52.0, голос — в v1.56.0; осталась матрица паритета.
  Голос теперь на обоих клиентах, и это зафиксировано в parity-комментарии `internal/bot/miniapp_voice.go`
  (Telegram: voice/audio/video_note → Transcribe → ответ в чате; Mini App: микрофон → `POST /api/transcribe` →
  текст в composer; общий клиент, валидация и лимиты).
- Спринт 6 в работе: 👍/👎-фидбэк закрыт (миграция 15, `/feedback` и кнопки в Mini App), гибридный RAG закрыт
  (миграция 16, embeddings + FTS5), per-chat доставка ответа Mini App в Telegram — v1.54.0 (миграция 17); остальное — по списку ниже.

## Спринт 6 — качество ответов (≈3–4 недели)

| Задача | Зачем | Объём |
|---|---|---|
| ✅ Обратная связь 👍/👎 | Данные для промптов, персон и RAG; сейчас фидбэка нет вообще | сделано (миграция 15, `/feedback`, Mini App) |
| Гибридный RAG: embeddings + FTS5 | Лексический поиск не находит перефразировки; FTS5-кандидаты уже есть, вектор — следующий слой | ✅ сделано: миграция 16 (`rag_chunk_embeddings`), клиент `internal/embed`, `final = (1-w)*lexical + w*cosine` (w = 0.4), vector-кандидаты из 64 ближайших, backfill `cmd/ragembed`; качество — `rag_quality_test.go`, гибридный кейс с paraphrase |
| Стриминг через `SendMessageDraft` | ✅ сделано: приватные чаты получают draft-превью вместо правок placeholder; группы и sender без поддержки остаются на прежнем edit-пути, ошибка draft откатывается на edits | миграция не нужна |
| Prompt caching и сжатие контекста | ✅ сделано: окно строится ходами (user + все его assistant-строки), ход входит целиком или не входит, вопрос нового хода сохраняется всегда | тесты `context_turns_test.go` |
| Оценка качества RAG на наборе вопросов | ✅ сделано: 8 документов/41 чанк, 19 answerable + 1 no-answer запрос, recall@1/@3/@5 = 1.000, MRR@5 = 1.000; пороги recall@3 ≥ 0.95 и MRR@5 ≥ 0.85 ловят регрессию ранжирования | `rag_quality_test.go` |
| Per-chat доставка ответа Mini App в Telegram | Глобальный `MINIAPP_TELEGRAM_FALLBACK` не даёт выбрать поведение для отдельного чата | ✅ сделано (v1.54.0): миграция 17 (`chat_miniapp_fallback`), `/miniapp on\|off\|reset`, переключатель «Mini App» в Settings, `GET/PUT/DELETE /api/settings/miniapp-fallback` |

**DoD:** фидбэк пишется в БД и виден в `/stats`; RAG отвечает на перефразированный вопрос; черновик стримится без 429.

## Спринт 7 — масштаб (квартал, по триггеру)

| Задача | Триггер перехода | Объём |
|---|---|---|
| Реестр провайдеров: несколько endpoint, fallback, per-chat routing, cost | Нужна вторая модель/резервный endpoint | ✅ v1.57.0: упорядоченная цепочка `STRATA_FALLBACKS` (до 8 endpoint, свои ключи/таймауты), per-chat routing, токены по моделям (`model_tokens_total`); автоматического выбора по cost/латентности пока нет | 5–7 дней |
| `store.Repo` + Postgres | Вторая реплика бота или веб-клиент с своим write-path; критерии и план — [ADR-0007](adr/0007-store-repo-postgres.md) | 2–3 недели (только по триггеру) |
| Мультитенантность | Появляется второй клиент/семейство | 3–4 недели |
| Групповой режим: только mention/reply, per-user лимиты | Бот добавляют в группы с несколькими участниками | ✅ v1.59.0: `GROUPS_ENABLED`, гейт по mention/reply/`/cmd@bot`, allowlist отправителя, упоминание вырезается, квоты не расходуются на игнор |
| i18n бота (EN) + `/language` | Второй язык интерфейса | ✅ v1.59.0: `DEFAULT_LANGUAGE` + `/language ru\|en\|reset` (миграция 19), каталог `internal/i18n`, переведены основные команды и обёртки ответа; остаток — `docs/I18N_STATUS.md` |

## Мелкий техдолг (можно параллельно)

- Заменить 38 `time.Sleep` в тестах инъектируемыми часами (часть уже есть через `Dispatcher.now`).
- ✅ `-shuffle=on` добавлен в CI (unit/integration/race) и локальные обёртки `test.ps1`, `test-race.ps1`, `smoke.ps1`, `release-check.ps1`; seed печатается при падении, повтор — `-shuffle=<seed>` (2026-11-10).
- ✅ Renovate: `renovate.json` — weekly, grouped (три Go-модуля, Docker base images, GitHub Actions), automerge off, dependency dashboard on (2026-11-10).
- ✅ `RELEASE_NOTES.md` разбит на `CHANGELOG/` по шести диапазонам; индекс оставляет v1.56.0 inline, 92 заголовка до и после (2026-11-10).

### Открытые решения (owner / trigger)

- [ ] **GitLab-токен со scope `api` для release** — owner: владелец репозитория (release). Trigger: первая публикация образа в registry или первый тег после включения push. Сейчас `release` собирает образ и сохраняет артефакты, но не пушит его: registry credential/scope отсутствует (комментарий у job `release` в `.gitlab-ci.yml`).
- [ ] **`tuya-dashboard` в корне сайта** — owner: владелец инфраструктуры. Trigger: изменение владельца корня `/` или остановка dashboard. Корень обслуживает отдельный проект `tuya-dashboard`; на 2026-11-10 контейнер `tuya-llm-dashboard` запущен и `/` отвечает 200 (в ревью 2026-11-08 он был остановлен и `/` отдавал 503, `docs/CODE_REVIEW_2026-11-08.md:351`). Mini App от него не зависит, пока `MINIAPP_PUBLIC_URL` содержит `/miniapp`.
- [ ] **Судьба исторических `deploy/*`** — owner: владелец инфраструктуры. Trigger: удаление остановленного контейнера `tg-llm-proxy-gateway-1` или отказ от обещанного rollback. Сейчас `deploy/Caddyfile`, `deploy/traefik.yml`, `deploy/dynamic.yml` и `scripts/test-gateway.ps1` оставлены как rollback-артефакты: bind-mounts старого контейнера ссылаются на эти пути (`deploy/README.md`).
- [ ] **Правило рестарта контейнера (ADR-0006)** — owner: владелец репозитория. Trigger: изменение `restart:` в compose или пересмотр ADR. ADR-0006 accepted 2026-11-08: `restart: unless-stopped` сохраняется, crash loop обнаруживает монитор; альтернатива `on-failure:3` отклонена. Открыт только вопрос формального закрытия/пересмотра записи.

## Рекомендация

1. **Спринт 4, первым делом `/readyz` + `/metrics` + три алерта** — 3–4 дня, и деградация перестаёт быть невидимой.
2. **Обратная связь 👍/👎** — 1–2 дня, самая дешёвая продуктовая польза.
3. **Спринт 5: чат в Mini App** — самый заметный прирост для пользователя.
4. **Эмбеддинги RAG** — сделано в v1.48.0: фидбэк и метрика качества появились раньше, поэтому гибридный слой
   включён opt-in и проверяется отдельным quality-кейсом, а не заменяет лексическое ранжирование.
