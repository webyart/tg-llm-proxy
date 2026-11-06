package bot

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

func TestDocumentSearchSuccessAndUsage(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	documentID, _, err := store.PutRAGDocument(context.Background(), db, 300, 11, "handbook.txt", "Aurora deployment uses a local runbook.")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := dispatch(t, d, testEvent(300, 11, 1700, "/docs search Aurora")); err != nil {
		t.Fatalf("search: %v", err)
	}
	reply := r.texts()[len(r.texts())-1]
	for _, want := range []string{"handbook.txt", fmt.Sprintf("id=%d", documentID), "score=", "Aurora deployment uses a local runbook."} {
		if !strings.Contains(reply, want) {
			t.Errorf("search reply does not contain %q: %q", want, reply)
		}
	}
	if m.callCount() != 0 {
		t.Fatalf("search called the model %d times", m.callCount())
	}
	for i, command := range []string{"/docs help", "/docs usage"} {
		if err := dispatch(t, d, testEvent(300, 11, int64(1701+i), command)); err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if reply := r.texts()[len(r.texts())-1]; !strings.Contains(reply, "/docs search <query>") {
			t.Errorf("%s reply=%q", command, reply)
		}
	}
}

func TestDocumentSearchEmptyAndInvalid(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	for i, command := range []string{"/docs search", "/docs search   "} {
		if err := dispatch(t, d, testEvent(310, 11, int64(1710+i), command)); err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if reply := r.texts()[len(r.texts())-1]; !strings.Contains(reply, "непустой запрос") || !strings.Contains(reply, "/docs search <query>") {
			t.Errorf("invalid search reply=%q", reply)
		}
	}
	if _, _, err := store.PutRAGDocument(context.Background(), db, 310, 11, "notes.txt", "known content"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := dispatch(t, d, testEvent(310, 11, 1712, "/docs search absenttoken")); err != nil {
		t.Fatalf("empty search: %v", err)
	}
	if reply := r.texts()[len(r.texts())-1]; !strings.Contains(reply, "ничего не найдено") || !strings.Contains(reply, "текущего чата") {
		t.Fatalf("empty search reply=%q", reply)
	}
	uses, err := store.CountEvents(context.Background(), db, store.EventUsage)
	if err != nil || uses != 0 {
		t.Fatalf("usage events=%d err=%v", uses, err)
	}
}

func TestDocumentSearchIsChatScoped(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	if _, _, err := store.PutRAGDocument(context.Background(), db, 320, 11, "local.txt", "localsecret belongs to this chat"); err != nil {
		t.Fatalf("local Put: %v", err)
	}
	if _, _, err := store.PutRAGDocument(context.Background(), db, 321, 11, "foreign.txt", "foreignsecret must stay foreign"); err != nil {
		t.Fatalf("foreign Put: %v", err)
	}
	if err := dispatch(t, d, testEvent(320, 11, 1720, "/docs search foreignsecret")); err != nil {
		t.Fatalf("foreign search: %v", err)
	}
	if reply := r.texts()[len(r.texts())-1]; !strings.Contains(reply, "ничего не найдено") || strings.Contains(reply, "foreign.txt") || strings.Contains(reply, "must stay foreign") {
		t.Fatalf("cross-chat search reply=%q", reply)
	}
	if err := dispatch(t, d, testEvent(320, 11, 1721, "/docs search localsecret")); err != nil {
		t.Fatalf("local search: %v", err)
	}
	if reply := r.texts()[len(r.texts())-1]; !strings.Contains(reply, "local.txt") || strings.Contains(reply, "foreign.txt") {
		t.Fatalf("local search reply=%q", reply)
	}
}

func TestDocumentSearchResultAndReplyCaps(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	for i := 0; i < RAGResultLimit+1; i++ {
		content := fmt.Sprintf("needletoken document %d %s", i, strings.Repeat("content ", 100))
		if _, _, err := store.PutRAGDocument(context.Background(), db, 330, 11, fmt.Sprintf("doc-%d.txt", i), content); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	if err := dispatch(t, d, testEvent(330, 11, 1730, "/docs search needletoken")); err != nil {
		t.Fatalf("search: %v", err)
	}
	if reply := r.texts()[len(r.texts())-1]; strings.Count(reply, "score=") != RAGResultLimit {
		t.Fatalf("result count=%d, want %d; reply=%q", strings.Count(reply, "score="), RAGResultLimit, reply)
	}
	d.deps.Cfg.MaxMessageChunk = 256
	if err := dispatch(t, d, testEvent(330, 11, 1731, "/docs search needletoken")); err != nil {
		t.Fatalf("capped search: %v", err)
	}
	texts := r.texts()
	reply := texts[len(texts)-1]
	if len(texts) != 2 || Runes(reply) > 256 || !strings.Contains(reply, "ответ обрезан") {
		t.Fatalf("capped reply messages=%d runes=%d reply=%q", len(texts), Runes(reply), reply)
	}
}

func TestDocumentSearchTruncationAndAutomaticWarning(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	documentID, _, err := store.PutRAGDocument(ctx, db, 340, 11, "large.txt", "oldesttoken")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (
			SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 10000
		)
		INSERT INTO rag_chunks (document_id, chat_id, ordinal, content)
		SELECT ?, ?, n, CASE WHEN n = 10000 THEN 'recenttoken newest' ELSE 'filler ' || n END
		FROM seq`, documentID, int64(340))
	if err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	if err := dispatch(t, d, testEvent(340, 11, 1740, "/docs search recenttoken")); err != nil {
		t.Fatalf("truncated search: %v", err)
	}
	reply := r.texts()[len(r.texts())-1]
	for _, want := range []string{"корпус документов неполный", "10000 из 10001", "recenttoken newest"} {
		if !strings.Contains(reply, want) {
			t.Errorf("truncated search reply does not contain %q: %q", want, reply)
		}
	}
	if err := dispatch(t, d, testEvent(340, 11, 1741, "/docs search oldesttoken")); err != nil {
		t.Fatalf("outside-window search: %v", err)
	}
	reply = r.texts()[len(r.texts())-1]
	if !strings.Contains(reply, "ничего не найдено") || !strings.Contains(reply, "10000 из 10001") {
		t.Fatalf("outside-window search reply=%q", reply)
	}
	messages, sources, err := d.addRAGContext(ctx, 340, 11, "recenttoken", []model.Message{{Role: model.RoleUser, Content: "recenttoken"}})
	if err != nil {
		t.Fatalf("addRAGContext: %v", err)
	}
	if len(sources) != 1 || sources[0] != "large.txt" {
		t.Fatalf("sources=%v", sources)
	}
	foundWarning := false
	for _, message := range messages {
		if message.Role == model.RoleSystem && strings.Contains(message.Content, "корпус документов неполный") && strings.Contains(message.Content, "10000 из 10001") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("automatic RAG warning missing: %+v", messages)
	}
	uses, err := store.CountEvents(ctx, db, store.EventUsage)
	if err != nil || uses != 1 {
		t.Fatalf("usage events=%d err=%v", uses, err)
	}
	unchanged := []model.Message{{Role: model.RoleUser, Content: "oldesttoken"}}
	messages, sources, err = d.addRAGContext(ctx, 340, 11, "oldesttoken", unchanged)
	if err != nil || len(messages) != len(unchanged) || messages[0].Content != unchanged[0].Content || len(sources) != 0 {
		t.Fatalf("empty automatic RAG messages=%+v sources=%v err=%v", messages, sources, err)
	}
	uses, err = store.CountEvents(ctx, db, store.EventUsage)
	if err != nil || uses != 1 {
		t.Fatalf("usage events after empty result=%d err=%v", uses, err)
	}
}

func TestDocumentMenuSearchPendingCompletionCancelAndExpiry(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12", "PROXY_FREE_TEXT": "false"})
	m := &fakeModel{}
	r := &menuEditReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	if _, _, err := store.PutRAGDocument(context.Background(), db, 350, 11, "local.txt", "Aurora local runbook"); err != nil {
		t.Fatalf("local Put: %v", err)
	}
	if _, _, err := store.PutRAGDocument(context.Background(), db, 351, 11, "foreign.txt", "Aurora foreign runbook"); err != nil {
		t.Fatalf("foreign Put: %v", err)
	}
	if err := dispatchMenuData(t, d, 350, 11, 700, "menu:docs"); err != nil {
		t.Fatal(err)
	}
	if !hasMenuCallback(r.lastEdit().markup, "menu:docs:search") {
		t.Fatalf("documents menu has no search: %+v", r.lastEdit())
	}
	if err := dispatchMenuData(t, d, 350, 11, 700, "menu:docs:search"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "/docs search <query>") || !strings.Contains(r.lastEdit().text, "2 минуты") || findMenuCallback(r.lastEdit().markup, "menu:input:cancel:") == "" {
		t.Fatalf("document search prompt = %+v", r.lastEdit())
	}
	if _, consumed := d.pendingEvent(testEvent(350, 12, 701, "Aurora")); consumed {
		t.Fatal("wrong user consumed document search input")
	}
	pending, consumed := d.pendingEvent(testEvent(350, 11, 702, "Aurora"))
	if !consumed || pending.Text != "/docs search Aurora" {
		t.Fatalf("document search pending = %q consumed=%v", pending.Text, consumed)
	}
	if err := dispatch(t, d, pending); err != nil {
		t.Fatal(err)
	}
	reply := r.texts()[len(r.texts())-1]
	if !strings.Contains(reply, "local.txt") || !strings.Contains(reply, "Aurora local runbook") || strings.Contains(reply, "foreign") {
		t.Fatalf("menu document search reply = %q", reply)
	}
	if m.callCount() != 0 {
		t.Fatalf("menu document search called the model %d times", m.callCount())
	}
	if _, consumed := d.pendingEvent(testEvent(350, 11, 703, "second query")); consumed {
		t.Fatal("document search input was consumed twice")
	}

	if err := dispatchMenuData(t, d, 350, 11, 700, "menu:docs:search"); err != nil {
		t.Fatal(err)
	}
	cancel := findMenuCallback(r.lastEdit().markup, "menu:input:cancel:")
	if err := dispatchMenuData(t, d, 350, 11, 700, cancel); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "Поиск отменён") {
		t.Fatalf("cancelled document search menu = %q", r.lastEdit().text)
	}
	if _, consumed := d.pendingEvent(testEvent(350, 11, 704, "не поиск")); consumed {
		t.Fatal("cancelled document search input remained pending")
	}

	if err := dispatchMenuData(t, d, 350, 11, 700, "menu:docs:search"); err != nil {
		t.Fatal(err)
	}
	set := pendingInputsFor(d)
	set.mu.Lock()
	for key, pending := range set.pending {
		pending.ExpiresAt = pending.ExpiresAt.Add(-2 * pendingInputTTL)
		set.pending[key] = pending
	}
	set.mu.Unlock()
	expired := testEvent(350, 11, 705, "запрос после expiry")
	if got, consumed := d.pendingEvent(expired); consumed || got.Text != expired.Text {
		t.Fatalf("expired document search intercepted text: %+v consumed=%v", got, consumed)
	}
	if m.callCount() != 0 {
		t.Fatalf("document search lifecycle called the model %d times", m.callCount())
	}
}
