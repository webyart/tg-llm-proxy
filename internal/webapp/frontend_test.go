package webapp

import (
	"regexp"
	"strings"
	"testing"
)

func frontendAsset(t *testing.T, name string) string {
	t.Helper()
	content, err := staticFiles.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestFrontendDefaultsToRussianAndHasLanguageSwitch(t *testing.T) {
	index := frontendAsset(t, "static/index.html")
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`lang="ru"`, `id="language"`, `value="ru"`, `value="en"`, `data-i18n="closeSession"`,
		`data-i18n="tabHistory"`, `data-i18n="tabDocuments"`, `data-i18n="tabLinks"`,
		`data-i18n="tabTasks"`, `data-i18n="tabSettings"`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("index misses %q", want)
		}
	}
	for _, want := range []string{
		`"История"`, `"Документы"`, `"Ссылки"`, `"Задачи"`, `"Настройки"`,
		`"Закрыть сессию"`, `"Обновить"`, `"Применить модель"`, `"Сбросить к общей"`,
		`"Применить пресет"`, `"Сбросить роль"`, `"Сохранить стриминг"`,
		`"Сессия истекла. Откройте Mini App заново из Telegram."`,
		`localStorage.getItem("miniapp-language")`, `localStorage.setItem("miniapp-language", language)`,
		`initialLanguage()`, `applyTranslations()`, `translations.ru`, `en: {`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses %q", want)
		}
	}
	for _, unsafe := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("} {
		if strings.Contains(script, unsafe) {
			t.Errorf("app.js uses unsafe HTML rendering %q", unsafe)
		}
	}
}

func TestEveryStaticTranslationKeyHasCatalogEntry(t *testing.T) {
	index := frontendAsset(t, "static/index.html")
	script := frontendAsset(t, "static/app.js")
	keys := regexp.MustCompile(`data-i18n(?:-aria)?="([a-zA-Z]+)"`).FindAllStringSubmatch(index, -1)
	if len(keys) < 15 {
		t.Fatalf("only %d static translation keys found", len(keys))
	}
	for _, match := range keys {
		if !strings.Contains(script, match[1]+":") {
			t.Errorf("translation key %s has no catalog entry", match[1])
		}
	}
}

func TestFrontendMobileRulesWrapAndCapWidths(t *testing.T) {
	css := frontendAsset(t, "static/style.css")
	for _, want := range []string{
		"flex-wrap: wrap", "overflow-x: visible", "min-width: 0", "max-width: 100%",
		"overflow-wrap: anywhere", "word-break: break-word", "@media (max-width: 420px)",
		"nav button { flex: 1 1 calc(50% - 5px); }",
		".settings-row select, .settings-row button { flex: 1 1 100%; width: 100%; }",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("responsive CSS misses %q", want)
		}
	}
	for _, rejected := range []string{"overflow-x: auto", "white-space: nowrap"} {
		if strings.Contains(css, rejected) {
			t.Errorf("responsive CSS retains horizontal overflow rule %q", rejected)
		}
	}
	fixedWidth := regexp.MustCompile(`(?:^|[;{])\s*width:\s*[0-9]{3,}px`)
	if fixedWidth.MatchString(css) {
		t.Error("responsive CSS contains a fixed pixel width that can overflow mobile viewports")
	}
}

func TestFrontendShowsDegradedModelsAndRecoverySuppression(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`data.warning === "models_unavailable"`, `useModel.disabled = modelsUnavailable`,
		`data.recovery_suppressed`, `recoverySuppressed: "Recovery suppressed"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses %q", want)
		}
	}
}

func TestFrontendTaskControlsAndUsageAreScopedToAllowedActions(t *testing.T) {
	index := frontendAsset(t, "static/index.html")
	script := frontendAsset(t, "static/app.js")
	if !strings.Contains(index, `data-tab="usage"`) || !strings.Contains(index, `data-i18n="tabUsage"`) {
		t.Fatal("Usage tab is missing")
	}
	for _, want := range []string{
		`item.expires_at`, `item.error`, `changeTask("/api/tasks/cancel")`,
		"index === 0", "changeTask(`/api/tasks/${item.id}/resume`)", `renderUsage(view.data)`,
		`api("/api/usage")`, `networkApprovalTelegram`, `data.limits.model`, `data.limits.network_tools`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses %q", want)
		}
	}
	if strings.Contains(script, "/approve") {
		t.Fatal("Mini App exposes a network approval action")
	}
}

func TestFrontendTaskClearRequiresInlineConfirmation(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`clearTasks: "Очистить задачи"`, `clearTasksConfirm: "Подтвердить"`,
		`clearTasks: "Clear tasks"`, `clearTasksConfirm: "Confirm"`,
		`clearTasksWarning: "Удалить все задачи этого чата, включая активные и завершённые? Это действие нельзя отменить."`,
		`clearTasksWarning: "Delete every task in this chat, including active and completed tasks? This cannot be undone."`,
		`settingsButton(t("clearTasks"), () => setTaskClearArmed(true, items))`,
		`settingsButton(t("clearTasksConfirm"), () => changeTask("/api/tasks/clear"))`,
		`settingsButton(t("cancelTask"), () => setTaskClearArmed(false, items), true)`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses task clear regression %q", want)
		}
	}
	if got := strings.Count(script, `changeTask("/api/tasks/clear")`); got != 1 {
		t.Errorf("task clear exact endpoint call count = %d, want 1", got)
	}

	armBody := regexp.MustCompile(`(?s)function setTaskClearArmed\(armed, items\) \{(.*?)\n  \}`).FindStringSubmatch(script)
	if len(armBody) != 2 {
		t.Fatal("task clear armed-state helper is missing")
	}
	for _, rejected := range []string{`changeTask(`, `api(`, `fetch(`} {
		if strings.Contains(armBody[1], rejected) {
			t.Errorf("showing or cancelling task clear confirmation unexpectedly calls %q", rejected)
		}
	}
	for _, rejected := range []string{`window.confirm`, `confirm(`, `innerHTML`} {
		if strings.Contains(script, rejected) {
			t.Errorf("task clear uses forbidden browser or HTML API %q", rejected)
		}
	}
}

func TestFrontendLinkDeleteAndClearRequireInlineConfirmation(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`deleteLink: "Удалить"`, `deleteLink: "Delete"`,
		`clearLinks: "Очистить ссылки"`, `clearLinks: "Clear links"`,
		`deleteLinkWarning: "Удалить эту сохранённую ссылку? Это действие нельзя отменить."`,
		`deleteLinkWarning: "Delete this saved link? This cannot be undone."`,
		`clearLinksWarning: "Удалить все сохранённые ссылки этого чата? Это действие нельзя отменить."`,
		`clearLinksWarning: "Delete every saved link in this chat? This cannot be undone."`,
		`settingsButton(t("deleteLink"), () => setLinkDeleteArmed(item.id, items), true)`,
		`() => changeLink(` + "`/api/links/${item.id}/delete`" + `)`,
		`() => setLinkDeleteArmed(null, items)`,
		`settingsButton(t("clearLinks"), () => setLinkClearArmed(true, items))`,
		`settingsButton(t("clearLinksConfirm"), () => changeLink("/api/links/clear"))`,
		`settingsButton(t("cancelTask"), () => setLinkClearArmed(false, items), true)`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses link deletion regression %q", want)
		}
	}
	for _, endpoint := range []string{`changeLink("/api/links/clear")`, "changeLink(`/api/links/${item.id}/delete`)"} {
		if got := strings.Count(script, endpoint); got != 1 {
			t.Errorf("link exact endpoint call count for %q = %d, want 1", endpoint, got)
		}
	}
	for _, functionName := range []string{"setLinkClearArmed", "setLinkDeleteArmed"} {
		body := regexp.MustCompile(`(?s)function ` + functionName + `\([^\n]*\) \{(.*?)\n  \}`).FindStringSubmatch(script)
		if len(body) != 2 {
			t.Fatalf("link armed-state helper %s is missing", functionName)
		}
		for _, rejected := range []string{`changeLink(`, `api(`, `fetch(`} {
			if strings.Contains(body[1], rejected) {
				t.Errorf("showing or cancelling link confirmation unexpectedly calls %q", rejected)
			}
		}
	}
}

func TestFrontendDocumentDeleteAndClearRequireInlineConfirmation(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`deleteDocument: "Удалить"`, `deleteDocument: "Delete"`,
		`deleteDocumentConfirm: "Подтвердить"`, `deleteDocumentConfirm: "Confirm"`,
		`clearDocuments: "Очистить документы"`, `clearDocuments: "Clear documents"`,
		`clearDocumentsConfirm: "Подтвердить"`, `clearDocumentsConfirm: "Confirm"`,
		`deleteDocumentWarning: "Удалить этот документ и его фрагменты? Это действие нельзя отменить."`,
		`deleteDocumentWarning: "Delete this document and its chunks? This cannot be undone."`,
		`clearDocumentsWarning: "Удалить все документы этого чата и их фрагменты? Это действие нельзя отменить."`,
		`clearDocumentsWarning: "Delete every document in this chat and its chunks? This cannot be undone."`,
		`settingsButton(t("deleteDocument"), () => setDocumentDeleteArmed(item.id, items), true)`,
		`() => changeDocument(` + "`/api/documents/${item.id}/delete`" + `)`,
		`() => setDocumentDeleteArmed(null, items)`,
		`settingsButton(t("clearDocuments"), () => setDocumentClearArmed(true, items))`,
		`settingsButton(t("clearDocumentsConfirm"), () => changeDocument("/api/documents/clear"))`,
		`settingsButton(t("cancelTask"), () => setDocumentClearArmed(false, items), true)`,
		`document_mutation_failed: "documentMutationFailed"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses document deletion regression %q", want)
		}
	}
	for _, endpoint := range []string{`changeDocument("/api/documents/clear")`, "changeDocument(`/api/documents/${item.id}/delete`)"} {
		if got := strings.Count(script, endpoint); got != 1 {
			t.Errorf("document exact endpoint call count for %q = %d, want 1", endpoint, got)
		}
	}
	for _, functionName := range []string{"setDocumentClearArmed", "setDocumentDeleteArmed"} {
		body := regexp.MustCompile(`(?s)function ` + functionName + `\([^\n]*\) \{(.*?)\n  \}`).FindStringSubmatch(script)
		if len(body) != 2 {
			t.Fatalf("document armed-state helper %s is missing", functionName)
		}
		for _, rejected := range []string{`changeDocument(`, `api(`, `fetch(`} {
			if strings.Contains(body[1], rejected) {
				t.Errorf("showing or cancelling document confirmation unexpectedly calls %q", rejected)
			}
		}
	}
	clearBlock := regexp.MustCompile(`(?s)if \(!items\.length\) \{(.*?)\n    \}\n    if \(tab === "documents"\) \{(.*?)\n    \}`).FindStringSubmatch(script)
	if len(clearBlock) != 3 || !strings.Contains(clearBlock[1], "return;") || !strings.Contains(clearBlock[2], `t("clearDocuments")`) {
		t.Error("Clear documents must be rendered only for a non-empty document list")
	}
	for _, rejected := range []string{`window.confirm`, `confirm(`, `innerHTML`} {
		if strings.Contains(script, rejected) {
			t.Errorf("document deletion uses forbidden browser or HTML API %q", rejected)
		}
	}
}

func TestFrontendOnlyTerminalTasksHaveDeleteAndKeepResume(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`deleteTask: "Удалить"`, `deleteTask: "Delete"`,
		`deleteTaskWarning: "Удалить эту задачу из истории? Это действие нельзя отменить."`,
		`deleteTaskWarning: "Delete this task from history? This cannot be undone."`,
		`} else if (["completed", "failed", "cancelled"].includes(item.status)) {`,
		`settingsButton(t("deleteTask"), () => setTaskDeleteArmed(item.id, items), true)`,
		`() => changeTask(` + "`/api/tasks/${item.id}/delete`" + `)`,
		`() => setTaskDeleteArmed(null, items)`,
		`settingsButton(t("resumeTask"), () => changeTask(` + "`/api/tasks/${item.id}/resume`" + `))`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses terminal task deletion regression %q", want)
		}
	}
	if got := strings.Count(script, "changeTask(`/api/tasks/${item.id}/delete`)"); got != 1 {
		t.Errorf("task delete exact endpoint call count = %d, want 1", got)
	}

	activeBody := regexp.MustCompile(`(?s)if \(\["ready", "running", "awaiting_approval"\]\.includes\(item\.status\)\) \{(.*?)\} else if`).FindStringSubmatch(script)
	if len(activeBody) != 2 {
		t.Fatal("active task action branch is missing")
	}
	if !strings.Contains(activeBody[1], `changeTask("/api/tasks/cancel")`) {
		t.Error("active task lost its Cancel action")
	}
	for _, rejected := range []string{"deleteTask", "resumeTask", "/delete"} {
		if strings.Contains(activeBody[1], rejected) {
			t.Errorf("active task unexpectedly exposes %q", rejected)
		}
	}

	armBody := regexp.MustCompile(`(?s)function setTaskDeleteArmed\([^\n]*\) \{(.*?)\n  \}`).FindStringSubmatch(script)
	if len(armBody) != 2 {
		t.Fatal("task delete armed-state helper is missing")
	}
	for _, rejected := range []string{`changeTask(`, `api(`, `fetch(`} {
		if strings.Contains(armBody[1], rejected) {
			t.Errorf("showing or cancelling task delete confirmation unexpectedly calls %q", rejected)
		}
	}
}

func TestFrontendMutationGuardsAndConfirmationResets(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, functionName := range []string{"changeDocument", "changeLink", "changeTask", "changeReminder"} {
		body := regexp.MustCompile(`(?s)async function ` + functionName + `\(path\) \{(.*?)\n  \}`).FindStringSubmatch(script)
		if len(body) != 2 {
			t.Fatalf("mutation helper %s is missing", functionName)
		}
		for _, want := range []string{
			`if (loading || !session) return;`,
			`setBusy(true);`,
			`await api(path, { method: "POST" });`,
			`setBusy(false);`,
			"if (changed) {\n      resetArmedStates();\n      resetSearchState();",
		} {
			if !strings.Contains(body[1], want) {
				t.Errorf("%s misses mutation guard %q", functionName, want)
			}
		}
		if strings.Contains(body[1], "body:") || strings.Contains(body[1], "?") {
			t.Errorf("%s does not use a strictly empty POST body without query parameters", functionName)
		}
	}
	for _, functionName := range []string{"setDocumentClearArmed", "setLinkClearArmed", "setTaskClearArmed", "setDocumentDeleteArmed", "setLinkDeleteArmed", "setTaskDeleteArmed", "setReminderDeleteArmed"} {
		body := regexp.MustCompile(`(?s)function ` + functionName + `\([^\n]*\) \{(.*?)\n  \}`).FindStringSubmatch(script)
		if len(body) != 2 || !strings.Contains(body[1], "if (loading) return;") {
			t.Errorf("%s does not block stale confirmations while loading", functionName)
		}
	}
	for _, want := range []string{
		"if (!button || loading) return;\n    resetArmedStates();\n    resetSearchState();",
		`ui.refresh.addEventListener("click", () => { resetArmedStates(); resetSearchState(); nextBefore = null; load(); });`,
		"setBusy(true);\n    resetArmedStates();\n    resetSearchState();\n    if (![\"status\", \"settings\", \"usage\", \"reminders\"].includes(activeTab)) renderItems(activeTab, view.items);",
		`ui.more.addEventListener("click", () => { resetArmedStates(); resetSearchState(); load(true); });`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses transient-state reset regression %q", want)
		}
	}
	resetBody := regexp.MustCompile(`(?s)function resetArmedStates\(\) \{(.*?)\n  \}`).FindStringSubmatch(script)
	if len(resetBody) != 2 {
		t.Fatal("shared confirmation reset helper is missing")
	}
	for _, variable := range []string{"clearDocumentsArmed", "clearLinksArmed", "clearTasksArmed", "deleteDocumentArmed", "deleteLinkArmed", "deleteTaskArmed", "deleteReminderArmed"} {
		if !strings.Contains(resetBody[1], variable) {
			t.Errorf("confirmation reset misses %s", variable)
		}
	}
}

func TestFrontendSuccessNoticesAreSeparateFromErrors(t *testing.T) {
	index := frontendAsset(t, "static/index.html")
	script := frontendAsset(t, "static/app.js")
	css := frontendAsset(t, "static/style.css")
	for _, want := range []string{
		`<div id="error" role="alert" hidden></div>`,
		`<div id="notice" role="status" hidden></div>`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("index misses notice regression %q", want)
		}
	}
	for _, key := range []string{"settingsSaved", "documentUpdated", "linkUpdated", "taskUpdated", "reminderUpdated"} {
		if want := `showNotice(t("` + key + `"))`; !strings.Contains(script, want) {
			t.Errorf("app.js misses success notice %q", want)
		}
		if rejected := `showError(t("` + key + `"))`; strings.Contains(script, rejected) {
			t.Errorf("app.js still shows success as an error %q", rejected)
		}
	}
	for _, test := range []struct {
		function string
		other    string
	}{
		{"showError", "notice"},
		{"showNotice", "error"},
	} {
		body := regexp.MustCompile(`(?s)function ` + test.function + `\(message\) \{(.*?)\n  \}`).FindStringSubmatch(script)
		if len(body) != 2 {
			t.Fatalf("%s helper is missing", test.function)
		}
		for _, want := range []string{`ui.` + test.other + `.textContent = "";`, `ui.` + test.other + `.hidden = true;`} {
			if !strings.Contains(body[1], want) {
				t.Errorf("%s does not reset the other notice: %q", test.function, want)
			}
		}
	}
	for _, want := range []string{
		`notice: document.getElementById("notice")`,
		`link_mutation_failed: "linkMutationFailed"`,
		`#error, #notice`,
		`#notice { color: var(--tg-theme-text-color, #17202a); background: var(--tg-theme-secondary-bg-color, white);`,
	} {
		asset := script
		if strings.HasPrefix(want, "#") {
			asset = css
		}
		if !strings.Contains(asset, want) {
			t.Errorf("frontend misses notice regression %q", want)
		}
	}
}

func TestFrontendDocumentSearchUsesBoundedReadOnlyResults(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`searchDocuments: "Найти"`, `searchDocuments: "Search"`,
		`documentSearchAria: "Поиск по документам"`, `documentSearchAria: "Search documents"`,
		`documentSearchTruncated: "Поиск охватил только часть фрагментов документов; более старые фрагменты не проверялись."`,
		`documentSearchTruncated: "Search covered only part of the document chunks; older chunks were not checked."`,
		`input.type = "search"`, `input.maxLength = 4000`,
		"api(`/api/documents/search?q=${encodeURIComponent(normalized)}`)",
		`.slice(0, 3)`, `documentSearch.items.slice(0, 3)`,
		`itemShell(item.document_id)`, `item.name`, `item.score`, `item.content`,
		`data.corpus && data.corpus.truncated`, `t("documentSearchTruncated")`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses document search regression %q", want)
		}
	}
	if got := strings.Count(script, "/api/documents/search"); got != 1 {
		t.Errorf("document search endpoint count = %d, want 1", got)
	}
	body := regexp.MustCompile(`(?s)async function searchDocuments\(query\) \{(.*?)\n  \}`).FindStringSubmatch(script)
	if len(body) != 2 {
		t.Fatal("document search helper is missing")
	}
	for _, want := range []string{`if (loading || !session) return;`, `setBusy(true);`, `resetArmedStates();`, `setBusy(false);`} {
		if !strings.Contains(body[1], want) {
			t.Errorf("document search misses %q", want)
		}
	}
	for _, rejected := range []string{`method: "POST"`, `/api/settings/model`, `/api/tasks`, `/api/reminders`, `innerHTML`} {
		if strings.Contains(body[1], rejected) {
			t.Errorf("document search unexpectedly contains %q", rejected)
		}
	}
}

func TestFrontendRemindersAreScopedToActiveAndFailedActions(t *testing.T) {
	index := frontendAsset(t, "static/index.html")
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{`data-tab="reminders"`, `data-i18n="tabReminders"`} {
		if !strings.Contains(index, want) {
			t.Errorf("index misses reminders regression %q", want)
		}
	}
	for _, want := range []string{
		`tabReminders: "Напоминания"`, `tabReminders: "Reminders"`,
		`activeReminders: "Активные напоминания"`, `activeReminders: "Active reminders"`,
		`failedReminders: "Недоставленные напоминания"`, `failedReminders: "Failed reminders"`,
		`retryReminder: "Повторить"`, `retryReminder: "Retry"`,
		`deleteReminderWarning: "Удалить это недоставленное напоминание? Это действие нельзя отменить."`,
		`deleteReminderWarning: "Delete this failed reminder? This cannot be undone."`,
		`api("/api/reminders")`, `data.active || []`, `data.failed || []`,
		`item.text`, `item.cadence`, `item.next_run`, `item.attempts`,
		`settingsButton(t("cancelTask"), () => setReminderCancelArmed(item.id, view.data), true)`,
		`settingsButton(t("retryReminder"), () => changeReminder(` + "`/api/reminders/${item.id}/retry`" + `))`,
		`settingsButton(t("deleteReminder"), () => setReminderDeleteArmed(item.id, view.data), true)`,
		`() => changeReminder(` + "`/api/reminders/${item.id}/delete`" + `)`,
		`() => setReminderDeleteArmed(null, view.data)`,
		`() => changeReminder(` + "`/api/reminders/${item.id}/cancel`" + `)`,
		`() => setReminderCancelArmed(null, view.data)`,
		`reminder_mutation_failed: "reminderMutationFailed"`,
		`renderReminderCreation()`, `cancelReminderWarning`, `setReminderCancelArmed(null, view.data)`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses reminders regression %q", want)
		}
	}
	for _, action := range []string{"cancel", "retry", "delete"} {
		endpoint := "changeReminder(`/api/reminders/${item.id}/" + action + "`)"
		if got := strings.Count(script, endpoint); got != 1 {
			t.Errorf("reminder %s endpoint count = %d, want 1", action, got)
		}
	}
	for _, want := range []string{`function createReminder(`, `const cadence = cadenceControl.value === "daily" ? "daily" : "once";`, `payload.daily_time = dailyTime`, `payload.next_run = nextRun.toISOString()`, `createResource("/api/reminders", payload`} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses reminder creation %q", want)
		}
	}
	armBody := regexp.MustCompile(`(?s)function setReminderDeleteArmed\([^\n]*\) \{(.*?)\n  \}`).FindStringSubmatch(script)
	if len(armBody) != 2 {
		t.Fatal("reminder delete armed-state helper is missing")
	}
	for _, rejected := range []string{`changeReminder(`, `api(`, `fetch(`} {
		if strings.Contains(armBody[1], rejected) {
			t.Errorf("showing or cancelling reminder confirmation unexpectedly calls %q", rejected)
		}
	}
	failedActions := regexp.MustCompile(`(?s)if \(failed\) \{(.*?)\n      \} else if \(cancelReminderArmed`).FindStringSubmatch(script)
	if len(failedActions) != 2 {
		t.Fatal("reminder failed action branch is missing")
	}
	for _, want := range []string{"/retry", "/delete", "deleteReminderWarning"} {
		if !strings.Contains(failedActions[1], want) {
			t.Errorf("failed reminders miss %q", want)
		}
	}
	if strings.Contains(failedActions[1], "/cancel") {
		t.Error("failed reminders unexpectedly expose active-only Cancel")
	}
	if !strings.Contains(script, `} else if (cancelReminderArmed === item.id) {`) {
		t.Error("reminder active Cancel confirmation is missing")
	}
}

func TestFrontendBusyControlsAreDisabledAndRestored(t *testing.T) {
	index := frontendAsset(t, "static/index.html")
	script := frontendAsset(t, "static/app.js")
	css := frontendAsset(t, "static/style.css")
	for _, want := range []string{
		`<div id="loading" class="loading" role="status" data-i18n="loading" hidden>Загрузка…</div>`,
		`<div id="content" aria-live="polite" aria-busy="false"></div>`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("index misses busy regression %q", want)
		}
	}
	if !strings.Contains(css, ".loading {") {
		t.Error("visible loading indicator has no styling")
	}
	for _, want := range []string{`loading: "Загрузка…"`, `loading: "Loading…"`} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses busy translation %q", want)
		}
	}
	body := regexp.MustCompile(`(?s)function syncBusyState\(\) \{(.*?)\n  \}`).FindStringSubmatch(script)
	if len(body) != 2 {
		t.Fatal("shared busy-state helper is missing")
	}
	for _, want := range []string{
		`ui.loading.hidden = !loading;`, `ui.content.setAttribute("aria-busy", String(loading));`,
		`document.querySelectorAll("button, input, select, textarea")`,
		`control.dataset.wasDisabled = String(control.disabled);`, `control.disabled = true;`,
		`control.disabled = control.dataset.wasDisabled === "true";`,
		`delete control.dataset.busyDisabled;`, `delete control.dataset.wasDisabled;`,
	} {
		if !strings.Contains(body[1], want) {
			t.Errorf("busy-state helper misses %q", want)
		}
	}
	for _, functionName := range []string{"searchDocuments", "load"} {
		body := regexp.MustCompile(`(?s)async function ` + functionName + `\([^\n]*\) \{(.*?)\n  \}`).FindStringSubmatch(script)
		if len(body) != 2 || !strings.Contains(body[1], "finally {") || !strings.Contains(body[1], "setBusy(false);") {
			t.Errorf("%s does not always clear busy state", functionName)
		}
	}
	for _, rejected := range []string{`ui.refresh.disabled = false`, `ui.more.disabled = false`} {
		if strings.Contains(script, rejected) {
			t.Errorf("busy state bypasses disabled-state restoration with %q", rejected)
		}
	}
}

func TestFrontendLiteralTranslationAndErrorKeysHaveBothCatalogs(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	catalogs := regexp.MustCompile(`(?s)\n    ru: \{(.*?)\n    \},\n    en: \{(.*?)\n    \},\n  \};`).FindStringSubmatch(script)
	if len(catalogs) != 3 {
		t.Fatal("RU/EN translation catalogs were not found")
	}
	keys := map[string]bool{}
	for _, match := range regexp.MustCompile(`\bt\("([a-zA-Z][a-zA-Z0-9_]*)"`).FindAllStringSubmatch(script, -1) {
		keys[match[1]] = true
	}
	errorMap := regexp.MustCompile(`(?s)const errorKeys = \{(.*?)\n  \};`).FindStringSubmatch(script)
	if len(errorMap) != 2 {
		t.Fatal("frontend error key map was not found")
	}
	for _, match := range regexp.MustCompile(`:\s*"([a-zA-Z][a-zA-Z0-9_]*)"`).FindAllStringSubmatch(errorMap[1], -1) {
		keys[match[1]] = true
	}
	if len(keys) < 50 {
		t.Fatalf("only %d literal translation and error keys found", len(keys))
	}
	for index, language := range []string{"ru", "en"} {
		for key := range keys {
			if !strings.Contains(catalogs[index+1], key+":") {
				t.Errorf("%s catalog misses literal t() or error key %q", language, key)
			}
		}
	}
}

func TestFrontendHistorySearchAndFullContent(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`searchHistory: "Найти"`, `searchHistory: "Search"`,
		`historySearchAria: "Поиск по истории"`, `historySearchAria: "Search history"`,
		`historySearchEmpty: "По запросу ничего не найдено."`, `historySearchEmpty: "No messages matched the query."`,
		`historySearchTruncated: "Поиск охватил только последние 10000 сообщений; более старые сообщения не проверялись."`,
		`historySearchTruncated: "Search covered only the latest 10000 messages; older messages were not checked."`,
		`input.type = "search"`, `input.maxLength = 4000`, `Array.from(normalized).length > 4000`,
		"api(`/api/history/search?q=${encodeURIComponent(normalized)}`)",
		`.slice(0, 20)`, `historySearch.items.slice(0, 20)`,
		`.filter((item) => item.role === "user" || item.role === "assistant")`,
		`data.truncated || (data.corpus && data.corpus.truncated)`,
		`!["user", "assistant"].includes(full.role)`,
		`showFullContent: "Показать полностью"`, `showFullContent: "Show full text"`,
		`if (item.truncated)`, `loadFullContent("history", item.id)`,
		"api(`/api/${tab}/item?id=${encodeURIComponent(id)}`)",
		`const full = data.item || data`, `update(historySearch.items)`, `item.truncated = false`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses history regression %q", want)
		}
	}
	if got := strings.Count(script, "/api/history/search"); got != 1 {
		t.Errorf("history search endpoint count = %d, want 1", got)
	}
	for _, functionName := range []string{"searchHistory", "loadFullContent"} {
		body := regexp.MustCompile(`(?s)async function ` + functionName + `\([^\n]*\) \{(.*?)\n  \}`).FindStringSubmatch(script)
		if len(body) != 2 {
			t.Fatalf("%s helper is missing", functionName)
		}
		for _, want := range []string{`if (loading || !session`, `setBusy(true);`, `finally {`, `setBusy(false);`} {
			if !strings.Contains(body[1], want) {
				t.Errorf("%s misses loading/double-click guard %q", functionName, want)
			}
		}
	}
	for _, rejected := range []string{"/api/tasks/item", "/api/tasks/search", "/api/tasks/export", "item.plan", "item.result"} {
		if strings.Contains(script, rejected) {
			t.Errorf("Mini App unexpectedly exposes task plans/results through %q", rejected)
		}
	}
}

func TestFrontendHistoryExportUsesAuthenticatedBlobDownload(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`historyExportMD: "Скачать Markdown"`, `historyExportMD: "Download Markdown"`,
		`historyExportJSON: "Скачать JSON"`, `historyExportJSON: "Download JSON"`,
		`historyExportReady: "Экспорт истории подготовлен."`, `historyExportReady: "History export is ready."`,
		`historyExportEmpty: "Нет сообщений для экспорта."`, `historyExportEmpty: "There are no messages to export."`,
		`history_empty: "historyExportEmpty"`, `exportHistory("md")`, `exportHistory("json")`,
		"fetch(miniAppBase + `/api/history/export?format=${format}`",
		`new Headers({ "Authorization": ` + "`Bearer ${session}`" + ` })`,
		`const blob = await response.blob()`, `blob.size > 5 * 1024 * 1024`,
		`URL.createObjectURL(blob)`, "anchor.download = `history.${format}`", `anchor.click()`,
		`URL.revokeObjectURL(objectURL)`, `showNotice(t("historyExportReady"))`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses history export regression %q", want)
		}
	}
	if got := strings.Count(script, "/api/history/export"); got != 1 {
		t.Errorf("history export endpoint count = %d, want 1", got)
	}
	body := regexp.MustCompile(`(?s)async function exportHistory\(format\) \{(.*?)\n  \}`).FindStringSubmatch(script)
	if len(body) != 2 {
		t.Fatal("history export helper is missing")
	}
	for _, want := range []string{`if (loading || !session) return;`, `setBusy(true);`, `showError("");`, `setBusy(false);`} {
		if !strings.Contains(body[1], want) {
			t.Errorf("history export misses busy/error handling %q", want)
		}
	}
	errorAt := strings.Index(body[1], `if (!response.ok)`)
	blobAt := strings.Index(body[1], `await response.blob()`)
	createAt := strings.Index(body[1], `URL.createObjectURL(blob)`)
	if errorAt < 0 || blobAt < 0 || createAt < 0 || !(errorAt < blobAt && blobAt < createAt) {
		t.Error("history export must reject HTTP errors before creating a Blob download")
	}
	revoke := regexp.MustCompile(`(?s)finally \{(.*?)\n      \}`).FindStringSubmatch(body[1])
	if len(revoke) != 2 || !strings.Contains(revoke[1], `URL.revokeObjectURL(objectURL)`) || !strings.Contains(revoke[1], `anchor.remove()`) {
		t.Error("history export must remove the temporary anchor and revoke its object URL in finally")
	}
}

func TestFrontendLinkFullContentIsLimitedToTruncatedItems(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`if (item.truncated) actions.appendChild(settingsButton(t("showFullContent"), () => loadFullContent("links", item.id)))`,
		`!["history", "links"].includes(tab)`, `typeof full.content !== "string"`,
		`fullContentFailed: "Не удалось загрузить полный текст."`, `fullContentFailed: "The full text could not be loaded."`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses link full-content regression %q", want)
		}
	}
	if strings.Contains(script, `loadFullContent("documents"`) || strings.Contains(script, `loadFullContent("tasks"`) {
		t.Error("full-content reads must be limited to history and links")
	}
}

func TestFrontendTTSSettingsAreConditionalAndLocalized(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`if (data.tts && data.tts.enabled)`,
		`ttsTitle: "Озвучивание (TTS)"`, `ttsTitle: "Speech synthesis (TTS)"`,
		`ttsSpeedAria: "Скорость озвучивания"`, `ttsSpeedAria: "Speech synthesis speed"`,
		`["0.75", "1", "1.25", "1.5"]`, `String(data.tts.current) === value`,
		`changeSettings("/api/settings/tts-speed", "PUT", { speed: speedChoice.value }, "ttsSpeedSaved")`,
		`changeSettings("/api/settings/tts-speed", "DELETE", undefined, "ttsSpeedReset")`,
		`ttsSpeedSaved: "Скорость озвучивания сохранена."`, `ttsSpeedSaved: "Speech synthesis speed saved."`,
		`ttsSpeedReset: "Скорость озвучивания сброшена."`, `ttsSpeedReset: "Speech synthesis speed reset."`,
		`invalid_tts_speed: "errorInvalidTTSSpeed"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses TTS settings regression %q", want)
		}
	}
	if got := strings.Count(script, `"/api/settings/tts-speed"`); got != 2 {
		t.Errorf("TTS speed endpoint count = %d, want PUT and DELETE only", got)
	}
	body := regexp.MustCompile(`(?s)async function changeSettings\([^\n]*\) \{(.*?)\n  \}`).FindStringSubmatch(script)
	if len(body) != 2 {
		t.Fatal("settings mutation helper is missing")
	}
	for _, want := range []string{`if (loading || !session) return;`, `setBusy(true);`, `setBusy(false);`, `showNotice(t(successKey))`} {
		if !strings.Contains(body[1], want) {
			t.Errorf("TTS settings mutation misses %q", want)
		}
	}
}

func TestFrontendRateLimitedErrorsAreLocalized(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`rate_limited: "errorRateLimited"`,
		`errorRateLimited: "Слишком много запросов. Повторите попытку позже."`,
		`errorRateLimited: "Too many requests. Try again later."`,
		`status === 429 ? "errorRateLimited"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses localized 429 regression %q", want)
		}
	}
}

func TestFrontendUsesInjectedBaseForAPIAndExport(t *testing.T) {
	script := frontendAsset(t, "static/app.js")
	for _, want := range []string{
		`document.querySelector('meta[name="miniapp-base"]')`,
		`baseMeta.getAttribute("content")`,
		`fetch(miniAppBase + path`,
		"fetch(miniAppBase + `/api/history/export?format=${format}`",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses forwarded base regression %q", want)
		}
	}
}
