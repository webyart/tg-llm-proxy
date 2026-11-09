(() => {
  "use strict";

  const translations = {
    ru: {
      documentTitle: "Бот — компаньон", headerTitle: "Бот — компаньон", headerSubtitle: "Данные чата и его настройки",
      languageLabel: "Язык", closeSession: "Закрыть сессию", launchTitle: "Запуск из Telegram",
      launchText: "Откройте Mini App через «Ещё → Mini App» в меню бота. Ссылка запуска и подпись Telegram проверяются до показа данных чата.",
      navLabel: "Разделы чата", tabStatus: "Статус", tabHistory: "История", tabDocuments: "Документы",
      tabLinks: "Ссылки", tabTasks: "Задачи", tabReminders: "Напоминания", tabUsage: "Использование", tabSettings: "Настройки", refresh: "Обновить", loadOlder: "Показать ранее", loading: "Загрузка…",
      tabChat: "Чат", chatTitle: "Диалог с моделью", chatIntro: "Сообщение проходит тот же конвейер, что и в Telegram: история чата, документы, лимиты и дневная квота.",
      tabChatShort: "Чат", tabStatusShort: "Статус", tabHistoryShort: "История", tabDocumentsShort: "Докум.",
      tabLinksShort: "Ссылки", tabTasksShort: "Задачи", tabRemindersShort: "Напом.", tabUsageShort: "Использ.", tabSettingsShort: "Настр.",
      chatPlaceholder: "Напишите сообщение…", chatInputAria: "Текст сообщения", chatSend: "Отправить",
      chatPending: "Модель отвечает…", chatThinking: "Модель думает…", chatReadingURL: "Читаю страницу…", chatSearching: "Ищу информацию…",
      chatMessageEmpty: "Введите текст сообщения.", chatMessageTooLong: "Сообщение должно быть не длиннее 2000 символов.",
      chatReset: "Очистить контекст", chatResetConfirm: "Подтвердить", chatResetWarning: "Очистить контекст диалога? История останется, но модель перестанет её видеть.",
      chatResetDone: "Контекст очищен.", chatResetFailed: "Не удалось очистить контекст.",
      chatSkip: "Не восстанавливать", chatUnskip: "Возобновить восстановление", chatSkipDone: "Автовосстановление вопроса отключено.",
      chatUnskipDone: "Автовосстановление снова разрешено.", chatSkipNoop: "Ожидающего вопроса нет: изменений не требуется.",
      chatSkipFailed: "Не удалось изменить автовосстановление.", chatFailed: "Не удалось получить ответ. Попробуйте ещё раз.",
      chatStreamUnsupported: "Браузер не поддерживает потоковый ответ.", chatNetworkFailed: "Поток ответа прервался. Проверьте соединение и повторите попытку.",
      chatIdentity: "user_id: {user} · chat_id: {chat}", chatForbidden: "Чат в Mini App доступен только пользователям из allowlist.",
      chatInvalidText: "Отправьте непустое сообщение длиной до 2000 символов.", chatTooLongError: "Сообщение должно быть не длиннее 2000 символов.",
      chatUnavailable: "Чат недоступен в этом развёртывании Mini App.", chatMutationFailed: "Не удалось изменить состояние чата.",
      sessionExpired: "Сессия истекла. Откройте Mini App заново из Telegram.",
      launchRequired: "Запустите страницу через «Ещё → Mini App» в Telegram.",
      verifyLaunch: "Проверяем запуск из Telegram…", launchFailed: "Не удалось проверить запуск. Откройте Mini App заново из Telegram.",
      sessionClosed: "Сессия закрыта. Откройте Mini App из Telegram, чтобы начать новую.",
      lastActivity: "Последняя активность", messages: "Сообщения", documents: "Документы", savedLinks: "Ссылки",
      tasks: "Задачи", activeTasks: "Активные задачи", awaitingAnswer: "Ожидает ответа", recoverySuppressed: "Автовосстановление отключено", yes: "Да", no: "Нет",
      emptyHistory: "Сообщений пока нет.", emptyItems: "Здесь пока пусто.", you: "Вы", assistant: "Ассистент",
      bytes: "байт", chunks: "фрагментов", step: "шаг", attempts: "попыток", due: "срок",
      task_ready: "готова", task_running: "выполняется", task_awaiting_approval: "ждёт разрешения",
      task_completed: "завершена", task_cancelled: "отменена", task_failed: "ошибка",
      expires: "истекает", taskError: "Причина", cancelTask: "Отменить", resumeTask: "Возобновить",
      confirmAction: "Подтвердить", createAction: "Создать", addAction: "Добавить",
      deleteHistory: "Удалить", clearHistory: "Очистить историю",
      deleteHistoryWarning: "Удалить это сообщение? Это действие нельзя отменить.",
      clearHistoryWarning: "Удалить всю историю этого чата? Это действие нельзя отменить.",
      historyUpdated: "История обновлена.", historyMutationFailed: "Не удалось изменить историю.",
      cancelTaskWarning: "Отменить активную задачу? Это действие нельзя отменить.",
      cancelReminderWarning: "Отменить это напоминание? Это действие нельзя отменить.",
      addLinkTitle: "Добавить ссылку", addLinkText: "Сохраните веб-страницу для поиска и работы ассистента.",
      linkURL: "URL", linkURLPlaceholder: "https://example.com/page", linkCreated: "Ссылка добавлена.",
      linkInvalidURL: "Введите корректный HTTPS URL длиной до 2048 байт.",
      searchLinks: "Найти", linkSearchAria: "Поиск по ссылкам", linkSearchPlaceholder: "Поиск по сохранённым ссылкам",
      linkSearchResults: "Результаты поиска", linkSearchEmpty: "По запросу ничего не найдено.",
      linkSearchEmptyQuery: "Введите непустой поисковый запрос.", linkSearchInvalid: "Введите запрос длиной до 200 символов.", linkSearchFailed: "Не удалось выполнить поиск ссылок.",
      createReminderTitle: "Новое напоминание", reminderText: "Текст напоминания", reminderTextPlaceholder: "Что нужно напомнить?",
      reminderDateTime: "Дата и время", reminderDailyTime: "Время ежедневно", reminderCreated: "Напоминание создано.",
      reminderTextInvalid: "Введите текст от 1 до 500 символов без NUL.",
      reminderDateInvalid: "Выберите дату в будущем, не более чем через 3650 дней.", reminderTimeInvalid: "Выберите время в формате HH:MM.",
      reminderInvalid: "Проверьте текст, периодичность и время напоминания.",
      createTaskTitle: "Новая задача", createTaskText: "Опишите цель — план и выполнение создаются тем же обработчиком, что и в Telegram.",
      taskGoal: "Цель задачи", taskGoalPlaceholder: "Например: подготовить краткий обзор проекта", taskCreated: "Задача создана.",
      taskGoalInvalid: "Введите цель от 1 до 2000 символов без NUL.",
      deleteDocument: "Удалить", deleteDocumentConfirm: "Подтвердить", clearDocuments: "Очистить документы", clearDocumentsConfirm: "Подтвердить",
      deleteDocumentWarning: "Удалить этот документ и его фрагменты? Это действие нельзя отменить.",
      clearDocumentsWarning: "Удалить все документы этого чата и их фрагменты? Это действие нельзя отменить.",
      documentUpdated: "Документы удалены.", documentMutationFailed: "Не удалось удалить документы.",
      searchDocuments: "Найти", documentSearchAria: "Поиск по документам", documentSearchPlaceholder: "Поисковый запрос",
      documentSearchResults: "Результаты поиска", documentSearchEmpty: "По запросу ничего не найдено.",
      documentSearchEmptyQuery: "Введите непустой поисковый запрос.", documentSearchFailed: "Не удалось выполнить поиск документов.",
      documentSearchId: "id документа", documentSearchScore: "оценка",
      documentSearchTruncated: "Совпадений больше, чем помещается в один поиск; оценена только часть фрагментов.",
      uploadDocumentTitle: "Загрузить документ",
      uploadDocumentHint: "Текст UTF-8 до 64 КиБ, PDF или DOCX до 20 МиБ.",
      documentFileAria: "Файл для загрузки",
      uploadDocument: "Загрузить", documentUploading: "Загрузка…",
      documentUploaded: "Документ загружен.",
      documentUploadFailed: "Не удалось загрузить документ.",
    documentUploadTimeout: "Загрузка прервалась. Проверьте соединение и отправьте файл ещё раз.",
      documentUploadChoose: "Выберите файл.",
      documentUploadEmpty: "Файл пустой.",
      documentUploadTooLarge: "Текстовый файл больше 64 КиБ: для текста лимит 64 КиБ.",
      documentUploadBinaryTooLarge: "PDF/DOCX больше 20 МиБ: это предел Bot API getFile.",
      documentUploadLimit: "Файл больше лимита: текст — до 64 КиБ, PDF/DOCX — до 20 МиБ.",
      documentUploadType: "Поддерживаются UTF-8 текст, PDF и DOCX. Старый .doc не поддерживается.",
      documentUploadNoText: "В документе нет текстового слоя — OCR недоступен.",
      documentUploadEncrypted: "PDF защищён паролем. Снимите пароль и повторите.",
      documentUploadCorrupt: "Не удалось разобрать PDF/DOCX. Проверьте файл.",
      documentLimitReached: "Достигнут лимит документов этого чата. Удалите один документ и повторите.",
      searchHistory: "Найти", historySearchAria: "Поиск по истории", historySearchPlaceholder: "Поисковый запрос",
      historySearchResults: "Результаты поиска", historySearchEmpty: "По запросу ничего не найдено.",
      historySearchEmptyQuery: "Введите непустой поисковый запрос.", historySearchFailed: "Не удалось выполнить поиск по истории.",
      historySearchTruncated: "Показаны только самые новые совпадения; более старые совпадения не вошли в ответ.",
      historyExportMD: "Скачать Markdown", historyExportJSON: "Скачать JSON",
      historyExportReady: "Экспорт истории подготовлен.", historyExportFailed: "Не удалось экспортировать историю.",
      historyExportEmpty: "Нет сообщений для экспорта.", historyExportTooLarge: "Экспорт истории превышает 5 МиБ.",
      feedbackUp: "👍 Полезно", feedbackDown: "👎 Не полезно",
      feedbackGroup: "Оценка ответа",
      feedbackSaved: "Оценка сохранена.", feedbackFailed: "Не удалось сохранить оценку.",
      feedbackForbidden: "Оценить можно только ответ ассистента в этом чате.",
      showFullContent: "Показать полностью", fullContentFailed: "Не удалось загрузить полный текст.",
      activeReminders: "Активные напоминания", failedReminders: "Недоставленные напоминания",
      emptyActiveReminders: "Активных напоминаний нет.", emptyFailedReminders: "Недоставленных напоминаний нет.",
      reminderCadence: "Периодичность", reminderOnce: "один раз", reminderDaily: "ежедневно", reminderNextRun: "Следующий запуск",
      retryReminder: "Повторить", deleteReminder: "Удалить", deleteReminderConfirm: "Подтвердить",
      deleteReminderWarning: "Удалить это недоставленное напоминание? Это действие нельзя отменить.",
      reminderUpdated: "Напоминание обновлено.", reminderMutationFailed: "Не удалось изменить напоминание.",
      deleteLink: "Удалить", deleteLinkConfirm: "Подтвердить", clearLinks: "Очистить ссылки", clearLinksConfirm: "Подтвердить",
      deleteLinkWarning: "Удалить эту сохранённую ссылку? Это действие нельзя отменить.",
      clearLinksWarning: "Удалить все сохранённые ссылки этого чата? Это действие нельзя отменить.",
      linkUpdated: "Ссылка удалена.", linkMutationFailed: "Не удалось удалить ссылку.",
      deleteTask: "Удалить", deleteTaskConfirm: "Подтвердить",
      deleteTaskWarning: "Удалить эту задачу из истории? Это действие нельзя отменить.",
      clearTasks: "Очистить задачи", clearTasksConfirm: "Подтвердить",
      clearTasksWarning: "Удалить все задачи этого чата, включая активные и завершённые? Это действие нельзя отменить.",
      taskUpdated: "Задача обновлена.", taskNotActive: "Активной задачи для отмены нет.",
      taskNotDeletable: "Удалить можно только завершённую, отменённую или завершившуюся ошибкой задачу.",
      taskNotResumable: "Возобновить можно только последнюю задачу с ошибкой или отменённую задачу.",
      taskMutationFailed: "Не удалось изменить задачу.", networkApprovalTelegram: "Сетевое действие подтверждается только в Telegram.",
      usageDate: "Дата", modelCalls: "вызовов", tokensIn: "входящих токенов", tokensOut: "исходящих токенов",
      toolsTitle: "Инструменты", dailyLimits: "Дневные лимиты", used: "использовано", limit: "лимит",
      remaining: "осталось", unlimited: "без ограничений", modelUnspecified: "модель не указана",
      networkTools: "Сетевые инструменты", noUsage: "Обращений за сегодня не было.",
      settingsSaved: "Настройки чата сохранены.", settingsSaveFailed: "Не удалось сохранить настройку.",
      dataLoadFailed: "Не удалось загрузить данные.", invalidResponse: "Сервер вернул некорректный ответ.",
      networkError: "Нет соединения с сервером. Проверьте сеть и повторите попытку.", requestFailed: "Запрос не выполнен (код {status}).",
      errorModels: "Не удалось получить текущий список моделей провайдера.",
      errorInvalidModel: "Выберите модель из текущего списка провайдера.",
      errorInvalidPersona: "Выберите существующий пресет или введите от 1 до 4000 символов без NUL.",
      errorInvalidStream: "Укажите корректное состояние стриминга.", errorInvalidJSON: "Некорректные или неизвестные поля настроек.",
      errorContentType: "Ожидался строгий JSON запрос.", errorBodyLarge: "Слишком большой размер запроса.",
      errorMethod: "Этот метод не разрешён.", errorPagination: "Некорректные параметры страницы.",
      errorInvalidQuery: "Введите один непустой запрос длиной до 4000 символов.", reminderNotMutable: "Напоминание отсутствует или его нельзя изменить в текущем состоянии.",
      errorRead: "Не удалось прочитать данные.", errorOverloaded: "Mini App перегружен. Повторите попытку позже.",
      errorRateLimited: "Слишком много запросов. Повторите попытку позже.",
      errorInvalidTTSSpeed: "Выберите скорость 0,75×, 1×, 1,25× или 1,5×.",
      errorNotFound: "Страница или API не найдены.",
      sourceChat: "настройка этого чата", sourceGlobal: "общая настройка", current: "Текущее",
      globalDefault: "Глобальное значение", customText: "пользовательский текст", modelTitle: "Модель",
    accountTitle: "Язык и сессия", accountHint: "Язык интерфейса и закрытие текущей сессии Mini App.",
      availableModels: "Доступные модели провайдера", noModels: "Провайдер не сообщил ни одной модели",
      useModel: "Применить модель", resetDefault: "Сбросить к общей",
      modelsTruncated: "Показаны первые 50 моделей. Обновите, чтобы снова проверить endpoint.",
      personaTitle: "Роль (persona)", personaPresets: "Пресеты роли", usePreset: "Применить пресет",
      resetPersona: "Сбросить роль", customPlaceholder: "Пользовательский текст роли (1–4000 символов Unicode)",
      customAria: "Пользовательский текст роли", saveCustom: "Сохранить свою роль",
      streamTitle: "Стриминг ответов", streamAria: "Стриминг ответов", on: "Включён", off: "Выключен",
      saveStream: "Сохранить стриминг", resetGlobal: "Сбросить к общей",
      ttsTitle: "Озвучивание (TTS)", ttsSpeedAria: "Скорость озвучивания",
      saveTTSSpeed: "Сохранить скорость", resetTTSSpeed: "Сбросить скорость",
      ttsSpeedSaved: "Скорость озвучивания сохранена.", ttsSpeedReset: "Скорость озвучивания сброшена.",
      persona_default: "по умолчанию", persona_custom: "пользовательская", persona_assistant: "ассистент",
      persona_coder: "программист", persona_writer: "писатель", persona_translator: "переводчик",
      preset_assistant_desc: "универсальный помощник", preset_coder_desc: "программирование, код и архитектура",
      preset_writer_desc: "тексты, редактура и структура", preset_translator_desc: "перевод с сохранением смысла и формата",
    },
    en: {
      documentTitle: "Bot companion", headerTitle: "Bot companion", headerSubtitle: "Chat data and per-chat settings",
      languageLabel: "Language", closeSession: "Close session", launchTitle: "Launch from Telegram",
      launchText: "Open this Mini App with More → Mini App in the bot menu. The launch link and Telegram signature are checked before any chat data is shown.",
      navLabel: "Chat sections", tabStatus: "Status", tabHistory: "History", tabDocuments: "Documents",
      tabLinks: "Links", tabTasks: "Tasks", tabReminders: "Reminders", tabUsage: "Usage", tabSettings: "Settings", refresh: "Refresh", loadOlder: "Load older", loading: "Loading…",
      tabChat: "Chat", chatTitle: "Chat with the model", chatIntro: "Each message runs through the same pipeline as in Telegram: chat history, documents, quotas, and daily limits.",
      tabChatShort: "Chat", tabStatusShort: "Status", tabHistoryShort: "History", tabDocumentsShort: "Docs",
      tabLinksShort: "Links", tabTasksShort: "Tasks", tabRemindersShort: "Remind.", tabUsageShort: "Usage", tabSettingsShort: "Settings",
      chatPlaceholder: "Type a message…", chatInputAria: "Message text", chatSend: "Send",
      chatPending: "The model is answering…", chatThinking: "The model is thinking…", chatReadingURL: "Reading the page…", chatSearching: "Searching…",
      chatMessageEmpty: "Enter a message.", chatMessageTooLong: "The message must be at most 2000 characters.",
      chatReset: "Reset context", chatResetConfirm: "Confirm", chatResetWarning: "Reset the conversation context? History stays, but the model will no longer see it.",
      chatResetDone: "Context reset.", chatResetFailed: "The context could not be reset.",
      chatSkip: "Skip recovery", chatUnskip: "Resume recovery", chatSkipDone: "Automatic recovery of the pending question is disabled.",
      chatUnskipDone: "Automatic recovery is allowed again.", chatSkipNoop: "There is no pending question, nothing to change.",
      chatSkipFailed: "Recovery could not be changed.", chatFailed: "The answer could not be received. Try again.",
      chatStreamUnsupported: "This browser cannot stream the answer.", chatNetworkFailed: "The answer stream broke. Check your connection and try again.",
      chatIdentity: "user_id: {user} · chat_id: {chat}", chatForbidden: "The Mini App chat is available only to allowlisted users.",
      chatInvalidText: "Send a nonempty message of at most 2000 characters.", chatTooLongError: "The message must be at most 2000 characters.",
      chatUnavailable: "The chat is unavailable in this Mini App deployment.", chatMutationFailed: "The chat state could not be changed.",
      sessionExpired: "Session expired. Reopen the Mini App from Telegram.",
      launchRequired: "This page needs a launch from More → Mini App in Telegram.",
      verifyLaunch: "Verifying Telegram launch…", launchFailed: "Launch verification failed. Reopen the Mini App from Telegram.",
      sessionClosed: "Session closed. Reopen the Mini App from Telegram to start a new one.",
      lastActivity: "Last activity", messages: "Messages", documents: "Documents", savedLinks: "Saved links",
      tasks: "Tasks", activeTasks: "Active tasks", awaitingAnswer: "Awaiting answer", recoverySuppressed: "Recovery suppressed", yes: "Yes", no: "No",
      emptyHistory: "No messages yet.", emptyItems: "Nothing here yet.", you: "You", assistant: "Assistant",
      bytes: "bytes", chunks: "chunks", step: "step", attempts: "attempts", due: "due",
      task_ready: "ready", task_running: "running", task_awaiting_approval: "awaiting approval",
      task_completed: "completed", task_cancelled: "cancelled", task_failed: "failed",
      expires: "expires", taskError: "Reason", cancelTask: "Cancel", resumeTask: "Resume",
      confirmAction: "Confirm", createAction: "Create", addAction: "Add",
      deleteHistory: "Delete", clearHistory: "Clear history",
      deleteHistoryWarning: "Delete this message? This cannot be undone.",
      clearHistoryWarning: "Delete the entire history of this chat? This cannot be undone.",
      historyUpdated: "History updated.", historyMutationFailed: "The history could not be changed.",
      cancelTaskWarning: "Cancel the active task? This cannot be undone.",
      cancelReminderWarning: "Cancel this reminder? This cannot be undone.",
      addLinkTitle: "Add a link", addLinkText: "Save a web page for search and assistant workflows.",
      linkURL: "URL", linkURLPlaceholder: "https://example.com/page", linkCreated: "Link added.",
      linkInvalidURL: "Enter a valid HTTPS URL of at most 2048 bytes.",
      searchLinks: "Search", linkSearchAria: "Search links", linkSearchPlaceholder: "Search saved links",
      linkSearchResults: "Search results", linkSearchEmpty: "No links matched the query.",
      linkSearchEmptyQuery: "Enter a non-empty search query.", linkSearchInvalid: "Enter a query of at most 200 characters.", linkSearchFailed: "Link search failed.",
      createReminderTitle: "New reminder", reminderText: "Reminder text", reminderTextPlaceholder: "What should we remind you about?",
      reminderDateTime: "Date and time", reminderDailyTime: "Daily time", reminderCreated: "Reminder created.",
      reminderTextInvalid: "Enter 1 to 500 characters without NUL.",
      reminderDateInvalid: "Choose a future date no more than 3650 days ahead.", reminderTimeInvalid: "Choose a time in HH:MM format.",
      reminderInvalid: "Check the reminder text, cadence, and time.",
      createTaskTitle: "New task", createTaskText: "Describe the goal; the same handler as in Telegram creates the plan and runs it.",
      taskGoal: "Task goal", taskGoalPlaceholder: "For example: prepare a concise project overview", taskCreated: "Task created.",
      taskGoalInvalid: "Enter a goal of 1 to 2000 characters without NUL.",
      deleteDocument: "Delete", deleteDocumentConfirm: "Confirm", clearDocuments: "Clear documents", clearDocumentsConfirm: "Confirm",
      deleteDocumentWarning: "Delete this document and its chunks? This cannot be undone.",
      clearDocumentsWarning: "Delete every document in this chat and its chunks? This cannot be undone.",
      documentUpdated: "Documents deleted.", documentMutationFailed: "The documents could not be deleted.",
      searchDocuments: "Search", documentSearchAria: "Search documents", documentSearchPlaceholder: "Search query",
      documentSearchResults: "Search results", documentSearchEmpty: "No documents matched the query.",
      documentSearchEmptyQuery: "Enter a non-empty search query.", documentSearchFailed: "Document search failed.",
      documentSearchId: "document id", documentSearchScore: "score",
      documentSearchTruncated: "More chunks match than one search scores; only part of them was ranked.",
      uploadDocumentTitle: "Upload a document",
      uploadDocumentHint: "UTF-8 text up to 64 KiB, PDF or DOCX up to 20 MiB.",
      documentFileAria: "File to upload",
      uploadDocument: "Upload", documentUploading: "Uploading…",
      documentUploaded: "Document uploaded.",
      documentUploadFailed: "The document could not be uploaded.",
    documentUploadTimeout: "The upload was interrupted. Check the connection and send the file again.",
      documentUploadChoose: "Choose a file.",
      documentUploadEmpty: "The file is empty.",
      documentUploadTooLarge: "The text file is larger than 64 KiB; the text limit is 64 KiB.",
      documentUploadBinaryTooLarge: "The PDF/DOCX is larger than 20 MiB; that is the Telegram Bot API getFile limit.",
      documentUploadLimit: "The file is over the limit: text up to 64 KiB, PDF/DOCX up to 20 MiB.",
      documentUploadType: "Supported files are UTF-8 text, PDF and DOCX. Legacy .doc is not supported.",
      documentUploadNoText: "The document has no text layer — OCR is not available.",
      documentUploadEncrypted: "The PDF is password-protected. Remove the password and retry.",
      documentUploadCorrupt: "The file could not be read as a PDF or DOCX.",
      documentLimitReached: "This chat reached its document limit. Delete one document and retry.",
      searchHistory: "Search", historySearchAria: "Search history", historySearchPlaceholder: "Search query",
      historySearchResults: "Search results", historySearchEmpty: "No messages matched the query.",
      historySearchEmptyQuery: "Enter a non-empty search query.", historySearchFailed: "History search failed.",
      historySearchTruncated: "Only the newest matches are shown; older matches did not fit the answer.",
      historyExportMD: "Download Markdown", historyExportJSON: "Download JSON",
      historyExportReady: "History export is ready.", historyExportFailed: "History export failed.",
      historyExportEmpty: "There are no messages to export.", historyExportTooLarge: "History export exceeds 5 MiB.",
      feedbackUp: "👍 Helpful", feedbackDown: "👎 Not helpful",
      feedbackGroup: "Rate the answer",
      feedbackSaved: "Feedback saved.", feedbackFailed: "The feedback could not be saved.",
      feedbackForbidden: "Only an assistant answer in this chat can be rated.",
      showFullContent: "Show full text", fullContentFailed: "The full text could not be loaded.",
      activeReminders: "Active reminders", failedReminders: "Failed reminders",
      emptyActiveReminders: "No active reminders.", emptyFailedReminders: "No failed reminders.",
      reminderCadence: "Cadence", reminderOnce: "once", reminderDaily: "daily", reminderNextRun: "Next run",
      retryReminder: "Retry", deleteReminder: "Delete", deleteReminderConfirm: "Confirm",
      deleteReminderWarning: "Delete this failed reminder? This cannot be undone.",
      reminderUpdated: "Reminder updated.", reminderMutationFailed: "The reminder could not be changed.",
      deleteLink: "Delete", deleteLinkConfirm: "Confirm", clearLinks: "Clear links", clearLinksConfirm: "Confirm",
      deleteLinkWarning: "Delete this saved link? This cannot be undone.",
      clearLinksWarning: "Delete every saved link in this chat? This cannot be undone.",
      linkUpdated: "Link deleted.", linkMutationFailed: "The link could not be deleted.",
      deleteTask: "Delete", deleteTaskConfirm: "Confirm",
      deleteTaskWarning: "Delete this task from history? This cannot be undone.",
      clearTasks: "Clear tasks", clearTasksConfirm: "Confirm",
      clearTasksWarning: "Delete every task in this chat, including active and completed tasks? This cannot be undone.",
      taskUpdated: "Task updated.", taskNotActive: "There is no active task to cancel.",
      taskNotDeletable: "Only a completed, cancelled, or failed task can be deleted.",
      taskNotResumable: "Only the latest failed or cancelled task can be resumed.",
      taskMutationFailed: "The task could not be changed.", networkApprovalTelegram: "Approve network actions only in Telegram.",
      usageDate: "Date", modelCalls: "calls", tokensIn: "input tokens", tokensOut: "output tokens",
      toolsTitle: "Tools", dailyLimits: "Daily limits", used: "used", limit: "limit",
      remaining: "remaining", unlimited: "unlimited", modelUnspecified: "model not specified",
      networkTools: "Network tools", noUsage: "No calls today.",
      settingsSaved: "Settings saved for this chat.", settingsSaveFailed: "The setting could not be saved.",
      dataLoadFailed: "The data could not be loaded.", invalidResponse: "The server returned an invalid response.",
      networkError: "The server could not be reached. Check your connection and try again.", requestFailed: "Request failed ({status}).",
      errorModels: "The current provider model list could not be read.",
      errorInvalidModel: "Choose a model from the current provider list.",
      errorInvalidPersona: "Choose an existing preset or enter 1 to 4000 Unicode characters without NUL.",
      errorInvalidStream: "Choose a valid stream setting.", errorInvalidJSON: "Invalid or unknown settings fields.",
      errorContentType: "A strict JSON request was expected.", errorBodyLarge: "Request body is too large.",
      errorMethod: "Method not allowed.", errorPagination: "Invalid pagination parameters.",
      errorInvalidQuery: "Enter one nonempty query of at most 4000 characters.", reminderNotMutable: "The reminder is missing or cannot be changed in its current state.",
      errorRead: "The data could not be read.", errorOverloaded: "The Mini App is busy. Try again shortly.",
      errorRateLimited: "Too many requests. Try again later.",
      errorInvalidTTSSpeed: "Choose a speed of 0.75×, 1×, 1.25×, or 1.5×.",
      errorNotFound: "Page or API endpoint not found.",
      sourceChat: "this chat override", sourceGlobal: "global default", current: "Current",
      globalDefault: "Global default", customText: "custom text", modelTitle: "Model",
    accountTitle: "Language and session", accountHint: "Interface language and closing the current Mini App session.",
      availableModels: "Available provider models", noModels: "No models reported by the provider",
      useModel: "Use model", resetDefault: "Reset to default",
      modelsTruncated: "Only the first 50 provider models are shown; refresh to check the endpoint again.",
      personaTitle: "Persona", personaPresets: "Persona presets", usePreset: "Use preset",
      resetPersona: "Reset persona", customPlaceholder: "Custom persona text (1–4000 Unicode characters)",
      customAria: "Custom persona text", saveCustom: "Save custom persona",
      streamTitle: "Stream responses", streamAria: "Stream responses", on: "On", off: "Off",
      saveStream: "Save stream setting", resetGlobal: "Reset to global",
      ttsTitle: "Speech synthesis (TTS)", ttsSpeedAria: "Speech synthesis speed",
      saveTTSSpeed: "Save speed", resetTTSSpeed: "Reset speed",
      ttsSpeedSaved: "Speech synthesis speed saved.", ttsSpeedReset: "Speech synthesis speed reset.",
      persona_default: "default", persona_custom: "custom", persona_assistant: "assistant",
      persona_coder: "coder", persona_writer: "writer", persona_translator: "translator",
      preset_assistant_desc: "general assistant", preset_coder_desc: "programming, code and architecture",
      preset_writer_desc: "writing, editing and structure", preset_translator_desc: "translation preserving meaning and format",
    },
  };

  const errorKeys = {
    invalid_launch: "launchFailed", session_expired: "sessionExpired", models_unavailable: "errorModels",
    invalid_model: "errorInvalidModel", invalid_persona: "errorInvalidPersona", invalid_stream: "errorInvalidStream",
    invalid_json: "errorInvalidJSON", content_type: "errorContentType", body_too_large: "errorBodyLarge",
    method_not_allowed: "errorMethod", invalid_pagination: "errorPagination", invalid_query: "errorInvalidQuery", read_failed: "errorRead",
    overloaded: "errorOverloaded", rate_limited: "errorRateLimited", invalid_tts_speed: "errorInvalidTTSSpeed",
    export_failed: "historyExportFailed", export_too_large: "historyExportTooLarge", history_empty: "historyExportEmpty",
    not_found: "errorNotFound", invalid_body: "errorInvalidJSON", upload_timeout: "documentUploadTimeout", static_error: "errorRead", reminder_not_mutable: "reminderNotMutable",
    task_not_active: "taskNotActive", task_not_resumable: "taskNotResumable", task_mutation_failed: "taskMutationFailed",
    task_not_deletable: "taskNotDeletable", document_mutation_failed: "documentMutationFailed",
    document_empty: "documentUploadEmpty", document_too_large: "documentUploadLimit",
    unsupported_document: "documentUploadType", document_limit: "documentLimitReached",
    document_no_text: "documentUploadNoText", document_encrypted: "documentUploadEncrypted",
    invalid_document: "documentUploadCorrupt",
    link_mutation_failed: "linkMutationFailed", reminder_mutation_failed: "reminderMutationFailed",
    history_mutation_failed: "historyMutationFailed", invalid_url: "linkInvalidURL", invalid_reminder: "reminderInvalid",
    feedback_forbidden: "feedbackForbidden",
    invalid_goal: "taskGoalInvalid", chat_forbidden: "chatForbidden", invalid_text: "chatInvalidText", text_too_long: "chatTooLongError",
    chat_unavailable: "chatUnavailable", chat_failed: "chatFailed", chat_mutation_failed: "chatMutationFailed",
  };

  function initialLanguage() {
    try { return localStorage.getItem("miniapp-language") === "en" ? "en" : "ru"; } catch (_) { return "ru"; }
  }

  let language = initialLanguage();
  function t(key, values = {}) {
    let text = (translations[language] && translations[language][key]) || translations.ru[key] || key;
    for (const [name, value] of Object.entries(values)) text = text.replaceAll(`{${name}}`, String(value));
    return text;
  }
  function maybe(key, fallback) {
    const translated = (translations[language] && translations[language][key]) || translations.ru[key];
    return translated || fallback;
  }

  const baseMeta = document.querySelector('meta[name="miniapp-base"]');
  const miniAppBase = baseMeta ? baseMeta.getAttribute("content") || "" : "";
  const launch = new URLSearchParams(window.location.hash.slice(1));
  const ticket = launch.get("ticket") || "";
  const initData = (window.Telegram && window.Telegram.WebApp && window.Telegram.WebApp.initData) || launch.get("tgWebAppData") || "";
  if (window.Telegram && window.Telegram.WebApp) {
    window.Telegram.WebApp.ready();
    window.Telegram.WebApp.expand();
  }
  window.history.replaceState(null, "", window.location.pathname + window.location.search);

  const ui = {
    app: document.getElementById("app"), launch: document.getElementById("launch"),
    error: document.getElementById("error"), notice: document.getElementById("notice"), loading: document.getElementById("loading"),
    content: document.getElementById("content"), title: document.getElementById("section-title"), more: document.getElementById("more"),
    refresh: document.getElementById("refresh"), logout: document.getElementById("logout"),
    language: document.getElementById("language"),
    // The language and the session control are moved into Settings on every render; the nodes stay alive in
    // this persistent container so their listeners and the current value survive tab switches.
    accountControls: document.getElementById("account-controls"),
  };
  let session = "";
  // Chat is the first screen: the Mini App opens where a conversation happens, not on a status table.
  let activeTab = "chat";
  let noticeTimer = 0;
  let nextBefore = null;
  let loading = false;
  let clearHistoryArmed = false;
  let clearDocumentsArmed = false;
  let clearLinksArmed = false;
  let clearTasksArmed = false;
  let deleteHistoryArmed = null;
  let deleteDocumentArmed = null;
  let deleteLinkArmed = null;
  let deleteTaskArmed = null;
  let deleteReminderArmed = null;
  let cancelTaskArmed = false;
  let cancelReminderArmed = null;
  let documentSearch = { query: "", active: false, items: [], truncated: false };
  // The selected File survives a re-render of the Documents tab (a FileList cannot be restored, the object can),
  // so arming a delete does not silently throw away the file the user just picked.
  let documentUploadFile = null;
  let historySearch = { query: "", active: false, items: [], truncated: false };
  let linkSearch = { query: "", active: false, items: [] };
  let view = { tab: "status", data: null, items: [] };
  let chat = { messages: [], pending: false, streaming: false, suppressed: false, stage: "", errorText: "", resetArmed: false, identity: null };
  let chatNodes = { log: null, answer: null, status: null };
  let chatLiveTimer = null;
  let chatLiveNode = null;
  let chatLiveText = "";

  function resetArmedStates() {
    chat.resetArmed = false;
    clearHistoryArmed = false;
    clearDocumentsArmed = false;
    clearLinksArmed = false;
    clearTasksArmed = false;
    deleteHistoryArmed = null;
    deleteDocumentArmed = null;
    deleteLinkArmed = null;
    deleteTaskArmed = null;
    deleteReminderArmed = null;
    cancelTaskArmed = false;
    cancelReminderArmed = null;
  }

  function resetSearchState() {
    documentSearch = { query: "", active: false, items: [], truncated: false };
    historySearch = { query: "", active: false, items: [], truncated: false };
    linkSearch = { query: "", active: false, items: [] };
  }

  function syncBusyState() {
    ui.loading.hidden = !loading;
    ui.content.setAttribute("aria-busy", String(loading));
    for (const control of document.querySelectorAll("button, input, select, textarea")) {
      if (loading) {
        if (control.dataset.busyDisabled !== "true") {
          control.dataset.busyDisabled = "true";
          control.dataset.wasDisabled = String(control.disabled);
        }
        control.disabled = true;
      } else if (control.dataset.busyDisabled === "true") {
        control.disabled = control.dataset.wasDisabled === "true";
        delete control.dataset.busyDisabled;
        delete control.dataset.wasDisabled;
      }
    }
  }

  function setBusy(busy) {
    loading = busy;
    syncBusyState();
  }

  function showError(message) {
    clearTimeout(noticeTimer);
    ui.notice.textContent = "";
    ui.notice.hidden = true;
    ui.error.textContent = message;
    ui.error.hidden = !message;
    if (message) noticeTimer = setTimeout(() => { ui.error.hidden = true; }, 10000);
  }

  function showNotice(message) {
    clearTimeout(noticeTimer);
    ui.error.textContent = "";
    ui.error.hidden = true;
    ui.notice.textContent = message;
    ui.notice.hidden = !message;
    // A confirmation is transient: ten seconds is enough to read it, and the toast never blocks the tab bar
    // because it sits at the very top with pointer-events:none (the taps go through to the menu below it).
    if (message) noticeTimer = setTimeout(() => { ui.notice.hidden = true; }, 10000);
  }

  function expire(message) {
    session = "";
    ui.app.hidden = true;
    ui.logout.hidden = true;
    ui.launch.hidden = false;
    showError(message || t("sessionExpired"));
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  // Markdown is rendered into DOM nodes, never into an HTML string. Every character the parser
  // does not recognise becomes a text node, which is equivalent to escaping & < > " ' first and is
  // safe by construction: model output can never create an element or an attribute.
  function textNode(value) {
    const text = String(value);
    return typeof document.createTextNode === "function" ? document.createTextNode(text) : element("span", "", text);
  }

  function safeLinkURL(raw) {
    const value = String(raw || "").trim();
    if (!/^https?:\/\/[^\s]+$/i.test(value)) return "";
    try {
      const url = new URL(value);
      if (url.protocol !== "http:" && url.protocol !== "https:") return "";
      return url.href;
    } catch (_) {
      return "";
    }
  }

  function trimBareURL(raw) {
    let value = raw.replace(/[.,;:!?*_~]+$/, "");
    while (value.endsWith(")") && (value.match(/\(/g) || []).length < (value.match(/\)/g) || []).length) value = value.slice(0, -1);
    while (value.endsWith("]") && (value.match(/\[/g) || []).length < (value.match(/\]/g) || []).length) value = value.slice(0, -1);
    while (value.endsWith("}") && (value.match(/\{/g) || []).length < (value.match(/\}/g) || []).length) value = value.slice(0, -1);
    return value;
  }

  function appendMarkdownLink(target, href, label) {
    const anchor = element("a");
    anchor.href = href;
    anchor.target = "_blank";
    anchor.rel = "noopener noreferrer";
    appendInlineMarkdown(anchor, label, false);
    target.appendChild(anchor);
  }

  function appendInlineMarkdown(target, source, allowAutolink = true) {
    const text = String(source || "");
    let plain = "";
    let index = 0;
    const flush = () => {
      if (plain) {
        target.appendChild(textNode(plain));
        plain = "";
      }
    };
    while (index < text.length) {
      const rest = text.slice(index);
      if (rest.startsWith("`")) {
        const close = text.indexOf("`", index + 1);
        if (close > index + 1) {
          flush();
          target.appendChild(element("code", "", text.slice(index + 1, close)));
          index = close + 1;
          continue;
        }
      }
      if (rest.startsWith("**")) {
        const close = text.indexOf("**", index + 2);
        if (close > index + 2) {
          flush();
          const strong = element("strong");
          appendInlineMarkdown(strong, text.slice(index + 2, close), allowAutolink);
          target.appendChild(strong);
          index = close + 2;
          continue;
        }
      }
      if (rest.startsWith("~~")) {
        const close = text.indexOf("~~", index + 2);
        if (close > index + 2) {
          flush();
          const deleted = element("del");
          appendInlineMarkdown(deleted, text.slice(index + 2, close), allowAutolink);
          target.appendChild(deleted);
          index = close + 2;
          continue;
        }
      }
      if (rest.startsWith("*") && !rest.startsWith("**")) {
        const close = text.indexOf("*", index + 1);
        if (close > index + 1 && text[close + 1] !== "*") {
          flush();
          const em = element("em");
          appendInlineMarkdown(em, text.slice(index + 1, close), allowAutolink);
          target.appendChild(em);
          index = close + 1;
          continue;
        }
      }
      if (allowAutolink && rest.startsWith("[")) {
        const closeLabel = text.indexOf("]", index + 1);
        if (closeLabel > index + 1 && text[closeLabel + 1] === "(") {
          const closeHref = text.indexOf(")", closeLabel + 2);
          if (closeHref > closeLabel + 2) {
            const href = safeLinkURL(text.slice(closeLabel + 2, closeHref));
            if (href) {
              flush();
              appendMarkdownLink(target, href, text.slice(index + 1, closeLabel));
              index = closeHref + 1;
              continue;
            }
          }
        }
      }
      if (allowAutolink && (rest.startsWith("http://") || rest.startsWith("https://"))) {
        const before = index === 0 ? "" : text[index - 1];
        if (!before || /[\s([{>]/.test(before)) {
          let end = index;
          while (end < text.length && !/[\s<>"`]/.test(text[end])) end += 1;
          const candidate = trimBareURL(text.slice(index, end));
          const href = safeLinkURL(candidate);
          if (href) {
            flush();
            appendMarkdownLink(target, href, candidate);
            index += candidate.length;
            continue;
          }
        }
      }
      plain += text[index];
      index += 1;
    }
    flush();
  }

  function markdownBlockStart(line) {
    const trimmed = line.trimStart();
    return trimmed.startsWith("```")
      || /^#{1,3}\s+/.test(trimmed)
      || /^>\s?/.test(trimmed)
      || /^[-*+]\s+/.test(trimmed)
      || /^\d+[.)]\s+/.test(trimmed)
      || /^([-*_])\1{2,}\s*$/.test(trimmed);
  }

  function appendMarkdown(target, source) {
    const lines = String(source || "").replace(/\r\n?/g, "\n").split("\n");
    let index = 0;
    while (index < lines.length) {
      const line = lines[index];
      const trimmed = line.trimStart();
      if (!trimmed) {
        index += 1;
        continue;
      }
      if (trimmed.startsWith("```")) {
        const info = trimmed.slice(3).trim().split(/\s+/)[0] || "";
        const codeLines = [];
        index += 1;
        while (index < lines.length && !lines[index].trimStart().startsWith("```")) {
          codeLines.push(lines[index]);
          index += 1;
        }
        if (index < lines.length) index += 1;
        const pre = element("pre");
        if (info) pre.appendChild(element("span", "md-code-lang", info));
        pre.appendChild(element("code", "", codeLines.join("\n")));
        target.appendChild(pre);
        continue;
      }
      const heading = /^(#{1,3})\s+(.*)$/.exec(trimmed);
      if (heading) {
        const node = element(`h${heading[1].length}`, "md-heading");
        appendInlineMarkdown(node, heading[2]);
        target.appendChild(node);
        index += 1;
        continue;
      }
      if (/^>\s?/.test(trimmed)) {
        const quote = element("blockquote");
        let first = true;
        while (index < lines.length && /^\s*>\s?/.test(lines[index])) {
          if (!first) quote.appendChild(element("br"));
          appendInlineMarkdown(quote, lines[index].replace(/^\s*>\s?/, ""));
          first = false;
          index += 1;
        }
        target.appendChild(quote);
        continue;
      }
      if (/^\s*[-*+]\s+/.test(trimmed)) {
        const list = element("ul");
        while (index < lines.length) {
          const item = /^\s*[-*+]\s+(.*)$/.exec(lines[index]);
          if (!item) break;
          const entry = element("li");
          appendInlineMarkdown(entry, item[1]);
          list.appendChild(entry);
          index += 1;
        }
        target.appendChild(list);
        continue;
      }
      if (/^\s*\d+[.)]\s+/.test(trimmed)) {
        const list = element("ol");
        while (index < lines.length) {
          const item = /^\s*\d+[.)]\s+(.*)$/.exec(lines[index]);
          if (!item) break;
          const entry = element("li");
          appendInlineMarkdown(entry, item[1]);
          list.appendChild(entry);
          index += 1;
        }
        target.appendChild(list);
        continue;
      }
      if (/^([-*_])\1{2,}\s*$/.test(trimmed)) {
        target.appendChild(element("hr"));
        index += 1;
        continue;
      }
      const paragraph = element("p");
      let first = true;
      while (index < lines.length && lines[index].trim() && !markdownBlockStart(lines[index])) {
        if (!first) paragraph.appendChild(element("br"));
        appendInlineMarkdown(paragraph, lines[index]);
        first = false;
        index += 1;
      }
      if (paragraph.childNodes.length) target.appendChild(paragraph);
      else index += 1;
    }
  }

  function renderMarkdownInto(target, source) {
    target.replaceChildren();
    appendMarkdown(target, source);
  }

  function responseError(code, status) {
    const key = status === 429 ? "errorRateLimited" : errorKeys[code] || "requestFailed";
    return t(key, { status });
  }

  async function api(path, options = {}) {
    const headers = new Headers(options.headers || {});
    if (session) headers.set("Authorization", `Bearer ${session}`);
    let response;
    try {
      response = await fetch(miniAppBase + path, { ...options, headers, cache: "no-store" });
    } catch (_) {
      throw new Error(t("networkError"));
    }
    let body = null;
    try { body = await response.json(); } catch (_) { body = null; }
    if (!response.ok) {
      if (response.status === 401 && path !== "/api/session") expire(t("sessionExpired"));
      const code = body && body.error && body.error.code;
      throw new Error(responseError(code, response.status));
    }
    if (!body) throw new Error(t("invalidResponse"));
    return body;
  }

  function itemShell(id, timestamp) {
    const wrapper = element("article", "item");
    wrapper.appendChild(element("div", "meta", `#${id}${timestamp ? ` · ${timestamp}` : ""}`));
    return wrapper;
  }

  function renderStatus(data) {
    ui.content.replaceChildren();
    if (data.last_activity) ui.content.appendChild(element("p", "meta", `${t("lastActivity")}: ${data.last_activity}`));
    const grid = element("div", "stats");
    const values = [
      [t("messages"), data.messages], [t("documents"), data.documents], [t("savedLinks"), data.links],
      [t("tasks"), data.tasks], [t("activeTasks"), data.active_tasks], [t("awaitingAnswer"), data.pending ? t("yes") : t("no")],
      [t("recoverySuppressed"), data.recovery_suppressed ? t("yes") : t("no")],
    ];
    for (const [label, value] of values) {
      const card = element("div", "stat");
      card.appendChild(element("strong", "", String(value)));
      card.appendChild(element("span", "", label));
      grid.appendChild(card);
    }
    ui.content.appendChild(grid);
  }

  function setHistoryClearArmed(armed, items) {
    if (loading) return;
    resetArmedStates();
    clearHistoryArmed = armed;
    renderItems("history", items);
  }

  function setHistoryDeleteArmed(id, items) {
    if (loading) return;
    resetArmedStates();
    deleteHistoryArmed = id;
    renderItems("history", items);
  }

  function setDocumentClearArmed(armed, items) {
    if (loading) return;
    resetArmedStates();
    clearDocumentsArmed = armed;
    renderItems("documents", items);
  }

  function setDocumentDeleteArmed(id, items) {
    if (loading) return;
    resetArmedStates();
    deleteDocumentArmed = id;
    renderItems("documents", items);
  }

  function setLinkClearArmed(armed, items) {
    if (loading) return;
    resetArmedStates();
    clearLinksArmed = armed;
    renderItems("links", items);
  }

  function setTaskClearArmed(armed, items) {
    if (loading) return;
    resetArmedStates();
    clearTasksArmed = armed;
    renderItems("tasks", items);
  }

  function setTaskCancelArmed(armed, items) {
    if (loading) return;
    resetArmedStates();
    cancelTaskArmed = armed;
    renderItems("tasks", items);
  }

  function setLinkDeleteArmed(id, items) {
    if (loading) return;
    resetArmedStates();
    deleteLinkArmed = id;
    renderItems("links", items);
  }

  function setTaskDeleteArmed(id, items) {
    if (loading) return;
    resetArmedStates();
    deleteTaskArmed = id;
    renderItems("tasks", items);
  }

  function setReminderDeleteArmed(id, data) {
    if (loading) return;
    resetArmedStates();
    deleteReminderArmed = id;
    renderReminders(data);
  }

  function setReminderCancelArmed(id, data) {
    if (loading) return;
    resetArmedStates();
    cancelReminderArmed = id;
    renderReminders(data);
  }

  function inlineConfirmation(warning, confirmLabel, onConfirm, onCancel) {
    const confirmation = element("div", "inline-confirm");
    confirmation.setAttribute("role", "group");
    confirmation.setAttribute("aria-label", warning);
    confirmation.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onCancel();
      }
    });
    confirmation.appendChild(element("p", "inline-confirm-warning", warning));
    const actions = element("div", "task-actions inline-confirm-actions");
    actions.appendChild(settingsButton(confirmLabel, onConfirm));
    actions.appendChild(settingsButton(t("cancelTask"), onCancel, true));
    confirmation.appendChild(actions);
    return confirmation;
  }

  function creationPanel(title, description) {
    const section = element("section", "creation-panel");
    section.appendChild(element("h3", "", title));
    if (description) section.appendChild(element("p", "meta", description));
    const form = element("form", "creation-form");
    form.noValidate = true;
    section.appendChild(form);
    return { section, form };
  }

  function formField(labelText, control, wide = false) {
    const label = element("label", wide ? "field field-wide" : "field");
    label.appendChild(element("span", "", labelText));
    label.appendChild(control);
    return label;
  }

  function submitButton(label) {
    const button = element("button", "", label);
    button.type = "submit";
    return button;
  }

  function invalidField(control, message) {
    control.setAttribute("aria-invalid", "true");
    showError(message);
    if (typeof control.focus === "function") control.focus();
  }

  function clearFieldValidity(control) {
    control.addEventListener("input", () => control.setAttribute("aria-invalid", "false"));
  }

  function historyFeedbackButton(label, active, handler) {
    const button = settingsButton(label, handler, !active);
    button.setAttribute("aria-pressed", String(active));
    return button;
  }

  function appendHistoryFeedback(wrapper, item) {
    const actions = element("div", "task-actions");
    actions.appendChild(historyFeedbackButton(t("feedbackUp"), item.feedback === 1, () => rateFeedback(item, 1)));
    actions.appendChild(historyFeedbackButton(t("feedbackDown"), item.feedback === -1, () => rateFeedback(item, -1)));
    wrapper.appendChild(actions);
  }

  function appendHistoryContent(wrapper, item) {
    const role = element("div", "role");
    role.appendChild(element("span", item.role === "assistant" ? "badge badge-success" : "badge", item.role === "user" ? t("you") : t("assistant")));
    wrapper.appendChild(role);
    const body = element("div", item.role === "assistant" ? "content markdown" : "content");
    if (item.role === "assistant") renderMarkdownInto(body, item.content);
    else body.textContent = item.content;
    wrapper.appendChild(body);
    if (item.truncated) {
      const actions = element("div", "task-actions");
      actions.appendChild(settingsButton(t("showFullContent"), () => loadFullContent("history", item.id)));
      wrapper.appendChild(actions);
    }
    if (item.role === "assistant") appendHistoryFeedback(wrapper, item);
  }

  function renderHistorySearch() {
    const section = element("section", "history-search");
    const row = element("div", "history-search-row");
    const input = element("input");
    input.type = "search";
    input.maxLength = 4000;
    input.value = historySearch.query;
    input.placeholder = t("historySearchPlaceholder");
    input.setAttribute("aria-label", t("historySearchAria"));
    input.addEventListener("input", () => { historySearch.query = input.value; });
    input.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        searchHistory(input.value);
      }
    });
    row.appendChild(input);
    row.appendChild(settingsButton(t("searchHistory"), () => searchHistory(input.value)));
    section.appendChild(row);
    const exportRow = element("div", "history-export");
    exportRow.appendChild(settingsButton(t("historyExportMD"), () => exportHistory("md"), true));
    exportRow.appendChild(settingsButton(t("historyExportJSON"), () => exportHistory("json"), true));
    section.appendChild(exportRow);
    if (historySearch.active) {
      const results = element("div", "search-results");
      results.appendChild(element("h3", "", t("historySearchResults")));
      if (historySearch.truncated) results.appendChild(element("p", "task-clear-warning", t("historySearchTruncated")));
      if (!historySearch.items.length) {
        results.appendChild(element("p", "empty", t("historySearchEmpty")));
      }
      for (const item of historySearch.items.slice(0, 20)) {
        const wrapper = itemShell(item.id, item.created_at);
        appendHistoryContent(wrapper, item);
        results.appendChild(wrapper);
      }
      section.appendChild(results);
    }
    return section;
  }

  // The server-side document contract of /docs add: UTF-8 text up to 64 KiB and PDF/DOCX up to 20 MiB (the
  // Telegram Bot API getFile ceiling). The extension lists are what the input accepts and what the pre-check
  // uses; the MIME lists cover a picker that does not know the type. The server validates every byte and decides
  // the format extension-first exactly as here, so an obviously wrong file never leaves the phone.
  const documentUploadTextMaxBytes = 64 * 1024;
  const documentUploadBinaryMaxBytes = 20 * 1024 * 1024;
  const documentUploadExtensions = [
    ".txt", ".md", ".markdown", ".log", ".csv", ".tsv", ".json", ".jsonl", ".xml", ".yaml", ".yml",
    ".toml", ".ini", ".cfg", ".html", ".css", ".js", ".ts", ".go", ".py", ".java", ".c", ".h", ".cpp",
    ".cs", ".rs", ".sh", ".ps1", ".sql",
  ];
  const documentUploadBinaryExtensions = [".pdf", ".docx"];
  const documentUploadMIMEs = [
    "application/json", "application/ld+json", "application/xml", "application/xhtml+xml", "application/x-yaml",
    "application/yaml", "application/x-ndjson", "application/sql", "application/javascript", "application/x-sh",
    "application/x-httpd-php",
  ];
  const documentUploadBinaryMIMEs = [
    "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  ];
  const documentUploadAccept = documentUploadExtensions.concat(documentUploadBinaryExtensions).join(",") +
    ",text/*,application/json,application/pdf,application/vnd.openxmlformats-officedocument.wordprocessingml.document";

  function documentUploadIssue(file) {
    if (!file || !file.name) return "documentUploadChoose";
    if (!file.size) return "documentUploadEmpty";
    const mime = String(file.type || "").toLowerCase().split(";")[0].trim();
    // Extension first: .csv often arrives as application/vnd.ms-excel and .md as application/x-markdown, while
    // a picker may report an empty type for a real .pdf. A known text extension therefore stays text even with
    // a vendor MIME, and a known binary extension is binary even when the MIME is generic.
    const dot = file.name.lastIndexOf(".");
    const extension = dot < 0 ? "" : file.name.slice(dot).toLowerCase();
    if (extension === ".doc") return "documentUploadType";
    const textual = documentUploadExtensions.includes(extension) ||
      mime.startsWith("text/") || documentUploadMIMEs.includes(mime);
    const binary = documentUploadBinaryExtensions.includes(extension) ||
      (!textual && documentUploadBinaryMIMEs.includes(mime));
    if (binary) {
      if (file.size > documentUploadBinaryMaxBytes) return "documentUploadBinaryTooLarge";
      return "";
    }
    if (!textual) return "documentUploadType";
    if (file.size > documentUploadTextMaxBytes) return "documentUploadTooLarge";
    return "";
  }

  function renderDocumentUpload() {
    const section = element("section", "document-upload");
    section.appendChild(element("h3", "", t("uploadDocumentTitle")));
    section.appendChild(element("p", "form-hint", t("uploadDocumentHint")));
    const row = element("div", "document-upload-row");
    const input = element("input");
    input.type = "file";
    input.className = "document-file";
    input.accept = documentUploadAccept;
    input.setAttribute("aria-label", t("documentFileAria"));
    const selected = element("p", "document-file-name", documentUploadFile ? documentUploadFile.name : "");
    input.addEventListener("change", () => {
      documentUploadFile = input.files && input.files[0] ? input.files[0] : null;
      selected.textContent = documentUploadFile ? documentUploadFile.name : "";
    });
    row.appendChild(input);
    const button = element("button", "", t("uploadDocument"));
    button.type = "button";
    button.addEventListener("click", () => uploadDocument(input, button));
    row.appendChild(button);
    section.appendChild(row);
    section.appendChild(selected);
    return section;
  }

  async function uploadDocument(fileInput, button) {
    if (loading || !session) return;
    const file = documentUploadFile || (fileInput.files && fileInput.files[0]) || null;
    const issue = documentUploadIssue(file);
    if (issue) {
      showError(t(issue));
      return;
    }
    button.textContent = t("documentUploading");
    setBusy(true);
    showError("");
    let uploaded = false;
    try {
      const form = new FormData();
      form.append("file", file, file.name);
      const data = await api("/api/documents", { method: "POST", body: form });
      if (!data || data.id === undefined || typeof data.name !== "string" || typeof data.chunks !== "number") {
        throw new Error(t("invalidResponse"));
      }
      uploaded = true;
    } catch (error) {
      showError(error.message || t("documentUploadFailed"));
    } finally {
      setBusy(false);
      if (!uploaded) renderItems("documents", view.items);
    }
    if (uploaded) {
      documentUploadFile = null;
      resetArmedStates();
      await load();
      showNotice(t("documentUploaded"));
    }
  }

  function renderDocumentSearch() {
    const section = element("section", "document-search");
    const row = element("div", "document-search-row");
    const input = element("input");
    input.type = "search";
    input.maxLength = 4000;
    input.value = documentSearch.query;
    input.placeholder = t("documentSearchPlaceholder");
    input.setAttribute("aria-label", t("documentSearchAria"));
    input.addEventListener("input", () => { documentSearch.query = input.value; });
    input.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        searchDocuments(input.value);
      }
    });
    row.appendChild(input);
    row.appendChild(settingsButton(t("searchDocuments"), () => searchDocuments(input.value)));
    section.appendChild(row);
    if (documentSearch.active) {
      const results = element("div", "search-results");
      results.appendChild(element("h3", "", t("documentSearchResults")));
      if (documentSearch.truncated) results.appendChild(element("p", "task-clear-warning", t("documentSearchTruncated")));
      if (!documentSearch.items.length) {
        results.appendChild(element("p", "empty", t("documentSearchEmpty")));
      }
      for (const item of documentSearch.items.slice(0, 3)) {
        const wrapper = itemShell(item.document_id);
        wrapper.appendChild(element("div", "content", item.name));
        wrapper.appendChild(element("div", "meta", `${t("documentSearchId")}: ${item.document_id} · ${t("documentSearchScore")}: ${item.score}`));
        wrapper.appendChild(element("div", "content", item.content));
        results.appendChild(wrapper);
      }
      section.appendChild(results);
    }
    return section;
  }

  function renderLinkControls() {
    const container = element("div", "link-controls");
    const creation = creationPanel(t("addLinkTitle"), t("addLinkText"));
    const urlInput = element("input");
    urlInput.type = "url";
    urlInput.maxLength = 2048;
    urlInput.required = true;
    urlInput.placeholder = t("linkURLPlaceholder");
    urlInput.setAttribute("aria-label", t("linkURL"));
    urlInput.setAttribute("autocomplete", "url");
    clearFieldValidity(urlInput);
    creation.form.appendChild(formField(t("linkURL"), urlInput, true));
    const createActions = element("div", "form-actions");
    createActions.appendChild(submitButton(t("addAction")));
    creation.form.appendChild(createActions);
    creation.form.addEventListener("submit", (event) => {
      event.preventDefault();
      createLink(urlInput.value, urlInput);
    });
    container.appendChild(creation.section);

    const search = element("section", "link-search");
    const row = element("div", "link-search-row");
    const input = element("input");
    input.type = "search";
    input.maxLength = 200;
    input.value = linkSearch.query;
    input.placeholder = t("linkSearchPlaceholder");
    input.setAttribute("aria-label", t("linkSearchAria"));
    input.addEventListener("input", () => { linkSearch.query = input.value; });
    input.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        searchLinks(input.value);
      }
    });
    row.appendChild(input);
    row.appendChild(settingsButton(t("searchLinks"), () => searchLinks(input.value)));
    search.appendChild(row);
    if (linkSearch.active) {
      const results = element("div", "search-results");
      results.appendChild(element("h3", "", t("linkSearchResults")));
      if (!linkSearch.items.length) results.appendChild(element("p", "empty", t("linkSearchEmpty")));
      for (const item of linkSearch.items.slice(0, 20)) {
        const wrapper = itemShell(item.id, item.created_at || item.updated_at);
        wrapper.appendChild(element("div", "content", item.title));
        if (/^https:\/\//i.test(item.url)) {
          const anchor = element("a", "", item.url);
          anchor.href = item.url;
          anchor.target = "_blank";
          anchor.rel = "noopener noreferrer";
          wrapper.appendChild(anchor);
        } else {
          wrapper.appendChild(element("div", "content", item.url));
        }
        wrapper.appendChild(element("div", "content", item.content));
        results.appendChild(wrapper);
      }
      search.appendChild(results);
    }
    container.appendChild(search);
    return container;
  }

  function renderTaskCreation() {
    const creation = creationPanel(t("createTaskTitle"), t("createTaskText"));
    const goalInput = element("textarea");
    goalInput.maxLength = 2000;
    goalInput.required = true;
    goalInput.placeholder = t("taskGoalPlaceholder");
    goalInput.setAttribute("aria-label", t("taskGoal"));
    clearFieldValidity(goalInput);
    creation.form.appendChild(formField(t("taskGoal"), goalInput, true));
    const counter = element("div", "counter", "0 / 2000");
    goalInput.addEventListener("input", () => { counter.textContent = `${Array.from(goalInput.value).length} / 2000`; });
    creation.form.appendChild(counter);
    const actions = element("div", "form-actions");
    actions.appendChild(submitButton(t("createAction")));
    creation.form.appendChild(actions);
    creation.form.addEventListener("submit", (event) => {
      event.preventDefault();
      createTask(goalInput.value, goalInput);
    });
    return creation.section;
  }

  function renderReminderCreation() {
    const creation = creationPanel(t("createReminderTitle"), "");
    const textInput = element("textarea");
    textInput.maxLength = 500;
    textInput.required = true;
    textInput.placeholder = t("reminderTextPlaceholder");
    textInput.setAttribute("aria-label", t("reminderText"));
    clearFieldValidity(textInput);

    const cadenceChoice = element("select");
    cadenceChoice.setAttribute("aria-label", t("reminderCadence"));
    for (const [value, label] of [["once", t("reminderOnce")], ["daily", t("reminderDaily")]]) {
      const option = element("option", "", label);
      option.value = value;
      cadenceChoice.appendChild(option);
    }
    cadenceChoice.value = "once";

    const nextRunInput = element("input");
    nextRunInput.type = "datetime-local";
    nextRunInput.required = true;
    nextRunInput.setAttribute("aria-label", t("reminderDateTime"));
    clearFieldValidity(nextRunInput);
    const dailyTimeInput = element("input");
    dailyTimeInput.type = "time";
    dailyTimeInput.disabled = true;
    dailyTimeInput.setAttribute("aria-label", t("reminderDailyTime"));
    clearFieldValidity(dailyTimeInput);

    const grid = element("div", "form-grid");
    grid.appendChild(formField(t("reminderText"), textInput, true));
    grid.appendChild(formField(t("reminderCadence"), cadenceChoice));
    const nextRunField = formField(t("reminderDateTime"), nextRunInput);
    const dailyTimeField = formField(t("reminderDailyTime"), dailyTimeInput);
    dailyTimeField.hidden = true;
    grid.appendChild(nextRunField);
    grid.appendChild(dailyTimeField);
    creation.form.appendChild(grid);
    cadenceChoice.addEventListener("change", () => {
      const daily = cadenceChoice.value === "daily";
      nextRunField.hidden = daily;
      nextRunInput.disabled = daily;
      nextRunInput.required = !daily;
      dailyTimeField.hidden = !daily;
      dailyTimeInput.disabled = !daily;
      dailyTimeInput.required = daily;
    });
    const actions = element("div", "form-actions");
    actions.appendChild(submitButton(t("createAction")));
    creation.form.appendChild(actions);
    creation.form.addEventListener("submit", (event) => {
      event.preventDefault();
      createReminder(textInput, cadenceChoice, nextRunInput, dailyTimeInput);
    });
    return creation.section;
  }

  function renderReminderSection(title, emptyText, items, failed) {
    const section = element("section", "reminder-section");
    section.appendChild(element("h3", "", title));
    if (!items.length) {
      section.appendChild(element("p", "empty", emptyText));
      return section;
    }
    for (const item of items) {
      const wrapper = itemShell(item.id);
      wrapper.appendChild(element("div", "content", item.text));
      const cadence = item.cadence === "daily" ? `${t("reminderDaily")} ${item.daily_time || ""}`.trim() : t("reminderOnce");
      wrapper.appendChild(element("div", "meta", `${t("reminderCadence")}: ${cadence} · ${t("reminderNextRun")}: ${item.next_run} · ${item.attempts} ${t("attempts")}`));
      const actions = element("div", "task-actions");
      if (failed) {
        actions.appendChild(settingsButton(t("retryReminder"), () => changeReminder(`/api/reminders/${item.id}/retry`)));
        if (deleteReminderArmed === item.id) {
          wrapper.appendChild(inlineConfirmation(
            t("deleteReminderWarning"), t("deleteReminderConfirm"),
            () => changeReminder(`/api/reminders/${item.id}/delete`), () => setReminderDeleteArmed(null, view.data),
          ));
        } else {
          actions.appendChild(settingsButton(t("deleteReminder"), () => setReminderDeleteArmed(item.id, view.data), true));
        }
      } else if (cancelReminderArmed === item.id) {
        wrapper.appendChild(inlineConfirmation(
          t("cancelReminderWarning"), t("confirmAction"),
          () => changeReminder(`/api/reminders/${item.id}/cancel`), () => setReminderCancelArmed(null, view.data),
        ));
      } else {
        actions.appendChild(settingsButton(t("cancelTask"), () => setReminderCancelArmed(item.id, view.data), true));
      }
      if (actions.childNodes.length) wrapper.appendChild(actions);
      section.appendChild(wrapper);
    }
    return section;
  }

  function renderReminders(data) {
    ui.content.replaceChildren();
    ui.content.appendChild(renderReminderCreation());
    ui.content.appendChild(renderReminderSection(t("activeReminders"), t("emptyActiveReminders"), data.active || [], false));
    ui.content.appendChild(renderReminderSection(t("failedReminders"), t("emptyFailedReminders"), data.failed || [], true));
    syncBusyState();
  }

  function renderItems(tab, items) {
    ui.content.replaceChildren();
    if (tab === "history") ui.content.appendChild(renderHistorySearch());
    if (tab === "documents") {
      ui.content.appendChild(renderDocumentUpload());
      ui.content.appendChild(renderDocumentSearch());
    }
    if (tab === "links") ui.content.appendChild(renderLinkControls());
    if (tab === "tasks") ui.content.appendChild(renderTaskCreation());
    if (!items.length) {
      if (tab === "history") ui.content.appendChild(element("p", "empty", t("emptyHistory")));
      else ui.content.appendChild(element("p", "empty", t("emptyItems")));
      syncBusyState();
      return;
    }
    if (tab === "history") {
      const clear = element("div", clearHistoryArmed ? "task-clear history-clear task-clear-confirm" : "task-clear history-clear");
      if (clearHistoryArmed) {
        clear.appendChild(element("p", "task-clear-warning", t("clearHistoryWarning")));
        const actions = element("div", "task-actions task-clear-actions");
        actions.appendChild(settingsButton(t("confirmAction"), () => changeHistory("/api/history/clear")));
        actions.appendChild(settingsButton(t("cancelTask"), () => setHistoryClearArmed(false, items), true));
        clear.appendChild(actions);
      } else {
        clear.appendChild(settingsButton(t("clearHistory"), () => setHistoryClearArmed(true, items)));
      }
      ui.content.appendChild(clear);
    }
    if (tab === "documents") {
      const clear = element("div", clearDocumentsArmed ? "task-clear document-clear task-clear-confirm" : "task-clear document-clear");
      if (clearDocumentsArmed) {
        clear.appendChild(element("p", "task-clear-warning", t("clearDocumentsWarning")));
        const actions = element("div", "task-actions task-clear-actions");
        actions.appendChild(settingsButton(t("clearDocumentsConfirm"), () => changeDocument("/api/documents/clear")));
        actions.appendChild(settingsButton(t("cancelTask"), () => setDocumentClearArmed(false, items), true));
        clear.appendChild(actions);
      } else {
        clear.appendChild(settingsButton(t("clearDocuments"), () => setDocumentClearArmed(true, items)));
      }
      ui.content.appendChild(clear);
    }
    if (tab === "links") {
      const clear = element("div", clearLinksArmed ? "task-clear link-clear task-clear-confirm" : "task-clear link-clear");
      if (clearLinksArmed) {
        clear.appendChild(element("p", "task-clear-warning", t("clearLinksWarning")));
        const actions = element("div", "task-actions task-clear-actions");
        actions.appendChild(settingsButton(t("clearLinksConfirm"), () => changeLink("/api/links/clear")));
        actions.appendChild(settingsButton(t("cancelTask"), () => setLinkClearArmed(false, items), true));
        clear.appendChild(actions);
      } else {
        clear.appendChild(settingsButton(t("clearLinks"), () => setLinkClearArmed(true, items)));
      }
      ui.content.appendChild(clear);
    }
    if (tab === "tasks") {
      const clear = element("div", clearTasksArmed ? "task-clear task-clear-confirm" : "task-clear");
      if (clearTasksArmed) {
        clear.appendChild(element("p", "task-clear-warning", t("clearTasksWarning")));
        const actions = element("div", "task-actions task-clear-actions");
        actions.appendChild(settingsButton(t("clearTasksConfirm"), () => changeTask("/api/tasks/clear")));
        actions.appendChild(settingsButton(t("cancelTask"), () => setTaskClearArmed(false, items), true));
        clear.appendChild(actions);
      } else {
        clear.appendChild(settingsButton(t("clearTasks"), () => setTaskClearArmed(true, items)));
      }
      ui.content.appendChild(clear);
    }
    for (const [index, item] of items.entries()) {
      const wrapper = itemShell(item.id, item.created_at || item.updated_at);
      if (tab === "history") {
        appendHistoryContent(wrapper, item);
        if (["user", "assistant"].includes(item.role)) {
          if (deleteHistoryArmed === item.id) {
            wrapper.appendChild(inlineConfirmation(
              t("deleteHistoryWarning"), t("confirmAction"),
              () => changeHistory(`/api/history/${item.id}/delete`), () => setHistoryDeleteArmed(null, items),
            ));
          } else {
            const actions = element("div", "task-actions");
            actions.appendChild(settingsButton(t("deleteHistory"), () => setHistoryDeleteArmed(item.id, items), true));
            wrapper.appendChild(actions);
          }
        }
      } else if (tab === "documents") {
        wrapper.appendChild(element("div", "content", item.name));
        wrapper.appendChild(element("div", "meta", `${item.size_bytes} ${t("bytes")} · ${item.chunks} ${t("chunks")}`));
        if (deleteDocumentArmed === item.id) {
          wrapper.appendChild(inlineConfirmation(
            t("deleteDocumentWarning"), t("deleteDocumentConfirm"),
            () => changeDocument(`/api/documents/${item.id}/delete`), () => setDocumentDeleteArmed(null, items),
          ));
        } else {
          const actions = element("div", "task-actions");
          actions.appendChild(settingsButton(t("deleteDocument"), () => setDocumentDeleteArmed(item.id, items), true));
          wrapper.appendChild(actions);
        }
      } else if (tab === "links") {
        wrapper.appendChild(element("div", "content", item.title));
        if (/^https:\/\//i.test(item.url)) {
          const anchor = element("a", "", item.url);
          anchor.href = item.url;
          anchor.target = "_blank";
          anchor.rel = "noopener noreferrer";
          wrapper.appendChild(anchor);
        } else {
          wrapper.appendChild(element("div", "content", item.url));
        }
        wrapper.appendChild(element("div", "content", item.content));
        if (deleteLinkArmed === item.id) {
          wrapper.appendChild(inlineConfirmation(
            t("deleteLinkWarning"), t("deleteLinkConfirm"),
            () => changeLink(`/api/links/${item.id}/delete`), () => setLinkDeleteArmed(null, items),
          ));
        } else {
          const actions = element("div", "task-actions");
          if (item.truncated) actions.appendChild(settingsButton(t("showFullContent"), () => loadFullContent("links", item.id)));
          actions.appendChild(settingsButton(t("deleteLink"), () => setLinkDeleteArmed(item.id, items), true));
          wrapper.appendChild(actions);
        }
      } else if (tab === "tasks") {
        const status = maybe(`task_${item.status}`, item.status);
        const statusClass = item.status === "completed" ? "badge badge-success" : ["failed", "cancelled"].includes(item.status) ? "badge badge-danger" : "badge";
        wrapper.appendChild(element("span", statusClass, status));
        wrapper.appendChild(element("div", "content", item.goal));
        wrapper.appendChild(element("div", "meta", `${status} · ${t("step")} ${item.next_step} · ${item.attempts} ${t("attempts")} · ${t("due")} ${item.due_at}`));
        wrapper.appendChild(element("div", "meta", `${t("expires")}: ${item.expires_at}`));
        if (item.error) wrapper.appendChild(element("div", "task-error", `${t("taskError")}: ${item.error}`));
        if (item.status === "awaiting_approval") wrapper.appendChild(element("p", "meta", t("networkApprovalTelegram")));
        const actions = element("div", "task-actions");
        if (["ready", "running", "awaiting_approval"].includes(item.status)) {
          if (cancelTaskArmed) {
            wrapper.appendChild(inlineConfirmation(
              t("cancelTaskWarning"), t("confirmAction"),
              () => changeTask("/api/tasks/cancel"), () => setTaskCancelArmed(false, items),
            ));
          } else {
            actions.appendChild(settingsButton(t("cancelTask"), () => setTaskCancelArmed(true, items), true));
          }
        } else if (["completed", "failed", "cancelled"].includes(item.status)) {
          if (deleteTaskArmed === item.id) {
            wrapper.appendChild(inlineConfirmation(
              t("deleteTaskWarning"), t("deleteTaskConfirm"),
              () => changeTask(`/api/tasks/${item.id}/delete`), () => setTaskDeleteArmed(null, items),
            ));
          } else {
            if (index === 0 && ["failed", "cancelled"].includes(item.status)) {
              actions.appendChild(settingsButton(t("resumeTask"), () => changeTask(`/api/tasks/${item.id}/resume`)));
            }
            actions.appendChild(settingsButton(t("deleteTask"), () => setTaskDeleteArmed(item.id, items), true));
          }
        }
        if (actions.childNodes.length) wrapper.appendChild(actions);
      }
      ui.content.appendChild(wrapper);
    }
    syncBusyState();
  }

  function settingsButton(label, handler, secondary = false) {
    const button = element("button", secondary ? "secondary" : "", label);
    button.type = "button";
    button.addEventListener("click", handler);
    return button;
  }

  function settingsSection(title, description) {
    const section = element("section", "settings-section");
    section.appendChild(element("h3", "", title));
    if (description) section.appendChild(element("p", "meta", description));
    return section;
  }

  async function searchHistory(query) {
    if (loading || !session) return;
    const normalized = query.trim();
    if (!normalized) {
      showError(t("historySearchEmptyQuery"));
      return;
    }
    if (Array.from(normalized).length > 4000) {
      showError(t("errorInvalidQuery"));
      return;
    }
    setBusy(true);
    resetArmedStates();
    showError("");
    try {
      const data = await api(`/api/history/search?q=${encodeURIComponent(normalized)}`);
      historySearch = {
        query: normalized,
        active: true,
        items: (Array.isArray(data.items) ? data.items : []).filter((item) => item.role === "user" || item.role === "assistant").slice(0, 20),
        truncated: Boolean(data.truncated || (data.corpus && data.corpus.truncated)),
      };
      renderItems("history", view.items);
    } catch (error) {
      showError(error.message || t("historySearchFailed"));
    } finally {
      setBusy(false);
    }
  }

  function updateStoredFeedback(id, value) {
    for (const items of [view.items, historySearch.items]) {
      for (const entry of items) {
        if (entry.id === id) entry.feedback = value;
      }
    }
  }

  async function rateFeedback(item, value) {
    if (loading || !session) return;
    setBusy(true);
    showError("");
    try {
      const data = await api(`/api/history/${item.id}/feedback`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ value }),
      });
      updateStoredFeedback(item.id, Number(data.value) === -1 ? -1 : 1);
      renderItems("history", view.items);
      showNotice(t("feedbackSaved"));
    } catch (error) {
      showError(error.message || t("feedbackFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function searchLinks(query) {
    if (loading || !session) return;
    const normalized = query.trim();
    if (!normalized) {
      showError(t("linkSearchEmptyQuery"));
      return;
    }
    if (Array.from(normalized).length > 200) {
      showError(t("linkSearchInvalid"));
      return;
    }
    setBusy(true);
    resetArmedStates();
    showError("");
    try {
      const data = await api(`/api/links/search?q=${encodeURIComponent(normalized)}`);
      linkSearch = {
        query: normalized,
        active: true,
        items: (Array.isArray(data.items) ? data.items : []).slice(0, 20),
      };
      renderItems("links", view.items);
    } catch (error) {
      showError(error.message || t("linkSearchFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function exportHistory(format) {
    if (loading || !session) return;
    setBusy(true);
    showError("");
    try {
      const headers = new Headers({ "Authorization": `Bearer ${session}` });
      let response;
      try {
        response = await fetch(miniAppBase + `/api/history/export?format=${format}`, { headers, cache: "no-store" });
      } catch (_) {
        throw new Error(t("networkError"));
      }
      if (!response.ok) {
        let body = null;
        try { body = await response.json(); } catch (_) { body = null; }
        if (response.status === 401) expire(t("sessionExpired"));
        const code = body && body.error && body.error.code;
        throw new Error(responseError(code, response.status));
      }
      const blob = await response.blob();
      if (blob.size > 5 * 1024 * 1024) throw new Error(t("historyExportTooLarge"));
      let objectURL = "";
      let anchor = null;
      try {
        objectURL = URL.createObjectURL(blob);
        anchor = element("a");
        anchor.href = objectURL;
        anchor.download = `history.${format}`;
        anchor.hidden = true;
        document.body.appendChild(anchor);
        anchor.click();
      } finally {
        if (anchor) anchor.remove();
        if (objectURL) URL.revokeObjectURL(objectURL);
      }
      showNotice(t("historyExportReady"));
    } catch (error) {
      showError(error.message || t("historyExportFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function loadFullContent(tab, id) {
    if (loading || !session || !["history", "links"].includes(tab)) return;
    setBusy(true);
    showError("");
    try {
      const data = await api(`/api/${tab}/item?id=${encodeURIComponent(id)}`);
      const full = data.item || data;
      if (!full || typeof full.content !== "string") throw new Error(t("invalidResponse"));
      if (tab === "history" && !["user", "assistant"].includes(full.role)) throw new Error(t("invalidResponse"));
      const update = (items) => {
        for (const item of items) {
          if (item.id === id) {
            item.content = full.content;
            item.truncated = false;
          }
        }
      };
      update(view.items);
      if (tab === "history") update(historySearch.items);
      renderItems(tab, view.items);
    } catch (error) {
      showError(error.message || t("fullContentFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function searchDocuments(query) {
    if (loading || !session) return;
    const normalized = query.trim();
    if (!normalized) {
      showError(t("documentSearchEmptyQuery"));
      return;
    }
    setBusy(true);
    resetArmedStates();
    renderItems("documents", view.items);
    showError("");
    try {
      const data = await api(`/api/documents/search?q=${encodeURIComponent(normalized)}`);
      documentSearch = {
        query: normalized,
        active: true,
        items: (Array.isArray(data.items) ? data.items : []).slice(0, 3),
        truncated: Boolean(data.corpus && data.corpus.truncated),
      };
      renderItems("documents", view.items);
    } catch (error) {
      showError(error.message || t("documentSearchFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function createResource(path, payload, successKey, failureKey) {
    if (loading || !session) return;
    setBusy(true);
    showError("");
    let created = false;
    try {
      await api(path, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      created = true;
    } catch (error) {
      showError(error.message || t(failureKey));
    } finally {
      setBusy(false);
    }
    if (created) {
      resetArmedStates();
      resetSearchState();
      await load();
      showNotice(t(successKey));
    }
  }

  function createLink(value, control) {
    if (loading || !session) return;
    const normalized = value.trim();
    let parsed;
    try { parsed = new URL(normalized); } catch (_) { parsed = null; }
    if (!parsed || parsed.protocol !== "https:" || new TextEncoder().encode(normalized).length > 2048 || normalized.includes("\0")) {
      invalidField(control, t("linkInvalidURL"));
      return;
    }
    control.setAttribute("aria-invalid", "false");
    createResource("/api/links", { url: normalized }, "linkCreated", "linkMutationFailed");
  }

  function createTask(value, control) {
    if (loading || !session) return;
    const normalized = value.trim();
    const length = Array.from(normalized).length;
    if (!length || length > 2000 || normalized.includes("\0")) {
      invalidField(control, t("taskGoalInvalid"));
      return;
    }
    control.setAttribute("aria-invalid", "false");
    createResource("/api/tasks", { goal: normalized }, "taskCreated", "taskMutationFailed");
  }

  function createReminder(textControl, cadenceControl, nextRunControl, dailyTimeControl) {
    if (loading || !session) return;
    const text = textControl.value.trim();
    const textLength = Array.from(text).length;
    if (!textLength || textLength > 500 || text.includes("\0")) {
      invalidField(textControl, t("reminderTextInvalid"));
      return;
    }
    textControl.setAttribute("aria-invalid", "false");
    const cadence = cadenceControl.value === "daily" ? "daily" : "once";
    const payload = { text, cadence };
    if (cadence === "daily") {
      const dailyTime = dailyTimeControl.value;
      if (!/^(?:[01]\d|2[0-3]):[0-5]\d$/.test(dailyTime)) {
        invalidField(dailyTimeControl, t("reminderTimeInvalid"));
        return;
      }
      dailyTimeControl.setAttribute("aria-invalid", "false");
      payload.daily_time = dailyTime;
    } else {
      const nextRun = new Date(nextRunControl.value);
      const delay = nextRun.getTime() - Date.now();
      if (!nextRunControl.value || Number.isNaN(nextRun.getTime()) || delay <= 0 || delay > 3650 * 24 * 60 * 60 * 1000) {
        invalidField(nextRunControl, t("reminderDateInvalid"));
        return;
      }
      nextRunControl.setAttribute("aria-invalid", "false");
      payload.next_run = nextRun.toISOString();
    }
    createResource("/api/reminders", payload, "reminderCreated", "reminderMutationFailed");
  }

  async function changeSettings(path, method, payload, successKey = "") {
    if (loading || !session) return;
    setBusy(true);
    showError("");
    let saved = false;
    try {
      const options = { method };
      if (payload !== undefined) {
        options.headers = { "Content-Type": "application/json" };
        options.body = JSON.stringify(payload);
      }
      await api(path, options);
      saved = true;
    } catch (error) {
      showError(error.message || t("settingsSaveFailed"));
    } finally {
      setBusy(false);
    }
    if (saved) {
      resetArmedStates();
      resetSearchState();
      await load();
      if (successKey) showNotice(t(successKey));
      else showNotice(t("settingsSaved"));
    }
  }

  async function changeHistory(path) {
    if (loading || !session) return;
    setBusy(true);
    showError("");
    let changed = false;
    try {
      await api(path, { method: "POST" });
      changed = true;
    } catch (error) {
      showError(error.message || t("historyMutationFailed"));
    } finally {
      setBusy(false);
    }
    if (changed) {
      resetArmedStates();
      resetSearchState();
      await load();
      showNotice(t("historyUpdated"));
    }
  }

  async function changeDocument(path) {
    if (loading || !session) return;
    setBusy(true);
    showError("");
    let changed = false;
    try {
      await api(path, { method: "POST" });
      changed = true;
    } catch (error) {
      showError(error.message || t("documentMutationFailed"));
    } finally {
      setBusy(false);
    }
    if (changed) {
      resetArmedStates();
      resetSearchState();
      await load();
      showNotice(t("documentUpdated"));
    }
  }

  async function changeLink(path) {
    if (loading || !session) return;
    setBusy(true);
    showError("");
    let changed = false;
    try {
      await api(path, { method: "POST" });
      changed = true;
    } catch (error) {
      showError(error.message || t("linkMutationFailed"));
    } finally {
      setBusy(false);
    }
    if (changed) {
      resetArmedStates();
      resetSearchState();
      await load();
      showNotice(t("linkUpdated"));
    }
  }

  async function changeTask(path) {
    if (loading || !session) return;
    setBusy(true);
    showError("");
    let changed = false;
    try {
      await api(path, { method: "POST" });
      changed = true;
    } catch (error) {
      showError(error.message || t("taskMutationFailed"));
    } finally {
      setBusy(false);
    }
    if (changed) {
      resetArmedStates();
      resetSearchState();
      await load();
      showNotice(t("taskUpdated"));
    }
  }

  async function changeReminder(path) {
    if (loading || !session) return;
    setBusy(true);
    showError("");
    let changed = false;
    try {
      await api(path, { method: "POST" });
      changed = true;
    } catch (error) {
      showError(error.message || t("reminderMutationFailed"));
    } finally {
      setBusy(false);
    }
    if (changed) {
      resetArmedStates();
      resetSearchState();
      await load();
      showNotice(t("reminderUpdated"));
    }
  }

  function renderSettings(data) {
    ui.content.replaceChildren();
    const source = (value) => t(value === "chat" ? "sourceChat" : "sourceGlobal");
    const personaName = (value) => maybe(`persona_${value}`, value);
    const modelsUnavailable = data.warning === "models_unavailable";
    if (modelsUnavailable) showError(responseError(data.warning, 200));

    const model = settingsSection(t("modelTitle"), `${t("current")}: ${data.model.current} · ${source(data.model.source)} · ${t("globalDefault")}: ${data.model.default}`);
    const modelChoice = element("select");
    modelChoice.setAttribute("aria-label", t("availableModels"));
    modelChoice.disabled = modelsUnavailable;
    for (const name of data.model.available) {
      const option = element("option", "", name);
      option.value = name;
      option.selected = name === data.model.current;
      modelChoice.appendChild(option);
    }
    if (!data.model.available.length) {
      const option = element("option", "", t("noModels"));
      option.disabled = true;
      modelChoice.appendChild(option);
    }
    const modelRow = element("div", "settings-row");
    const useModel = settingsButton(t("useModel"), () => changeSettings("/api/settings/model", "PUT", { model: modelChoice.value }));
    useModel.disabled = modelsUnavailable;
    modelRow.appendChild(modelChoice);
    modelRow.appendChild(useModel);
    modelRow.appendChild(settingsButton(t("resetDefault"), () => changeSettings("/api/settings/model", "DELETE"), true));
    model.appendChild(modelRow);
    if (data.model.available_truncated) model.appendChild(element("p", "meta", t("modelsTruncated")));
    ui.content.appendChild(model);

    const customNote = data.persona.custom ? ` · ${t("customText")}` : "";
    const persona = settingsSection(t("personaTitle"), `${t("current")}: ${personaName(data.persona.current)} · ${source(data.persona.source)}${customNote}`);
    const presetChoice = element("select");
    presetChoice.setAttribute("aria-label", t("personaPresets"));
    for (const preset of data.persona.presets) {
      const label = `${personaName(preset.name)} — ${maybe(`preset_${preset.name}_desc`, preset.description)}`;
      const option = element("option", "", label);
      option.value = preset.name;
      option.selected = preset.name === data.persona.current;
      presetChoice.appendChild(option);
    }
    const presetRow = element("div", "settings-row");
    presetRow.appendChild(presetChoice);
    presetRow.appendChild(settingsButton(t("usePreset"), () => changeSettings("/api/settings/persona", "PUT", { preset: presetChoice.value })));
    presetRow.appendChild(settingsButton(t("resetPersona"), () => changeSettings("/api/settings/persona", "DELETE"), true));
    persona.appendChild(presetRow);
    const custom = element("textarea");
    custom.maxLength = 8000;
    custom.placeholder = t("customPlaceholder");
    custom.setAttribute("aria-label", t("customAria"));
    const counter = element("div", "counter", "0 / 4000");
    custom.addEventListener("input", () => { counter.textContent = `${Array.from(custom.value).length} / 4000`; });
    persona.appendChild(custom);
    persona.appendChild(counter);
    const customRow = element("div", "settings-row");
    customRow.appendChild(settingsButton(t("saveCustom"), () => changeSettings("/api/settings/persona", "PUT", { custom: custom.value })));
    persona.appendChild(customRow);
    ui.content.appendChild(persona);

    const stream = settingsSection(t("streamTitle"), `${t("current")}: ${data.stream.enabled ? t("on") : t("off")} · ${source(data.stream.source)}`);
    const streamChoice = element("select");
    streamChoice.setAttribute("aria-label", t("streamAria"));
    for (const [value, label] of [["true", t("on")], ["false", t("off")]]) {
      const option = element("option", "", label);
      option.value = value;
      option.selected = String(data.stream.enabled) === value;
      streamChoice.appendChild(option);
    }
    const streamRow = element("div", "settings-row");
    streamRow.appendChild(streamChoice);
    streamRow.appendChild(settingsButton(t("saveStream"), () => changeSettings("/api/settings/stream", "PUT", { enabled: streamChoice.value === "true" })));
    streamRow.appendChild(settingsButton(t("resetGlobal"), () => changeSettings("/api/settings/stream", "DELETE"), true));
    stream.appendChild(streamRow);
    ui.content.appendChild(stream);

    if (data.tts && data.tts.enabled) {
      const tts = settingsSection(t("ttsTitle"), `${t("current")}: ${data.tts.current}×`);
      const speedChoice = element("select");
      speedChoice.setAttribute("aria-label", t("ttsSpeedAria"));
      for (const value of ["0.75", "1", "1.25", "1.5"]) {
        const option = element("option", "", `${value}×`);
        option.value = value;
        option.selected = String(data.tts.current) === value;
        speedChoice.appendChild(option);
      }
      const speedRow = element("div", "settings-row");
      speedRow.appendChild(speedChoice);
      speedRow.appendChild(settingsButton(t("saveTTSSpeed"), () => changeSettings("/api/settings/tts-speed", "PUT", { speed: speedChoice.value }, "ttsSpeedSaved")));
      speedRow.appendChild(settingsButton(t("resetTTSSpeed"), () => changeSettings("/api/settings/tts-speed", "DELETE", undefined, "ttsSpeedReset"), true));
      tts.appendChild(speedRow);
      ui.content.appendChild(tts);
    }

    // Language and the session control live here, not in the header: the header keeps only the brand so the tab
    // bar stays high on a phone screen.
    const account = settingsSection(t("accountTitle"), t("accountHint"));
    if (ui.accountControls) {
      ui.accountControls.hidden = false;
      ui.accountControls.classList.add("account-controls");
      account.appendChild(ui.accountControls);
    }
    ui.content.appendChild(account);
    syncBusyState();
  }

  function renderUsage(data) {
    ui.content.replaceChildren();
    ui.content.appendChild(element("p", "meta", `${t("usageDate")}: ${data.date} (UTC)`));
    const models = settingsSection(t("modelTitle"), "");
    if (!data.models.length) models.appendChild(element("p", "empty", t("noUsage")));
    for (const model of data.models) {
      const row = element("div", "usage-row");
      row.appendChild(element("div", "content", model.model || t("modelUnspecified")));
      row.appendChild(element("div", "meta", `${model.calls} ${t("modelCalls")} · ${model.tokens_in} ${t("tokensIn")} · ${model.tokens_out} ${t("tokensOut")}`));
      models.appendChild(row);
    }
    ui.content.appendChild(models);

    const tools = settingsSection(t("toolsTitle"), "");
    for (const name of ["read_url", "web_search", "calculator", "rag"]) {
      tools.appendChild(element("div", "meta", `${name}: ${data.tools[name]}`));
    }
    ui.content.appendChild(tools);

    const limits = settingsSection(t("dailyLimits"), "");
    const grid = element("div", "stats");
    for (const [label, value] of [[t("modelTitle"), data.limits.model], [t("networkTools"), data.limits.network_tools]]) {
      const card = element("div", "stat");
      const remaining = value.remaining === null ? t("unlimited") : String(value.remaining);
      card.appendChild(element("strong", "", remaining));
      card.appendChild(element("span", "", `${label} · ${t("remaining")}`));
      card.appendChild(element("div", "meta", `${t("used")}: ${value.used} · ${t("limit")}: ${value.limit || t("unlimited")}`));
      grid.appendChild(card);
    }
    limits.appendChild(grid);
    ui.content.appendChild(limits);
  }

  function chatStageText(stage) {
    if (stage === "read_url") return t("chatReadingURL");
    if (stage === "web_search") return t("chatSearching");
    if (stage === "thinking") return t("chatThinking");
    return t("chatPending");
  }

  function chatMessageNode(message) {
    const assistant = message.role === "assistant";
    const wrapper = element("article", assistant ? "chat-message chat-message-assistant" : "chat-message chat-message-user");
    const role = element("div", "role");
    role.appendChild(element("span", assistant ? "badge badge-success" : "badge", assistant ? t("assistant") : t("you")));
    wrapper.appendChild(role);
    const body = element("div", assistant ? "chat-text content markdown" : "chat-text content");
    if (assistant) renderMarkdownInto(body, message.text || "");
    else body.textContent = message.text || "";
    wrapper.appendChild(body);
    if (assistant && message.id) wrapper.appendChild(chatFeedbackControls(message));
    return { wrapper, body };
  }

  function applyChatFeedbackState(controls, value) {
    for (const button of controls.querySelectorAll("button")) {
      const active = Number(button.dataset.value) === value;
      button.className = active ? "" : "secondary";
      button.setAttribute("aria-pressed", String(active));
    }
  }

  function chatFeedbackControls(message) {
    const controls = element("div", "chat-feedback");
    controls.setAttribute("role", "group");
    controls.setAttribute("aria-label", t("feedbackGroup"));
    for (const [value, label, glyph] of [[1, "feedbackUp", "👍"], [-1, "feedbackDown", "👎"]]) {
      const button = element("button", "", glyph);
      button.type = "button";
      button.dataset.value = String(value);
      button.setAttribute("aria-label", t(label));
      button.title = t(label);
      button.addEventListener("click", () => rateChatFeedback(message, value, controls));
      controls.appendChild(button);
    }
    applyChatFeedbackState(controls, Number(message.feedback) || 0);
    return controls;
  }

  async function rateChatFeedback(message, value, controls) {
    if (!session || !message.id || message.feedbackBusy) return;
    const previous = Number(message.feedback) || 0;
    const next = value === -1 ? -1 : 1;
    message.feedback = next;
    message.feedbackBusy = true;
    applyChatFeedbackState(controls, next);
    try {
      const data = await api(`/api/history/${message.id}/feedback`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ value: next }),
      });
      const stored = Number(data.value) === -1 ? -1 : 1;
      message.feedback = stored;
      updateStoredFeedback(message.id, stored);
      applyChatFeedbackState(controls, stored);
      showNotice(t("feedbackSaved"));
    } catch (error) {
      message.feedback = previous;
      applyChatFeedbackState(controls, previous);
      showError(error.message || t("feedbackFailed"));
    } finally {
      message.feedbackBusy = false;
    }
  }

  function chatNearBottom() {
    if (!chatNodes.log) return true;
    const log = chatNodes.log;
    const distance = log.scrollHeight - log.scrollTop - log.clientHeight;
    return !Number.isFinite(distance) || distance <= 96;
  }

  function scrollChat(force = false) {
    if (!chatNodes.log) return;
    if (!force && !chatNearBottom()) return;
    chatNodes.log.scrollTop = chatNodes.log.scrollHeight;
  }

  function cancelChatLiveRender() {
    if (chatLiveTimer !== null) {
      clearTimeout(chatLiveTimer);
      chatLiveTimer = null;
    }
    chatLiveNode = null;
    chatLiveText = "";
  }

  function scheduleChatLiveRender(node, text) {
    chatLiveNode = node;
    chatLiveText = text;
    if (chatLiveTimer !== null) return;
    // Streaming deltas arrive faster than the screen needs them: re-render markdown at most every
    // 80ms instead of on every fragment, so partial ** or ``` never blocks the stream.
    chatLiveTimer = setTimeout(() => {
      chatLiveTimer = null;
      if (chatLiveNode === chatNodes.answer && chatNodes.answer) renderMarkdownInto(chatNodes.answer, chatLiveText);
      scrollChat();
    }, 80);
  }

  function updateChatLive(answer) {
    if (chatNodes.answer) {
      if (answer.text) scheduleChatLiveRender(chatNodes.answer, answer.text);
      else chatNodes.answer.textContent = "…";
    }
    if (chatNodes.status && chat.streaming) {
      chatNodes.status.replaceChildren(element("span", "chat-pending", chatStageText(chat.stage)));
    }
    scrollChat();
  }

  function autoGrowChatInput(input) {
    if (!input.style) return;
    input.style.height = "auto";
    const max = 140;
    const height = input.scrollHeight || 44;
    input.style.height = `${Math.min(height, max)}px`;
    input.style.overflowY = height > max ? "auto" : "hidden";
  }

  function renderChat() {
    ui.content.replaceChildren();
    cancelChatLiveRender();
    const section = element("section", "chat-panel");
    section.appendChild(element("h3", "chat-title", t("chatTitle")));
    section.appendChild(element("p", "meta chat-intro", t("chatIntro")));
    if (chat.identity) section.appendChild(element("p", "meta chat-identity", t("chatIdentity", { user: chat.identity.user_id, chat: chat.identity.chat_id })));

    const log = element("div", "chat-log");
    log.setAttribute("role", "log");
    log.setAttribute("aria-live", "polite");
    log.setAttribute("aria-label", t("chatTitle"));
    chatNodes = { log, answer: null, status: null };
    if (!chat.messages.length) log.appendChild(element("p", "empty", t("emptyHistory")));
    for (const message of chat.messages) {
      const node = chatMessageNode(message);
      if (chat.streaming && message === chat.messages[chat.messages.length - 1] && message.role === "assistant") {
        node.wrapper.className = `${node.wrapper.className} chat-message-live`;
        chatNodes.answer = node.body;
        if (!message.text) node.body.textContent = "…";
      }
      log.appendChild(node.wrapper);
    }

    const status = element("div", "chat-status");
    status.setAttribute("role", "status");
    if (chat.streaming) status.appendChild(element("span", "chat-pending", chatStageText(chat.stage)));
    else if (chat.pending) status.appendChild(element("span", "badge", t("awaitingAnswer")));
    if (chat.errorText) status.appendChild(element("p", "chat-error", chat.errorText));
    chatNodes.status = status;
    log.appendChild(status);
    section.appendChild(log);

    if (chat.resetArmed) {
      section.appendChild(inlineConfirmation(t("chatResetWarning"), t("chatResetConfirm"), performChatReset, () => {
        chat.resetArmed = false;
        renderChat();
      }));
    } else {
      const controls = element("div", "chat-actions");
      const reset = settingsButton(t("chatReset"), () => {
        chat.resetArmed = true;
        renderChat();
      }, true);
      reset.disabled = chat.streaming;
      controls.appendChild(reset);
      const skip = settingsButton(chat.suppressed ? t("chatUnskip") : t("chatSkip"), toggleChatSkip, true);
      skip.disabled = chat.streaming || (!chat.pending && !chat.suppressed);
      controls.appendChild(skip);
      section.appendChild(controls);
    }

    const form = element("form", "chat-form");
    form.noValidate = true;
    const composer = element("div", "chat-composer");
    const input = element("textarea", "chat-input");
    input.id = "chat-input";
    input.rows = 1;
    input.maxLength = 2000;
    input.placeholder = t("chatPlaceholder");
    input.setAttribute("aria-label", t("chatInputAria"));
    input.disabled = chat.streaming;
    composer.appendChild(input);
    const send = submitButton(t("chatSend"));
    send.className = "chat-send";
    send.disabled = chat.streaming;
    composer.appendChild(send);
    form.appendChild(composer);
    const counter = element("div", "counter chat-counter", "0 / 2000");
    form.appendChild(counter);
    input.addEventListener("input", () => {
      counter.textContent = `${Array.from(input.value).length} / 2000`;
      input.setAttribute("aria-invalid", "false");
      autoGrowChatInput(input);
    });
    input.addEventListener("keydown", (event) => {
      if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
        event.preventDefault();
        sendChat(input.value, input);
      }
    });
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      sendChat(input.value, input);
    });
    section.appendChild(form);

    ui.content.appendChild(section);
    syncBusyState();
    // syncBusyState may restore a previous disabled state after a global loading phase.
    input.disabled = chat.streaming;
    send.disabled = chat.streaming;
    autoGrowChatInput(input);
    scrollChat(true);
  }

  function parseChatEvent(block) {
    let name = "message";
    const data = [];
    for (const line of block.split("\n")) {
      if (line.startsWith("event:")) name = line.slice(6).trim();
      else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
    }
    if (!data.length) return null;
    try { return { name, data: JSON.parse(data.join("\n")) }; } catch (_) { return null; }
  }

  function finishChatError(message, pending) {
    cancelChatLiveRender();
    chat.streaming = false;
    chat.stage = "";
    chat.pending = pending;
    chat.errorText = message;
    const last = chat.messages[chat.messages.length - 1];
    if (last && last.role === "assistant" && !last.text) chat.messages.pop();
    showError(message);
    if (activeTab === "chat") renderChat();
  }

  async function streamChat(text, answer) {
    const headers = new Headers({ "Content-Type": "application/json", "Authorization": `Bearer ${session}` });
    let response;
    try {
      response = await fetch(miniAppBase + "/api/messages", { method: "POST", headers, body: JSON.stringify({ text }), cache: "no-store" });
    } catch (_) {
      finishChatError(t("networkError"), false);
      return;
    }
    if (!response.ok) {
      let body = null;
      try { body = await response.json(); } catch (_) { body = null; }
      if (response.status === 401) {
        expire(t("sessionExpired"));
        return;
      }
      const code = body && body.error && body.error.code;
      finishChatError(responseError(code, response.status), false);
      return;
    }
    if (!response.body || typeof response.body.getReader !== "function") {
      finishChatError(t("chatStreamUnsupported"), true);
      return;
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let doneSeen = false;
    try {
      for (;;) {
        const chunk = await reader.read();
        if (chunk.done) break;
        buffer += decoder.decode(chunk.value, { stream: true });
        let boundary = buffer.indexOf("\n\n");
        while (boundary >= 0) {
          const event = parseChatEvent(buffer.slice(0, boundary));
          buffer = buffer.slice(boundary + 2);
          if (event) {
            if (event.name === "delta") {
              answer.text += typeof event.data.text === "string" ? event.data.text : "";
              updateChatLive(answer);
            } else if (event.name === "stage") {
              chat.stage = typeof event.data.stage === "string" ? event.data.stage : "";
              updateChatLive(answer);
            } else if (event.name === "done") {
              if (typeof event.data.text === "string") answer.text = event.data.text;
              // The server sends the stored assistant row id; without it the bubble has no
              // feedback buttons, which is the correct state for a backend that stored nothing.
              const messageID = Number(event.data.message_id);
              if (Number.isSafeInteger(messageID) && messageID > 0) answer.id = messageID;
              answer.feedback = 0;
              doneSeen = true;
            } else if (event.name === "error") {
              finishChatError(typeof event.data.message === "string" ? event.data.message : t("chatFailed"), true);
              return;
            }
          }
          boundary = buffer.indexOf("\n\n");
        }
      }
    } catch (_) {
      finishChatError(t("chatNetworkFailed"), true);
      return;
    }
    if (!doneSeen) {
      finishChatError(t("chatNetworkFailed"), true);
      return;
    }
    chat.streaming = false;
    chat.pending = false;
    chat.stage = "";
    chat.errorText = "";
    cancelChatLiveRender();
    if (activeTab === "chat") renderChat();
  }

  async function sendChat(value, control) {
    if (chat.streaming || !session) return;
    const text = String(value || "").trim();
    const length = Array.from(text).length;
    if (!length) {
      invalidField(control, t("chatMessageEmpty"));
      return;
    }
    if (length > 2000 || text.includes("\0")) {
      invalidField(control, t("chatMessageTooLong"));
      return;
    }
    control.setAttribute("aria-invalid", "false");
    showError("");
    showNotice("");
    chat.errorText = "";
    chat.stage = "";
    chat.pending = true;
    chat.streaming = true;
    chat.messages.push({ role: "user", text, id: null, feedback: 0 });
    const answer = { role: "assistant", text: "", id: null, feedback: 0 };
    chat.messages.push(answer);
    renderChat();
    await streamChat(text, answer);
  }

  async function performChatReset() {
    if (loading || !session || chat.streaming) return;
    setBusy(true);
    showError("");
    showNotice("");
    let reset = false;
    try {
      await api("/api/context/reset", { method: "POST" });
      reset = true;
    } catch (error) {
      showError(error.message || t("chatResetFailed"));
    } finally {
      setBusy(false);
    }
    if (!reset) return;
    chat.resetArmed = false;
    chat.messages = [];
    chat.pending = false;
    chat.streaming = false;
    chat.stage = "";
    chat.errorText = "";
    chat.suppressed = false;
    await load();
    showNotice(t("chatResetDone"));
  }

  async function toggleChatSkip() {
    if (loading || !session || chat.streaming) return;
    const skip = !chat.suppressed;
    setBusy(true);
    showError("");
    let data = null;
    try {
      data = await api("/api/pending/skip", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ skip }),
      });
    } catch (error) {
      showError(error.message || t("chatSkipFailed"));
    } finally {
      setBusy(false);
    }
    if (!data) return;
    if (data.changed) {
      chat.suppressed = skip;
      showNotice(skip ? t("chatSkipDone") : t("chatUnskipDone"));
    } else {
      showNotice(t("chatSkipNoop"));
    }
    await load();
  }

  function renderView() {
    if (view.tab !== activeTab) return;
    if (activeTab === "status" && view.data) renderStatus(view.data);
    else if (activeTab === "settings" && view.data) renderSettings(view.data);
    else if (activeTab === "usage" && view.data) renderUsage(view.data);
    else if (activeTab === "reminders" && view.data) renderReminders(view.data);
    else if (activeTab === "chat") renderChat();
    else if (!["status", "settings", "usage", "reminders"].includes(activeTab)) renderItems(activeTab, view.items);
  }

  function syncTabs(selected) {
    ui.content.setAttribute("role", "tabpanel");
    ui.content.tabIndex = 0;
    for (const tab of document.querySelectorAll("button[data-tab]")) {
      const active = tab === selected;
      tab.classList.toggle("active", active);
      tab.setAttribute("aria-selected", String(active));
      tab.tabIndex = active ? 0 : -1;
    }
    ui.content.setAttribute("aria-labelledby", selected.id || `tab-${selected.dataset.tab}`);
    ui.content.classList.toggle("is-chat", activeTab === "chat");
  }

  function applyTranslations() {
    document.documentElement.lang = language;
    document.title = t("documentTitle");
    ui.language.value = language;
    for (const node of document.querySelectorAll("[data-i18n]")) node.textContent = t(node.dataset.i18n);
    for (const node of document.querySelectorAll("[data-i18n-aria]")) node.setAttribute("aria-label", t(node.dataset.i18nAria));
    ui.title.textContent = t(`tab${activeTab[0].toUpperCase()}${activeTab.slice(1)}`);
    const selected = document.querySelector(`button[data-tab="${activeTab}"]`);
    if (selected) syncTabs(selected);
    if (!loading) renderView();
  }

  async function load(append = false) {
    if (loading || !session) return;
    setBusy(true);
    resetArmedStates();
    resetSearchState();
    if (!["status", "settings", "usage", "reminders"].includes(activeTab)) renderItems(activeTab, view.items);
    showError("");
    try {
      if (activeTab === "status") {
        view = { tab: activeTab, data: await api("/api/status"), items: [] };
        renderStatus(view.data);
        nextBefore = null;
      } else if (activeTab === "settings") {
        view = { tab: activeTab, data: await api("/api/settings"), items: [] };
        renderSettings(view.data);
        nextBefore = null;
      } else if (activeTab === "usage") {
        view = { tab: activeTab, data: await api("/api/usage"), items: [] };
        renderUsage(view.data);
        nextBefore = null;
      } else if (activeTab === "reminders") {
        view = { tab: activeTab, data: await api("/api/reminders"), items: [] };
        renderReminders(view.data);
        nextBefore = null;
      } else if (activeTab === "chat") {
        if (chat.streaming) {
          // A switch of tabs must not throw away the answer that is arriving right now.
          renderChat();
        } else {
          const status = await api("/api/status");
          const history = await api("/api/history?limit=20");
          const identity = await api("/api/whoami");
          chat.pending = Boolean(status.pending);
          chat.suppressed = Boolean(status.recovery_suppressed);
          chat.identity = identity;
          chat.stage = "";
          chat.errorText = "";
          chat.messages = (Array.isArray(history.items) ? history.items : [])
            .filter((item) => item.role === "user" || item.role === "assistant")
            .reverse()
            .map((item) => ({
              role: item.role,
              text: item.content,
              id: item.id,
              feedback: Number(item.feedback) === -1 ? -1 : Number(item.feedback) === 1 ? 1 : 0,
            }));
          renderChat();
        }
        nextBefore = null;
      } else {
        if (!append) view = { tab: activeTab, data: null, items: [] };
        const params = new URLSearchParams({ limit: "10" });
        if (append && nextBefore) params.set("before", String(nextBefore));
        const data = await api(`/api/${activeTab}?${params}`);
        nextBefore = data.next_before_id || null;
        view.items = view.items.concat(data.items);
        renderItems(activeTab, view.items);
      }
      ui.more.hidden = ["status", "settings", "usage", "reminders"].includes(activeTab) || !nextBefore;
    } catch (error) {
      showError(error.message || t("dataLoadFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function exchange() {
    if (!initData) {
      showError(t("launchRequired"));
      return;
    }
    showError(t("verifyLaunch"));
    try {
      const response = await api("/api/session", {
        method: "POST", headers: { "Content-Type": "text/plain", "X-Launch-Ticket": ticket }, body: initData,
      });
      session = response.token;
      ui.launch.hidden = true;
      ui.app.hidden = false;
      ui.logout.hidden = false;
      setMenuOpen(false);
      showError("");
      await load();
    } catch (error) {
      expire(error.message || t("launchFailed"));
    }
  }

  const menuToggle = document.getElementById("menu-toggle");
  const tabsBar = document.getElementById("tabs");

  // The sections live in a dropdown behind the header button: one tap opens it, choosing a section or tapping
  // outside closes it. The nodes are plain buttons, so keyboard navigation inside the panel keeps working.
  function setMenuOpen(open) {
    if (!tabsBar || !menuToggle) return;
    tabsBar.hidden = !open;
    menuToggle.setAttribute("aria-expanded", String(open));
  }
  if (menuToggle && tabsBar) {
    menuToggle.addEventListener("click", (event) => {
      event.stopPropagation();
      setMenuOpen(tabsBar.hidden);
    });
    document.addEventListener("click", (event) => {
      if (!tabsBar.hidden && !tabsBar.contains(event.target) && !menuToggle.contains(event.target)) setMenuOpen(false);
    });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && !tabsBar.hidden) {
        setMenuOpen(false);
        menuToggle.focus();
      }
    });
  }

  document.getElementById("tabs").addEventListener("click", (event) => {
    const button = event.target.closest("button[data-tab]");
    if (!button || loading) return;
    resetArmedStates();
    resetSearchState();
    setMenuOpen(false); // choosing a section closes the dropdown as it opens the page
    activeTab = button.dataset.tab;
    nextBefore = null;
    view = { tab: activeTab, data: null, items: [] };
    ui.title.textContent = t(`tab${activeTab[0].toUpperCase()}${activeTab.slice(1)}`);
    ui.content.replaceChildren();
    ui.content.scrollTop = 0; // a tab is a page: it opens at its top, not where the previous page was scrolled
    syncTabs(button);
    ui.more.hidden = true;
    load();
  });
  document.getElementById("tabs").addEventListener("keydown", (event) => {
    const current = event.target.closest("button[data-tab]");
    if (!current || loading) return;
    const tabs = document.querySelectorAll("button[data-tab]");
    const index = tabs.indexOf(current);
    let nextIndex = index;
    if (event.key === "ArrowRight") nextIndex = (index + 1) % tabs.length;
    else if (event.key === "ArrowLeft") nextIndex = (index - 1 + tabs.length) % tabs.length;
    else if (event.key === "Home") nextIndex = 0;
    else if (event.key === "End") nextIndex = tabs.length - 1;
    else return;
    event.preventDefault();
    tabs[nextIndex].focus();
    tabs[nextIndex].click();
  });
  ui.language.addEventListener("change", () => {
    language = ui.language.value === "en" ? "en" : "ru";
    try { localStorage.setItem("miniapp-language", language); } catch (_) { /* storage is optional */ }
    showError("");
    applyTranslations();
  });
  ui.refresh.addEventListener("click", () => { resetArmedStates(); resetSearchState(); nextBefore = null; load(); });
  ui.more.addEventListener("click", () => { resetArmedStates(); resetSearchState(); load(true); });
  ui.logout.addEventListener("click", async () => {
    if (loading) return;
    setBusy(true);
    try { await api("/api/session", { method: "DELETE" }); } catch (_) { /* local close still applies */ }
    setBusy(false);
    expire(t("sessionClosed"));
  });

  applyTranslations();
  exchange();
})();
