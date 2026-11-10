package bot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tgmodels "github.com/go-telegram/bot/models"

	"bot/internal/i18n"
	"bot/internal/store"
)

// newLongTailDispatcher builds a dispatcher for the long-tail command tests: one allowlisted user,
// a fake model and a fresh store.
func newLongTailDispatcher(t *testing.T, overrides map[string]string) (*Dispatcher, *fakeReplier) {
	t.Helper()
	cfg := testConfig(t, merge(map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}, overrides))
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, nil, r, testStore(t))
	return d, r
}

// overrideLanguage writes the chat_language row directly: these tests exercise the rendering of an
// already-resolved language, not the /language command itself.
func overrideLanguage(t *testing.T, d *Dispatcher, chatID int64, language string) {
	t.Helper()
	if err := store.EnsureChat(context.Background(), d.deps.DB, chatID, 11, ""); err != nil {
		t.Fatalf("EnsureChat(%d): %v", chatID, err)
	}
	if err := store.SetChatLanguage(context.Background(), d.deps.DB, chatID, language); err != nil {
		t.Fatalf("SetChatLanguage(%d, %s): %v", chatID, language, err)
	}
}

func TestDocsRendersInChatLanguage(t *testing.T) {
	d, r := newLongTailDispatcher(t, nil)

	if err := dispatch(t, d, testEvent(100, 11, 7001, "/docs")); err != nil {
		t.Fatalf("RU /docs: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Локальная база документов пуста. Отправь текстовый файл") {
		t.Fatalf("RU /docs = %q", got)
	}
	if err := dispatch(t, d, testEvent(100, 11, 7003, "/docs menu")); err != nil {
		t.Fatalf("RU /docs menu: %v", err)
	}
	if got := flattenButtons(r.menus[len(r.menus)-1])["menu:root"]; got != "← Назад" {
		t.Fatalf("RU /docs back button = %q", got)
	}

	overrideLanguage(t, d, 200, "en")
	if err := dispatch(t, d, testEvent(200, 11, 7002, "/docs")); err != nil {
		t.Fatalf("EN /docs: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "The local document base is empty. Send a text file") {
		t.Fatalf("EN /docs = %q", got)
	}
	if err := dispatch(t, d, testEvent(200, 11, 7004, "/docs menu")); err != nil {
		t.Fatalf("EN /docs menu: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Documents of this chat:"+"\n"+"The local base is empty.") {
		t.Fatalf("EN /docs menu = %q", got)
	}
	if got := flattenButtons(r.menus[len(r.menus)-1])["menu:root"]; got != "← Back" {
		t.Fatalf("EN /docs back button = %q", got)
	}
}

func TestLinksRendersInChatLanguage(t *testing.T) {
	d, r := newLongTailDispatcher(t, nil)

	if err := dispatch(t, d, testEvent(100, 11, 7011, "/links")); err != nil {
		t.Fatalf("RU /links: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Ссылки текущего чата: показано 0") || !strings.Contains(got, "Ссылок нет") {
		t.Fatalf("RU /links = %q", got)
	}

	overrideLanguage(t, d, 200, "en")
	if err := dispatch(t, d, testEvent(200, 11, 7012, "/links")); err != nil {
		t.Fatalf("EN /links: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Links of this chat: showing 0") || !strings.Contains(got, "No links yet") {
		t.Fatalf("EN /links = %q", got)
	}
	menu := r.menus[len(r.menus)-1].InlineKeyboard
	if menu[0][0].Text != "Search" || menu[0][1].Text != "Refresh" {
		t.Fatalf("EN /links buttons = %q, %q", menu[0][0].Text, menu[0][1].Text)
	}
	if menu[0][0].CallbackData != "menu:links:search" || menu[1][0].CallbackData != "menu:docs" {
		t.Fatalf("EN /links callbacks changed: %q, %q", menu[0][0].CallbackData, menu[1][0].CallbackData)
	}
}

func TestRemindRendersInChatLanguage(t *testing.T) {
	d, r := newLongTailDispatcher(t, nil)

	if err := dispatch(t, d, testEvent(100, 11, 7021, "/remind через 1 минуту купить молоко")); err != nil {
		t.Fatalf("RU /remind: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Напоминание #") || !strings.Contains(got, "сохранено") {
		t.Fatalf("RU /remind = %q", got)
	}
	if err := dispatch(t, d, testEvent(100, 11, 7022, "/reminders")); err != nil {
		t.Fatalf("RU /reminders: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Активные напоминания") {
		t.Fatalf("RU /reminders = %q", got)
	}

	overrideLanguage(t, d, 200, "en")
	// The reminder parser keeps its historical Russian grammar; the response follows the chat language.
	if err := dispatch(t, d, testEvent(200, 11, 7023, "/remind через 1 минуту buy milk")); err != nil {
		t.Fatalf("EN /remind: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Reminder #") || !strings.Contains(got, "saved") {
		t.Fatalf("EN /remind = %q", got)
	}
	if err := dispatch(t, d, testEvent(200, 11, 7024, "/reminders")); err != nil {
		t.Fatalf("EN /reminders: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Active reminders") {
		t.Fatalf("EN /reminders = %q", got)
	}
}

func TestTaskRendersInChatLanguage(t *testing.T) {
	d, r := newLongTailDispatcher(t, nil)

	if err := dispatch(t, d, testEvent(100, 11, 7031, "/task")); err != nil {
		t.Fatalf("RU /task: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Активной или завершённой задачи нет.") {
		t.Fatalf("RU /task = %q", got)
	}
	if got := r.menus[len(r.menus)-1].InlineKeyboard[0][0].Text; got != "Обновить" {
		t.Fatalf("RU /task refresh = %q", got)
	}

	overrideLanguage(t, d, 200, "en")
	if err := dispatch(t, d, testEvent(200, 11, 7032, "/task")); err != nil {
		t.Fatalf("EN /task: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "There is no active or completed task.") {
		t.Fatalf("EN /task = %q", got)
	}
	if got := r.menus[len(r.menus)-1].InlineKeyboard[0][0].Text; got != "Refresh" {
		t.Fatalf("EN /task refresh = %q", got)
	}
}

func TestTTSRendersInChatLanguage(t *testing.T) {
	d, r := newLongTailDispatcher(t, map[string]string{"TTS_ENABLED": "true"})

	if err := dispatch(t, d, testEvent(100, 11, 7041, "/tts speed")); err != nil {
		t.Fatalf("RU /tts speed: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Текущая скорость озвучивания для этого чата: 1×.") {
		t.Fatalf("RU /tts speed = %q", got)
	}

	overrideLanguage(t, d, 200, "en")
	if err := dispatch(t, d, testEvent(200, 11, 7042, "/tts speed")); err != nil {
		t.Fatalf("EN /tts speed: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Current speech speed for this chat: 1×.") {
		t.Fatalf("EN /tts speed = %q", got)
	}

	if err := dispatch(t, d, testEvent(100, 11, 7043, "/tts")); err != nil {
		t.Fatalf("RU /tts prompt: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Отправь текст для озвучивания") {
		t.Fatalf("RU /tts prompt = %q", got)
	}
	if err := dispatch(t, d, testEvent(200, 11, 7044, "/tts")); err != nil {
		t.Fatalf("EN /tts prompt: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Send the text to read aloud") {
		t.Fatalf("EN /tts prompt = %q", got)
	}
}

func TestAdminRendersInChatLanguage(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11,12",
		"TELEGRAM_ADMIN_USERS":   "11",
	})
	cfg.DBPath = filepath.Join(t.TempDir(), "bot.db")
	if err := os.WriteFile(cfg.DBPath, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write stat fixture: %v", err)
	}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, nil, r, testStore(t))

	if err := dispatch(t, d, testEvent(100, 11, 7051, "/admin")); err != nil {
		t.Fatalf("RU /admin: %v", err)
	}
	joined := r.joined()
	if !strings.Contains(joined, "Администрирование:") || !strings.Contains(joined, "Чатов:") {
		t.Fatalf("RU /admin = %q", joined)
	}

	overrideLanguage(t, d, 200, "en")
	if err := dispatch(t, d, testEvent(200, 11, 7052, "/admin")); err != nil {
		t.Fatalf("EN /admin: %v", err)
	}
	joined = r.joined()
	if !strings.Contains(joined, "Administration:") || !strings.Contains(joined, "Chats:") {
		t.Fatalf("EN /admin = %q", joined)
	}

	// Authorization refusal is translated too: user 12 is allowlisted but not an administrator.
	if err := dispatch(t, d, testEvent(300, 12, 7053, "/admin")); err != nil {
		t.Fatalf("RU /admin refusal: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "Команда доступна только администраторам.") {
		t.Fatalf("RU /admin refusal = %q", got)
	}
	overrideLanguage(t, d, 300, "en")
	if err := dispatch(t, d, testEvent(300, 12, 7054, "/admin")); err != nil {
		t.Fatalf("EN /admin refusal: %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "This command is available to administrators only.") {
		t.Fatalf("EN /admin refusal = %q", got)
	}
}

// TestMenuButtonsFollowChatLanguage — the root inline keyboard is rendered in the chat language while
// every callback stays byte-identical to the Russian one.
func TestMenuButtonsFollowChatLanguage(t *testing.T) {
	d, r := newLongTailDispatcher(t, nil)

	if err := dispatch(t, d, testEvent(100, 11, 7061, "/start")); err != nil {
		t.Fatalf("RU /start: %v", err)
	}
	ruButtons := flattenButtons(r.menus[len(r.menus)-1])
	if ruButtons["menu:ask"] != "Спросить" || ruButtons["menu:docs"] != "Документы" {
		t.Fatalf("RU root menu = %+v", ruButtons)
	}

	overrideLanguage(t, d, 200, "en")
	if err := dispatch(t, d, testEvent(200, 11, 7062, "/start")); err != nil {
		t.Fatalf("EN /start: %v", err)
	}
	enButtons := flattenButtons(r.menus[len(r.menus)-1])
	wantLabels := map[string]string{
		"menu:ask": "Ask", "menu:reset": "New context", "menu:history": "History", "menu:docs": "Documents",
		"menu:settings": "Settings", "menu:status": "Status", "menu:help": "Help", "menu:more": "More",
	}
	for callback, label := range wantLabels {
		if enButtons[callback] != label {
			t.Errorf("EN button %s = %q, want %q", callback, enButtons[callback], label)
		}
	}
	if len(ruButtons) != len(enButtons) {
		t.Fatalf("RU has %d buttons, EN has %d", len(ruButtons), len(enButtons))
	}
	for callback := range ruButtons {
		if _, ok := enButtons[callback]; !ok {
			t.Errorf("EN menu lost the callback %q", callback)
		}
	}

	// The Usage menu is reached from the status menu and must follow the chat language as well.
	if err := dispatch(t, d, testEvent(200, 11, 7063, "/usage menu")); err != nil {
		t.Fatalf("EN /usage menu: %v", err)
	}
	usageButtons := flattenButtons(r.menus[len(r.menus)-1])
	if usageButtons["menu:usage"] != "Refresh" || usageButtons["menu:status"] != "← Back" {
		t.Fatalf("EN /usage menu = %+v", usageButtons)
	}
}

// TestPersonaPresetIDsAndCallbacksAreStable — only display strings are localized. The stored preset
// identity, the callback data and the /persona argument stay byte-identical in both languages.
func TestPersonaPresetIDsAndCallbacksAreStable(t *testing.T) {
	wantIDs := []string{"assistant", "coder", "writer", "translator"}
	definitions := PersonaPresetDefinitions()
	if len(definitions) != len(wantIDs) {
		t.Fatalf("persona presets = %d, want %d", len(definitions), len(wantIDs))
	}
	for index, want := range wantIDs {
		if definitions[index].Name != want {
			t.Fatalf("preset %d name = %q, want %q", index, definitions[index].Name, want)
		}
	}

	ruButtons := flattenButtons(personaMarkupFor(i18n.RU))
	enButtons := flattenButtons(personaMarkupFor(i18n.EN))
	wantCallbacks := []string{"persona:assistant", "persona:coder", "persona:writer", "persona:translator", "persona:custom", "persona:reset"}
	for _, callback := range wantCallbacks {
		if _, ok := ruButtons[callback]; !ok {
			t.Errorf("RU persona markup misses %q", callback)
		}
		if _, ok := enButtons[callback]; !ok {
			t.Errorf("EN persona markup misses %q", callback)
		}
	}
	if ruButtons["persona:assistant"] != "Ассистент" || enButtons["persona:assistant"] != "Assistant" {
		t.Fatalf("preset display name is not localized: RU=%q EN=%q", ruButtons["persona:assistant"], enButtons["persona:assistant"])
	}

	ruMenu := flattenButtons(personaMenuMarkupFor(i18n.RU))
	enMenu := flattenButtons(personaMenuMarkupFor(i18n.EN))
	for _, id := range wantIDs {
		callback := "menu:persona:set:" + id
		if _, ok := ruMenu[callback]; !ok {
			t.Errorf("RU persona menu misses %q", callback)
		}
		if _, ok := enMenu[callback]; !ok {
			t.Errorf("EN persona menu misses %q", callback)
		}
	}
	if ruMenu["menu:persona:set:assistant"] != "универсальный помощник" || enMenu["menu:persona:set:assistant"] != "general-purpose assistant" {
		t.Fatalf("preset description is not localized: RU=%q EN=%q", ruMenu["menu:persona:set:assistant"], enMenu["menu:persona:set:assistant"])
	}

	d := newTestDispatcher(t, testConfig(t, nil), nil, &fakeReplier{}, testStore(t))
	if got, ok := menuCallbackText("menu:persona:set:coder"); !ok || got != "/persona menu:set:coder" {
		t.Fatalf("menu:persona:set:coder = %q ok=%v", got, ok)
	}
	if got, ok := d.callbackText("persona:coder"); !ok || got != "/persona coder" {
		t.Fatalf("persona:coder = %q ok=%v", got, ok)
	}
}

// TestStreamPlaceholderFollowsChatLanguage — the interim stream views use the chat language; the
// exported Russian placeholder stays for the Mini App and the integration harness.
func TestStreamPlaceholderFollowsChatLanguage(t *testing.T) {
	r := &streamReplier{}
	d := newTestDispatcher(t, testConfig(t, nil), nil, r, testStore(t))

	ru := d.newStreamSession(context.Background(), 100, r)
	ru.post()
	if got := r.postTexts()[0]; got != StreamPlaceholder {
		t.Fatalf("RU placeholder = %q", got)
	}
	ru.stage(streamStageReadURL)
	if got := r.lastEdit(); got != "Читаю страницу…" {
		t.Fatalf("RU stage = %q", got)
	}

	overrideLanguage(t, d, 200, "en")
	en := d.newStreamSession(context.Background(), 200, r)
	en.post()
	if got := r.postTexts()[len(r.postTexts())-1]; got != "The model is thinking…" {
		t.Fatalf("EN placeholder = %q", got)
	}
	en.stage(streamStageReadURL)
	if got := r.lastEdit(); got != "Reading the page…" {
		t.Fatalf("EN stage = %q", got)
	}
}

// TestRefusalTextsFollowLanguage — the variadic language keeps the old Russian call sites working.
func TestRefusalTextsFollowLanguage(t *testing.T) {
	if got := refuseClosed(11); got != refuseClosed(11, i18n.RU) {
		t.Fatalf("RU default refusal changed: %q", got)
	}
	if got := refuseClosed(11, i18n.EN); !strings.Contains(got, "The bot is closed") || !strings.Contains(got, "user_id is 11") {
		t.Fatalf("EN refuseClosed = %q", got)
	}
	if got := refuseNotListed(11, i18n.EN); !strings.Contains(got, "You are not in TELEGRAM_ALLOWED_USERS") {
		t.Fatalf("EN refuseNotListed = %q", got)
	}
}

func flattenButtons(markup tgmodels.InlineKeyboardMarkup) map[string]string {
	buttons := map[string]string{}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			buttons[button.CallbackData] = button.Text
		}
	}
	return buttons
}
