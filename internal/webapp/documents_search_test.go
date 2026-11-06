package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"bot/internal/bot"
	"bot/internal/store"
)

func decodeDocumentSearch(t *testing.T, body []byte) documentSearchResponse {
	t.Helper()
	var decoded documentSearchResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("search response=%s err=%v", body, err)
	}
	return decoded
}

func TestDocumentSearchRequiresGetAndExactlyOneBoundedQuery(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)

	status, body, headers := apiRequest(t, server, http.MethodPost, "/api/documents/search?q=needle", bearer)
	if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodGet {
		t.Fatalf("POST search status=%d Allow=%q body=%s", status, headers.Get("Allow"), body)
	}
	for _, path := range []string{
		"/api/documents/search",
		"/api/documents/search?q=",
		"/api/documents/search?q=%20%20",
		"/api/documents/search?q=one&q=two",
		"/api/documents/search?q=needle&limit=1",
		"/api/documents/search?limit=1",
		"/api/documents/search?q=needle;limit=1",
		"/api/documents/search?q=%ff",
		"/api/documents/search?q=" + url.QueryEscape(strings.Repeat("я", store.RAGQueryMaxRunes+1)),
	} {
		status, body, _ = apiRequest(t, server, http.MethodGet, path, bearer)
		if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"invalid_query"`) {
			t.Errorf("GET %s status=%d body=%s", path, status, body)
		}
	}

	exactLimit := strings.Repeat("я", store.RAGQueryMaxRunes)
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/documents/search?q="+url.QueryEscape(exactLimit), bearer)
	if status != http.StatusOK || !strings.Contains(string(body), `"items":[]`) {
		t.Fatalf("exact-limit search status=%d body=%s", status, body)
	}
	decoded := decodeDocumentSearch(t, body)
	if len(decoded.Items) != 0 || decoded.Corpus.Scanned != 0 || decoded.Corpus.Total != 0 || decoded.Corpus.Truncated {
		t.Fatalf("empty exact-limit search=%+v", decoded)
	}
	client := app.model.(*fakeModelClient)
	client.mu.Lock()
	completions, models := client.completeCalls, client.modelsCalls
	client.mu.Unlock()
	if completions != 0 || models != 0 {
		t.Fatalf("search made model calls: completions=%d models=%d", completions, models)
	}
}

func TestDocumentSearchIsChatScopedAndUsesTelegramCaps(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	currentIDs := map[int64]bool{}
	for index := 0; index < bot.RAGResultLimit+2; index++ {
		name := fmt.Sprintf("current-%d.txt", index)
		content := "needle " + strings.Repeat("x", 893)
		id, chunks, err := store.PutRAGDocument(context.Background(), app.db, testChatID, testUserID, name, content)
		if err != nil || chunks != 1 {
			t.Fatalf("PutRAGDocument id=%d chunks=%d err=%v", id, chunks, err)
		}
		currentIDs[id] = true
	}
	otherID, _, err := store.PutRAGDocument(context.Background(), app.db, testChatID+1, testUserID, "other-chat-secret.txt", "needle from another chat")
	if err != nil {
		t.Fatal(err)
	}

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/documents/search?q=needle", bearer)
	if status != http.StatusOK {
		t.Fatalf("search status=%d body=%s", status, body)
	}
	decoded := decodeDocumentSearch(t, body)
	if len(decoded.Items) != bot.RAGResultLimit {
		t.Fatalf("search items=%d want=%d body=%s", len(decoded.Items), bot.RAGResultLimit, body)
	}
	if decoded.Corpus.Scanned != bot.RAGResultLimit+2 || decoded.Corpus.Total != bot.RAGResultLimit+2 || decoded.Corpus.Truncated {
		t.Fatalf("search corpus=%+v", decoded.Corpus)
	}
	contentRunes := 0
	for _, item := range decoded.Items {
		if !currentIDs[item.DocumentID] || item.DocumentID == otherID || item.Name == "other-chat-secret.txt" || item.Score <= 0 || item.Content == "" {
			t.Errorf("unexpected search item=%+v", item)
		}
		contentRunes += len([]rune(item.Content))
	}
	if contentRunes != bot.RAGMaxRunes {
		t.Fatalf("search content runes=%d want=%d", contentRunes, bot.RAGMaxRunes)
	}
	if strings.Contains(string(body), "other-chat-secret") || strings.Contains(string(body), "another chat") {
		t.Fatalf("search leaked another chat: %s", body)
	}
	client := app.model.(*fakeModelClient)
	client.mu.Lock()
	completions, models := client.completeCalls, client.modelsCalls
	client.mu.Unlock()
	if completions != 0 || models != 0 {
		t.Fatalf("search made model calls: completions=%d models=%d", completions, models)
	}
}

func TestDocumentSearchReportsTruncatedCorpus(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	ctx := context.Background()
	oldID, _, err := store.PutRAGDocument(ctx, app.db, testChatID, testUserID, "oldest-secret.txt", "needle in the oldest chunk")
	if err != nil {
		t.Fatal(err)
	}
	recentID, _, err := store.PutRAGDocument(ctx, app.db, testChatID, testUserID, "recent.txt", "needle in the newest matching chunk")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := app.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO rag_documents (chat_id, name, size_bytes) VALUES (?, 'filler.txt', 0)`, testChatID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	fillerID, err := result.LastInsertId()
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO rag_chunks (document_id, chat_id, ordinal, content) VALUES (?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	for index := 0; index < 9999; index++ {
		if _, err := statement.ExecContext(ctx, fillerID, testChatID, index, fmt.Sprintf("ordinary unrelated filler %d", index)); err != nil {
			_ = statement.Close()
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/documents/search?q=needle", bearer)
	if status != http.StatusOK {
		t.Fatalf("truncated search status=%d body=%s", status, body)
	}
	decoded := decodeDocumentSearch(t, body)
	if decoded.Corpus.Scanned != 10000 || decoded.Corpus.Total != 10001 || !decoded.Corpus.Truncated {
		t.Fatalf("truncated search corpus=%+v", decoded.Corpus)
	}
	if len(decoded.Items) != 1 || decoded.Items[0].DocumentID != recentID || decoded.Items[0].DocumentID == oldID {
		t.Fatalf("truncated search items=%+v recent=%d old=%d", decoded.Items, recentID, oldID)
	}
	if strings.Contains(string(body), "oldest-secret") || strings.Contains(string(body), "oldest chunk") {
		t.Fatalf("search returned a chunk outside the scan window: %s", body)
	}
}
