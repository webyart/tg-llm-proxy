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
		"if (changed) {\n      clearTasksArmed = false;",
		"if (!button || loading) return;\n    clearTasksArmed = false;",
		`ui.refresh.addEventListener("click", () => { clearTasksArmed = false; nextBefore = null; load(); });`,
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
