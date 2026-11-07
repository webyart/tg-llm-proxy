(() => {
  "use strict";

  const translations = {
    ru: {
      documentTitle: "Бот — компаньон", headerTitle: "Бот — компаньон", headerSubtitle: "Данные чата и его настройки",
      languageLabel: "Язык", closeSession: "Закрыть сессию", launchTitle: "Запуск из Telegram",
      launchText: "Откройте Mini App через «Ещё → Mini App» в меню бота. Ссылка запуска и подпись Telegram проверяются до показа данных чата.",
      navLabel: "Разделы чата", tabStatus: "Статус", tabHistory: "История", tabDocuments: "Документы",
      tabLinks: "Ссылки", tabTasks: "Задачи", tabReminders: "Напоминания", tabUsage: "Использование", tabSettings: "Настройки", refresh: "Обновить", loadOlder: "Показать ранее", loading: "Загрузка…",
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
      documentSearchTruncated: "Поиск охватил только часть фрагментов документов; более старые фрагменты не проверялись.",
      searchHistory: "Найти", historySearchAria: "Поиск по истории", historySearchPlaceholder: "Поисковый запрос",
      historySearchResults: "Результаты поиска", historySearchEmpty: "По запросу ничего не найдено.",
      historySearchEmptyQuery: "Введите непустой поисковый запрос.", historySearchFailed: "Не удалось выполнить поиск по истории.",
      historySearchTruncated: "Поиск охватил только последние 10000 сообщений; более старые сообщения не проверялись.",
      historyExportMD: "Скачать Markdown", historyExportJSON: "Скачать JSON",
      historyExportReady: "Экспорт истории подготовлен.", historyExportFailed: "Не удалось экспортировать историю.",
      historyExportEmpty: "Нет сообщений для экспорта.", historyExportTooLarge: "Экспорт истории превышает 5 МиБ.",
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
      documentSearchTruncated: "Search covered only part of the document chunks; older chunks were not checked.",
      searchHistory: "Search", historySearchAria: "Search history", historySearchPlaceholder: "Search query",
      historySearchResults: "Search results", historySearchEmpty: "No messages matched the query.",
      historySearchEmptyQuery: "Enter a non-empty search query.", historySearchFailed: "History search failed.",
      historySearchTruncated: "Search covered only the latest 10000 messages; older messages were not checked.",
      historyExportMD: "Download Markdown", historyExportJSON: "Download JSON",
      historyExportReady: "History export is ready.", historyExportFailed: "History export failed.",
      historyExportEmpty: "There are no messages to export.", historyExportTooLarge: "History export exceeds 5 MiB.",
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
    not_found: "errorNotFound", invalid_body: "errorInvalidJSON", static_error: "errorRead", reminder_not_mutable: "reminderNotMutable",
    task_not_active: "taskNotActive", task_not_resumable: "taskNotResumable", task_mutation_failed: "taskMutationFailed",
    task_not_deletable: "taskNotDeletable", document_mutation_failed: "documentMutationFailed",
    link_mutation_failed: "linkMutationFailed", reminder_mutation_failed: "reminderMutationFailed",
    history_mutation_failed: "historyMutationFailed", invalid_url: "linkInvalidURL", invalid_reminder: "reminderInvalid",
    invalid_goal: "taskGoalInvalid",
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
  };
  let session = "";
  let activeTab = "status";
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
  let historySearch = { query: "", active: false, items: [], truncated: false };
  let linkSearch = { query: "", active: false, items: [] };
  let view = { tab: "status", data: null, items: [] };

  function resetArmedStates() {
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
    ui.notice.textContent = "";
    ui.notice.hidden = true;
    ui.error.textContent = message;
    ui.error.hidden = !message;
  }

  function showNotice(message) {
    ui.error.textContent = "";
    ui.error.hidden = true;
    ui.notice.textContent = message;
    ui.notice.hidden = !message;
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

  function appendHistoryContent(wrapper, item) {
    const role = element("div", "role");
    role.appendChild(element("span", item.role === "assistant" ? "badge badge-success" : "badge", item.role === "user" ? t("you") : t("assistant")));
    wrapper.appendChild(role);
    wrapper.appendChild(element("div", "content", item.content));
    if (item.truncated) {
      const actions = element("div", "task-actions");
      actions.appendChild(settingsButton(t("showFullContent"), () => loadFullContent("history", item.id)));
      wrapper.appendChild(actions);
    }
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
    if (tab === "documents") ui.content.appendChild(renderDocumentSearch());
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

  function renderView() {
    if (view.tab !== activeTab) return;
    if (activeTab === "status" && view.data) renderStatus(view.data);
    else if (activeTab === "settings" && view.data) renderSettings(view.data);
    else if (activeTab === "usage" && view.data) renderUsage(view.data);
    else if (activeTab === "reminders" && view.data) renderReminders(view.data);
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
    if (!ticket || !initData) {
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
      showError("");
      await load();
    } catch (error) {
      expire(error.message || t("launchFailed"));
    }
  }

  document.getElementById("tabs").addEventListener("click", (event) => {
    const button = event.target.closest("button[data-tab]");
    if (!button || loading) return;
    resetArmedStates();
    resetSearchState();
    activeTab = button.dataset.tab;
    nextBefore = null;
    view = { tab: activeTab, data: null, items: [] };
    ui.title.textContent = t(`tab${activeTab[0].toUpperCase()}${activeTab.slice(1)}`);
    ui.content.replaceChildren();
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
