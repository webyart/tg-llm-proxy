# I18N status: RU/EN

Состояние интерфейсной локализации бота после добавления `/language` и перевода long-tail команд.
Модельные ответы, промпты, системные инструкции, логи и имена audit-событий не переводятся и в этот
документ как «долг» не входят.

## 1. Контракт `/language`

| Аргумент | Поведение |
| --- | --- |
| `/language` | Текущий язык чата и источник: «этот чат» (есть override) или «по умолчанию» (`DEFAULT_LANGUAGE`). В EN — `this chat` / `default`. |
| `/language ru` | Сохраняет override `ru` в `chat_language`, подтверждает на русском. |
| `/language en` | Сохраняет override `en`, подтверждает на английском. |
| `/language reset` | Удаляет строку override, подтверждает язык, на который чат теперь падает по `DEFAULT_LANGUAGE`. |
| неизвестный аргумент | Печатает usage на текущем языке; запись в БД не выполняется. |

`/language` не вызывает модель, не пишет вопрос в историю и не требует `NeedsArgs`. Команда
зарегистрирована в `registry()` (`commands.go`), её описание переводится в `/help`.

## 2. Правило разрешения языка

`d.chatLanguage(ctx, chatID)` в `internal/bot/language.go`:

1. читает override из `chat_language` (`store.ChatLanguage`);
2. если строки нет, ошибки нет или `DB == nil` — возвращает `DEFAULT_LANGUAGE` (`deps.Cfg.DefaultLanguage`);
3. нормализует значение через `i18n.Normalize` (неизвестное значение → `ru`).

Состояние не кэшируется глобально: каждый обработчик разрешает язык один раз в начале вызова и
передаёт его в formatting-функции, поэтому два чата не делят язык, а смена через `/language`
действует со следующей команды. `chatLanguageState` дополнительно сообщает, был ли override, чтобы
`/language` показал источник без второго запроса.

## 3. Каталог

`internal/i18n/catalog.go`:

- два каталога, `ruMessages` и `enMessages`, с одинаковым набором стабильных ID; сейчас по **442
  ключа** в каждом (254 добавлено этой задачей);
- `i18n.T(lang, key, args...)` — `printf`-шаблон, подстановки только через явные аргументы, без
  конкатенации переведённых фрагментов;
- fallback: запрошенный язык → `ru` → сам ключ (чтобы пропуск был виден, а не превращался в пустую строку);
- `i18n.Lookup` и `i18n.Normalize` для мест, где нужен не бросок, а проверка;
- `catalog_test.go`: `TestCatalogsHaveTheSameKeys` (полное совпадение ключей), fallback на RU при
  удалённом EN-ключе, fallback неизвестного языка, видимость неизвестного ключа.

Добавленные семейства ключей: `docs.*`, `links.*`, `remind.*`/`reminders.*`, `task.*` (включая
`task.approval.*` и `task.scope.*`), `tts.*`, `admin.*`, `menu.*`, `stream.*`,
`persona.preset.*`, `refuse.*`. Форматирование с числом/именем идёт через `T` (`docs.menu_line`,
`links.heading`, `remind.created`, `task.step_line`, `tts.speed_current`, `admin.events`,
`stream.suffix`, `task.approval.request` и т. п.).

Для меню и разметки добавлены language-aware варианты (`menuMarkupFor`, `historyMenuMarkupFor`,
`simpleBackMarkupFor`, `refreshBackMarkupFor`, `linksMenuMarkupFor`, `reminderMenuMarkupFor`,
`taskMenuMarkupFor`, `personaMarkupFor`, `usageMenuMarkupFor` и т. п.). Исторические RU-обёртки и
константы (`linksUsage`, `ReminderUsage`, `TaskUsage`, `msgTTS*`, `StreamPlaceholder`,
`menuExpiredText`, `documentTruncationNotice`) оставлены как fallback для Mini App и тестов; командная
поверхность Telegram их не использует.

Для `/help` реестр `Command.Description` оставлен русским: его читает `setMyCommands` в `run.go`
(файл вне этой задачи), а переводимые описания лежат в параллельной карте каталога
(`command.<name>.description`) и используются `helpLinesFor(lang)`.

## 4. Что переведено полностью

Все ответы, подтверждения, ошибки и inline-кнопки этих команд идут через каталог:

`/start`, `/help` (включая `menu:*`), `/ping`, `/status`, `/model`, `/stream`, `/miniapp`,
`/persona`, `/language`, `/reset`, `/skip`, `/unskip`, `/whoami`, `/stats`, `/usage`, `/feedback`,
`/history`, `/export`, `/search`, `/delete`, `/ask` (обёртки ответа), **`/docs`**, **`/links`**,
**`/remind`**, **`/reminders`**, **`/task`**, **`/tts`**, **`/admin`**.

Переведены также:

- `/docs`: add/list/search/export/delete/clear, подсказки ввода, подтверждения, ошибки загрузки
  (общие `ask.attachment_*`, включая PDF/DOCX, пустой документ, пароль, `.doc`, OCR), лимиты;
- `/links`: add/search/summary/delete, предложение сохранения, лимиты и все ошибки чтения страницы;
- `/remind` и `/reminders`: создание и разбор ошибок, быстрые кнопки, список, отмена, недоставленные,
  повтор/удаление, сообщения планировщика о доставке и окончательной неудаче;
- `/task`: создание/статус/отмена/возобновление/очистка, строки прогресса и шагов, ошибки шагов,
  истёкшие задачи, подтверждения сетевых действий (текст, кнопки, recovery-notice);
- `/tts`: синтез, `last`, скорость, disabled/unavailable/too-long/timeout/delivery;
- `/admin`: агрегированный вывод и отказ в авторизации;
- inline-меню (`menu.go`), включая `menu:*`, отмену ввода, истёкшие меню;
- статусы стриминга: placeholder, стадии чтения/поиска, счётчики, маркеры обрезки/обрыва,
  «ответ ниже»;
- описания и экранные имена persona-пресетов; хранимые ID (`assistant`, `coder`, `writer`,
  `translator`) и callback-данные не менялись;
- `refuseClosed`/`refuseNotListed` — через variadic-язык; командная поверхность передаёт язык чата,
  старые вызовы без языка остаются на RU.

## 5. Что осталось вне перевода (осознанно)

### 5.1. Языконезависимые артефакты

- Содержимое экспортируемых файлов: история (MD/JSON) и RAG JSON — это артефакт, а не сообщение чата.
  UI-facing подписи (`export.caption`, `docs.export_caption`) переводятся, содержимое файла — нет.
- Промпты модели, system-инструкции, тексты целей/шагов, сгенерированные моделью, и промпты
  `links summary` / `task planner` / RAG-контекста.
- Логи, audit-события и имена event-kind.
- `Command.Description` для `setMyCommands`: список команд бота общий для всех чатов и не может быть
  per-chat в Telegram; `/help` при этом переводится.
- Глобальная кнопка меню чата «Меню» (`menu_button.go`): она публикуется на бота целиком, не на чат.
- Mini App (`miniapp_chat.go`) — собственный интерфейс и собственные тексты, вне Telegram-команд.
- `personaPresets[].description` — RU-fallback для `/api/settings` webapp; Telegram берёт перевод из
  каталога, ID пресетов и их промпты не менялись.

### 5.2. Отложено из-за параллельной задачи

`dispatch.go` и `middleware.go` в этой задаче не редактировались (файлы заняты задачей про приватность
метрик). Поэтому остаются русскими:

- `msgOverloaded`, `msgChatBusy`, `msgQuestionNotStored`;
- тексты middleware (общая ошибка обработки и rate-limit);
- вызовы `refuseClosed`/`refuseNotListed` из этих двух файлов: они не передают язык и получают
  исторический RU-вариант. Командная поверхность (`/start` и др.) уже передаёт язык.

### 5.3. Ограничения, не являющиеся строками интерфейса

- `ParseReminder` принимает только русскую грамматику (`через …`, `каждый день …`). EN-подсказка
  `help.remind_example` показывает `in 1 minute`, но такой ввод пока не разбирается — это ограничение
  парсера, а не непереведённая строка.
- `/ask`-подтверждение сетевого действия в `tool_loop.go` остаётся русским: файл вне списка этой
  задачи; task-подтверждения (`task_scheduler.go`) переведены.
- Legacy RU-константы (`linksUsage`, `ReminderUsage`, `TaskUsage`, `msgTTS*`, `StreamPlaceholder`,
  `menuExpiredText`, `documentTruncationNotice`) сохранены для Mini App/тестов и не выводятся
  командной поверхностью Telegram.

## 6. Проверка

- `go test -count=1 ./internal/i18n` — паритет каталогов и fallback;
- `go test -count=1 ./internal/bot` — существующие тесты плюс новые:
  `TestDocsRendersInChatLanguage`, `TestLinksRendersInChatLanguage`, `TestRemindRendersInChatLanguage`,
  `TestTaskRendersInChatLanguage`, `TestTTSRendersInChatLanguage`, `TestAdminRendersInChatLanguage`,
  `TestMenuButtonsFollowChatLanguage`, `TestPersonaPresetIDsAndCallbacksAreStable`,
  `TestStreamPlaceholderFollowsChatLanguage`, `TestRefusalTextsFollowLanguage`;
- `gofmt -l .`; `go build ./...`; Docker `--target test` и
  `docker run --rm tg-llm-proxy-test:latest go test -count=1 ./internal/bot ./internal/i18n`
  (см. отчёт задачи).

Ключи каталога и паритет проверяются автоматически; при добавлении новых сообщений обновляйте оба
каталога и этот документ.
