package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"bot/internal/store"
)

func createWebappDocument(t *testing.T, app *App, chatID int64, name string) int64 {
	t.Helper()
	id, chunks, err := store.PutRAGDocument(context.Background(), app.db, chatID, testUserID, name, strings.Repeat("private document body. ", 200))
	if err != nil || chunks < 2 {
		t.Fatalf("PutRAGDocument id=%d chunks=%d err=%v", id, chunks, err)
	}
	return id
}

func webappDocumentCount(t *testing.T, app *App, chatID int64) int {
	t.Helper()
	var count int
	if err := app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM rag_documents WHERE chat_id = ?`, chatID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func webappDocumentExists(t *testing.T, app *App, chatID int64, documentID int64) bool {
	t.Helper()
	var found int
	err := app.db.QueryRowContext(context.Background(), `SELECT 1 FROM rag_documents WHERE chat_id = ? AND id = ?`, chatID, documentID).Scan(&found)
	if err != nil {
		return false
	}
	return found == 1
}

func webappDocumentChunkCount(t *testing.T, app *App, documentID int64) int {
	t.Helper()
	var count int
	if err := app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM rag_chunks WHERE document_id = ?`, documentID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func webappChatChunkCount(t *testing.T, app *App, chatID int64) int {
	t.Helper()
	var count int
	if err := app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM rag_chunks WHERE chat_id = ?`, chatID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestDocumentDeleteIsScopedUsesCanonicalIDAndDeletesChunks(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, otherTestUserID, otherTestUserID)
	created := createWebappDocument(t, app, testChatID, "current")
	other := createWebappDocument(t, app, testChatID+1, "other")
	path := fmt.Sprintf("/api/documents/%d/delete", created)

	status, body := settingsRequest(t, server.URL, http.MethodPost, path, otherBearer, "", "")
	if status != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("cross-chat delete status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/documents/%d/delete", other), bearer, "", "")
	if status != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("wrong-chat delete status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, "/api/documents/999999/delete", bearer, "", "")
	if status != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("missing delete status=%d body=%s", status, body)
	}
	for _, invalidPath := range []string{
		"/api/documents/01/delete", "/api/documents/0/delete", "/api/documents/-1/delete", "/api/documents/+1/delete",
		"/api/documents/1.0/delete", "/api/documents/9223372036854775808/delete", "/api/documents/1/extra/delete",
		"/api/documents/delete", "/api/documents/1/delete/", "/api/documents/unknown",
	} {
		status, body = settingsRequest(t, server.URL, http.MethodPost, invalidPath, bearer, "", "")
		if status != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
			t.Errorf("non-canonical path %q status=%d body=%s", invalidPath, status, body)
		}
	}
	if !webappDocumentExists(t, app, testChatID, created) || !webappDocumentExists(t, app, testChatID+1, other) ||
		webappDocumentChunkCount(t, app, created) < 2 || webappDocumentChunkCount(t, app, other) < 2 {
		t.Fatal("rejected delete changed a document or its chunks")
	}

	status, body = settingsRequest(t, server.URL, http.MethodPost, path, bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", status, body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded) != 2 || decoded["ok"] != true || decoded["id"] != float64(created) {
		t.Fatalf("delete response=%s err=%v", body, err)
	}
	if strings.Contains(string(body), "private document body") || strings.Contains(string(body), "current") {
		t.Fatalf("delete response exposed document content: %s", body)
	}
	if webappDocumentExists(t, app, testChatID, created) || webappDocumentChunkCount(t, app, created) != 0 {
		t.Fatal("successful delete left the document or its chunks")
	}
	if !webappDocumentExists(t, app, testChatID+1, other) || webappDocumentChunkCount(t, app, other) < 2 {
		t.Fatal("successful delete changed another chat")
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, path, bearer, "", "")
	if status != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("second delete status=%d body=%s", status, body)
	}
}

func TestDocumentClearDeletesCurrentChatChunksAndAllowsEmpty(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	createWebappDocument(t, app, testChatID, "one")
	createWebappDocument(t, app, testChatID, "two")
	other := createWebappDocument(t, app, testChatID+1, "other")
	otherChunks := webappDocumentChunkCount(t, app, other)

	status, body := settingsRequest(t, server.URL, http.MethodPost, "/api/documents/clear", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", status, body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded) != 2 || decoded["ok"] != true || decoded["deleted"] != float64(2) {
		t.Fatalf("clear response=%s err=%v", body, err)
	}
	if got := webappDocumentCount(t, app, testChatID); got != 0 {
		t.Fatalf("clear left %d current chat documents", got)
	}
	if got := webappChatChunkCount(t, app, testChatID); got != 0 {
		t.Fatalf("clear left %d current chat chunks", got)
	}
	if got := webappDocumentCount(t, app, testChatID+1); got != 1 || !webappDocumentExists(t, app, testChatID+1, other) {
		t.Fatalf("clear changed other chat documents: count=%d", got)
	}
	if got := webappDocumentChunkCount(t, app, other); got != otherChunks {
		t.Fatalf("clear changed other chat chunks: count=%d want=%d", got, otherChunks)
	}

	status, body = settingsRequest(t, server.URL, http.MethodPost, "/api/documents/clear", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("empty clear status=%d body=%s", status, body)
	}
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded) != 2 || decoded["ok"] != true || decoded["deleted"] != float64(0) {
		t.Fatalf("empty clear response=%s err=%v", body, err)
	}
	if got := webappDocumentCount(t, app, testChatID+1); got != 1 || webappDocumentChunkCount(t, app, other) != otherChunks {
		t.Fatal("empty clear changed another chat")
	}
}

func TestDocumentMutationsRequireStrictMethodBodyAndQuery(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	created := createWebappDocument(t, app, testChatID, "current")
	paths := []string{"/api/documents/clear", fmt.Sprintf("/api/documents/%d/delete", created)}

	for _, path := range paths {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			status, body, headers := apiRequest(t, server, method, path, bearer)
			if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodPost {
				t.Errorf("%s %s status=%d Allow=%q body=%s", method, path, status, headers.Get("Allow"), body)
			}
		}
		for _, test := range []struct {
			name string
			path string
			body string
		}{
			{"JSON body", path, `{}`},
			{"invalid body", path, `{`},
			{"whitespace body", path, " \n"},
			{"query", path + "?chat_id=4242", ""},
		} {
			status, body := settingsRequest(t, server.URL, http.MethodPost, test.path, bearer, test.body, "")
			if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"invalid_body"`) {
				t.Errorf("%s %s status=%d body=%s", test.name, path, status, body)
			}
		}
		status, body := settingsRequest(t, server.URL, http.MethodPost, path, bearer, strings.Repeat("x", maxBodyBytes+1), "")
		if status != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), `"code":"body_too_large"`) {
			t.Errorf("oversized %s status=%d body=%s", path, status, body)
		}
	}
	if got := webappDocumentCount(t, app, testChatID); got != 1 || webappDocumentChunkCount(t, app, created) < 2 {
		t.Fatalf("rejected mutations deleted documents or chunks: count=%d", got)
	}
}

func TestDocumentMutationsRejectWrongSessionAndRemovedAllowlist(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	created := createWebappDocument(t, app, testChatID, "current")
	paths := []string{"/api/documents/clear", fmt.Sprintf("/api/documents/%d/delete", created)}
	for _, path := range paths {
		for _, wrongBearer := range []string{"", "not-a-session"} {
			status, body := settingsRequest(t, server.URL, http.MethodPost, path, wrongBearer, "", "")
			if status != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"session_expired"`) {
				t.Errorf("wrong session %s status=%d body=%s", path, status, body)
			}
		}
	}
	if got := webappDocumentCount(t, app, testChatID); got != 1 || webappDocumentChunkCount(t, app, created) < 2 {
		t.Fatalf("wrong sessions deleted documents or chunks: count=%d", got)
	}

	app, server = newTestApp(t)
	allowed := true
	app.auth.authorize = func(int64) bool { return allowed }
	bearer = launchSession(t, app, server)
	created = createWebappDocument(t, app, testChatID, "current")
	allowed = false
	for _, path := range []string{"/api/documents/clear", fmt.Sprintf("/api/documents/%d/delete", created)} {
		status, body := settingsRequest(t, server.URL, http.MethodPost, path, bearer, "", "")
		if status != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"session_expired"`) {
			t.Errorf("removed allowlist %s status=%d body=%s", path, status, body)
		}
	}
	if got := webappDocumentCount(t, app, testChatID); got != 1 || webappDocumentChunkCount(t, app, created) < 2 {
		t.Fatalf("removed allowlist requests deleted documents or chunks: count=%d", got)
	}
}

func TestDocumentMutationsReportStoreFailure(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	created := createWebappDocument(t, app, testChatID, "current")
	if err := app.db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/documents/clear", fmt.Sprintf("/api/documents/%d/delete", created)} {
		status, body := settingsRequest(t, server.URL, http.MethodPost, path, bearer, "", "")
		if status != http.StatusInternalServerError || !strings.Contains(string(body), `"code":"document_mutation_failed"`) {
			t.Errorf("store failure %s status=%d body=%s", path, status, body)
		}
		if strings.Contains(strings.ToLower(string(body)), "sql") || strings.Contains(string(body), "database is closed") {
			t.Errorf("store failure %s exposed internal details: %s", path, body)
		}
	}
}
