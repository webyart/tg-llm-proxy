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
