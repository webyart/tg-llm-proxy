package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

func TestDocumentCanBeAddedRetrievedAndDeleted(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "ответ модели"}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	d.deps.Attachments = &fakeAttachmentDownloader{data: []byte("Проект Aurora использует локальные модели."), mime: "text/plain"}
	add := testEvent(210, 11, 1600, "/docs add")
	add.Attachment = &Attachment{Kind: AttachmentDocument, FileID: "doc", FileName: "aurora.txt", MimeType: "text/plain"}
	if err := dispatch(t, d, add); err != nil {
		t.Fatalf("add: %v", err)
	}
	if m.callCount() != 0 || !strings.Contains(r.joined(), "сохранён") {
		t.Fatalf("add called model or failed: calls=%d reply=%q", m.callCount(), r.joined())
	}
	docs, err := store.ListRAGDocuments(context.Background(), db, 210)
	if err != nil || len(docs) != 1 {
		t.Fatalf("docs=%+v err=%v", docs, err)
	}
	if err := dispatch(t, d, testEvent(210, 11, 1601, "Что известно про Aurora?")); err != nil {
		t.Fatalf("ask: %v", err)
	}
	req := m.lastRequest()
	foundContext := false
	for _, message := range req.Messages {
		if message.Role == model.RoleSystem && strings.Contains(message.Content, "Проект Aurora") && strings.Contains(message.Content, "aurora.txt") {
			foundContext = true
		}
	}
	if !foundContext {
		t.Fatalf("RAG context missing: %+v", req.Messages)
	}
	if err := dispatch(t, d, testEvent(210, 11, 1602, "/docs delete 1")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := dispatch(t, d, testEvent(210, 11, 1603, "Расскажи про Aurora")); err != nil {
		t.Fatalf("ask after delete: %v", err)
	}
	for _, message := range m.lastRequest().Messages {
		if strings.Contains(message.Content, "Релевантные фрагменты") {
			t.Fatal("deleted RAG context was used")
		}
	}
}

func TestDocumentsClearRequiresConfirmation(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	if _, _, err := store.PutRAGDocument(context.Background(), db, 211, 11, "note", "текст"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := dispatch(t, d, testEvent(211, 11, 1610, "/docs clear")); err != nil {
		t.Fatalf("clear prompt: %v", err)
	}
	if docs, _ := store.ListRAGDocuments(context.Background(), db, 211); len(docs) != 1 {
		t.Fatal("clear prompt deleted documents")
	}
	if err := dispatch(t, d, testEvent(211, 11, 1611, "/docs clear confirm")); err != nil {
		t.Fatalf("clear confirm: %v", err)
	}
	if docs, _ := store.ListRAGDocuments(context.Background(), db, 211); len(docs) != 0 {
		t.Fatal("confirmed clear left documents")
	}
}
