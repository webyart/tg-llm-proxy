(() => {
  "use strict";

  // ---- Mini App color theme -------------------------------------------------
  // The theme is a pure UI preference: it lives in localStorage ("dsh-theme") and never reaches the
  // API. Five muted palettes are declared in style.css; an unknown stored value falls back to
  // graphite, and prefers-color-scheme only chooses the very first default (graphite vs sand).
  // This block is the earliest code app.js can run (the script is deferred), so the attribute is set
  // before the app shell is rendered; the CSS light/dark media query covers the pre-script paint.
  const themeStorageKey = "dsh-theme";
  const themeNames = ["graphite", "sand", "ocean", "forest", "lavender"];

  function storedTheme() {
    try {
      const value = localStorage.getItem(themeStorageKey);
      return typeof value === "string" ? value : "";
    } catch (_) {
      return "";
    }
  }

  function initialTheme() {
    const stored = storedTheme();
    if (stored) return themeNames.includes(stored) ? stored : "graphite";
    try {
      if (window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches) return "sand";
    } catch (_) { /* matchMedia is optional outside a browser */ }
    return "graphite";
  }

  let currentTheme = initialTheme();

  function applyTheme(name, persist) {
    const theme = themeNames.includes(name) ? name : "graphite";
    currentTheme = theme;
    document.documentElement.setAttribute("data-theme", theme);
    if (persist) {
      try { localStorage.setItem(themeStorageKey, theme); } catch (_) { /* storage is optional */ }
    }
    return theme;
  }

  function setTheme(name) {
    const theme = applyTheme(name, true);
    const select = document.getElementById("theme");
    if (select) select.value = theme;
    return theme;
  }

  applyTheme(currentTheme, false);

  // ---- Mini App chat timezone ----------------------------------------------
  // The zone is a display preference stored on the server (chat_timezone, migration 024). localStorage
  // mirrors it so the first paint after a reload already uses the right zone; the server value wins as
  // soon as /api/status or /api/settings arrives. Storage and every timestamp stay UTC.
  const timezoneStorageKey = "dsh-timezone";
  const timezoneFallback = "Asia/Yekaterinburg";
  const timezoneChoices = [
    "Asia/Yekaterinburg", "Europe/Moscow", "Europe/Berlin", "UTC",
    "Asia/Almaty", "Asia/Tbilisi", "America/New_York",
  ];
  const monthsRU = ["янв", "фев", "мар", "апр", "мая", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"];
  const monthsEN = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

  function validTimezone(name) {
    if (typeof name !== "string" || !name.trim()) return false;
    try {
      new Intl.DateTimeFormat("en-US", { timeZone: name }).format(0);
      return true;
    } catch (_) {
      return false;
    }
  }

  function storedTimezone() {
    try {
      const value = localStorage.getItem(timezoneStorageKey);
      return typeof value === "string" ? value : "";
    } catch (_) {
      return "";
    }
  }

  function initialTimezone() {
    const stored = storedTimezone();
    return validTimezone(stored) ? stored : timezoneFallback;
  }

  let timezoneName = initialTimezone();
  let timezoneDefault = timezoneFallback;
  let timezoneSource = "global";

  function setTimezoneState(name, source) {
    if (validTimezone(name)) timezoneName = name;
    timezoneSource = source === "chat" ? "chat" : "global";
    try { localStorage.setItem(timezoneStorageKey, timezoneName); } catch (_) { /* storage is optional */ }
  }

  function applyTimezoneFromServer(value) {
    if (!value || !validTimezone(value.current)) return;
    timezoneDefault = validTimezone(value.default) ? value.default : timezoneDefault;
    setTimezoneState(value.current, value.source);
  }

  function currentTimezoneOption() {
    return timezoneName;
  }

  function parseTimestamp(value) {
    if (value instanceof Date) return Number.isNaN(value.getTime()) ? null : value;
    if (typeof value === "number") {
      const fromNumber = new Date(value);
      return Number.isNaN(fromNumber.getTime()) ? null : fromNumber;
    }
    if (typeof value !== "string" || !value.trim()) return null;
    let text = value.trim();
    // SQLite stores `2026-11-11 10:00:00` without a zone; the storage contract is UTC.
    if (/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}/.test(text)) text = text.replace(" ", "T") + "Z";
    const date = new Date(text);
    return Number.isNaN(date.getTime()) ? null : date;
  }

  function zonedParts(value) {
    const date = parseTimestamp(value);
    if (!date) return null;
    try {
      const formatter = new Intl.DateTimeFormat("en-US", {
        timeZone: timezoneName, year: "numeric", month: "2-digit", day: "2-digit",
        hour: "2-digit", minute: "2-digit", hourCycle: "h23",
      });
      const parts = {};
      for (const part of formatter.formatToParts(date)) parts[part.type] = part.value;
      if (parts.hour === "24") parts.hour = "00";
      return {
        date,
        year: Number(parts.year), month: Number(parts.month), day: Number(parts.day),
        hour: Number(parts.hour), minute: Number(parts.minute),
      };
    } catch (_) {
      return { date, year: date.getUTCFullYear(), month: date.getUTCMonth() + 1, day: date.getUTCDate(), hour: date.getUTCHours(), minute: date.getUTCMinutes() };
    }
  }

  function clockText(parts) {
    return `${String(parts.hour).padStart(2, "0")}:${String(parts.minute).padStart(2, "0")}`;
  }

  function zoneOffsetMinutes(parts) {
    const asUTC = Date.UTC(parts.year, parts.month - 1, parts.day, parts.hour, parts.minute);
    const instant = Date.UTC(parts.date.getUTCFullYear(), parts.date.getUTCMonth(), parts.date.getUTCDate(), parts.date.getUTCHours(), parts.date.getUTCMinutes());
    return Math.round((asUTC - instant) / 60000);
  }

  // A short zone label where it helps: the server's zone abbreviation is not always available in Intl,
  // so the known deployment zones get their conventional names; everything else keeps the IANA name.
  function zoneAbbreviation(parts) {
    const offset = zoneOffsetMinutes(parts);
    switch (timezoneName) {
      case "Asia/Yekaterinburg": return "YEKT";
      case "Europe/Moscow": return "MSK";
      case "Asia/Almaty": return "ALMT";
      case "Asia/Tbilisi": return "GET";
      case "Europe/Berlin": return offset === 120 ? "CEST" : "CET";
      case "America/New_York": return offset === -240 ? "EDT" : "EST";
      case "UTC": return "UTC";
      default: return timezoneName;
    }
  }

  function formatDateTime(value, withZone = false) {
    const parts = zonedParts(value);
    if (!parts) return "";
    const months = language === "en" ? monthsEN : monthsRU;
    const nowParts = zonedParts(new Date());
    const year = nowParts && nowParts.year !== parts.year ? ` ${parts.year}` : "";
    const text = `${parts.day} ${months[parts.month - 1]}${year}, ${clockText(parts)}`;
    return withZone ? `${text} ${zoneAbbreviation(parts)}` : text;
  }

  function formatDateValue(value, withZone = false) {
    const parts = zonedParts(value);
    if (!parts) return "";
    const months = language === "en" ? monthsEN : monthsRU;
    const text = `${parts.day} ${months[parts.month - 1]} ${parts.year}`;
    return withZone ? `${text} ${zoneAbbreviation(parts)}` : text;
  }

  function sameCalendarDay(left, right) {
    return left.year === right.year && left.month === right.month && left.day === right.day;
  }

  function dayNumber(parts) {
    return Math.floor(Date.UTC(parts.year, parts.month - 1, parts.day) / 86400000);
  }

  function pluralDaysRU(count) {
    const mod100 = count % 100;
    if (mod100 >= 11 && mod100 <= 14) return "дней";
    switch (count % 10) {
      case 1: return "день";
      case 2: case 3: case 4: return "дня";
      default: return "дней";
    }
  }

  function relativeHoursText(value, future) {
    const hours = Math.floor(value / 3600000);
    const minutes = Math.floor((value % 3600000) / 60000);
    const body = minutes
      ? t("relativeHoursMinutes", { hours, minutes })
      : t("relativeHoursOnly", { hours });
    if (language === "en") return future ? `in ${body}` : `${body} ago`;
    return future ? `через ${body}` : `${body} назад`;
  }

  // formatRelative mirrors internal/timefmt.Relative: minutes and hours nearby, yesterday/tomorrow for
  // the adjacent day, a plural day count for the nearest week, and the absolute date beyond that.
  function formatRelative(value) {
    const target = zonedParts(value);
    const now = zonedParts(new Date());
    if (!target || !now) return "";
    const difference = target.date.getTime() - now.date.getTime();
    const future = difference > 0;
    const absolute = Math.abs(difference);
    if (absolute < 60000) return future ? t("relativeLessMinute") : t("relativeJustNow");
    if (absolute < 3600000) {
      const minutes = Math.floor(absolute / 60000);
      return future ? t("relativeInMinutes", { n: minutes }) : t("relativeMinutesAgo", { n: minutes });
    }
    if (sameCalendarDay(target, now)) return relativeHoursText(absolute, future);
    const days = dayNumber(target) - dayNumber(now);
    if (days === 1) return t("relativeTomorrow", { time: clockText(target) });
    if (days === -1) return t("relativeYesterday", { time: clockText(target) });
    if (days >= 2 && days <= 6) {
      if (language === "en") return t("relativeInDays", { n: days });
      return t("relativeInDays", { n: days, unit: pluralDaysRU(days) });
    }
    if (days <= -2 && days >= -6) {
      if (language === "en") return t("relativeDaysAgo", { n: -days });
      return t("relativeDaysAgo", { n: -days, unit: pluralDaysRU(-days) });
    }
    return formatDateTime(value);
  }

  function formatDuration(milliseconds) {
    const value = Number(milliseconds) || 0;
    if (value <= 0) return "0:00";
    if (value < 60000) return t("durationLessMinute");
    if (value < 3600000) {
      const totalSeconds = Math.floor(value / 1000);
      return `${Math.floor(totalSeconds / 60)}:${String(totalSeconds % 60).padStart(2, "0")}`;
    }
    const hours = Math.floor(value / 3600000);
    const minutes = Math.floor((value % 3600000) / 60000);
    return minutes ? t("durationHoursMinutes", { hours, minutes }) : t("durationHoursOnly", { hours });
  }


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
      chatLiveStats: "{stage} · {seconds} с · ~{tokens} токенов",
      chatStats: "⏱ {seconds} с · {input}→{output} токенов · {rate} · модель {model}",
      chatStatsNoRate: "⏱ {seconds} с · {input}→{output} токенов · модель {model}",
      chatStatsTTFT: "⏱ {seconds} с · {input}→{output} токенов · {rate} · ⚡ {ttft} с · модель {model}",
      chatStatsNoRateTTFT: "⏱ {seconds} с · {input}→{output} токенов · ⚡ {ttft} с · модель {model}",
      chatRate: "{rate} ток/с",
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
      chatApprovalTitle: "Подтверждение сетевого действия", chatApprovalToolReadURL: "Чтение страницы", chatApprovalToolWebSearch: "Веб-поиск",
      chatApprovalAllow: "Разрешить", chatApprovalDeny: "Отклонить", chatApprovalExpires: "Истекает через {seconds} с",
      chatApprovalApproved: "Разрешено. Выполняю действие…", chatApprovalDenied: "Отклонено. Действие не выполнено.",
      chatApprovalExpired: "Время подтверждения истекло. Действие не выполнено.", chatApprovalFailed: "Не удалось передать решение. Повторите попытку.",
      chatVoiceStart: "Записать голосовое", chatVoiceStop: "Остановить запись", chatVoiceCancel: "Отменить запись",
      chatVoiceRecording: "Идёт запись", chatVoiceElapsed: "{seconds} с", chatVoiceUploading: "Распознаю запись…",
      voiceUnsupported: "Браузер не поддерживает запись звука. Отправьте голосовое сообщение в Telegram.",
      voicePermissionDenied: "Нет доступа к микрофону. Разрешите его в настройках браузера или отправьте голосовое в Telegram.",
      voiceTooShort: "Запись слишком короткая. Говорите дольше и остановите запись.",
      voiceNetworkFailed: "Не удалось отправить запись. Проверьте соединение и повторите.",
      voiceFailed: "Не удалось распознать запись. Попробуйте ещё раз или отправьте голосовое в Telegram.",
      voiceUnsupportedAudio: "Этот формат записи не поддерживается. Отправьте голосовое в Telegram.",
      voiceTranscriptReady: "Расшифровка вставлена в поле ввода.",
      sessionExpired: "Сессия истекла. Откройте Mini App заново из Telegram.",
      launchRequired: "Запустите страницу через «Ещё → Mini App» в Telegram.",
      verifyLaunch: "Проверяем запуск из Telegram…", launchFailed: "Не удалось проверить запуск. Откройте Mini App заново из Telegram.",
      sessionClosed: "Сессия закрыта. Откройте Mini App из Telegram, чтобы начать новую.",
      lastActivity: "Последняя активность", messages: "Сообщения", documents: "Документы", savedLinks: "Ссылки",
      tasks: "Задачи", activeTasks: "Активные задачи", awaitingAnswer: "Ожидает ответа", recoverySuppressed: "Автовосстановление отключено", yes: "Да", no: "Нет",
      emptyHistory: "Сообщений пока нет.", emptyItems: "Здесь пока пусто.", you: "Вы", assistant: "Ассистент",
      bytes: "байт", chunks: "фрагментов", step: "шаг", attempts: "попыток", due: "срок",
      task_planning: "готовит план", task_ready: "готова", task_running: "выполняется", task_awaiting_approval: "ждёт разрешения",
      task_completed: "завершена", task_cancelled: "отменена", task_failed: "ошибка",
      planningInProgress: "План готовится…", taskPlanningStarted: "Задача создана, план готовится в фоне.",
      replanTask: "Повторить план", taskReplanStarted: "Повторяю план…", taskReplanFailed: "Не удалось повторить план.",
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
      createTaskTitle: "Новая задача", createTaskText: "Опишите цель — задача создаётся сразу, план появляется в фоне. Форма остаётся доступной.",
      taskGoal: "Цель задачи", taskGoalPlaceholder: "Например: подготовить краткий обзор проекта", taskCreated: "Задача создана.",
      taskGoalInvalid: "Введите цель от 1 до 2000 символов без NUL.",
      taskNotReplannable: "Повторить план можно только для последней задачи с неудачным планом.",
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
      usageRangeTitle: "Период", usageRangeLabel: "Диапазон статистики",
      usageRangeHour: "Час", usageRangeDay: "День", usageRangeWeek: "Неделя", usageRangeMonth: "Месяц",
      usageRangeHint: "Агрегаты без текстов запросов, ответов и целей задач; подписи — в выбранной зоне.",
      usageRangeTruncated: "Диапазон ограничен: показана только самая свежая часть.",
      usageCallsChart: "Вызовы модели", usageCallsChartHint: "Вызовы по интервалам; время — в выбранной зоне.",
      usageCallsChartAria: "График вызовов модели: {total} вызовов; интервалов: {buckets}.",
      usageTokensChart: "Токены", usageTokensChartHint: "Входящие и исходящие токены по интервалам; время — в выбранной зоне.",
      usageTokensChartAria: "График токенов: {tokens_in} входящих и {tokens_out} исходящих; интервалов: {buckets}.",
      usageTokensIn: "Входящие", usageTokensOut: "Исходящие",
      usageCost: "Стоимость", usageCostChart: "Стоимость", usageCostChartHint: "Расход по интервалам, USD; время — в выбранной зоне.",
      usageCostChartAria: "График стоимости: {cost} за {buckets} интервалов.",
      usageToolsChart: "Вызовы инструментов", usageToolsChartAria: "Использование инструментов: {total} вызовов.",
      usageRangeModels: "Модели за период", usageRangeTools: "Инструменты за период",
      usageTodayModels: "Модели за сегодня", noRangeUsage: "За выбранный период обращений не было.",
      usageToolReadURL: "Чтение URL", usageToolWebSearch: "Веб-поиск", usageToolCalculator: "Калькулятор", usageToolRAG: "RAG",
      settingsSaved: "Настройки чата сохранены.", settingsSaveFailed: "Не удалось сохранить настройку.",
      chatLimitsTitle: "Лимиты чатов", chatLimitsHint: "Администратор задаёт дневной лимит модели, дневной лимит сетевых инструментов, лимит сообщений в минуту, дневной бюджет USD и отключённые инструменты для любого активного чата или группы. 0 — без ограничения.",
      chatLimitsChat: "Чат", chatLimitsTypePrivate: "личный", chatLimitsTypeGroup: "группа",
      chatLimitsToday: "Сегодня (UTC): модель {model}, сеть {network}, read_url {read_url}, web_search {web_search}, calculator {calculator}",
      chatLimitsCostToday: "Расход за сегодня (UTC): {cost} из {limit}",
      chatLimitsCostWarning: "Израсходовано не менее 80% дневного бюджета этого чата.",
      chatLimitsModel: "Вызовов модели в сутки", chatLimitsNetwork: "Сетевых вызовов в сутки", chatLimitsMinute: "Сообщений в минуту",
      chatLimitsCost: "Бюджет USD в сутки",
      chatLimitsTools: "Отключённые инструменты", chatLimitsToolReadURL: "Чтение страниц (read_url)",
      chatLimitsToolWebSearch: "Веб-поиск (web_search)", chatLimitsToolCalculator: "Калькулятор (calculator)",
      chatLimitsSave: "Сохранить", chatLimitsReset: "Сбросить",
      chatLimitsSaved: "Лимиты чата сохранены.", chatLimitsResetDone: "Лимиты чата сброшены.",
      chatLimitsFailed: "Не удалось сохранить лимиты чата.", chatLimitsLoadFailed: "Не удалось загрузить список чатов.",
      chatLimitsEmpty: "Активных чатов пока нет.", chatLimitsInvalid: "Лимиты — целые числа от 0 до 1000000.",
      chatLimitsCostInvalid: "Бюджет — число от 0 до 100000 USD (0 — без ограничения).",
      providerSettingsTitle: "Провайдеры и модели", providerSettingsHint: "Администратор меняет список моделей, модель по умолчанию и доступность провайдера без перезапуска. base_url, api_key и таймаут задаются только в MODEL_PROVIDERS; сброс возвращает значения из env.",
      providerSettingsEmpty: "Именованные провайдеры не настроены.", providerSettingsLoadFailed: "Не удалось загрузить провайдеров.",
      providerSettingsSave: "Сохранить", providerSettingsReset: "Сбросить к env",
      providerSettingsSaved: "Настройки провайдера сохранены.", providerSettingsResetDone: "Провайдер сброшен к значениям env.",
      providerSettingsFailed: "Не удалось сохранить настройки провайдера.",
      providerSettingsInvalid: "Список моделей: от 1 до 20 непустых уникальных строк; модель по умолчанию должна быть в списке.",
      providerEnabled: "Провайдер включён", providerModelsLabel: "Модели (по одной в строке)",
      providerModelsPlaceholder: "model-a\nmodel-b", providerDefaultModel: "Модель по умолчанию",
      providerSource: "Источник: {source}", providerSourceEnv: "env (MODEL_PROVIDERS)",
      providerSourceDB: "база (сохранено в Mini App)", providerBaseHost: "host: {host}",
      providerConfiguredModels: "env-список: {models}", providerModelsCount: "{count} / 20",
      errorAdminRequired: "Раздел доступен только администратору.", errorChatNotFound: "Чат не найден.",
      errorInvalidTimezone: "Неизвестный часовой пояс. Выберите IANA-название из списка.",
      errorTimezoneForbidden: "Менять часовой пояс может только владелец личного чата или администратор.",
      errorInvalidProvider: "Провайдер не может быть изменён: strata зарезервирован за основным endpoint.",
      errorUnknownProvider: "Провайдер не настроен в MODEL_PROVIDERS.",
      errorProviderValidation: "Проверьте список моделей: 1–20 непустых уникальных строк и default из списка.",
      errorInvalidLimits: "Проверьте лимиты: целые числа от 0 до 1000000.", errorInvalidTools: "Неизвестный инструмент: доступны read_url, web_search, calculator.",
      dataLoadFailed: "Не удалось загрузить данные.", invalidResponse: "Сервер вернул некорректный ответ.",
      networkError: "Нет соединения с сервером. Проверьте сеть и повторите попытку.", requestFailed: "Запрос не выполнен (код {status}).",
      errorModels: "Не удалось получить текущий список моделей провайдера.",
      errorInvalidModel: "Выберите модель из текущего списка провайдера.",
      errorInvalidPersona: "Выберите существующий пресет или введите от 1 до 4000 символов без NUL.",
      errorInvalidStream: "Укажите корректное состояние стриминга.", errorInvalidMiniAppFallback: "Укажите, доставлять ли ответ Mini App в Telegram.", errorInvalidJSON: "Некорректные или неизвестные поля настроек.",
      errorContentType: "Ожидался строгий JSON запрос.", errorBodyLarge: "Слишком большой размер запроса.",
      errorMethod: "Этот метод не разрешён.", errorPagination: "Некорректные параметры страницы.",
      errorInvalidQuery: "Введите один непустой запрос длиной до 4000 символов.", reminderNotMutable: "Напоминание отсутствует или его нельзя изменить в текущем состоянии.",
      errorRead: "Не удалось прочитать данные.", errorOverloaded: "Mini App перегружен. Повторите попытку позже.",
      errorRateLimited: "Слишком много запросов. Повторите попытку позже.",
      errorStreamLimit: "В этом чате уже формируется ответ. Дождитесь его завершения и повторите.",
      errorInvalidTTSSpeed: "Выберите скорость 0,75×, 1×, 1,25× или 1,5×.",
      errorNotFound: "Страница или API не найдены.",
      errorInternal: "Mini App не удалось обработать запрос. Повторите попытку или откройте её заново из Telegram.",
      lifecycleUnavailable: "Это действие недоступно в автономном развёртывании Mini App.",
      taskActive: "В этом чате уже есть активная задача.",
      linkLimitReached: "Достигнут лимит сохранённых ссылок этого чата.",
      reminderLimitReached: "Достигнут лимит активных напоминаний этого чата.",
      urlReaderDisabled: "Сохранение ссылки читает страницу, но чтение URL отключено в этом развёртывании.",
      sourceChat: "настройка этого чата", sourceGlobal: "общая настройка", current: "Текущее",
      globalDefault: "Глобальное значение", customText: "пользовательский текст", modelTitle: "Модель",
    accountTitle: "Язык и сессия", accountHint: "Язык интерфейса и закрытие текущей сессии Mini App.",
      themeTitle: "Тема оформления", themeHint: "Пять приглушённых схем: графит, песочная, океан, лес и лаванда. Выбор применяется сразу и сохраняется в этом браузере.",
      themeAria: "Тема оформления", themeGraphite: "Графит (мягкая тёмная)", themeSand: "Песочная (тёплая светлая)",
      themeOcean: "Океан (серо-синяя)", themeForest: "Лес (приглушённая зелёная)", themeLavender: "Лаванда (пыльная светлая)",
      timezoneTitle: "Часовой пояс", timezoneHint: "Время задач, напоминаний, истории и статистики показывается в этой зоне. Хранение и вычисления остаются в UTC.",
      timezoneAria: "Часовой пояс чата", timezoneDefaultOption: "По умолчанию ({zone})",
      timezoneSourceChat: "этот чат", timezoneSourceDefault: "по умолчанию",
      timezoneSaved: "Часовой пояс применён.", timezoneSaveFailed: "Не удалось сохранить часовой пояс.",
      relativeJustNow: "только что", relativeLessMinute: "через <1 мин",
      relativeInMinutes: "через {n} мин", relativeMinutesAgo: "{n} мин назад",
      relativeHoursMinutes: "{hours} ч {minutes} мин", relativeHoursOnly: "{hours} ч",
      relativeYesterday: "вчера {time}", relativeTomorrow: "завтра {time}",
      relativeInDays: "через {n} {unit}", relativeDaysAgo: "{n} {unit} назад",
      durationLessMinute: "~<1 мин", durationHoursOnly: "{hours} ч", durationHoursMinutes: "{hours} ч {minutes} мин",
      createdAt: "создана", dueAt: "срок", expiresAt: "до", durationLabel: "длительность",
      availableModels: "Доступные модели провайдера", noModels: "Провайдер не сообщил ни одной модели",
      useModel: "Применить модель", resetDefault: "Сбросить к общей",
      modelsTruncated: "Показаны первые 50 моделей. Обновите, чтобы снова проверить endpoint.",
      personaTitle: "Роль (persona)", personaPresets: "Пресеты роли", usePreset: "Применить пресет",
      resetPersona: "Сбросить роль", customPlaceholder: "Пользовательский текст роли (1–4000 символов Unicode)",
      customAria: "Пользовательский текст роли", saveCustom: "Сохранить свою роль",
      streamTitle: "Стриминг ответов", streamAria: "Стриминг ответов", on: "Включён", off: "Выключен",
      saveStream: "Сохранить стриминг", resetGlobal: "Сбросить к общей",
      miniAppTitle: "Mini App", miniAppFallbackAria: "Доставка ответов Mini App в Telegram",
      saveMiniAppFallback: "Сохранить доставку в Telegram", resetMiniAppFallback: "Сбросить доставку в Telegram",
      miniappFallbackSaved: "Доставка ответов Mini App в Telegram сохранена.",
      miniappFallbackReset: "Доставка ответов Mini App в Telegram сброшена.",
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
      chatLiveStats: "{stage} · {seconds}s · ~{tokens} tokens",
      chatStats: "⏱ {seconds}s · {input}→{output} tokens · {rate} · model {model}",
      chatStatsNoRate: "⏱ {seconds}s · {input}→{output} tokens · model {model}",
      chatStatsTTFT: "⏱ {seconds}s · {input}→{output} tokens · {rate} · ⚡ {ttft}s · model {model}",
      chatStatsNoRateTTFT: "⏱ {seconds}s · {input}→{output} tokens · ⚡ {ttft}s · model {model}",
      chatRate: "{rate} tok/s",
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
      chatApprovalTitle: "Network action approval", chatApprovalToolReadURL: "Reading the page", chatApprovalToolWebSearch: "Web search",
      chatApprovalAllow: "Allow", chatApprovalDeny: "Deny", chatApprovalExpires: "Expires in {seconds} s",
      chatApprovalApproved: "Approved. Running the action…", chatApprovalDenied: "Denied. The action was not performed.",
      chatApprovalExpired: "The approval expired. The action was not performed.", chatApprovalFailed: "The decision could not be delivered. Try again.",
      chatVoiceStart: "Record a voice message", chatVoiceStop: "Stop recording", chatVoiceCancel: "Cancel recording",
      chatVoiceRecording: "Recording", chatVoiceElapsed: "{seconds} s", chatVoiceUploading: "Transcribing the recording…",
      voiceUnsupported: "This browser cannot record audio. Send a voice message in Telegram instead.",
      voicePermissionDenied: "Microphone access is blocked. Allow it in the browser settings or send a voice message in Telegram.",
      voiceTooShort: "The recording is too short. Speak longer and stop the recording.",
      voiceNetworkFailed: "The recording could not be uploaded. Check your connection and try again.",
      voiceFailed: "The recording could not be transcribed. Try again or send a voice message in Telegram.",
      voiceUnsupportedAudio: "This recording format is not supported. Send a voice message in Telegram instead.",
      voiceTranscriptReady: "The transcript was inserted into the message field.",
      sessionExpired: "Session expired. Reopen the Mini App from Telegram.",
      launchRequired: "This page needs a launch from More → Mini App in Telegram.",
      verifyLaunch: "Verifying Telegram launch…", launchFailed: "Launch verification failed. Reopen the Mini App from Telegram.",
      sessionClosed: "Session closed. Reopen the Mini App from Telegram to start a new one.",
      lastActivity: "Last activity", messages: "Messages", documents: "Documents", savedLinks: "Saved links",
      tasks: "Tasks", activeTasks: "Active tasks", awaitingAnswer: "Awaiting answer", recoverySuppressed: "Recovery suppressed", yes: "Yes", no: "No",
      emptyHistory: "No messages yet.", emptyItems: "Nothing here yet.", you: "You", assistant: "Assistant",
      bytes: "bytes", chunks: "chunks", step: "step", attempts: "attempts", due: "due",
      task_planning: "planning", task_ready: "ready", task_running: "running", task_awaiting_approval: "awaiting approval",
      task_completed: "completed", task_cancelled: "cancelled", task_failed: "failed",
      planningInProgress: "Preparing the plan…", taskPlanningStarted: "Task created, the plan is being prepared in the background.",
      replanTask: "Retry plan", taskReplanStarted: "Preparing the plan again…", taskReplanFailed: "The plan could not be retried.",
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
      createTaskTitle: "New task", createTaskText: "Describe the goal; the task is created immediately and the plan appears in the background. The form stays usable.",
      taskGoal: "Task goal", taskGoalPlaceholder: "For example: prepare a concise project overview", taskCreated: "Task created.",
      taskGoalInvalid: "Enter a goal of 1 to 2000 characters without NUL.",
      taskNotReplannable: "Only the latest task with a failed plan can be replanned.",
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
      usageRangeTitle: "Range", usageRangeLabel: "Statistics range",
      usageRangeHour: "Hour", usageRangeDay: "Day", usageRangeWeek: "Week", usageRangeMonth: "Month",
      usageRangeHint: "Aggregates without request texts, answers, or task goals; labels use the selected zone.",
      usageRangeTruncated: "The range is capped; only the newest part is shown.",
      usageCallsChart: "Model calls", usageCallsChartHint: "Calls per bucket; time in the selected zone.",
      usageCallsChartAria: "Model calls chart: {total} calls across {buckets} buckets.",
      usageTokensChart: "Tokens", usageTokensChartHint: "Input and output tokens per bucket; time in the selected zone.",
      usageTokensChartAria: "Tokens chart: {tokens_in} input and {tokens_out} output tokens across {buckets} buckets.",
      usageTokensIn: "Input", usageTokensOut: "Output",
      usageCost: "Cost", usageCostChart: "Cost", usageCostChartHint: "Spend per bucket, USD; time in the selected zone.",
      usageCostChartAria: "Cost chart: {cost} across {buckets} buckets.",
      usageToolsChart: "Tool calls", usageToolsChartAria: "Tool usage: {total} calls.",
      usageRangeModels: "Models in range", usageRangeTools: "Tools in range",
      usageTodayModels: "Models today", noRangeUsage: "No calls in the selected range.",
      usageToolReadURL: "URL reading", usageToolWebSearch: "Web search", usageToolCalculator: "Calculator", usageToolRAG: "RAG",
      settingsSaved: "Settings saved for this chat.", settingsSaveFailed: "The setting could not be saved.",
      chatLimitsTitle: "Chat limits", chatLimitsHint: "An administrator sets a daily model limit, a daily network-tool limit, a per-minute message limit, a daily USD cost budget and disabled tools for any active chat or group. 0 means unlimited.",
      chatLimitsChat: "Chat", chatLimitsTypePrivate: "private", chatLimitsTypeGroup: "group",
      chatLimitsToday: "Today (UTC): model {model}, network {network}, read_url {read_url}, web_search {web_search}, calculator {calculator}",
      chatLimitsCostToday: "Today's spend (UTC): {cost} of {limit}",
      chatLimitsCostWarning: "At least 80% of this chat's daily cost budget is spent.",
      chatLimitsModel: "Model calls per day", chatLimitsNetwork: "Network calls per day", chatLimitsMinute: "Messages per minute",
      chatLimitsCost: "USD budget per day",
      chatLimitsTools: "Disabled tools", chatLimitsToolReadURL: "Page reader (read_url)",
      chatLimitsToolWebSearch: "Web search (web_search)", chatLimitsToolCalculator: "Calculator (calculator)",
      chatLimitsSave: "Save", chatLimitsReset: "Reset",
      chatLimitsSaved: "Chat limits saved.", chatLimitsResetDone: "Chat limits reset.",
      chatLimitsFailed: "The chat limits could not be saved.", chatLimitsLoadFailed: "The chat list could not be loaded.",
      chatLimitsEmpty: "There are no active chats yet.", chatLimitsInvalid: "Limits are integers from 0 to 1000000.",
      chatLimitsCostInvalid: "The budget must be a number from 0 to 100000 USD (0 means unlimited).",
      providerSettingsTitle: "Providers and models", providerSettingsHint: "An administrator edits the model list, the default model and the provider enabled flag without a restart. base_url, api_key and timeout stay in MODEL_PROVIDERS; Reset restores the env values.",
      providerSettingsEmpty: "No named providers are configured.", providerSettingsLoadFailed: "The providers could not be loaded.",
      providerSettingsSave: "Save", providerSettingsReset: "Reset to env",
      providerSettingsSaved: "Provider settings saved.", providerSettingsResetDone: "Provider reset to the env values.",
      providerSettingsFailed: "The provider settings could not be saved.",
      providerSettingsInvalid: "Models must be 1 to 20 non-empty unique lines, and the default model must be in the list.",
      providerEnabled: "Provider enabled", providerModelsLabel: "Models (one per line)",
      providerModelsPlaceholder: "model-a\nmodel-b", providerDefaultModel: "Default model",
      providerSource: "Source: {source}", providerSourceEnv: "env (MODEL_PROVIDERS)",
      providerSourceDB: "database (saved in the Mini App)", providerBaseHost: "host: {host}",
      providerConfiguredModels: "env list: {models}", providerModelsCount: "{count} / 20",
      errorAdminRequired: "This section is available to administrators only.", errorChatNotFound: "The chat was not found.",
      errorInvalidTimezone: "Unknown timezone. Choose an IANA name from the list.",
      errorTimezoneForbidden: "Only the private chat owner or an administrator can change the timezone.",
      errorInvalidProvider: "This provider cannot be edited: strata is reserved for the default endpoint.",
      errorUnknownProvider: "The provider is not configured in MODEL_PROVIDERS.",
      errorProviderValidation: "Check the models: 1 to 20 non-empty unique lines and a default from that list.",
      errorInvalidLimits: "Check the limits: integers from 0 to 1000000.", errorInvalidTools: "Unknown tool: only read_url, web_search and calculator are allowed.",
      dataLoadFailed: "The data could not be loaded.", invalidResponse: "The server returned an invalid response.",
      networkError: "The server could not be reached. Check your connection and try again.", requestFailed: "Request failed ({status}).",
      errorModels: "The current provider model list could not be read.",
      errorInvalidModel: "Choose a model from the current provider list.",
      errorInvalidPersona: "Choose an existing preset or enter 1 to 4000 Unicode characters without NUL.",
      errorInvalidStream: "Choose a valid stream setting.", errorInvalidMiniAppFallback: "Choose an enabled or disabled Mini App Telegram delivery.", errorInvalidJSON: "Invalid or unknown settings fields.",
      errorContentType: "A strict JSON request was expected.", errorBodyLarge: "Request body is too large.",
      errorMethod: "Method not allowed.", errorPagination: "Invalid pagination parameters.",
      errorInvalidQuery: "Enter one nonempty query of at most 4000 characters.", reminderNotMutable: "The reminder is missing or cannot be changed in its current state.",
      errorRead: "The data could not be read.", errorOverloaded: "The Mini App is busy. Try again shortly.",
      errorRateLimited: "Too many requests. Try again later.",
      errorStreamLimit: "An answer is already streaming in this chat. Wait for it to finish and retry.",
      errorInvalidTTSSpeed: "Choose a speed of 0.75×, 1×, 1.25×, or 1.5×.",
      errorNotFound: "Page or API endpoint not found.",
      errorInternal: "The Mini App could not handle this request. Retry or reopen it from Telegram.",
      lifecycleUnavailable: "This action is unavailable in the standalone Mini App.",
      taskActive: "An active task already exists in this chat.",
      linkLimitReached: "This chat reached its saved-link limit.",
      reminderLimitReached: "This chat reached its active reminder limit.",
      urlReaderDisabled: "Saving a link reads the page, but the URL reader is disabled in this deployment.",
      sourceChat: "this chat override", sourceGlobal: "global default", current: "Current",
      globalDefault: "Global default", customText: "custom text", modelTitle: "Model",
    accountTitle: "Language and session", accountHint: "Interface language and closing the current Mini App session.",
      themeTitle: "Color theme", themeHint: "Five muted schemes: graphite, sand, ocean, forest and lavender. The choice applies immediately and stays in this browser.",
      themeAria: "Color theme", themeGraphite: "Graphite (soft dark)", themeSand: "Sand (warm light)",
      themeOcean: "Ocean (cool blue-gray)", themeForest: "Forest (muted green)", themeLavender: "Lavender (dusty light)",
      timezoneTitle: "Timezone", timezoneHint: "Task, reminder, history and usage times render in this zone. Storage and calculations stay in UTC.",
      timezoneAria: "Chat timezone", timezoneDefaultOption: "Default ({zone})",
      timezoneSourceChat: "this chat", timezoneSourceDefault: "default",
      timezoneSaved: "Timezone applied.", timezoneSaveFailed: "The timezone could not be saved.",
      relativeJustNow: "just now", relativeLessMinute: "in <1 min",
      relativeInMinutes: "in {n} min", relativeMinutesAgo: "{n} min ago",
      relativeHoursMinutes: "{hours} h {minutes} min", relativeHoursOnly: "{hours} h",
      relativeYesterday: "yesterday {time}", relativeTomorrow: "tomorrow {time}",
      relativeInDays: "in {n} days", relativeDaysAgo: "{n} days ago",
      durationLessMinute: "~<1 min", durationHoursOnly: "{hours} h", durationHoursMinutes: "{hours} h {minutes} min",
      createdAt: "created", dueAt: "due", expiresAt: "until", durationLabel: "duration",
      availableModels: "Available provider models", noModels: "No models reported by the provider",
      useModel: "Use model", resetDefault: "Reset to default",
      modelsTruncated: "Only the first 50 provider models are shown; refresh to check the endpoint again.",
      personaTitle: "Persona", personaPresets: "Persona presets", usePreset: "Use preset",
      resetPersona: "Reset persona", customPlaceholder: "Custom persona text (1–4000 Unicode characters)",
      customAria: "Custom persona text", saveCustom: "Save custom persona",
      streamTitle: "Stream responses", streamAria: "Stream responses", on: "On", off: "Off",
      saveStream: "Save stream setting", resetGlobal: "Reset to global",
      miniAppTitle: "Mini App", miniAppFallbackAria: "Mini App answer delivery to Telegram",
      saveMiniAppFallback: "Save Telegram delivery", resetMiniAppFallback: "Reset Telegram delivery",
      miniappFallbackSaved: "Mini App Telegram delivery saved.",
      miniappFallbackReset: "Mini App Telegram delivery reset.",
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
    invalid_miniapp_fallback: "errorInvalidMiniAppFallback",
    invalid_json: "errorInvalidJSON", content_type: "errorContentType", body_too_large: "errorBodyLarge",
    method_not_allowed: "errorMethod", invalid_pagination: "errorPagination", invalid_query: "errorInvalidQuery", read_failed: "errorRead",
    overloaded: "errorOverloaded", rate_limited: "errorRateLimited", stream_limit: "errorStreamLimit", invalid_tts_speed: "errorInvalidTTSSpeed",
    export_failed: "historyExportFailed", export_too_large: "historyExportTooLarge", history_empty: "historyExportEmpty",
    not_found: "errorNotFound", invalid_body: "errorInvalidJSON", upload_timeout: "documentUploadTimeout", static_error: "errorRead", reminder_not_mutable: "reminderNotMutable",
    task_not_active: "taskNotActive", task_not_resumable: "taskNotResumable", task_mutation_failed: "taskMutationFailed",
    task_not_deletable: "taskNotDeletable", task_not_replannable: "taskNotReplannable", document_mutation_failed: "documentMutationFailed",
    document_empty: "documentUploadEmpty", document_too_large: "documentUploadLimit",
    unsupported_document: "documentUploadType", document_limit: "documentLimitReached",
    document_no_text: "documentUploadNoText", document_encrypted: "documentUploadEncrypted",
    invalid_document: "documentUploadCorrupt",
    link_mutation_failed: "linkMutationFailed", reminder_mutation_failed: "reminderMutationFailed",
    history_mutation_failed: "historyMutationFailed", invalid_url: "linkInvalidURL", invalid_reminder: "reminderInvalid",
    feedback_forbidden: "feedbackForbidden",
    invalid_goal: "taskGoalInvalid", chat_forbidden: "chatForbidden", invalid_text: "chatInvalidText", text_too_long: "chatTooLongError",
    chat_unavailable: "chatUnavailable", chat_failed: "chatFailed", chat_mutation_failed: "chatMutationFailed",
    approval_expired: "chatApprovalExpired", approval_not_found: "chatApprovalExpired", approval_failed: "chatApprovalFailed",
    audio_too_short: "voiceTooShort", unsupported_audio: "voiceUnsupportedAudio", transcription_failed: "voiceFailed",
    task_active: "taskActive", link_limit: "linkLimitReached", reminder_limit: "reminderLimitReached",
    url_reader_disabled: "urlReaderDisabled", lifecycle_unavailable: "lifecycleUnavailable", internal_error: "errorInternal",
    admin_required: "errorAdminRequired", chat_not_found: "errorChatNotFound",
    invalid_limits: "errorInvalidLimits", invalid_tools: "errorInvalidTools", invalid_limit: "errorInvalidLimits",
    invalid_provider: "errorInvalidProvider", unknown_provider: "errorUnknownProvider",
    provider_validation: "errorProviderValidation",
    invalid_timezone: "errorInvalidTimezone", timezone_forbidden: "errorTimezoneForbidden",
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
  // Background task planning: a draft survives the 2.5-second task poll (the form must stay usable while
  // the card updates), and taskPollTimer is the single pending refresh of the tasks tab.
  let taskDraft = "";
  let taskPollTimer = 0;
  // Admin chat limits: the fetched chat list survives a re-render of the Settings tab (so changing the
  // selected chat or the language does not refetch), and null means "fetch on the next render". The
  // state exists even for a non-admin session — it simply stays empty and no request is made.
  let adminLimits = { chats: null, selected: null, loading: false, error: "" };
  // Admin provider/model overrides. The fetched list survives a re-render of Settings (a language switch
  // does not refetch); null means "fetch on the next render". A non-admin never calls the endpoint.
  let adminProviders = { items: null, loading: false, error: "" };
  let chat = { messages: [], pending: false, streaming: false, suppressed: false, stage: "", errorText: "", resetArmed: false, identity: null, approval: null, startedAt: 0, streamChars: 0 };
  let chatNodes = { log: null, answer: null, status: null, approvalNote: null };
  let chatLiveTimer = null;
  let chatLiveNode = null;
  let chatLiveText = "";
  let chatStatsTimer = null;
  let chatApprovalTimer = null;
  // Voice input state survives a re-render of the Chat tab: MediaRecorder lives in this object, the DOM
  // nodes are rebuilt around it, and the timer is rescheduled after every render.
  let chatVoice = { state: "idle", recorder: null, stream: null, chunks: [], mime: "", startedAt: 0, timer: null };

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

  function stopTaskPoll() {
    if (taskPollTimer) {
      clearTimeout(taskPollTimer);
      taskPollTimer = 0;
    }
  }

  // The tasks tab refreshes itself only while a row is still planning: every other tab and every idle
  // task list keep the manual Refresh button only. The form is never put into the global busy state by
  // this poll, so a slow plan cannot disable it.
  function scheduleTaskPoll() {
    stopTaskPoll();
    if (!session || activeTab !== "tasks" || loading) return;
    if (!view.items.some((item) => item.status === "planning")) return;
    taskPollTimer = setTimeout(pollTaskPlanning, 2500);
  }

  async function pollTaskPlanning() {
    taskPollTimer = 0;
    if (!session || activeTab !== "tasks" || loading) return;
    const active = document.activeElement;
    const typingGoal = Boolean(active && active.getAttribute && active.getAttribute("aria-label") === t("taskGoal"));
    try {
      const data = await api("/api/tasks?limit=10");
      view.items = Array.isArray(data.items) ? data.items : [];
      nextBefore = data.next_before_id || null;
      renderItems("tasks", view.items);
      if (typingGoal) {
        const goal = ui.content.querySelector(`[aria-label="${t("taskGoal")}"]`);
        if (goal) goal.focus();
      }
    } catch (_) {
      // A transient poll failure must not replace the page with an error banner; the next tick retries.
    }
    scheduleTaskPoll();
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
    cancelVoiceRecording();
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
    // A known code is more precise than the generic 429 text: stream_limit says another answer is
    // already running in this chat, while rate_limited still falls back to the shared message.
    const key = errorKeys[code] || (status === 429 ? "errorRateLimited" : "requestFailed");
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
    const stamp = timestamp ? ` · ${formatDateTime(timestamp)}` : "";
    wrapper.appendChild(element("div", "meta", `#${id}${stamp}`));
    return wrapper;
  }

  function renderStatus(data) {
    ui.content.replaceChildren();
    if (data.last_activity) ui.content.appendChild(element("p", "meta", `${t("lastActivity")}: ${formatRelative(data.last_activity)}`));
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
    // The poll re-renders the tab while a plan is being prepared: keep the text the user is typing and
    // restore focus after the render instead of silently clearing the form.
    goalInput.value = taskDraft;
    clearFieldValidity(goalInput);
    creation.form.appendChild(formField(t("taskGoal"), goalInput, true));
    const counter = element("div", "counter", `${Array.from(goalInput.value).length} / 2000`);
    goalInput.addEventListener("input", () => {
      taskDraft = goalInput.value;
      counter.textContent = `${Array.from(goalInput.value).length} / 2000`;
    });
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
      const wrapper = itemShell(item.id, item.created_at);
      wrapper.appendChild(element("div", "content", item.text));
      const cadence = item.cadence === "daily" ? `${t("reminderDaily")} ${item.daily_time || ""}`.trim() : t("reminderOnce");
      wrapper.appendChild(element("div", "meta", `${t("reminderCadence")}: ${cadence} · ${t("reminderNextRun")}: ${formatRelative(item.next_run)} · ${item.attempts} ${t("attempts")}`));
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
        const planning = item.status === "planning";
        const status = planning ? t("planningInProgress") : maybe(`task_${item.status}`, item.status);
        const statusClass = item.status === "completed" ? "badge badge-success" : ["failed", "cancelled"].includes(item.status) ? "badge badge-danger" : "badge";
        wrapper.appendChild(element("span", statusClass, status));
        wrapper.appendChild(element("div", "content", item.goal));
        if (planning) {
          // A planning card is the visible progress of the asynchronous create: do not show a due date
          // that was only the insert time.
          wrapper.appendChild(element("div", "meta", `${t("planningInProgress")} · ${t("createdAt")}: ${formatRelative(item.created_at)}`));
        } else {
          wrapper.appendChild(element("div", "meta", `${status} · ${t("step")} ${item.next_step} · ${item.attempts} ${t("attempts")} · ${t("createdAt")}: ${formatRelative(item.created_at)} · ${t("dueAt")}: ${formatRelative(item.due_at)}`));
          wrapper.appendChild(element("div", "meta", `${t("expiresAt")}: ${formatRelative(item.expires_at)}`));
        }
        if (item.duration_ms > 0) wrapper.appendChild(element("div", "meta", `${t("durationLabel")}: ${formatDuration(item.duration_ms)}`));
        if (item.error) wrapper.appendChild(element("div", "task-error", `${t("taskError")}: ${item.error}`));
        if (item.status === "awaiting_approval") wrapper.appendChild(element("p", "meta", t("networkApprovalTelegram")));
        const actions = element("div", "task-actions");
        if (["planning", "ready", "running", "awaiting_approval"].includes(item.status)) {
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
            if (index === 0 && item.status === "failed" && !item.has_plan) {
              // The planner never produced a plan: there is nothing to resume, only to replan.
              actions.appendChild(settingsButton(t("replanTask"), () => replanTask(item.id)));
            } else if (index === 0 && ["failed", "cancelled"].includes(item.status) && item.has_plan) {
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

  // Task creation is the one asynchronous mutation of the Mini App: the server creates the planning row
  // and returns at once, so this handler never sets the global busy state. The new card appears
  // immediately and the form stays available for the next goal.
  async function createTask(value, control) {
    if (loading || !session) return;
    const normalized = value.trim();
    const length = Array.from(normalized).length;
    if (!length || length > 2000 || normalized.includes("\0")) {
      invalidField(control, t("taskGoalInvalid"));
      return;
    }
    control.setAttribute("aria-invalid", "false");
    showError("");
    let created = null;
    try {
      created = await api("/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ goal: normalized }),
      });
    } catch (error) {
      showError(error.message || t("taskMutationFailed"));
      return;
    }
    resetArmedStates();
    resetSearchState();
    taskDraft = "";
    if (created && created.id) {
      const now = new Date().toISOString();
      const item = {
        id: created.id, goal: normalized, status: created.status || "planning", next_step: 0,
        attempts: 0, has_plan: false, due_at: now, expires_at: now, created_at: now, updated_at: now,
      };
      view.items = [item, ...view.items.filter((existing) => existing.id !== item.id)];
      renderItems("tasks", view.items);
      scheduleTaskPoll();
    } else {
      await load();
    }
    showNotice(t("taskCreated"));
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

  // The failed-plan retry is asynchronous on the server as well: the endpoint only flips the row back to
  // planning, so the card becomes a planning card again and the poll takes over.
  async function replanTask(id) {
    if (loading || !session) return;
    showError("");
    let changed = false;
    try {
      const data = await api(`/api/tasks/${id}/replan`, { method: "POST" });
      const item = view.items.find((candidate) => candidate.id === id);
      if (item) {
        item.status = data.status || "planning";
        item.has_plan = false;
        item.error = "";
      }
      changed = true;
    } catch (error) {
      showError(error.message || t("taskReplanFailed"));
    }
    if (!changed) return;
    resetArmedStates();
    resetSearchState();
    renderItems("tasks", view.items);
    scheduleTaskPoll();
    showNotice(t("taskReplanStarted"));
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

  // The admin-only "Chat limits" section. The server decides who is an admin (is_admin of /api/settings)
  // and re-checks every /api/admin request; this code only avoids showing a section a non-admin cannot
  // use. Chats are labelled with id, title and type because a group has a negative id and a long title.
  function chatLimitChatLabel(chat) {
    const kind = chat.type === "group" ? t("chatLimitsTypeGroup") : t("chatLimitsTypePrivate");
    const title = chat.title || kind;
    return `${chat.id} · ${title} · ${kind}`;
  }

  function chatLimitNumber(input) {
    const raw = String(input.value).trim();
    if (!/^[0-9]+$/.test(raw)) return null;
    const value = Number(raw);
    if (!Number.isSafeInteger(value) || value < 0 || value > 1000000) return null;
    return value;
  }

  // The cost budget is a dollar amount, not a call counter: up to 100000 USD with up to six decimals (the
  // API stores micro-dollars). The value is rounded here so the strict PUT body carries no float dust.
  function chatLimitCostUSD(input) {
    const raw = String(input.value).trim();
    if (!/^[0-9]+(?:\.[0-9]{1,6})?$/.test(raw)) return null;
    const value = Number(raw);
    if (!Number.isFinite(value) || value < 0 || value > 100000) return null;
    return Math.round(value * 1000000) / 1000000;
  }

  async function refreshAdminLimits(body) {
    if (adminLimits.chats !== null || adminLimits.loading) {
      renderAdminLimitsBody(body);
      return;
    }
    adminLimits.loading = true;
    try {
      const data = await api("/api/admin/chats?limit=50");
      adminLimits.chats = Array.isArray(data.items) ? data.items : [];
      adminLimits.error = "";
      const known = adminLimits.chats.some((chat) => chat.id === adminLimits.selected);
      if (!known) adminLimits.selected = adminLimits.chats.length ? adminLimits.chats[0].id : null;
    } catch (error) {
      // chats stays null so the next explicit render (a tab switch or Refresh) tries again; the error
      // text is what the section shows meanwhile.
      adminLimits.chats = null;
      adminLimits.error = error.message || t("chatLimitsLoadFailed");
    }
    adminLimits.loading = false;
    // The section may have been re-rendered (a language switch, another load) while the request was in
    // flight: the response belongs to the section that is attached NOW, not to the detached body.
    const live = ui.content.querySelector(".chat-limits-body");
    if (!live) return;
    if (adminLimits.error) {
      live.replaceChildren();
      live.appendChild(element("p", "meta", adminLimits.error));
      return;
    }
    if (activeTab !== "settings") return;
    renderAdminLimitsBody(live);
  }

  function renderAdminLimitsBody(body) {
    body.replaceChildren();
    if (adminLimits.loading) {
      body.appendChild(element("p", "meta", t("loading")));
      return;
    }
    if (adminLimits.error) {
      body.appendChild(element("p", "meta", adminLimits.error));
      return;
    }
    if (!adminLimits.chats || !adminLimits.chats.length) {
      body.appendChild(element("p", "meta", t("chatLimitsEmpty")));
      return;
    }
    const selected = adminLimits.chats.find((chat) => chat.id === adminLimits.selected) || adminLimits.chats[0];
    adminLimits.selected = selected.id;

    const selectRow = element("div", "settings-row");
    const chatChoice = element("select");
    chatChoice.setAttribute("aria-label", t("chatLimitsChat"));
    for (const chat of adminLimits.chats) {
      const option = element("option", "", chatLimitChatLabel(chat));
      option.value = String(chat.id);
      option.selected = chat.id === selected.id;
      chatChoice.appendChild(option);
    }
    chatChoice.addEventListener("change", () => {
      adminLimits.selected = Number(chatChoice.value);
      renderAdminLimitsBody(body);
    });
    selectRow.appendChild(chatChoice);
    body.appendChild(selectRow);

    const today = selected.today || { model_calls: 0, read_url: 0, web_search: 0, calculator: 0 };
    body.appendChild(element("p", "meta", t("chatLimitsToday", {
      model: today.model_calls, network: today.read_url + today.web_search,
      read_url: today.read_url, web_search: today.web_search, calculator: today.calculator,
    })));

    const limits = selected.limits || { daily_model_limit: 0, daily_network_limit: 0, per_minute_limit: 0, daily_cost_limit_usd: 0, disabled_tools: [] };
    const inputs = {};
    const grid = element("div", "chat-limits-grid");
    for (const [key, label] of [
      ["daily_model_limit", t("chatLimitsModel")],
      ["daily_network_limit", t("chatLimitsNetwork")],
      ["per_minute_limit", t("chatLimitsMinute")],
    ]) {
      const field = element("label", "chat-limits-field");
      field.appendChild(element("span", "", label));
      const input = element("input");
      input.type = "number";
      input.min = "0";
      input.max = "1000000";
      input.step = "1";
      input.setAttribute("inputmode", "numeric");
      input.value = String(Number(limits[key]) || 0);
      field.appendChild(input);
      grid.appendChild(field);
      inputs[key] = input;
    }
    // Daily cost budget: a decimal USD amount, converted by the server into micro-dollars.
    const costField = element("label", "chat-limits-field");
    costField.appendChild(element("span", "", t("chatLimitsCost")));
    const costInput = element("input");
    costInput.type = "number";
    costInput.min = "0";
    costInput.max = "100000";
    costInput.step = "0.01";
    costInput.setAttribute("inputmode", "decimal");
    costInput.value = String(Number(limits.daily_cost_limit_usd) || 0);
    costField.appendChild(costInput);
    grid.appendChild(costField);
    inputs.daily_cost_limit_usd = costInput;
    body.appendChild(grid);

    const costLimit = Number(limits.daily_cost_limit_usd) || 0;
    body.appendChild(element("p", "meta", t("chatLimitsCostToday", {
      cost: formatUSD(today.cost_usd),
      limit: costLimit > 0 ? formatUSD(costLimit) : t("unlimited"),
    })));
    if (today.warning === "chat_cost_budget_warning") {
      body.appendChild(element("p", "meta chat-limits-warning", t("chatLimitsCostWarning")));
    }

    const disabled = Array.isArray(limits.disabled_tools) ? limits.disabled_tools : [];
    const toolsBox = element("fieldset", "chat-limits-tools");
    toolsBox.appendChild(element("legend", "", t("chatLimitsTools")));
    const toggles = {};
    for (const [name, label] of [
      ["read_url", t("chatLimitsToolReadURL")],
      ["web_search", t("chatLimitsToolWebSearch")],
      ["calculator", t("chatLimitsToolCalculator")],
    ]) {
      const option = element("label", "chat-limits-tool");
      const box = element("input");
      box.type = "checkbox";
      box.checked = disabled.includes(name);
      option.appendChild(box);
      option.appendChild(element("span", "", label));
      toolsBox.appendChild(option);
      toggles[name] = box;
    }
    body.appendChild(toolsBox);

    const actions = element("div", "settings-row");
    actions.appendChild(settingsButton(t("chatLimitsSave"), () => saveAdminChatLimits(selected.id, inputs, toggles)));
    actions.appendChild(settingsButton(t("chatLimitsReset"), () => resetAdminChatLimits(selected.id), true));
    body.appendChild(actions);
  }

  async function saveAdminChatLimits(chatID, inputs, toggles) {
    const payload = {};
    for (const [key, input] of Object.entries(inputs)) {
      const value = key === "daily_cost_limit_usd" ? chatLimitCostUSD(input) : chatLimitNumber(input);
      if (value === null) {
        showError(t(key === "daily_cost_limit_usd" ? "chatLimitsCostInvalid" : "chatLimitsInvalid"));
        return;
      }
      payload[key] = value;
    }
    payload.disabled_tools = Object.keys(toggles).filter((name) => toggles[name].checked);
    await mutateAdminChatLimits(chatID, "PUT", payload, "chatLimitsSaved");
  }

  async function resetAdminChatLimits(chatID) {
    await mutateAdminChatLimits(chatID, "DELETE", undefined, "chatLimitsResetDone");
  }

  async function mutateAdminChatLimits(chatID, method, payload, successKey) {
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
      await api(`/api/admin/chats/${chatID}/limits`, options);
      saved = true;
    } catch (error) {
      showError(error.message || t("chatLimitsFailed"));
    } finally {
      setBusy(false);
    }
    if (!saved) return;
    // Today's usage of the saved chat may have changed while the form was open: the next render fetches
    // the list again instead of showing the numbers from before the save.
    adminLimits.chats = null;
    adminLimits.selected = chatID;
    await load();
    showNotice(t(successKey));
  }

  // Admin-only "Providers and models" section. The server decides who is an admin (is_admin of
  // /api/settings) and re-checks every /api/admin/providers request; the section is only a convenience.
  // A provider is rendered as one card: enabled checkbox, models textarea, default select, Save/Reset. The
  // textarea is parsed on every input, so the default select always offers exactly the current lines and
  // the 20-model limit is visible before Save is pressed. No HTML string and no inline handler is used.
  function providerModelLines(textarea) {
    return String(textarea.value).split(/\r?\n/).map((line) => line.trim()).filter((line) => line !== "");
  }

  function providerModelsValid(models, defaultModel) {
    if (models.length < 1 || models.length > 20) return false;
    if (new Set(models).size !== models.length) return false;
    return models.includes(defaultModel);
  }

  function fillProviderDefaults(select, models, preferred) {
    const chosen = models.includes(preferred) ? preferred : (models.length ? models[0] : "");
    select.replaceChildren();
    for (const model of models) {
      const option = element("option", "", model);
      option.value = model;
      option.selected = model === chosen;
      select.appendChild(option);
    }
    select.value = chosen;
    return chosen;
  }

  function providerSourceLabel(source) {
    return t(source === "db" ? "providerSourceDB" : "providerSourceEnv");
  }

  function renderAdminProvidersBody(body) {
    body.replaceChildren();
    if (adminProviders.loading) {
      body.appendChild(element("p", "meta", t("loading")));
      return;
    }
    if (adminProviders.error) {
      body.appendChild(element("p", "meta", adminProviders.error));
      return;
    }
    if (!adminProviders.items || !adminProviders.items.length) {
      body.appendChild(element("p", "meta", t("providerSettingsEmpty")));
      return;
    }
    for (const provider of adminProviders.items) {
      const models = Array.isArray(provider.models) ? provider.models.slice() : [];
      const card = element("article", "provider-card");
      card.appendChild(element("h4", "", provider.name));
      const meta = [];
      if (provider.base_url) meta.push(t("providerBaseHost", { host: provider.base_url }));
      meta.push(t("providerSource", { source: providerSourceLabel(provider.source) }));
      card.appendChild(element("p", "meta", meta.join(" · ")));
      if (Array.isArray(provider.configured_models) && provider.configured_models.length) {
        card.appendChild(element("p", "meta", t("providerConfiguredModels", { models: provider.configured_models.join(", ") })));
      }

      const enabledLabel = element("label", "provider-enabled");
      const toggle = element("input");
      toggle.type = "checkbox";
      toggle.checked = Boolean(provider.enabled);
      toggle.setAttribute("aria-label", `${provider.name}: ${t("providerEnabled")}`);
      enabledLabel.appendChild(toggle);
      enabledLabel.appendChild(element("span", "", t("providerEnabled")));
      card.appendChild(enabledLabel);

      const modelsField = element("label", "provider-field");
      modelsField.appendChild(element("span", "", t("providerModelsLabel")));
      const textarea = element("textarea", "provider-models");
      textarea.value = models.join("\n");
      textarea.placeholder = t("providerModelsPlaceholder");
      textarea.setAttribute("aria-label", `${provider.name}: ${t("providerModelsLabel")}`);
      modelsField.appendChild(textarea);
      card.appendChild(modelsField);
      const count = element("p", "meta", t("providerModelsCount", { count: models.length }));
      card.appendChild(count);

      const defaultField = element("label", "provider-field");
      defaultField.appendChild(element("span", "", t("providerDefaultModel")));
      const defaultChoice = element("select");
      defaultChoice.setAttribute("aria-label", `${provider.name}: ${t("providerDefaultModel")}`);
      fillProviderDefaults(defaultChoice, models, provider.default_model);
      defaultField.appendChild(defaultChoice);
      card.appendChild(defaultField);

      textarea.addEventListener("input", () => {
        const current = providerModelLines(textarea);
        const chosen = fillProviderDefaults(defaultChoice, current, defaultChoice.value);
        const valid = providerModelsValid(current, chosen);
        count.textContent = t("providerModelsCount", { count: current.length });
        textarea.setAttribute("aria-invalid", String(!valid));
        defaultChoice.setAttribute("aria-invalid", String(!valid));
      });

      const actions = element("div", "settings-row");
      actions.appendChild(settingsButton(t("providerSettingsSave"),
        () => saveAdminProvider(provider.name, textarea, defaultChoice, toggle)));
      actions.appendChild(settingsButton(t("providerSettingsReset"),
        () => resetAdminProvider(provider.name), true));
      card.appendChild(actions);
      body.appendChild(card);
    }
  }

  async function refreshAdminProviders(body) {
    if (adminProviders.items !== null || adminProviders.loading) {
      renderAdminProvidersBody(body);
      return;
    }
    adminProviders.loading = true;
    try {
      const data = await api("/api/admin/providers");
      adminProviders.items = Array.isArray(data.items) ? data.items : [];
      adminProviders.error = "";
    } catch (error) {
      // items stays null so the next explicit render (a tab switch or Refresh) tries again.
      adminProviders.items = null;
      adminProviders.error = error.message || t("providerSettingsLoadFailed");
    }
    adminProviders.loading = false;
    const live = ui.content.querySelector(".provider-settings-body");
    if (!live) return;
    if (adminProviders.error) {
      live.replaceChildren();
      live.appendChild(element("p", "meta", adminProviders.error));
      return;
    }
    if (activeTab !== "settings") return;
    renderAdminProvidersBody(live);
  }

  async function saveAdminProvider(name, textarea, defaultChoice, toggle) {
    if (loading || !session) return;
    const models = providerModelLines(textarea);
    const defaultModel = defaultChoice.value;
    if (!providerModelsValid(models, defaultModel)) {
      textarea.setAttribute("aria-invalid", "true");
      defaultChoice.setAttribute("aria-invalid", "true");
      showError(t("providerSettingsInvalid"));
      return;
    }
    setBusy(true);
    showError("");
    let saved = false;
    try {
      await api(`/api/admin/providers/${encodeURIComponent(name)}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ models, default_model: defaultModel, enabled: toggle.checked }),
      });
      saved = true;
    } catch (error) {
      showError(error.message || t("providerSettingsFailed"));
    } finally {
      setBusy(false);
    }
    if (!saved) return;
    adminProviders.items = null;
    await load();
    showNotice(t("providerSettingsSaved"));
  }

  async function resetAdminProvider(name) {
    if (loading || !session) return;
    setBusy(true);
    showError("");
    let reset = false;
    try {
      await api(`/api/admin/providers/${encodeURIComponent(name)}`, { method: "DELETE" });
      reset = true;
    } catch (error) {
      showError(error.message || t("providerSettingsFailed"));
    } finally {
      setBusy(false);
    }
    if (!reset) return;
    adminProviders.items = null;
    await load();
    showNotice(t("providerSettingsResetDone"));
  }

  // changeTimezone applies the choice locally before the request, so the selector repaints immediately;
  // the server remains the source of truth and load() re-reads it after a successful PUT.
  async function changeTimezone(value, data) {
    if (loading || !session) return;
    const previousName = timezoneName;
    const previousSource = timezoneSource;
    const effective = value || data.timezone.default;
    const source = value ? "chat" : "global";
    setTimezoneState(effective, source);
    data.timezone.current = effective;
    data.timezone.source = source;
    renderSettings(data);
    setBusy(true);
    showError("");
    let saved = false;
    try {
      await api("/api/settings/timezone", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ timezone: value }),
      });
      saved = true;
    } catch (error) {
      showError(error.message || t("timezoneSaveFailed"));
    } finally {
      setBusy(false);
    }
    if (!saved) {
      setTimezoneState(previousName, previousSource);
      data.timezone.current = previousName;
      data.timezone.source = previousSource;
      renderSettings(data);
      return;
    }
    resetArmedStates();
    resetSearchState();
    await load();
    showNotice(t("timezoneSaved"));
  }

  function renderSettings(data) {
    ui.content.replaceChildren();
    if (!data.timezone || typeof data.timezone !== "object") {
      data.timezone = { current: timezoneName, default: timezoneDefault, source: timezoneSource };
    }
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

    const fallbackEnabled = Boolean(data.miniapp_fallback && data.miniapp_fallback.enabled);
    const fallback = settingsSection(t("miniAppTitle"), `${t("current")}: ${fallbackEnabled ? t("on") : t("off")} · ${source(data.miniapp_fallback ? data.miniapp_fallback.source : "global")}`);
    const fallbackToggle = element("button", "settings-toggle", fallbackEnabled ? t("on") : t("off"));
    fallbackToggle.type = "button";
    fallbackToggle.setAttribute("aria-pressed", String(fallbackEnabled));
    fallbackToggle.setAttribute("aria-label", t("miniAppFallbackAria"));
    fallbackToggle.addEventListener("click", () => {
      const next = fallbackToggle.getAttribute("aria-pressed") !== "true";
      fallbackToggle.setAttribute("aria-pressed", String(next));
      fallbackToggle.textContent = next ? t("on") : t("off");
    });
    const fallbackRow = element("div", "settings-row");
    fallbackRow.appendChild(fallbackToggle);
    fallbackRow.appendChild(settingsButton(t("saveMiniAppFallback"), () => changeSettings(
      "/api/settings/miniapp-fallback", "PUT", { enabled: fallbackToggle.getAttribute("aria-pressed") === "true" }, "miniappFallbackSaved")));
    fallbackRow.appendChild(settingsButton(t("resetMiniAppFallback"), () => changeSettings(
      "/api/settings/miniapp-fallback", "DELETE", undefined, "miniappFallbackReset"), true));
    fallback.appendChild(fallbackRow);
    ui.content.appendChild(fallback);

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

    const timezone = data.timezone;
    const timezoneSourceLabel = timezone.source === "chat" ? t("timezoneSourceChat") : t("timezoneSourceDefault");
    const timezoneSection = settingsSection(t("timezoneTitle"),
      `${t("current")}: ${timezone.current} · ${timezoneSourceLabel} · ${t("globalDefault")}: ${timezone.default}. ${t("timezoneHint")}`);
    const timezoneRow = element("div", "settings-row");
    const timezoneChoice = element("select", "timezone-select");
    timezoneChoice.id = "timezone";
    timezoneChoice.setAttribute("aria-label", t("timezoneAria"));
    const defaultOption = element("option", "", t("timezoneDefaultOption", { zone: timezone.default }));
    defaultOption.value = "";
    defaultOption.selected = timezone.source !== "chat";
    timezoneChoice.appendChild(defaultOption);
    const timezoneValues = timezoneChoices.slice();
    for (const name of [timezone.current, timezone.default]) {
      if (validTimezone(name) && !timezoneValues.includes(name)) timezoneValues.push(name);
    }
    for (const name of timezoneValues) {
      const option = element("option", "", name);
      option.value = name;
      option.selected = timezone.source === "chat" && name === timezone.current;
      timezoneChoice.appendChild(option);
    }
    timezoneChoice.addEventListener("change", () => { void changeTimezone(timezoneChoice.value, data); });
    timezoneRow.appendChild(timezoneChoice);
    timezoneSection.appendChild(timezoneRow);
    ui.content.appendChild(timezoneSection);

    if (data.is_admin) {
      const providers = element("section", "settings-section provider-settings");
      providers.appendChild(element("h3", "", t("providerSettingsTitle")));
      providers.appendChild(element("p", "meta", t("providerSettingsHint")));
      const providerBody = element("div", "provider-settings-body");
      providers.appendChild(providerBody);
      ui.content.appendChild(providers);
      void refreshAdminProviders(providerBody);

      const limits = element("section", "settings-section chat-limits");
      limits.appendChild(element("h3", "", t("chatLimitsTitle")));
      limits.appendChild(element("p", "meta", t("chatLimitsHint")));
      const body = element("div", "chat-limits-body");
      limits.appendChild(body);
      ui.content.appendChild(limits);
      void refreshAdminLimits(body);
    }

    // Theme is a local UI preference (localStorage "dsh-theme"), not a server setting: the selector
    // repaints <html data-theme> immediately and survives a reload on this device.
    const theme = settingsSection(t("themeTitle"), t("themeHint"));
    const themeRow = element("div", "settings-row");
    const themeChoice = element("select", "theme-select");
    themeChoice.id = "theme";
    themeChoice.setAttribute("aria-label", t("themeAria"));
    const themeLabels = [
      ["graphite", t("themeGraphite")],
      ["sand", t("themeSand")],
      ["ocean", t("themeOcean")],
      ["forest", t("themeForest")],
      ["lavender", t("themeLavender")],
    ];
    for (const [value, label] of themeLabels) {
      const option = element("option", "", label);
      option.value = value;
      option.selected = value === currentTheme;
      themeChoice.appendChild(option);
    }
    themeChoice.value = currentTheme;
    themeChoice.addEventListener("change", () => { setTheme(themeChoice.value); });
    themeRow.appendChild(themeChoice);
    theme.appendChild(themeRow);
    ui.content.appendChild(theme);

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

  const usageRangeValues = ["hour", "day", "week", "month"];
  const usageRangeKey = {
    hour: "usageRangeHour", day: "usageRangeDay", week: "usageRangeWeek", month: "usageRangeMonth",
  };
  const usageToolValues = ["read_url", "web_search", "calculator", "rag"];
  const usageToolKey = {
    read_url: "usageToolReadURL", web_search: "usageToolWebSearch",
    calculator: "usageToolCalculator", rag: "usageToolRAG",
  };
  const usageSVGNamespace = "http://www.w3.org/2000/svg";
  // The usage range is UI state, not server state: the next load of the tab asks for the same window.
  let usageRange = "day";

  function svgNode(tag, className) {
    const node = document.createElementNS(usageSVGNamespace, tag);
    if (className) node.setAttribute("class", className);
    return node;
  }

  function svgText(label, x, y, className, anchor) {
    const node = svgNode("text", className);
    node.setAttribute("x", String(x));
    node.setAttribute("y", String(y));
    if (anchor) node.setAttribute("text-anchor", anchor);
    node.textContent = label;
    return node;
  }

  function svgLine(x1, y1, x2, y2, className) {
    const node = svgNode("line", className);
    node.setAttribute("x1", String(x1));
    node.setAttribute("y1", String(y1));
    node.setAttribute("x2", String(x2));
    node.setAttribute("y2", String(y2));
    return node;
  }

  function svgRect(x, y, width, height, className) {
    const node = svgNode("rect", className);
    node.setAttribute("x", String(x));
    node.setAttribute("y", String(y));
    node.setAttribute("width", String(width));
    node.setAttribute("height", String(height));
    node.setAttribute("rx", "2");
    return node;
  }

  function svgPolyline(points, className) {
    const node = svgNode("polyline", className);
    node.setAttribute("points", points.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(" "));
    return node;
  }

  // A chart is an accessible image: role=img plus a text alternative, never an inline handler or a
  // canvas bitmap. The fixed viewBox scales the same drawing from a 320 px phone to a desktop panel.
  function usageChart(label, viewBox, parts) {
    const svg = svgNode("svg", "usage-chart");
    svg.setAttribute("viewBox", viewBox);
    svg.setAttribute("preserveAspectRatio", "xMidYMid meet");
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", label);
    svg.setAttribute("focusable", "false");
    for (const part of parts) svg.appendChild(part);
    return svg;
  }

  function compactUsageNumber(value) {
    const number = Number(value) || 0;
    if (number >= 1e9) return `${(number / 1e9).toFixed(1)}B`;
    if (number >= 1e6) return `${(number / 1e6).toFixed(1)}M`;
    if (number >= 1e4) return `${Math.round(number / 1e3)}k`;
    return String(number);
  }

  // USD is rendered with exactly two decimals in the UI; the API carries six so a tiny price still rounds
  // to a visible cent only when it really is one. Number(value) || 0 keeps a pre-cost payload at $0.00.
  function formatUSD(value, digits = 2) {
    return `$${(Number(value) || 0).toFixed(digits)}`;
  }

  function usageBucketLabel(start, range) {
    const parts = zonedParts(start);
    if (!parts) return "";
    const month = String(parts.month).padStart(2, "0");
    const day = String(parts.day).padStart(2, "0");
    const hour = String(parts.hour).padStart(2, "0");
    if (range === "hour") return `${hour}:00`;
    if (range === "month") return `${month}.${String(parts.year).slice(-2)}`;
    return `${day}.${month}`;
  }

  function usageAxisStep(range) {
    if (range === "hour") return 6;
    if (range === "month") return 2;
    return 1;
  }

  function usageChartAxis(parts, series, range, left, slot, height) {
    const step = usageAxisStep(range);
    series.forEach((bucket, index) => {
      if (index % step === 0) {
        parts.push(svgText(usageBucketLabel(bucket.start, range), left + slot * index + slot / 2, height - 7, "usage-chart-axis", "middle"));
      }
    });
    const last = series.length - 1;
    if (last >= 0 && last % step !== 0) {
      parts.push(svgText(usageBucketLabel(series[last].start, range), left + slot * last + slot / 2, height - 7, "usage-chart-axis", "middle"));
    }
  }

  function usageCallsChart(series, range) {
    const width = 320;
    const height = 138;
    const left = 30;
    const right = 8;
    const top = 10;
    const bottom = 26;
    const plotWidth = width - left - right;
    const plotHeight = height - top - bottom;
    const values = series.map((bucket) => Math.max(0, Number(bucket.model_calls) || 0));
    const maximum = Math.max(1, ...values);
    const parts = [];
    for (const fraction of [0, 0.5, 1]) {
      const y = top + plotHeight - plotHeight * fraction;
      parts.push(svgLine(left, y, width - right, y, "usage-chart-grid"));
      parts.push(svgText(compactUsageNumber(Math.round(maximum * fraction)), left - 4, y + 3, "usage-chart-axis", "end"));
    }
    const slot = plotWidth / Math.max(1, series.length);
    const barWidth = Math.max(2, slot * 0.62);
    series.forEach((bucket, index) => {
      const value = values[index];
      if (value > 0) {
        const barHeight = (value / maximum) * plotHeight;
        parts.push(svgRect(left + slot * index + (slot - barWidth) / 2, top + plotHeight - barHeight, barWidth, barHeight, "usage-chart-bar"));
      }
    });
    usageChartAxis(parts, series, range, left, slot, height);
    const total = values.reduce((sum, value) => sum + value, 0);
    return usageChart(t("usageCallsChartAria", { total, buckets: series.length }), `0 0 ${width} ${height}`, parts);
  }

  function usageTokensChart(series, range) {
    const width = 320;
    const height = 138;
    const left = 30;
    const right = 8;
    const top = 10;
    const bottom = 26;
    const plotWidth = width - left - right;
    const plotHeight = height - top - bottom;
    const valuesIn = series.map((bucket) => Math.max(0, Number(bucket.tokens_in) || 0));
    const valuesOut = series.map((bucket) => Math.max(0, Number(bucket.tokens_out) || 0));
    const maximum = Math.max(1, ...valuesIn, ...valuesOut);
    const parts = [];
    for (const fraction of [0, 0.5, 1]) {
      const y = top + plotHeight - plotHeight * fraction;
      parts.push(svgLine(left, y, width - right, y, "usage-chart-grid"));
      parts.push(svgText(compactUsageNumber(Math.round(maximum * fraction)), left - 4, y + 3, "usage-chart-axis", "end"));
    }
    const slot = plotWidth / Math.max(1, series.length);
    const points = (values) => values.map((value, index) => [
      left + slot * index + slot / 2,
      top + plotHeight - (value / maximum) * plotHeight,
    ]);
    parts.push(svgPolyline(points(valuesIn), "usage-chart-line-in"));
    parts.push(svgPolyline(points(valuesOut), "usage-chart-line-out"));
    usageChartAxis(parts, series, range, left, slot, height);
    const totalIn = valuesIn.reduce((sum, value) => sum + value, 0);
    const totalOut = valuesOut.reduce((sum, value) => sum + value, 0);
    return usageChart(t("usageTokensChartAria", { tokens_in: totalIn, tokens_out: totalOut, buckets: series.length }), `0 0 ${width} ${height}`, parts);
  }

  function usageCostChart(series, range) {
    const width = 320;
    const height = 138;
    const left = 42;
    const right = 8;
    const top = 10;
    const bottom = 26;
    const plotWidth = width - left - right;
    const plotHeight = height - top - bottom;
    const values = series.map((bucket) => Math.max(0, Number(bucket.cost_usd) || 0));
    const maximum = Math.max(0.01, ...values);
    const parts = [];
    for (const fraction of [0, 0.5, 1]) {
      const y = top + plotHeight - plotHeight * fraction;
      parts.push(svgLine(left, y, width - right, y, "usage-chart-grid"));
      parts.push(svgText(formatUSD(maximum * fraction), left - 4, y + 3, "usage-chart-axis", "end"));
    }
    const slot = plotWidth / Math.max(1, series.length);
    const barWidth = Math.max(2, slot * 0.62);
    series.forEach((bucket, index) => {
      const value = values[index];
      if (value > 0) {
        const barHeight = (value / maximum) * plotHeight;
        parts.push(svgRect(left + slot * index + (slot - barWidth) / 2, top + plotHeight - barHeight, barWidth, barHeight, "usage-chart-bar usage-chart-cost"));
      }
    });
    usageChartAxis(parts, series, range, left, slot, height);
    const total = values.reduce((sum, value) => sum + value, 0);
    return usageChart(t("usageCostChartAria", { cost: formatUSD(total), buckets: series.length }), `0 0 ${width} ${height}`, parts);
  }

  function usageToolsChart(tools) {
    const width = 320;
    const rowHeight = 26;
    const top = 2;
    const bottom = 2;
    const left = 88;
    const right = 34;
    const height = top + bottom + rowHeight * usageToolValues.length;
    const trackWidth = width - left - right;
    const values = usageToolValues.map((name) => Math.max(0, Number(tools[name]) || 0));
    const maximum = Math.max(1, ...values);
    const parts = [];
    usageToolValues.forEach((name, index) => {
      const y = top + index * rowHeight;
      const value = values[index];
      parts.push(svgText(t(usageToolKey[name]), left - 6, y + rowHeight / 2 + 3, "usage-chart-axis", "end"));
      parts.push(svgRect(left, y + 5, trackWidth, rowHeight - 10, "usage-chart-track"));
      if (value > 0) {
        parts.push(svgRect(left, y + 5, Math.max(2, (value / maximum) * trackWidth), rowHeight - 10, "usage-chart-tool"));
      }
      parts.push(svgText(String(value), width - 2, y + rowHeight / 2 + 3, "usage-chart-axis", "end"));
    });
    const total = values.reduce((sum, value) => sum + value, 0);
    return usageChart(t("usageToolsChartAria", { total }), `0 0 ${width} ${height}`, parts);
  }

  function usageRangeSwitch() {
    const group = element("div", "usage-range-switch");
    group.setAttribute("role", "group");
    group.setAttribute("aria-label", t("usageRangeLabel"));
    for (const value of usageRangeValues) {
      const active = value === usageRange;
      const button = element("button", active ? "usage-range-button active" : "usage-range-button", t(usageRangeKey[value]));
      button.type = "button";
      button.dataset.range = value;
      button.setAttribute("aria-pressed", String(active));
      button.addEventListener("click", () => selectUsageRange(value));
      group.appendChild(button);
    }
    return group;
  }

  async function selectUsageRange(value) {
    if (loading || !session || !usageRangeValues.includes(value) || value === usageRange) return;
    usageRange = value;
    await load();
  }

  function usageModelRows(section, models, emptyText) {
    if (!models.length) {
      section.appendChild(element("p", "empty", emptyText));
      return;
    }
    for (const model of models) {
      const row = element("div", "usage-row");
      row.appendChild(element("div", "content", model.model || t("modelUnspecified")));
      let meta = `${model.calls} ${t("modelCalls")} · ${model.tokens_in} ${t("tokensIn")} · ${model.tokens_out} ${t("tokensOut")}`;
      if (model.cost_usd !== undefined && model.cost_usd !== null) meta += ` · ${t("usageCost")}: ${formatUSD(model.cost_usd)}`;
      row.appendChild(element("div", "meta", meta));
      section.appendChild(row);
    }
  }

  function usageTokenLegend() {
    const legend = element("div", "usage-chart-legend");
    for (const [key, className] of [["usageTokensIn", "usage-legend-in"], ["usageTokensOut", "usage-legend-out"]]) {
      const item = element("span", "usage-legend-item");
      const swatch = element("span", `usage-legend-swatch ${className}`);
      swatch.setAttribute("aria-hidden", "true");
      item.appendChild(swatch);
      item.appendChild(element("span", "", t(key)));
      legend.appendChild(item);
    }
    return legend;
  }

  function renderUsage(data) {
    ui.content.replaceChildren();
    const series = Array.isArray(data.series) ? data.series : [];
    const totals = data.totals && typeof data.totals === "object" ? data.totals : {};
    const rangeTools = totals.tools && typeof totals.tools === "object" ? totals.tools : {};
    const range = usageRangeValues.includes(data.range) ? data.range : usageRange;
    if (usageRangeValues.includes(data.range)) usageRange = data.range;
    const sumSeries = (key) => series.reduce((sum, bucket) => sum + (Number(bucket[key]) || 0), 0);

    const usageDay = data.date ? formatDateValue(`${data.date}T00:00:00Z`, true) : "—";
    ui.content.appendChild(element("p", "meta", `${t("usageDate")}: ${usageDay}`));

    const rangeSection = settingsSection(t("usageRangeTitle"), t("usageRangeHint"));
    rangeSection.appendChild(usageRangeSwitch());
    if (data.truncated) rangeSection.appendChild(element("p", "meta", t("usageRangeTruncated")));
    ui.content.appendChild(rangeSection);

    const totalsGrid = element("div", "stats usage-totals");
    const totalsValues = [
      [t("modelCalls"), Number(totals.model_calls) || sumSeries("model_calls")],
      [t("tokensIn"), Number(totals.tokens_in) || sumSeries("tokens_in")],
      [t("tokensOut"), Number(totals.tokens_out) || sumSeries("tokens_out")],
      [t("usageCost"), formatUSD(Number(totals.cost_usd) || sumSeries("cost_usd"))],
      [t("usageToolsChart"), Number(totals.tool_calls) || sumSeries("tools_total")],
    ];
    for (const [label, value] of totalsValues) {
      const card = element("div", "stat");
      card.appendChild(element("strong", "", String(value)));
      card.appendChild(element("span", "", label));
      totalsGrid.appendChild(card);
    }
    ui.content.appendChild(totalsGrid);

    if (series.length) {
      const calls = settingsSection(t("usageCallsChart"), t("usageCallsChartHint"));
      calls.appendChild(usageCallsChart(series, range));
      ui.content.appendChild(calls);

      const tokens = settingsSection(t("usageTokensChart"), t("usageTokensChartHint"));
      tokens.appendChild(usageTokensChart(series, range));
      tokens.appendChild(usageTokenLegend());
      ui.content.appendChild(tokens);

      const cost = settingsSection(t("usageCostChart"), t("usageCostChartHint"));
      cost.appendChild(usageCostChart(series, range));
      ui.content.appendChild(cost);
    }

    const rangeModels = settingsSection(t("usageRangeModels"), "");
    usageModelRows(rangeModels, Array.isArray(totals.models) ? totals.models : [], t("noRangeUsage"));
    ui.content.appendChild(rangeModels);

    const toolsSection = settingsSection(t("usageRangeTools"), "");
    toolsSection.appendChild(usageToolsChart(rangeTools));
    for (const name of usageToolValues) {
      toolsSection.appendChild(element("div", "meta", `${t(usageToolKey[name])}: ${Number(rangeTools[name]) || 0}`));
    }
    ui.content.appendChild(toolsSection);

    const dailyModels = settingsSection(t("usageTodayModels"), "");
    usageModelRows(dailyModels, Array.isArray(data.models) ? data.models : [], t("noUsage"));
    ui.content.appendChild(dailyModels);

    const dailyTools = settingsSection(t("toolsTitle"), "");
    const todayTools = data.tools && typeof data.tools === "object" ? data.tools : {};
    for (const name of usageToolValues) {
      dailyTools.appendChild(element("div", "meta", `${t(usageToolKey[name])}: ${Number(todayTools[name]) || 0}`));
    }
    ui.content.appendChild(dailyTools);

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
    syncBusyState();
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
    if (assistant && message.stats && !String(message.text || "").includes("⏱")) {
      wrapper.appendChild(chatStatsNode(message.stats));
    }
    if (assistant && message.id) wrapper.appendChild(chatFeedbackControls(message));
    return { wrapper, body };
  }

  // chatStatsNode renders the same numbers as the Telegram latency footer for a history row, whose stored
  // content has no footer. Live answers already carry the formatter's footer, so chatMessageNode skips this.
  function chatStatsNode(stats) {
    const latency = Math.max(0, Number(stats.latencyMs) || 0);
    const input = Math.max(0, Number(stats.tokensIn) || 0);
    const output = Math.max(0, Number(stats.tokensOut) || 0);
    const firstToken = Math.max(0, Number(stats.firstTokenMs) || 0);
    const seconds = latency > 0 ? (latency / 1000).toFixed(latency < 10000 ? 1 : 0) : "0";
    const ttft = firstToken > 0 ? (firstToken / 1000).toFixed(firstToken < 10000 ? 1 : 0) : "";
    const rate = latency > 0 && output > 0 ? output / (latency / 1000) : 0;
    const rateText = rate > 0 ? t("chatRate", { rate: rate < 10 ? rate.toFixed(1) : String(Math.round(rate)) }) : "";
    const options = { seconds, input, output, rate: rateText, ttft, model: stats.model || "" };
    const text = rateText
      ? t(ttft ? "chatStatsTTFT" : "chatStats", options)
      : t(ttft ? "chatStatsNoRateTTFT" : "chatStatsNoRate", options);
    return element("div", "chat-stats", text);
  }

  // chatStatsFromPayload maps the done event's stats object. The footer of the same answer stays the
  // source of truth for live output (chatMessageNode skips a second block when the text has ⏱), so this
  // only feeds the fallback renderer used when a backend omits the footer.
  function chatStatsFromPayload(raw) {
    if (!raw || typeof raw !== "object") return null;
    const latencyMs = Number(raw.latency_ms) || 0;
    if (!latencyMs && raw.tokens_in == null && raw.tokens_out == null) return null;
    return {
      latencyMs,
      firstTokenMs: Number(raw.first_token_ms) || 0,
      tokensIn: Number(raw.tokens_in) || 0,
      tokensOut: Number(raw.tokens_out) || 0,
      model: typeof raw.model === "string" ? raw.model : "",
    };
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
    chat.streamChars = Array.from(answer.text || "").length;
    updateChatStatus();
    scrollChat();
  }

  function stopChatStatsTimer() {
    if (chatStatsTimer !== null) {
      clearInterval(chatStatsTimer);
      chatStatsTimer = null;
    }
  }

  function updateChatStatus() {
    if (!chatNodes.status || !chat.streaming) return;
    const seconds = chat.startedAt > 0 ? Math.max(0, (Date.now() - chat.startedAt) / 1000) : 0;
    const tokens = Math.round(chat.streamChars / 4);
    chatNodes.status.replaceChildren(element("span", "chat-pending", t("chatLiveStats", {
      stage: chatStageText(chat.stage),
      seconds: seconds.toFixed(seconds < 10 ? 1 : 0),
      tokens,
    })));
  }

  function startChatStatsTimer() {
    stopChatStatsTimer();
    if (!chat.streaming || typeof setInterval !== "function") return;
    chatStatsTimer = setInterval(updateChatStatus, 500);
  }

  function autoGrowChatInput(input) {
    if (!input.style) return;
    input.style.height = "auto";
    const max = 140;
    const height = input.scrollHeight || 44;
    input.style.height = `${Math.min(height, max)}px`;
    input.style.overflowY = height > max ? "auto" : "hidden";
  }

  function stopChatApprovalTimer() {
    if (chatApprovalTimer !== null) {
      clearTimeout(chatApprovalTimer);
      chatApprovalTimer = null;
    }
  }

  function chatApprovalToolLabel(tool) {
    if (tool === "read_url") return t("chatApprovalToolReadURL");
    if (tool === "web_search") return t("chatApprovalToolWebSearch");
    return tool;
  }

  function chatApprovalNotice(approval) {
    if (approval.state === "approved") return t("chatApprovalApproved");
    if (approval.state === "denied") return t("chatApprovalDenied");
    if (approval.state === "timeout" || approval.state === "expired") return t("chatApprovalExpired");
    if (approval.state === "failed") return t("chatApprovalFailed");
    const seconds = Math.max(0, Math.ceil((approval.deadline - Date.now()) / 1000));
    return t("chatApprovalExpires", { seconds });
  }

  function chatApprovalButton(label, handler) {
    // chat-send is the existing 44px target of the composer; reusing it keeps the approval buttons
    // thumb-sized without a stylesheet change (style.css is outside this change's file set).
    const button = settingsButton(label, handler, true);
    button.className = "chat-send";
    return button;
  }

  function chatApprovalCard() {
    const approval = chat.approval;
    const card = element("section", "inline-confirm chat-approval");
    card.setAttribute("role", "group");
    card.setAttribute("aria-label", t("chatApprovalTitle"));
    card.appendChild(element("h4", "chat-approval-title", t("chatApprovalTitle")));
    card.appendChild(element("p", "chat-approval-tool", chatApprovalToolLabel(approval.tool)));
    if (approval.summary) card.appendChild(element("code", "chat-approval-summary", approval.summary));
    const note = element("p", "chat-approval-note", chatApprovalNotice(approval));
    note.setAttribute("role", "status");
    card.appendChild(note);
    chatNodes.approvalNote = note;
    const actions = element("div", "task-actions inline-confirm-actions chat-approval-actions");
    const actionable = approval.state === "pending" || approval.state === "submitting";
    if (actionable) {
      const allow = chatApprovalButton(t("chatApprovalAllow"), () => decideChatApproval(true));
      const deny = chatApprovalButton(t("chatApprovalDeny"), () => decideChatApproval(false));
      const busy = approval.state === "submitting";
      allow.disabled = busy;
      deny.disabled = busy;
      actions.appendChild(allow);
      actions.appendChild(deny);
    }
    card.appendChild(actions);
    return card;
  }

  function scheduleChatApprovalTick() {
    stopChatApprovalTimer();
    const approval = chat.approval;
    if (!approval || approval.state !== "pending") return;
    chatApprovalTimer = setTimeout(() => {
      chatApprovalTimer = null;
      const current = chat.approval;
      if (!current || current.state !== "pending") return;
      const remaining = Math.max(0, Math.ceil((current.deadline - Date.now()) / 1000));
      if (remaining <= 0) {
        current.state = "timeout";
        if (activeTab === "chat") renderChat();
        return;
      }
      if (chatNodes.approvalNote) chatNodes.approvalNote.textContent = t("chatApprovalExpires", { seconds: remaining });
      scheduleChatApprovalTick();
    }, 1000);
  }

  async function decideChatApproval(approve) {
    const approval = chat.approval;
    if (!approval || approval.state !== "pending" || !session) return;
    approval.state = "submitting";
    if (activeTab === "chat") renderChat();
    try {
      await api("/api/chat/approval", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token: approval.token, approve }),
      });
      approval.state = approve ? "approved" : "denied";
    } catch (error) {
      approval.state = "failed";
      showError(error.message || t("chatApprovalFailed"));
    }
    if (activeTab === "chat") renderChat();
  }

  // Voice input: MediaRecorder -> POST /api/transcribe -> transcript in the composer. The server only
  // forwards the bytes to the same transcriber as a Telegram voice note and stores nothing; the client
  // never auto-sends, so the user can edit the inserted text first.
  const voiceMinBytes = 256;
  const voiceMimeCandidates = ["audio/webm;codecs=opus", "audio/ogg;codecs=opus", "audio/ogg", "audio/mp4"];

  function idleChatVoice() {
    return { state: "idle", recorder: null, stream: null, chunks: [], mime: "", startedAt: 0, timer: null };
  }

  function voiceSupported() {
    return typeof navigator === "object" && navigator !== null &&
      navigator.mediaDevices && typeof navigator.mediaDevices.getUserMedia === "function" &&
      typeof window.MediaRecorder === "function";
  }

  function voiceMimeType() {
    if (typeof window.MediaRecorder !== "function" || typeof window.MediaRecorder.isTypeSupported !== "function") return "";
    for (const mime of voiceMimeCandidates) {
      try {
        if (window.MediaRecorder.isTypeSupported(mime)) return mime;
      } catch (_) {
        // The next candidate is still worth asking about.
      }
    }
    return "";
  }

  function voiceExtension(mime) {
    if (mime.indexOf("ogg") >= 0) return "ogg";
    if (mime.indexOf("mp4") >= 0 || mime.indexOf("m4a") >= 0) return "mp4";
    return "webm";
  }

  function stopVoiceTracks(stream) {
    if (!stream || typeof stream.getTracks !== "function") return;
    for (const track of stream.getTracks()) {
      try { track.stop(); } catch (_) { /* the browser already released it */ }
    }
  }

  function scheduleChatVoiceTick() {
    const voice = chatVoice;
    if (voice.timer !== null) clearTimeout(voice.timer);
    voice.timer = null;
    if (voice.state !== "recording") return;
    voice.timer = setTimeout(() => {
      voice.timer = null;
      if (chatVoice !== voice || voice.state !== "recording") return;
      if (chatNodes.voiceElapsed) {
        const seconds = Math.max(0, Math.floor((Date.now() - voice.startedAt) / 1000));
        chatNodes.voiceElapsed.textContent = t("chatVoiceElapsed", { seconds });
      }
      scheduleChatVoiceTick();
    }, 1000);
  }

  async function startVoiceRecording() {
    if (!session || chat.streaming || chatVoice.state !== "idle") return;
    if (!voiceSupported() || !voiceMimeType()) {
      showError(t("voiceUnsupported"));
      return;
    }
    let stream = null;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    } catch (_) {
      showError(t("voicePermissionDenied"));
      return;
    }
    if (!stream) {
      showError(t("voicePermissionDenied"));
      return;
    }
    let recorder = null;
    try {
      recorder = new window.MediaRecorder(stream, { mimeType: voiceMimeType() });
    } catch (_) {
      stopVoiceTracks(stream);
      showError(t("voiceUnsupported"));
      return;
    }
    const voice = {
      state: "recording", recorder, stream, chunks: [], mime: voiceMimeType(), startedAt: Date.now(), timer: null,
    };
    chatVoice = voice;
    recorder.addEventListener("dataavailable", (event) => {
      if (event && event.data && event.data.size) voice.chunks.push(event.data);
    });
    recorder.addEventListener("stop", () => finishVoiceStop(voice));
    recorder.addEventListener("error", () => {
      if (chatVoice !== voice || voice.state !== "recording") return;
      cancelVoiceRecording();
      showError(t("voiceFailed"));
    });
    try {
      recorder.start();
    } catch (_) {
      resetChatVoice();
      if (activeTab === "chat") renderChat();
      showError(t("voiceFailed"));
      return;
    }
    showError("");
    if (activeTab === "chat") renderChat();
    scheduleChatVoiceTick();
  }

  function stopVoiceRecording() {
    const voice = chatVoice;
    if (!voice || voice.state !== "recording" || !voice.recorder) return;
    try {
      if (voice.recorder.state === "inactive") {
        finishVoiceStop(voice);
        return;
      }
      voice.recorder.stop();
    } catch (_) {
      finishVoiceStop(voice);
    }
  }

  function finishVoiceStop(voice) {
    if (chatVoice !== voice || voice.state !== "recording") return;
    voice.state = "uploading";
    if (voice.timer !== null) {
      clearTimeout(voice.timer);
      voice.timer = null;
    }
    const chunks = voice.chunks.slice();
    const mime = voice.mime;
    const stream = voice.stream;
    voice.recorder = null;
    voice.stream = null;
    stopVoiceTracks(stream);
    if (activeTab === "chat") renderChat();
    uploadVoiceRecording(chunks, mime);
  }

  function cancelVoiceRecording() {
    const voice = chatVoice;
    if (!voice || voice.state !== "recording") return;
    chatVoice = idleChatVoice();
    if (voice.timer !== null) {
      clearTimeout(voice.timer);
      voice.timer = null;
    }
    // Replace the state first: the stop event of the recorder then sees a different object and never uploads.
    if (voice.recorder && voice.recorder.state !== "inactive") {
      try { voice.recorder.stop(); } catch (_) { /* already stopped */ }
    }
    stopVoiceTracks(voice.stream);
    if (activeTab === "chat") renderChat();
  }

  function resetChatVoice() {
    const voice = chatVoice;
    if (voice && voice.timer !== null) clearTimeout(voice.timer);
    if (voice) stopVoiceTracks(voice.stream);
    chatVoice = idleChatVoice();
  }

  function mergeChatTranscript(existing, transcript) {
    const current = String(existing || "").replace(/\s+$/, "");
    if (!current) return transcript;
    return current + " " + transcript;
  }

  function setChatInputValue(input, value) {
    const text = String(value || "");
    const runes = Array.from(text);
    input.value = runes.length > 2000 ? runes.slice(0, 2000).join("") : text;
    input.setAttribute("aria-invalid", "false");
    const counter = document.getElementById("chat-counter");
    if (counter) counter.textContent = `${Array.from(input.value).length} / 2000`;
    autoGrowChatInput(input);
  }

  async function uploadVoiceRecording(chunks, mime) {
    if (!session) {
      resetChatVoice();
      if (activeTab === "chat") renderChat();
      return;
    }
    const blob = new Blob(chunks, { type: mime });
    if (blob.size < voiceMinBytes) {
      resetChatVoice();
      if (activeTab === "chat") renderChat();
      showError(t("voiceTooShort"));
      return;
    }
    const form = new FormData();
    form.append("file", blob, `voice.${voiceExtension(mime)}`);
    let transcript = "";
    try {
      const data = await api("/api/transcribe", { method: "POST", body: form });
      if (!data || typeof data.text !== "string") throw new Error(t("invalidResponse"));
      transcript = data.text.trim();
      if (!transcript) throw new Error(t("voiceTooShort"));
    } catch (error) {
      const message = error && error.message === t("networkError")
        ? t("voiceNetworkFailed")
        : (error && error.message) || t("voiceFailed");
      showError(message);
    } finally {
      resetChatVoice();
      if (activeTab === "chat") renderChat();
    }
    if (!transcript) return;
    const input = document.getElementById("chat-input");
    if (!input) return;
    setChatInputValue(input, mergeChatTranscript(input.value, transcript));
    input.focus();
    showNotice(t("voiceTranscriptReady"));
  }

  function renderChat() {
    ui.content.replaceChildren();
    cancelChatLiveRender();
    stopChatApprovalTimer();
    stopChatStatsTimer();
    const section = element("section", "chat-panel");
    section.appendChild(element("h3", "chat-title", t("chatTitle")));
    section.appendChild(element("p", "meta chat-intro", t("chatIntro")));
    if (chat.identity) section.appendChild(element("p", "meta chat-identity", t("chatIdentity", { user: chat.identity.user_id, chat: chat.identity.chat_id })));

    const log = element("div", "chat-log");
    log.setAttribute("role", "log");
    log.setAttribute("aria-live", "polite");
    log.setAttribute("aria-label", t("chatTitle"));
    chatNodes = { log, answer: null, status: null, approvalNote: null, voiceElapsed: null };
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

    if (chat.approval) log.appendChild(chatApprovalCard());

    const status = element("div", "chat-status");
    status.setAttribute("role", "status");
    if (chat.streaming) status.appendChild(element("span", "chat-pending", chatStageText(chat.stage)));
    else if (chat.pending) status.appendChild(element("span", "badge", t("awaitingAnswer")));
    if (chat.errorText) status.appendChild(element("p", "chat-error", chat.errorText));
    chatNodes.status = status;
    log.appendChild(status);
    if (chat.streaming) startChatStatsTimer();
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
    input.disabled = chat.streaming || chatVoice.state !== "idle";
    composer.appendChild(input);
    // The mic sits next to Send: a 44px target (style.css) with an accessible label, and aria-pressed
    // reflects the recording state that the voice bar below shows in seconds.
    const mic = element("button", "chat-mic");
    mic.type = "button";
    mic.setAttribute("aria-label", t("chatVoiceStart"));
    mic.setAttribute("aria-pressed", String(chatVoice.state === "recording"));
    mic.appendChild(element("span", "chat-mic-icon", "🎤"));
    mic.disabled = chat.streaming || chatVoice.state !== "idle";
    mic.addEventListener("click", startVoiceRecording);
    composer.appendChild(mic);
    const send = submitButton(t("chatSend"));
    send.className = "chat-send";
    send.disabled = chat.streaming || chatVoice.state !== "idle";
    composer.appendChild(send);
    form.appendChild(composer);
    if (chatVoice.state === "recording" || chatVoice.state === "uploading") {
      const bar = element("div", "chat-voice");
      bar.setAttribute("role", "group");
      bar.setAttribute("aria-label", t("chatVoiceRecording"));
      if (chatVoice.state === "recording") {
        bar.appendChild(element("span", "chat-voice-dot"));
        const elapsed = element("span", "chat-voice-time", t("chatVoiceElapsed", { seconds: 0 }));
        elapsed.setAttribute("role", "status");
        bar.appendChild(elapsed);
        chatNodes.voiceElapsed = elapsed;
        const stop = settingsButton(t("chatVoiceStop"), stopVoiceRecording);
        stop.className = "chat-voice-stop";
        bar.appendChild(stop);
        const cancel = settingsButton(t("chatVoiceCancel"), cancelVoiceRecording, true);
        cancel.className = "chat-voice-cancel";
        bar.appendChild(cancel);
      } else {
        bar.appendChild(element("span", "chat-voice-uploading", t("chatVoiceUploading")));
      }
      form.appendChild(bar);
    }
    const counter = element("div", "counter chat-counter", "0 / 2000");
    counter.id = "chat-counter";
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
    input.disabled = chat.streaming || chatVoice.state !== "idle";
    send.disabled = chat.streaming || chatVoice.state !== "idle";
    mic.disabled = chat.streaming || chatVoice.state !== "idle";
    if (chatVoice.state === "recording") scheduleChatVoiceTick();
    autoGrowChatInput(input);
    scrollChat(true);
    scheduleChatApprovalTick();
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
    stopChatApprovalTimer();
    stopChatStatsTimer();
    chat.streaming = false;
    chat.stage = "";
    chat.pending = pending;
    chat.errorText = message;
    if (chat.approval && chat.approval.state === "pending") chat.approval.state = "timeout";
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
            } else if (event.name === "approval") {
              const seconds = Number(event.data.expires_in_seconds);
              chat.approval = {
                token: typeof event.data.token === "string" ? event.data.token : "",
                tool: typeof event.data.tool === "string" ? event.data.tool : "",
                summary: typeof event.data.summary === "string" ? event.data.summary : "",
                deadline: Date.now() + (Number.isFinite(seconds) && seconds > 0 ? seconds : 120) * 1000,
                state: "pending",
              };
              chat.stage = "";
              if (activeTab === "chat") renderChat();
            } else if (event.name === "done") {
              if (typeof event.data.text === "string") answer.text = event.data.text;
              // The done stats duplicate the footer of a live answer by design: chatMessageNode keeps
              // the footer as the single block whenever the text already carries ⏱.
              const stats = chatStatsFromPayload(event.data.stats);
              if (stats) answer.stats = stats;
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
    stopChatStatsTimer();
    if (activeTab === "chat") renderChat();
  }

  async function sendChat(value, control) {
    if (chat.streaming || !session || chatVoice.state !== "idle") return;
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
    chat.approval = null;
    chat.startedAt = Date.now();
    chat.streamChars = 0;
    stopChatApprovalTimer();
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
    chat.approval = null;
    stopChatApprovalTimer();
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
        applyTimezoneFromServer(view.data.timezone);
        renderStatus(view.data);
        nextBefore = null;
      } else if (activeTab === "settings") {
        view = { tab: activeTab, data: await api("/api/settings"), items: [] };
        applyTimezoneFromServer(view.data.timezone);
        renderSettings(view.data);
        nextBefore = null;
      } else if (activeTab === "usage") {
        view = { tab: activeTab, data: await api(`/api/usage?range=${usageRange}`), items: [] };
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
          applyTimezoneFromServer(status.timezone);
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
              stats: item.role === "assistant" && (item.tokens_in != null || item.latency_ms != null)
                ? {
                  latencyMs: Number(item.latency_ms) || 0,
                  firstTokenMs: Number(item.first_token_ms) || 0,
                  tokensIn: Number(item.tokens_in) || 0,
                  tokensOut: Number(item.tokens_out) || 0,
                  model: typeof item.model === "string" ? item.model : "",
                }
                : null,
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
      if (activeTab === "tasks") scheduleTaskPoll();
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
    stopTaskPoll();
    // A recording is tied to the visible composer: leaving the Chat tab releases the microphone.
    cancelVoiceRecording();
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
