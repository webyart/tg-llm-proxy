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
	for _, functionName := range []string{"changeLink", "changeTask"} {
		body := regexp.MustCompile(`(?s)async function ` + functionName + `\(path\) \{(.*?)\n  \}`).FindStringSubmatch(script)
		if len(body) != 2 {
			t.Fatalf("mutation helper %s is missing", functionName)
		}
		for _, want := range []string{
			`if (loading || !session) return;`,
			`await api(path, { method: "POST" });`,
			"if (changed) {\n      resetArmedStates();",
		} {
			if !strings.Contains(body[1], want) {
				t.Errorf("%s misses mutation guard %q", functionName, want)
			}
		}
		if strings.Contains(body[1], "body:") || strings.Contains(body[1], "?") {
			t.Errorf("%s does not use a strictly empty POST body without query parameters", functionName)
		}
	}
	for _, functionName := range []string{"setLinkClearArmed", "setTaskClearArmed", "setLinkDeleteArmed", "setTaskDeleteArmed"} {
		body := regexp.MustCompile(`(?s)function ` + functionName + `\([^\n]*\) \{(.*?)\n  \}`).FindStringSubmatch(script)
		if len(body) != 2 || !strings.Contains(body[1], "if (loading) return;") {
			t.Errorf("%s does not block stale confirmations while loading", functionName)
		}
	}
	for _, want := range []string{
		"if (!button || loading) return;\n    resetArmedStates();",
		`ui.refresh.addEventListener("click", () => { resetArmedStates(); nextBefore = null; load(); });`,
		"loading = true;\n    resetArmedStates();\n    if (![\"status\", \"settings\", \"usage\"].includes(activeTab)) renderItems(activeTab, view.items);",
		`ui.more.addEventListener("click", () => load(true));`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js misses confirmation reset regression %q", want)
		}
	}
	resetBody := regexp.MustCompile(`(?s)function resetArmedStates\(\) \{(.*?)\n  \}`).FindStringSubmatch(script)
	if len(resetBody) != 2 {
		t.Fatal("shared confirmation reset helper is missing")
	}
	for _, variable := range []string{"clearLinksArmed", "clearTasksArmed", "deleteLinkArmed", "deleteTaskArmed"} {
		if !strings.Contains(resetBody[1], variable) {
			t.Errorf("confirmation reset misses %s", variable)
		}
	}
}
