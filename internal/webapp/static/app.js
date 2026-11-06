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
  let clearDocumentsArmed = false;
  let clearLinksArmed = false;
  let clearTasksArmed = false;
  let deleteDocumentArmed = null;
  let deleteLinkArmed = null;
  let deleteTaskArmed = null;
  let deleteReminderArmed = null;
  let documentSearch = { query: "", active: false, items: [], truncated: false };
  let historySearch = { query: "", active: false, items: [], truncated: false };
  let view = { tab: "status", data: null, items: [] };

  function resetArmedStates() {
    clearDocumentsArmed = false;
    clearLinksArmed = false;
    clearTasksArmed = false;
    deleteDocumentArmed = null;
    deleteLinkArmed = null;
    deleteTaskArmed = null;
    deleteReminderArmed = null;
  }

  function resetSearchState() {
    documentSearch = { query: "", active: false, items: [], truncated: false };
    historySearch = { query: "", active: false, items: [], truncated: false };
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
      response = await fetch(path, { ...options, headers, cache: "no-store" });
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

  function renderEmpty(text) {
    ui.content.replaceChildren(element("p", "empty", text));
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

  function inlineConfirmation(warning, confirmLabel, onConfirm, onCancel) {
    const confirmation = element("div", "inline-confirm");
    confirmation.appendChild(element("p", "inline-confirm-warning", warning));
    const actions = element("div", "task-actions inline-confirm-actions");
    actions.appendChild(settingsButton(confirmLabel, onConfirm));
    actions.appendChild(settingsButton(t("cancelTask"), onCancel, true));
    confirmation.appendChild(actions);
    return confirmation;
  }

  function appendHistoryContent(wrapper, item) {
    wrapper.appendChild(element("div", "role", item.role === "user" ? t("you") : t("assistant")));
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
      } else {
        actions.appendChild(settingsButton(t("cancelTask"), () => changeReminder(`/api/reminders/${item.id}/cancel`), true));
      }
      if (actions.childNodes.length) wrapper.appendChild(actions);
      section.appendChild(wrapper);
    }
    return section;
  }

  function renderReminders(data) {
    ui.content.replaceChildren();
    ui.content.appendChild(renderReminderSection(t("activeReminders"), t("emptyActiveReminders"), data.active || [], false));
    ui.content.appendChild(renderReminderSection(t("failedReminders"), t("emptyFailedReminders"), data.failed || [], true));
    syncBusyState();
  }

  function renderItems(tab, items) {
    ui.content.replaceChildren();
    if (tab === "history") ui.content.appendChild(renderHistorySearch());
    if (tab === "documents") ui.content.appendChild(renderDocumentSearch());
    if (!items.length) {
      if (tab === "documents") ui.content.appendChild(element("p", "empty", t("emptyItems")));
      else if (tab === "history") ui.content.appendChild(element("p", "empty", t("emptyHistory")));
      else renderEmpty(t("emptyItems"));
      syncBusyState();
      return;
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
        wrapper.appendChild(element("div", "content", item.goal));
        const status = maybe(`task_${item.status}`, item.status);
        wrapper.appendChild(element("div", "meta", `${status} · ${t("step")} ${item.next_step} · ${item.attempts} ${t("attempts")} · ${t("due")} ${item.due_at}`));
        wrapper.appendChild(element("div", "meta", `${t("expires")}: ${item.expires_at}`));
        if (item.error) wrapper.appendChild(element("div", "task-error", `${t("taskError")}: ${item.error}`));
        if (item.status === "awaiting_approval") wrapper.appendChild(element("p", "meta", t("networkApprovalTelegram")));
        const actions = element("div", "task-actions");
        if (["ready", "running", "awaiting_approval"].includes(item.status)) {
          actions.appendChild(settingsButton(t("cancelTask"), () => changeTask("/api/tasks/cancel"), true));
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

  async function exportHistory(format) {
    if (loading || !session) return;
    setBusy(true);
    showError("");
    try {
      const headers = new Headers({ "Authorization": `Bearer ${session}` });
      let response;
      try {
        response = await fetch(`/api/history/export?format=${format}`, { headers, cache: "no-store" });
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

  function applyTranslations() {
    document.documentElement.lang = language;
    document.title = t("documentTitle");
    ui.language.value = language;
    for (const node of document.querySelectorAll("[data-i18n]")) node.textContent = t(node.dataset.i18n);
    for (const node of document.querySelectorAll("[data-i18n-aria]")) node.setAttribute("aria-label", t(node.dataset.i18nAria));
    ui.title.textContent = t(`tab${activeTab[0].toUpperCase()}${activeTab.slice(1)}`);
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
    for (const tab of document.querySelectorAll("button[data-tab]")) tab.classList.toggle("active", tab === button);
    ui.more.hidden = true;
    load();
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
