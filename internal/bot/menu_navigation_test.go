package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

func newMenuNavigationDispatcher(t *testing.T) (*Dispatcher, *menuEditReplier) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	if err := os.WriteFile(dbPath, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11",
		"TELEGRAM_ADMIN_USERS":   "11",
		"TTS_ENABLED":            "true",
		"STREAMING_ENABLED":      "false",
		"DB_PATH":                dbPath,
	})
	r := &menuEditReplier{}
	m := &fakeModel{response: model.Response{Content: "Краткое содержание"}, models: []string{"model-one", "model-two"}}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	d.deps.TTS = &fakeSynthesizer{data: []byte("ID3-audio")}
	for _, message := range []store.Message{
		{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "вопрос"},
		{ChatID: 100, UserID: 11, Role: store.RoleAssistant, Content: "ответ"},
	} {
		if _, err := store.Append(context.Background(), db, message); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.PutRAGDocument(context.Background(), db, 100, 11, "document.txt", "содержимое"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutSavedLink(context.Background(), db, store.SavedLink{
		ChatID: 100, UserID: 11, Title: "Ссылка", CanonicalURL: "https://example.com", Content: "содержимое",
	}); err != nil {
		t.Fatal(err)
	}
	return d, r
}

func TestOrdinaryMenuCommandsNeverEditUserMessage(t *testing.T) {
	for _, command := range []string{"/start", "/help", "/status menu"} {
		t.Run(command, func(t *testing.T) {
			r := &menuEditReplier{}
			d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, testStore(t))
			if err := dispatch(t, d, testEvent(100, 11, 700, command)); err != nil {
				t.Fatalf("%s: %v", command, err)
			}
			if len(r.edits) != 0 || len(r.menus) != 1 || r.count() != 1 {
				t.Fatalf("ordinary command edits=%d menus=%d messages=%d", len(r.edits), len(r.menus), r.count())
			}
		})
	}
}

func TestMenuCallbackEditErrorHasNoSendFallbackAndIsSanitized(t *testing.T) {
	r := &menuEditReplier{editErr: errors.New("telegram 12345:test-token-not-a-secret rejected edit")}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, testStore(t))
	ev := testEvent(100, 11, 700, "/help menu:root")
	ev.MenuCallback = true
	err := d.renderMenu(context.Background(), ev, "menu", d.menuMarkup())
	if err == nil || strings.Contains(err.Error(), d.deps.Token) || !strings.Contains(err.Error(), "***") {
		t.Fatalf("edit error = %v", err)
	}
	if len(r.edits) != 1 || len(r.menus) != 0 || r.count() != 0 {
		t.Fatalf("failed callback edits=%d menus=%d messages=%d", len(r.edits), len(r.menus), r.count())
	}

	plain := &fakeReplier{}
	d = newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, plain, testStore(t))
	ev = testEvent(100, 11, 700, "/help menu:root")
	ev.MenuCallback = true
	if err := d.renderMenu(context.Background(), ev, "menu", d.menuMarkup()); err == nil {
		t.Fatal("callback without an editor unexpectedly succeeded")
	}
	if plain.count() != 0 || len(plain.menus) != 0 {
		t.Fatal("callback without an editor used SendMenu or Send fallback")
	}
}

func TestStatsPingAndHistoryCallbacksOnlyEdit(t *testing.T) {
	for _, test := range []struct {
		data     string
		wantText string
		back     string
	}{
		{data: "menu:stats", wantText: "Сообщений:", back: "menu:status"},
		{data: "menu:ping", wantText: "pong", back: "menu:status"},
		{data: "menu:history:5", wantText: "Последние 2 сообщений", back: "menu:root"},
		{data: "menu:history:20", wantText: "Последние 2 сообщений", back: "menu:root"},
	} {
		t.Run(test.data, func(t *testing.T) {
			d, r := newMenuNavigationDispatcher(t)
			if err := dispatchMenuData(t, d, 100, 11, 700, test.data); err != nil {
				t.Fatal(err)
			}
			if len(r.edits) != 1 || r.count() != 0 || len(r.menus) != 0 {
				t.Fatalf("edits=%d messages=%d menus=%d", len(r.edits), r.count(), len(r.menus))
			}
			edit := r.lastEdit()
			if edit.messageID != 700 || !strings.Contains(edit.text, test.wantText) || !hasMenuCallback(edit.markup, test.back) {
				t.Fatalf("edit = %+v", edit)
			}
		})
	}
}

func TestMenuCommandCallbackMatrixEditOrIntentionalMedia(t *testing.T) {
	media := map[string]string{
		"menu:export:md":   "document",
		"menu:export:json": "document",
		"menu:docs:export": "document",
		"menu:tts:last":    "audio",
	}
	keys := make([]string, 0, len(menuCommandCallbacks))
	for data := range menuCommandCallbacks {
		keys = append(keys, data)
	}
	sort.Strings(keys)
	for _, data := range keys {
		t.Run(data, func(t *testing.T) {
			d, r := newMenuNavigationDispatcher(t)
			if err := dispatchMenuData(t, d, 100, 11, 700, data); err != nil {
				t.Fatal(err)
			}
			if r.count() != 0 || len(r.menus) != 0 {
				t.Fatalf("callback sent a new message: messages=%d menus=%d", r.count(), len(r.menus))
			}
			switch media[data] {
			case "document":
				if len(r.documents) != 1 || len(r.edits) != 0 || len(r.audio) != 0 {
					t.Fatalf("document classification edits=%d documents=%d audio=%d", len(r.edits), len(r.documents), len(r.audio))
				}
			case "audio":
				if len(r.audio) != 1 || len(r.edits) != 0 || len(r.documents) != 0 {
					t.Fatalf("audio classification edits=%d documents=%d audio=%d", len(r.edits), len(r.documents), len(r.audio))
				}
			default:
				if len(r.edits) != 1 || len(r.documents) != 0 || len(r.audio) != 0 {
					t.Fatalf("edit classification edits=%d documents=%d audio=%d", len(r.edits), len(r.documents), len(r.audio))
				}
			}
		})
	}
}

func TestDynamicMenuCallbackMatrixEditsWithoutNewMessages(t *testing.T) {
	token := strings.Repeat("0", 32)
	callbacks := []string{
		"menu:model:set:0",
		"menu:persona:set:assistant",
		"menu:persona:set:coder",
		"menu:persona:set:writer",
		"menu:persona:set:translator",
		"menu:doc:delete:1",
		"menu:doc:confirm:" + token,
		"menu:doc:cancel:" + token,
		"menu:link:summary:1",
		"menu:link:delete:1",
		"menu:link:delete:confirm:" + token,
		"menu:link:delete:cancel:" + token,
		"menu:link:save:confirm:" + token,
		"menu:link:save:cancel:" + token,
		"menu:reminder:cancel:1",
		"menu:reminder:retry:1",
		"menu:reminder:delete:1",
		"menu:input:cancel:" + token,
		"menu:network:confirm:" + token,
		"menu:network:scope:" + token,
		"menu:network:cancel:" + token,
		"delete:confirm:" + token,
		"delete:cancel:" + token,
		"persona:assistant",
		"persona:coder",
		"persona:writer",
		"persona:translator",
		"persona:reset",
		"persona:custom",
		"cmd:ping",
	}
	for _, data := range callbacks {
		t.Run(data, func(t *testing.T) {
			d, r := newMenuNavigationDispatcher(t)
			if err := dispatchMenuData(t, d, 100, 11, 700, data); err != nil {
				t.Fatal(err)
			}
			if len(r.edits) != 1 || r.count() != 0 || len(r.menus) != 0 || len(r.documents) != 0 || len(r.audio) != 0 {
				t.Fatalf("edits=%d messages=%d menus=%d documents=%d audio=%d", len(r.edits), r.count(), len(r.menus), len(r.documents), len(r.audio))
			}
		})
	}
}
