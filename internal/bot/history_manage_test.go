package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

func TestExportAndSearchHistoryWithoutModel(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "не должно быть"}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	for _, row := range []store.Message{
		{ChatID: 200, UserID: 11, Role: store.RoleUser, Content: "Привет МИР"},
		{ChatID: 200, UserID: 11, Role: store.RoleAssistant, Content: "ответ"},
	} {
		if _, err := store.Append(context.Background(), db, row); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := dispatch(t, d, testEvent(200, 11, 1500, "/export json")); err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(r.documents) != 1 || r.documents[0].MIME != "application/json" {
		t.Fatalf("documents=%+v", r.documents)
	}
	var payload historyExport
	if err := json.Unmarshal(r.documents[0].Data, &payload); err != nil || len(payload.Messages) != 2 || payload.Messages[0].Content != "Привет МИР" {
		t.Fatalf("export payload=%+v err=%v", payload, err)
	}
	if err := dispatch(t, d, testEvent(200, 11, 1501, "/search мир")); err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(r.joined(), "Привет МИР") || m.callCount() != 0 {
		t.Fatalf("search reply=%q model calls=%d", r.joined(), m.callCount())
	}
}

func TestDeleteHistoryRequiresConfirmationAndPreservesPersona(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: 201, UserID: 11, Role: store.RoleUser, Content: "вопрос"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.SetChatPersona(context.Background(), db, 201, "coder"); err != nil {
		t.Fatalf("SetChatPersona: %v", err)
	}
	if err := dispatch(t, d, testEvent(201, 11, 1510, "/delete")); err != nil {
		t.Fatalf("delete prompt: %v", err)
	}
	if rows, _ := store.HistoryOfChat(context.Background(), db, 201, 10); len(rows) != 1 {
		t.Fatal("delete prompt removed history")
	}
	markup := r.menus[len(r.menus)-1]
	if len(markup.InlineKeyboard) != 1 || markup.InlineKeyboard[0][0].CallbackData != "delete:confirm" || markup.InlineKeyboard[0][1].CallbackData != "delete:cancel" {
		t.Fatalf("delete markup=%+v", markup)
	}
	if text, ok := d.callbackText("delete:cancel"); !ok || text != "/delete cancel" {
		t.Fatalf("cancel callback=%q ok=%v", text, ok)
	}
	if err := dispatch(t, d, testEvent(201, 11, 1511, "/delete cancel")); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := dispatch(t, d, testEvent(201, 11, 1512, "/delete confirm")); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if rows, _ := store.HistoryOfChat(context.Background(), db, 201, 10); len(rows) != 0 {
		t.Fatal("confirmed delete left history")
	}
	if _, found, err := store.ChatPersona(context.Background(), db, 201); err != nil || !found {
		t.Fatalf("persona was removed: found=%v err=%v", found, err)
	}
}
