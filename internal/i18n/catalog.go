// Package i18n holds the bot's user-interface catalog. It translates the interface, never the model:
// answers, prompts, system instructions, logs and audit event names stay exactly as the model or the
// handler produced them.
//
// Every message is addressed by a stable ID and stored as a printf-style template. A placeholder is
// formatted with fmt.Sprintf through T, so no caller builds a sentence by concatenating translated
// fragments (word order differs between Russian and English).
package i18n

import (
	"fmt"
	"sort"
	"strings"
)

// Language codes the catalog supports. The values match store.ChatLanguages and DEFAULT_LANGUAGE.
const (
	RU = "ru"
	EN = "en"
	// Default is the fallback language of the bot and of a missing key. DEFAULT_LANGUAGE defaults to
	// "ru" in config, so the bot keeps its historical language when nothing is configured.
	Default = RU
)

// catalogs maps a stable message ID to its template in every supported language. The two maps must
// have exactly the same key set; TestCatalogsHaveTheSameKeys enforces that rule so a half-translated
// sentence cannot ship unnoticed.
var catalogs = map[string]map[string]string{
	RU: ruMessages,
	EN: enMessages,
}

// Normalize returns a supported language code. An unknown or empty value falls back to Default
// instead of erroring: a stale row or a config typo must not make the bot silent.
func Normalize(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if _, ok := catalogs[lang]; ok {
		return lang
	}
	return Default
}

// Lookup returns the template of a key in lang. It falls back to Default when the key is missing in
// the requested language, and reports false when neither language knows the key.
func Lookup(lang string, key string) (string, bool) {
	lang = Normalize(lang)
	if template, ok := catalogs[lang][key]; ok {
		return template, true
	}
	template, ok := catalogs[Default][key]
	return template, ok
}

// T renders the message lang:key with the explicit printf arguments args. The fallback order is the
// requested language, Default, and finally the key itself — a missing translation stays visible as a
// stable ID instead of turning into an empty message.
func T(lang string, key string, args ...any) string {
	template, ok := Lookup(lang, key)
	if !ok {
		return key
	}
	if len(args) == 0 {
		return template
	}
	return fmt.Sprintf(template, args...)
}

// Keys returns the sorted key set of a language. It exists for tests and for the documentation
// generator; production code only uses T.
func Keys(lang string) []string {
	messages, ok := catalogs[Normalize(lang)]
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(messages))
	for key := range messages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var ruMessages = map[string]string{
	// The command registry descriptions (/help, setMyCommands stays on Command.Description).
	"command.start.description":     "регистрация чата и список команд",
	"command.help.description":      "справка по командам",
	"command.ping.description":      "health бота: pong + uptime",
	"command.status.description":    "стриминг, модель, uptime и ожидание ответа",
	"command.model.description":     "модель, endpoint, /model check, select и reset",
	"command.stream.description":    "стриминг для этого чата: on, off, reset",
	"command.miniapp.description":   "доставка ответа Mini App в Telegram: on, off, reset",
	"command.persona.description":   "роль модели для этого чата: пресеты, custom, reset",
	"command.ask.description":       "вопрос модели",
	"command.language.description":  "язык интерфейса бота: ru, en, reset",
	"command.reset.description":     "очистить контекст чата",
	"command.skip.description":      "не восстанавливать текущий unanswered-вопрос при старте",
	"command.unskip.description":    "снова разрешить recovery ранее пропущенного вопроса",
	"command.history.description":   "последние N сообщений (по умолчанию 5)",
	"command.docs.description":      "локальная RAG-база документов чата",
	"command.links.description":     "ссылки на потом: add, search, summary, delete",
	"command.remind.description":    "напоминание: через N минут/часов/дней или каждый день HH:MM",
	"command.reminders.description": "активные напоминания и отмена по id",
	"command.task.description":      "безопасная задача из 1–3 шагов: создать, статус, cancel",
	"command.tts.description":       "озвучить текст через opt-in TTS",
	"command.export.description":    "экспорт истории: md или json",
	"command.search.description":    "поиск по истории: /search <текст>",
	"command.delete.description":    "удаление истории с подтверждением",
	"command.stats.description":     "счётчики чата: сообщения, токены, ошибки",
	"command.feedback.description":  "оценка последнего ответа: up или down",
	"command.fb.description":        "то же, что /feedback: up или down",
	"command.usage.description":     "использование модели и инструментов за UTC-сутки",
	"command.whoami.description":    "user_id и chat_id для TELEGRAM_ALLOWED_USERS",
	"command.admin.description":     "агрегированные метрики для администратора",

	// /help and /start.
	"help.line":            "%s — %s",
	"help.args_suffix":     " <текст>",
	"help.remind_suffix":   " <время> <текст>",
	"help.language_suffix": " [ru|en|reset]",
	"help.remind_example":  "Пример: /remind через 1 минуту купить молоко",
	"help.attachments":     "Можно отправить альбом фото, текстовый документ, голосовое, аудио или видеосообщение.",
	"start.greeting":       "Привет! Я прокси к локальной модели STRATA. Команды:",

	// /ping.
	"ping.pong": "pong %s ",

	// /status.
	"status.title":           "Состояние бота:",
	"status.build":           "Build: %s",
	"status.uptime":          "Uptime: %s",
	"status.model":           "Модель: %s",
	"status.persona":         "Persona: %s",
	"status.streaming":       "Стриминг: %s",
	"status.streaming_off":   "выключен",
	"status.streaming_on":    "включён — правки каждые %d мс, idle %d с, total %d с",
	"status.override":        " (override этого чата)",
	"status.pending":         "Последний вопрос чата ждёт ответа: %s",
	"status.pending_no":      "нет",
	"status.pending_yes":     "да — сообщение id=%d",
	"status.pending_skipped": "; автовосстановление отключено (/skip)",

	// /model.
	"model.reset":          "Для этого чата восстановлена модель по умолчанию: %s",
	"model.check_fail":     "STRATA не отвечает: %s.",
	"model.empty_list":     "STRATA отвечает, но список моделей пуст. Endpoint: %s",
	"model.check_result":   "Endpoint: %s\nМодели (%d): %s",
	"model.select_current": "Текущая модель чата: %s\nДоступны (%d): %s\n/model select <id>, /model reset — вернуть %s",
	"model.not_found":      "Модель %q отсутствует в GET /models. Доступны: %s",
	"model.selected":       "Для этого чата выбрана модель: %s. /model reset — вернуть %s",
	"model.info":           "Модель: %s%s\nEndpoint: %s \n/model check — проверка доступности; /model select <id>, /model reset.",
	"model.override_note":  " (override; default %s)",
	"model.menu_current":   "Текущая модель: %s\nEndpoint: %s",
	"model.menu_checking":  "Проверка endpoint выполнена.",
	"model.menu_reset":     "Восстановлена модель по умолчанию: %s",
	"model.menu_selected":  "Для этого чата выбрана модель: %s",
	"model.endpoint_ok":    "Endpoint отвечает; моделей доступно: %d.",
	"model.first_n":        "Показаны первые %d моделей из %d.",

	// /stream.
	"stream.state_on":      "включён",
	"stream.state_off":     "выключен",
	"stream.saved":         "Стриминг для этого чата %s. Настройка сохранена и переживёт рестарт. /stream reset — вернуть общий STREAMING_ENABLED.",
	"stream.usage":         "Использование: /stream — состояние, /stream on, /stream off, /stream reset — общий STREAMING_ENABLED.",
	"stream.state":         "Стриминг для этого чата: %s. Источник: %s. /stream on, /stream off, /stream reset.",
	"stream.source_global": "общий STREAMING_ENABLED",
	"stream.source_chat":   "настройка этого чата",
	"stream.menu":          "Стриминг: %s.\nИсточник: %s.",
	"stream.menu_saved":    "Настройка сохранена для этого чата.",
	"stream.menu_reset":    "Восстановлен общий STREAMING_ENABLED.",

	// /miniapp.
	"miniapp.state_on":      "включена",
	"miniapp.state_off":     "выключена",
	"miniapp.saved":         "Доставка ответа Mini App в Telegram %s для этого чата. Настройка сохранена и переживёт рестарт. /miniapp reset — вернуть общий MINIAPP_TELEGRAM_FALLBACK.",
	"miniapp.usage":         "Использование: /miniapp — состояние, /miniapp on, /miniapp off, /miniapp reset — общий MINIAPP_TELEGRAM_FALLBACK.",
	"miniapp.reset_prefix":  "Настройка этого чата сброшена. ",
	"miniapp.state":         "Доставка ответа Mini App в Telegram: %s. Источник: %s. /miniapp on, /miniapp off, /miniapp reset.",
	"miniapp.source_global": "общий MINIAPP_TELEGRAM_FALLBACK",
	"miniapp.source_chat":   "настройка этого чата",

	// /persona.
	"persona.menu_unknown":      "Неизвестная или истёкшая persona.",
	"persona.reset":             "Для этого чата восстановлен глобальный SYSTEM_PROMPT. Изменение применится к следующему запросу.",
	"persona.custom_needs_text": "После custom нужен текст роли: /persona custom <системный промпт>.",
	"persona.custom_saved":      "Пользовательская persona сохранена для этого чата и будет применяться со следующего запроса. /persona reset — возврат к SYSTEM_PROMPT.",
	"persona.selected":          "Для этого чата выбрана persona %s. Изменение применится к следующему запросу. /persona reset — возврат к SYSTEM_PROMPT.",
	"persona.unknown":           "Неизвестная persona. Используйте /persona, чтобы увидеть список, или /persona custom <текст>.",
	"persona.source_global":     "глобальный SYSTEM_PROMPT",
	"persona.source_chat":       "настройка этого чата",
	"persona.current":           "Текущая persona: %s (%s).",
	"persona.presets_header":    "Доступные пресеты:",
	"persona.preset_line":       "/persona %s — %s",
	"persona.custom_line":       "/persona custom <текст> — своя роль",
	"persona.reset_line":        "/persona reset — вернуть глобальный SYSTEM_PROMPT",
	"persona.menu_reset":        "Восстановлен глобальный SYSTEM_PROMPT.",
	"persona.menu_selected":     "Для этого чата выбрана persona %s.",
	"persona.custom_hint":       "Отправь системный промпт для пользовательской persona. Ввод действует 2 минуты.",

	// /language.
	"language.current":        "Текущий язык: %s (источник: %s).",
	"language.source_chat":    "этот чат",
	"language.source_default": "по умолчанию",
	"language.self":           "русский",
	"language.set":            "Язык этого чата: %s. /language reset — вернуться к языку по умолчанию.",
	"language.reset":          "Настройка языка сброшена. Язык этого чата по умолчанию: %s.",
	"language.usage":          "Использование: /language — текущий язык, /language ru, /language en, /language reset — вернуть DEFAULT_LANGUAGE.",

	// /reset.
	"reset.done": "Контекст очищен: модель больше не видит %d сообщений. История осталась — /history.",

	// /skip and /unskip.
	"skip.none":   "Нет ожидающего ответа вопроса: пропускать нечего.",
	"skip.done":   "Автовосстановление вопроса id=%d отключено при следующих запусках. История сохранена; следующий вопрос будет обрабатываться как обычно.",
	"unskip.none": "Нет отключённого автовосстановления для этого чата: изменений не требуется.",
	"unskip.done": "Автовосстановление вопроса id=%d снова разрешено. Модель сейчас не вызывается: попытка будет сделана при следующем перезапуске, если этот вопрос останется самым новым без ответа и не превысит RECOVERY_MAX_AGE_HOURS.",

	// /history.
	"history.usage":  "/history ждёт число: /history 10 (по умолчанию %d).",
	"history.empty":  "История чата пуста.",
	"history.header": "Последние %d сообщений :",
	"history.line":   "%d. %s: %s",

	// /stats.
	"stats.messages": "Сообщений: %d ",
	"stats.tokens":   "Токенов: %d входящих, %d исходящих ",
	"stats.errors":   "Ошибок модели: %d ",
	"stats.timeouts": "Таймаутов модели: %d ",
	"stats.feedback": "Оценки: 👍 %d / 👎 %d",
	"stats.model":    "Модель: %s",
	"stats.last":     "Последнее сообщение: %s ",

	// /whoami.
	"whoami.allowed": "в allowlist: да ",
	"whoami.denied":  "в allowlist: нет — добавь %d в TELEGRAM_ALLOWED_USERS и перезапусти ",
	"whoami.closed":  "в allowlist: нет — бот закрыт, добавь %d в TELEGRAM_ALLOWED_USERS и перезапусти ",

	// /ask wrapper texts and the shared answer finalizer.
	"ask.needs_text":                "Нужен текст вопроса: /ask <текст>.",
	"ask.unsupported_media":         "Этот тип вложения пока не поддерживается. Отправь фото, текстовый документ, голосовое, аудио или видеосообщение.",
	"ask.attachment_unavailable":    "Вложение недоступно после перезапуска, поэтому отправь вложение повторно вместе с вопросом.",
	"ask.transcription_unavailable": "Транскрипция голосовых сообщений не настроена в этом клиенте модели.",
	"ask.transcription_failed":      "Не удалось распознать голосовое сообщение. Попробуй записать его короче или отправь текстом.",
	"ask.timeout":                   "Модель не ответила за %d с (%s), попробуй ещё раз.",
	"ask.model_error":               "Модель не ответила: %s. Запрос записан в events(kind='model_error').",
	"ask.empty":                     "Модель вернула пустой ответ — это ошибка, а не ответ. Попробуй ещё раз.",
	"ask.no_room":                   "Все слоты модели заняты. Подожди немного.",
	"ask.rate_limited":              "Слишком много вызовов модели за минуту. Попробуйте ещё раз через минуту.",
	"ask.daily_limited":             "Дневной лимит обращений к модели исчерпан. Попробуйте ещё раз после 00:00 UTC.",
	"ask.finish_length":             "Внимание: ответ обрезан лимитом модели (finish_reason=length).",
	"ask.finish_content_filter":     "Внимание: провайдер отфильтровал часть ответа (finish_reason=content_filter).",
	"ask.doc_truncated":             "Внимание: документ был обрезан по MAX_CONTEXT_CHARS перед отправкой модели.",
	"ask.sources":                   "Источники: %s",
	"ask.latency_no_tokens":         "⏱ %s · модель %s · попыток %d (токены не возвращены)",
	"ask.latency_tokens":            "⏱ %s · %d→%d токенов · модель %s · попыток %d",

	// Attachment errors surfaced by /ask.
	"ask.attachment_too_large":        "Вложение слишком большое. Для текста лимит — 64 КиБ, для PDF/DOCX — 20 МиБ (предел Bot API getFile); фото и аудио/видео ограничивает MEDIA_MAX_MB.",
	"ask.attachment_empty_document":   "Документ пустой: в нём нет текста, который можно сохранить.",
	"ask.attachment_voice_too_long":   "Аудио или видеосообщение слишком длинное. Лимит задаёт SPEECH_MAX_SECONDS (по умолчанию — 5 минут).",
	"ask.attachment_no_text":          "В документе нет текстового слоя — OCR недоступен. Загрузи PDF с текстом или распознай его отдельно.",
	"ask.attachment_encrypted":        "PDF защищён паролем. Сними пароль и отправь файл повторно.",
	"ask.attachment_unsupported_doc":  "Старый формат .doc не поддерживается. Сохрани документ как .docx или .pdf.",
	"ask.attachment_corrupt":          "Не удалось разобрать документ. Проверь, что файл не повреждён, и отправь его повторно.",
	"ask.attachment_type":             "Поддерживаются фото JPEG/PNG, текстовые документы UTF-8, PDF и DOCX, голосовые и аудиосообщения, а также видеосообщения.",
	"ask.attachment_unavailable_file": "Не удалось получить вложение из Telegram. Отправь его повторно.",
	"ask.attachment_read_failed":      "Не удалось прочитать вложение. Проверь файл и отправь его повторно.",

	// /feedback.
	"feedback.count":     "Оценки: 👍 %d / 👎 %d\n%s",
	"feedback.usage":     "Поставь оценку последнему ответу: /feedback up — 👍, /feedback down — 👎.",
	"feedback.no_answer": "В этом чате ещё нет ответа модели — оценивать нечего.",
	"feedback.unknown":   "Не понял оценку %q. %s",
	"feedback.already":   "Оценка уже стоит: %s.",
	"feedback.thanks":    "Спасибо! Оценка сохранена: %s.",

	// /export, /search and /delete.
	"export.usage":            "Используй /export md или /export json.",
	"export.too_large":        "Экспорт превышает 5 МиБ. Удали старую историю или используй более поздний чат.",
	"export.caption":          "Экспорт истории",
	"search.needs_text":       "Нужен текст поиска: /search <текст>.",
	"search.no_matches":       "Совпадений не найдено. Область поиска: вся история чата; вывод — до %d совпадений.",
	"search.header":           "Найдено сообщений: %d. Область поиска: вся история чата; вывод — до %d совпадений.",
	"search.truncated_suffix": " Показаны самые новые совпадения.",
	"search.line":             "%d. %s · %s: %s",
	"delete.confirm_text":     "Удалить всю историю этого чата? Настройки модели и persona сохранятся. Кнопки действуют 5 минут.",
	"delete.confirm_button":   "Подтвердить удаление",
	"delete.cancel_button":    "Отмена",
	"delete.fallback":         "Для подтверждения отправь /delete confirm. Для отмены — /delete cancel.",
	"delete.stale":            "Это меню удаления истекло или уже использовано. Отправь /delete заново.",
	"delete.wrong_user":       "Это меню удаления принадлежит другому пользователю. Отправь /delete самостоятельно.",
	"delete.wrong_message":    "Это меню удаления недействительно для этого чата или сообщения. Отправь /delete заново.",
	"delete.cancelled":        "Удаление истории отменено.",
	"delete.done":             "Удалено сообщений: %d. Настройки чата сохранены.",
	"delete.usage":            "Используй /delete, затем подтверди или отмени удаление кнопкой.",

	// /usage.
	"usage.title":         "Использование за %s (UTC):",
	"usage.models":        "Модели:",
	"usage.no_calls":      "- обращений не было",
	"usage.model_line":    "- %s: вызовов %d; токены %d входящих / %d исходящих",
	"usage.unnamed_model": "модель не указана",
	"usage.tools":         "Инструменты:",
	"usage.tool_line":     "- %s: %d",
	"usage.limits":        "Дневные лимиты:",
	"usage.limit_model":   "Модель",
	"usage.limit_network": "Сетевые инструменты",
	"usage.limit_zero":    "%s: лимит 0 (без ограничений); использовано %d; осталось без ограничений",
	"usage.limit":         "%s: лимит %d; использовано %d; осталось %d",
}

var enMessages = map[string]string{
	// The command registry descriptions.
	"command.start.description":     "register the chat and list commands",
	"command.help.description":      "command reference",
	"command.ping.description":      "bot health: pong + uptime",
	"command.status.description":    "streaming, model, uptime and a pending answer",
	"command.model.description":     "model, endpoint, /model check, select and reset",
	"command.stream.description":    "streaming for this chat: on, off, reset",
	"command.miniapp.description":   "Mini App answer delivery in Telegram: on, off, reset",
	"command.persona.description":   "model role for this chat: presets, custom, reset",
	"command.ask.description":       "ask the model a question",
	"command.language.description":  "bot interface language: ru, en, reset",
	"command.reset.description":     "clear the chat context",
	"command.skip.description":      "do not recover the current unanswered question on restart",
	"command.unskip.description":    "allow recovery of a previously skipped question again",
	"command.history.description":   "latest N messages (5 by default)",
	"command.docs.description":      "the chat's local RAG document base",
	"command.links.description":     "links for later: add, search, summary, delete",
	"command.remind.description":    "reminder: in N minutes/hours/days or every day at HH:MM",
	"command.reminders.description": "active reminders and cancellation by id",
	"command.task.description":      "safe 1–3 step task: create, status, cancel",
	"command.tts.description":       "read text aloud through opt-in TTS",
	"command.export.description":    "export history: md or json",
	"command.search.description":    "search the history: /search <text>",
	"command.delete.description":    "delete the history with confirmation",
	"command.stats.description":     "chat counters: messages, tokens, errors",
	"command.feedback.description":  "rate the latest answer: up or down",
	"command.fb.description":        "same as /feedback: up or down",
	"command.usage.description":     "model and tool usage for the UTC day",
	"command.whoami.description":    "user_id and chat_id for TELEGRAM_ALLOWED_USERS",
	"command.admin.description":     "aggregated metrics for the administrator",

	// /help and /start.
	"help.line":            "%s — %s",
	"help.args_suffix":     " <text>",
	"help.remind_suffix":   " <time> <text>",
	"help.language_suffix": " [ru|en|reset]",
	"help.remind_example":  "Example: /remind in 1 minute buy milk",
	"help.attachments":     "You can send a photo album, a text document, a voice message, audio or a video note.",
	"start.greeting":       "Hi! I am a proxy to the local STRATA model. Commands:",

	// /ping.
	"ping.pong": "pong %s ",

	// /status.
	"status.title":           "Bot status:",
	"status.build":           "Build: %s",
	"status.uptime":          "Uptime: %s",
	"status.model":           "Model: %s",
	"status.persona":         "Persona: %s",
	"status.streaming":       "Streaming: %s",
	"status.streaming_off":   "off",
	"status.streaming_on":    "on — edits every %d ms, idle %d s, total %d s",
	"status.override":        " (this chat's override)",
	"status.pending":         "The chat's latest question is waiting for an answer: %s",
	"status.pending_no":      "no",
	"status.pending_yes":     "yes — message id=%d",
	"status.pending_skipped": "; auto-recovery is disabled (/skip)",

	// /model.
	"model.reset":          "The default model is restored for this chat: %s",
	"model.check_fail":     "STRATA is not responding: %s.",
	"model.empty_list":     "STRATA responds, but the model list is empty. Endpoint: %s",
	"model.check_result":   "Endpoint: %s\nModels (%d): %s",
	"model.select_current": "Current chat model: %s\nAvailable (%d): %s\n/model select <id>, /model reset — return to %s",
	"model.not_found":      "Model %q is not in GET /models. Available: %s",
	"model.selected":       "Model selected for this chat: %s. /model reset — return to %s",
	"model.info":           "Model: %s%s\nEndpoint: %s \n/model check — availability check; /model select <id>, /model reset.",
	"model.override_note":  " (override; default %s)",
	"model.menu_current":   "Current model: %s\nEndpoint: %s",
	"model.menu_checking":  "Endpoint check completed.",
	"model.menu_reset":     "Default model restored: %s",
	"model.menu_selected":  "Model selected for this chat: %s",
	"model.endpoint_ok":    "Endpoint responds; models available: %d.",
	"model.first_n":        "Showing the first %d models out of %d.",

	// /stream.
	"stream.state_on":      "on",
	"stream.state_off":     "off",
	"stream.saved":         "Streaming for this chat is %s. The setting is saved and survives a restart. /stream reset — return to the global STREAMING_ENABLED.",
	"stream.usage":         "Usage: /stream — state, /stream on, /stream off, /stream reset — the global STREAMING_ENABLED.",
	"stream.state":         "Streaming for this chat: %s. Source: %s. /stream on, /stream off, /stream reset.",
	"stream.source_global": "the global STREAMING_ENABLED",
	"stream.source_chat":   "this chat's setting",
	"stream.menu":          "Streaming: %s.\nSource: %s.",
	"stream.menu_saved":    "The setting is saved for this chat.",
	"stream.menu_reset":    "The global STREAMING_ENABLED is restored.",

	// /miniapp.
	"miniapp.state_on":      "on",
	"miniapp.state_off":     "off",
	"miniapp.saved":         "Mini App answer delivery in Telegram is %s for this chat. The setting is saved and survives a restart. /miniapp reset — return to the global MINIAPP_TELEGRAM_FALLBACK.",
	"miniapp.usage":         "Usage: /miniapp — state, /miniapp on, /miniapp off, /miniapp reset — the global MINIAPP_TELEGRAM_FALLBACK.",
	"miniapp.reset_prefix":  "This chat's setting was reset. ",
	"miniapp.state":         "Mini App answer delivery in Telegram: %s. Source: %s. /miniapp on, /miniapp off, /miniapp reset.",
	"miniapp.source_global": "the global MINIAPP_TELEGRAM_FALLBACK",
	"miniapp.source_chat":   "this chat's setting",

	// /persona.
	"persona.menu_unknown":      "Unknown or expired persona.",
	"persona.reset":             "The global SYSTEM_PROMPT is restored for this chat. The change applies to the next request.",
	"persona.custom_needs_text": "custom needs the role text: /persona custom <system prompt>.",
	"persona.custom_saved":      "The custom persona is saved for this chat and applies from the next request. /persona reset — return to SYSTEM_PROMPT.",
	"persona.selected":          "Persona %s is selected for this chat. The change applies to the next request. /persona reset — return to SYSTEM_PROMPT.",
	"persona.unknown":           "Unknown persona. Use /persona to see the list, or /persona custom <text>.",
	"persona.source_global":     "the global SYSTEM_PROMPT",
	"persona.source_chat":       "this chat's setting",
	"persona.current":           "Current persona: %s (%s).",
	"persona.presets_header":    "Available presets:",
	"persona.preset_line":       "/persona %s — %s",
	"persona.custom_line":       "/persona custom <text> — your own role",
	"persona.reset_line":        "/persona reset — return to the global SYSTEM_PROMPT",
	"persona.menu_reset":        "The global SYSTEM_PROMPT is restored.",
	"persona.menu_selected":     "Persona %s is selected for this chat.",
	"persona.custom_hint":       "Send the system prompt for the custom persona. The input is valid for 2 minutes.",

	// /language.
	"language.current":        "Current language: %s (source: %s).",
	"language.source_chat":    "this chat",
	"language.source_default": "default",
	"language.self":           "English",
	"language.set":            "This chat's language: %s. /language reset — return to the default language.",
	"language.reset":          "The language setting was reset. This chat now uses the default language: %s.",
	"language.usage":          "Usage: /language — current language, /language ru, /language en, /language reset — return to DEFAULT_LANGUAGE.",

	// /reset.
	"reset.done": "Context cleared: the model no longer sees %d messages. The history is kept — /history.",

	// /skip and /unskip.
	"skip.none":   "There is no question waiting for an answer: nothing to skip.",
	"skip.done":   "Auto-recovery of question id=%d is disabled for the next starts. The history is kept; the next question will be handled as usual.",
	"unskip.none": "No disabled auto-recovery exists for this chat: nothing to change.",
	"unskip.done": "Auto-recovery of question id=%d is allowed again. The model is not called now: an attempt will be made on the next restart if this question is still the newest unanswered one and does not exceed RECOVERY_MAX_AGE_HOURS.",

	// /history.
	"history.usage":  "/history expects a number: /history 10 (%d by default).",
	"history.empty":  "The chat history is empty.",
	"history.header": "Latest %d messages:",
	"history.line":   "%d. %s: %s",

	// /stats.
	"stats.messages": "Messages: %d ",
	"stats.tokens":   "Tokens: %d in, %d out ",
	"stats.errors":   "Model errors: %d ",
	"stats.timeouts": "Model timeouts: %d ",
	"stats.feedback": "Ratings: 👍 %d / 👎 %d",
	"stats.model":    "Model: %s",
	"stats.last":     "Last message: %s ",

	// /whoami.
	"whoami.allowed": "in allowlist: yes ",
	"whoami.denied":  "in allowlist: no — add %d to TELEGRAM_ALLOWED_USERS and restart ",
	"whoami.closed":  "in allowlist: no — the bot is closed, add %d to TELEGRAM_ALLOWED_USERS and restart ",

	// /ask wrapper texts and the shared answer finalizer.
	"ask.needs_text":                "A question is required: /ask <text>.",
	"ask.unsupported_media":         "This attachment type is not supported yet. Send a photo, a text document, a voice message, audio or a video note.",
	"ask.attachment_unavailable":    "The attachment is unavailable after a restart, so send it again together with the question.",
	"ask.transcription_unavailable": "Voice transcription is not configured in this model client.",
	"ask.transcription_failed":      "Could not transcribe the voice message. Try a shorter recording or send text.",
	"ask.timeout":                   "The model did not answer within %d s (%s), try again.",
	"ask.model_error":               "The model did not answer: %s. The request was recorded in events(kind='model_error').",
	"ask.empty":                     "The model returned an empty answer — that is an error, not an answer. Try again.",
	"ask.no_room":                   "All model slots are busy. Wait a bit.",
	"ask.rate_limited":              "Too many model calls per minute. Try again in a minute.",
	"ask.daily_limited":             "The daily model call limit is exhausted. Try again after 00:00 UTC.",
	"ask.finish_length":             "Warning: the answer was cut by the model limit (finish_reason=length).",
	"ask.finish_content_filter":     "Warning: the provider filtered part of the answer (finish_reason=content_filter).",
	"ask.doc_truncated":             "Warning: the document was truncated by MAX_CONTEXT_CHARS before it was sent to the model.",
	"ask.sources":                   "Sources: %s",
	"ask.latency_no_tokens":         "⏱ %s · model %s · attempts %d (no token counts returned)",
	"ask.latency_tokens":            "⏱ %s · %d→%d tokens · model %s · attempts %d",

	// Attachment errors surfaced by /ask.
	"ask.attachment_too_large":        "The attachment is too large. Text is limited to 64 KiB, PDF/DOCX to 20 MiB (the Bot API getFile limit); MEDIA_MAX_MB limits photos and audio/video.",
	"ask.attachment_empty_document":   "The document is empty: it has no text to save.",
	"ask.attachment_voice_too_long":   "The audio or video message is too long. SPEECH_MAX_SECONDS sets the limit (5 minutes by default).",
	"ask.attachment_no_text":          "The document has no text layer — OCR is unavailable. Upload a PDF with text or recognize it separately.",
	"ask.attachment_encrypted":        "The PDF is password protected. Remove the password and send the file again.",
	"ask.attachment_unsupported_doc":  "The old .doc format is not supported. Save the document as .docx or .pdf.",
	"ask.attachment_corrupt":          "Could not parse the document. Check that the file is not damaged and send it again.",
	"ask.attachment_type":             "Supported attachments: JPEG/PNG photos, UTF-8 text documents, PDF and DOCX, voice and audio messages, and video notes.",
	"ask.attachment_unavailable_file": "Could not download the attachment from Telegram. Send it again.",
	"ask.attachment_read_failed":      "Could not read the attachment. Check the file and send it again.",

	// /feedback.
	"feedback.count":     "Ratings: 👍 %d / 👎 %d\n%s",
	"feedback.usage":     "Rate the latest answer: /feedback up — 👍, /feedback down — 👎.",
	"feedback.no_answer": "This chat has no model answer yet — there is nothing to rate.",
	"feedback.unknown":   "I did not understand the rating %q. %s",
	"feedback.already":   "The rating is already set: %s.",
	"feedback.thanks":    "Thanks! The rating is saved: %s.",

	// /export, /search and /delete.
	"export.usage":            "Use /export md or /export json.",
	"export.too_large":        "The export exceeds 5 MiB. Delete older history or use a newer chat.",
	"export.caption":          "History export",
	"search.needs_text":       "A search text is required: /search <text>.",
	"search.no_matches":       "No matches found. Search area: the whole chat history; output is limited to %d matches.",
	"search.header":           "Messages found: %d. Search area: the whole chat history; output is limited to %d matches.",
	"search.truncated_suffix": " The newest matches are shown.",
	"search.line":             "%d. %s · %s: %s",
	"delete.confirm_text":     "Delete the whole history of this chat? Model and persona settings are kept. The buttons are valid for 5 minutes.",
	"delete.confirm_button":   "Confirm deletion",
	"delete.cancel_button":    "Cancel",
	"delete.fallback":         "To confirm, send /delete confirm. To cancel, send /delete cancel.",
	"delete.stale":            "This deletion menu has expired or was already used. Send /delete again.",
	"delete.wrong_user":       "This deletion menu belongs to another user. Send /delete yourself.",
	"delete.wrong_message":    "This deletion menu is no longer valid for this chat or message. Send /delete again.",
	"delete.cancelled":        "History deletion cancelled.",
	"delete.done":             "Messages deleted: %d. The chat settings are kept.",
	"delete.usage":            "Use /delete, then confirm or cancel the deletion with the button.",

	// /usage.
	"usage.title":         "Usage for %s (UTC):",
	"usage.models":        "Models:",
	"usage.no_calls":      "- no calls",
	"usage.model_line":    "- %s: %d calls; tokens %d in / %d out",
	"usage.unnamed_model": "model not specified",
	"usage.tools":         "Tools:",
	"usage.tool_line":     "- %s: %d",
	"usage.limits":        "Daily limits:",
	"usage.limit_model":   "Model",
	"usage.limit_network": "Network tools",
	"usage.limit_zero":    "%s: limit 0 (unlimited); used %d; unlimited remaining",
	"usage.limit":         "%s: limit %d; used %d; remaining %d",
}
