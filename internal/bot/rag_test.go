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
	if err := dispatch(t, d, testEvent(210, 11, 1604, "/docs export")); err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(r.documents) != 1 || !strings.Contains(string(r.documents[0].Data), "Проект Aurora") || r.documents[0].Caption != "Экспорт RAG-документов" {
		t.Fatalf("RAG export=%+v", r.documents)
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
	if !strings.Contains(r.joined(), "Источники: aurora.txt") {
		t.Fatalf("RAG sources missing from reply: %q", r.joined())
	}
	uses, err := store.CountEvents(context.Background(), db, store.EventUsage)
	if err != nil || uses != 1 {
		t.Fatalf("RAG usage events=%d err=%v", uses, err)
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

func TestPDFCanBeAddedThroughDocsAdd(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	d.deps.Attachments = &fakeAttachmentDownloader{
		data: testPDF("BT /F1 16 Tf 72 720 Td (Aurora PDF via Telegram) Tj ET", 0),
		mime: "application/pdf",
	}
	add := testEvent(212, 11, 1620, "/docs add")
	add.Attachment = &Attachment{Kind: AttachmentDocument, FileID: "pdf", FileName: "aurora.pdf", MimeType: "application/pdf"}
	if err := dispatch(t, d, add); err != nil {
		t.Fatalf("add pdf: %v", err)
	}
	if !strings.Contains(r.joined(), "сохранён") {
		t.Fatalf("reply = %q", r.joined())
	}
	documents, err := store.ListRAGDocuments(context.Background(), db, 212)
	if err != nil || len(documents) != 1 || documents[0].Name != "aurora.pdf" {
		t.Fatalf("documents=%+v err=%v", documents, err)
	}
	var content string
	if err := db.QueryRow(`SELECT content FROM rag_chunks WHERE document_id = ? ORDER BY ordinal LIMIT 1`, documents[0].ID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Aurora PDF via Telegram") || strings.Contains(content, "%PDF") {
		t.Fatalf("stored chunk = %q", content)
	}
}

func TestScanOnlyPDFThroughDocsAddRepliesAndStoresNothing(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	d.deps.Attachments = &fakeAttachmentDownloader{
		data: testPDF("q 0 0 612 792 re f Q", 0),
		mime: "application/pdf",
	}
	add := testEvent(213, 11, 1621, "/docs add")
	add.Attachment = &Attachment{Kind: AttachmentDocument, FileID: "scan", FileName: "scan.pdf", MimeType: "application/pdf"}
	if err := dispatch(t, d, add); err != nil {
		t.Fatalf("add scan: %v", err)
	}
	if !strings.Contains(r.joined(), "нет текстового слоя") {
		t.Fatalf("reply = %q", r.joined())
	}
	if documents, err := store.ListRAGDocuments(context.Background(), db, 213); err != nil || len(documents) != 0 {
		t.Fatalf("documents=%+v err=%v", documents, err)
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
