package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

// languageTestDispatcher builds a dispatcher with an allowlisted user, a fake model and a fresh store.
func languageTestDispatcher(t *testing.T, overrides map[string]string) (*Dispatcher, *fakeReplier, *fakeModel) {
	t.Helper()
	var cfg = testConfig(t, merge(map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}, overrides))
	var replier = &fakeReplier{}
	var modelClient = &fakeModel{response: model.Response{Content: "ответ модели"}}
	var d = newTestDispatcher(t, cfg, modelClient, replier, testStore(t))
	return d, replier, modelClient
}

// TestLanguageEnglishRendersHelpStatusAndStart — /language en switches the command surface of this chat.
func TestLanguageEnglishRendersHelpStatusAndStart(t *testing.T) {
	var d, r, modelClient = languageTestDispatcher(t, nil)
	if err := dispatch(t, d, testEvent(100, 11, 400, "/language en")); err != nil {
		t.Fatalf("/language en: %v", err)
	}
	if text := r.texts()[0]; !strings.Contains(text, "This chat's language: English") {
		t.Fatalf("/language en confirmation = %q", text)
	}

	for _, command := range []string{"/help", "/status", "/start"} {
		if err := dispatch(t, d, testEvent(100, 11, int64(401+len(r.texts())), command)); err != nil {
			t.Fatalf("%s: %v", command, err)
		}
	}
	var all = r.joined()
	for _, want := range []string{
		"Hi! I am a proxy to the local STRATA model. Commands:",
		"command reference",
		"/language [ru|en|reset]",
		"Bot status:",
		"Model: ",
		"Streaming: off",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("the English command surface must contain %q, got %q", want, all)
		}
	}
	for _, russian := range []string{"Привет! Я прокси", "справка по командам", "Состояние бота:"} {
		if strings.Contains(all, russian) {
			t.Errorf("the English command surface still contains Russian %q: %q", russian, all)
		}
	}
	if modelClient.callCount() != 0 {
		t.Fatalf("/language, /help, /status and /start must not call the model, calls = %d", modelClient.callCount())
	}
}

// TestLanguageRussianRendersHelpAndStatus — /language ru returns the chat to Russian.
func TestLanguageRussianRendersHelpAndStatus(t *testing.T) {
	var d, r, _ = languageTestDispatcher(t, map[string]string{"DEFAULT_LANGUAGE": "en"})
	if err := dispatch(t, d, testEvent(100, 11, 410, "/language ru")); err != nil {
		t.Fatalf("/language ru: %v", err)
	}
	if text := r.texts()[0]; !strings.Contains(text, "Язык этого чата: русский") {
		t.Fatalf("/language ru confirmation = %q", text)
	}
	if err := dispatch(t, d, testEvent(100, 11, 411, "/help")); err != nil {
		t.Fatalf("/help: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 412, "/status")); err != nil {
		t.Fatalf("/status: %v", err)
	}
	var all = r.joined()
	for _, want := range []string{"справка по командам", "Состояние бота:", "Модель: "} {
		if !strings.Contains(all, want) {
			t.Errorf("the Russian command surface must contain %q, got %q", want, all)
		}
	}
	if strings.Contains(all, "command reference") {
		t.Errorf("the Russian command surface still contains the English help: %q", all)
	}
}

// TestLanguageOverrideIsPerChat — two chats resolve different languages from the same dispatcher.
func TestLanguageOverrideIsPerChat(t *testing.T) {
	var d, r, _ = languageTestDispatcher(t, nil)
	if err := dispatch(t, d, testEvent(100, 11, 420, "/language en")); err != nil {
		t.Fatalf("/language en: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 421, "/status")); err != nil {
		t.Fatalf("/status of chat 100: %v", err)
	}
	if err := dispatch(t, d, testEvent(200, 11, 422, "/status")); err != nil {
		t.Fatalf("/status of chat 200: %v", err)
	}
	var texts = r.texts()
	if len(texts) != 3 {
		t.Fatalf("sent %d texts, want 3: %q", len(texts), texts)
	}
	if !strings.Contains(texts[1], "Bot status:") {
		t.Errorf("chat 100 must be English, got %q", texts[1])
	}
	if !strings.Contains(texts[2], "Состояние бота:") {
		t.Errorf("chat 200 must stay Russian, got %q", texts[2])
	}
}

// TestLanguageResetFallsBackToDefault — reset deletes the override and the chat follows DEFAULT_LANGUAGE.
func TestLanguageResetFallsBackToDefault(t *testing.T) {
	var d, r, _ = languageTestDispatcher(t, map[string]string{"DEFAULT_LANGUAGE": "en"})
	if err := dispatch(t, d, testEvent(100, 11, 430, "/language ru")); err != nil {
		t.Fatalf("/language ru: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 431, "/status")); err != nil {
		t.Fatalf("/status before reset: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 432, "/language reset")); err != nil {
		t.Fatalf("/language reset: %v", err)
	}
	var text = r.texts()[len(r.texts())-1]
	if !strings.Contains(text, "The language setting was reset. This chat now uses the default language: English.") {
		t.Fatalf("/language reset confirmation = %q", text)
	}
	if _, found, err := store.ChatLanguage(context.Background(), d.deps.DB, 100); err != nil || found {
		t.Fatalf("the override survived reset: found=%v err=%v", found, err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 433, "/status")); err != nil {
		t.Fatalf("/status after reset: %v", err)
	}
	if got := r.texts()[len(r.texts())-1]; !strings.Contains(got, "Bot status:") {
		t.Fatalf("/status after reset must use DEFAULT_LANGUAGE=en, got %q", got)
	}
}

// TestLanguageUnknownArgumentDoesNotWrite — a bad argument is usage only, without a store row.
func TestLanguageUnknownArgumentDoesNotWrite(t *testing.T) {
	var d, r, modelClient = languageTestDispatcher(t, nil)
	if err := dispatch(t, d, testEvent(100, 11, 440, "/language de")); err != nil {
		t.Fatalf("/language de: %v", err)
	}
	if got := r.texts()[0]; !strings.Contains(got, "Использование: /language") {
		t.Fatalf("/language de must answer with usage, got %q", got)
	}
	if _, found, err := store.ChatLanguage(context.Background(), d.deps.DB, 100); err != nil || found {
		t.Fatalf("an unknown language wrote a row: found=%v err=%v", found, err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 441, "/status")); err != nil {
		t.Fatalf("/status: %v", err)
	}
	if got := r.texts()[1]; !strings.Contains(got, "Состояние бота:") {
		t.Fatalf("an unknown language changed the chat language: %q", got)
	}
	if modelClient.callCount() != 0 {
		t.Fatalf("/language must never call the model, calls = %d", modelClient.callCount())
	}
}

// TestLanguageCurrentReportsSource — the bare /language names the language and where it comes from.
func TestLanguageCurrentReportsSource(t *testing.T) {
	var d, r, _ = languageTestDispatcher(t, nil)
	if err := dispatch(t, d, testEvent(100, 11, 450, "/language")); err != nil {
		t.Fatalf("/language: %v", err)
	}
	if got := r.texts()[0]; !strings.Contains(got, "Текущий язык: русский (источник: по умолчанию).") {
		t.Fatalf("/language without an override = %q", got)
	}
	if err := dispatch(t, d, testEvent(100, 11, 451, "/language en")); err != nil {
		t.Fatalf("/language en: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 452, "/language")); err != nil {
		t.Fatalf("/language after the override: %v", err)
	}
	if got := r.texts()[len(r.texts())-1]; !strings.Contains(got, "Current language: English (source: this chat).") {
		t.Fatalf("/language with an override = %q", got)
	}
}

// TestLanguageCommandNeverUsesTheModel — the registry contract: /language is a local setting.
func TestLanguageCommandNeverUsesTheModel(t *testing.T) {
	var d = newTestDispatcher(t, testConfig(t, nil), nil, nil, testStore(t))
	var command, ok = d.index[CommandLanguage]
	if !ok {
		t.Fatal("/language is not registered")
	}
	if command.UsesModel {
		t.Fatal("/language must not be registered as a model command")
	}
	if command.Handler == nil {
		t.Fatal("/language has no handler")
	}
}
