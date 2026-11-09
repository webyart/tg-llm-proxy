package webapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"

	"bot/internal/bot"
	"bot/internal/store"
)

// uploadMultipart builds one multipart body with a single file part and optional extra form fields. The file
// part MIME is explicit so documentIsText sees the same declared type a browser would send.
func uploadMultipart(t *testing.T, filename, contentType string, data []byte, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

func uploadRequest(t *testing.T, server *httptest.Server, bearer string, body io.Reader, contentType string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/documents", body)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, payload
}

type uploadResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Chunks   int    `json:"chunks"`
	Embedded bool   `json:"embedded"`
}

func decodeUpload(t *testing.T, body []byte) uploadResponse {
	t.Helper()
	var decoded uploadResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("upload response=%s err=%v", body, err)
	}
	return decoded
}

func uploadErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var decoded struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("error response=%s err=%v", body, err)
	}
	return decoded.Error.Code
}

type fakeUploadEmbeddingClient struct {
	mu    sync.Mutex
	calls int
	err   error
	model string
}

func (f *fakeUploadEmbeddingClient) Model() string {
	if f.model == "" {
		return "fake-upload-2d"
	}
	return f.model
}

func (f *fakeUploadEmbeddingClient) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vectors[index] = []float32{1, 0}
	}
	return vectors, nil
}

func TestDocumentUploadRequiresSessionAndUsesTheSessionChat(t *testing.T) {
	app, server := newTestApp(t)
	content := "Aurora deployment notes live in this chat."
	payload, contentType := uploadMultipart(t, "notes.txt", "text/plain", []byte(content),
		map[string]string{"chat_id": "9999", "user_id": "8888"})

	status, body := uploadRequest(t, server, "", bytes.NewReader(payload.Bytes()), contentType)
	if status != http.StatusUnauthorized || uploadErrorCode(t, body) != "session_expired" {
		t.Fatalf("upload without a session status=%d body=%s", status, body)
	}
	if got := webappDocumentCount(t, app, testChatID); got != 0 {
		t.Fatalf("unauthenticated upload stored %d documents", got)
	}

	bearer := launchSession(t, app, server)
	status, body = uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
	if status != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", status, body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded) != 4 {
		t.Fatalf("upload response=%s err=%v want exactly id/name/chunks/embedded", body, err)
	}
	result := decodeUpload(t, body)
	if result.ID <= 0 || result.Name != "notes.txt" || result.Chunks < 1 || result.Embedded {
		t.Fatalf("upload result=%+v", result)
	}
	if strings.Contains(string(body), content) || strings.Contains(string(body), "Aurora") {
		t.Fatalf("upload response echoed the document text: %s", body)
	}
	var storedChat int64
	if err := app.db.QueryRowContext(context.Background(), `SELECT chat_id FROM rag_documents WHERE id = ?`, result.ID).Scan(&storedChat); err != nil {
		t.Fatal(err)
	}
	if storedChat != testChatID {
		t.Fatalf("stored chat_id=%d want the session chat %d", storedChat, testChatID)
	}
	if webappDocumentCount(t, app, 9999) != 0 {
		t.Fatal("a body chat_id selected the storage chat")
	}
	if got := webappDocumentChunkCount(t, app, result.ID); got != result.Chunks {
		t.Fatalf("stored chunks=%d response=%d", got, result.Chunks)
	}
}

func TestDocumentUploadRejectsEmptyBinaryAndOversizedText(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	for _, test := range []struct {
		name        string
		filename    string
		contentType string
		data        []byte
		wantCode    string
	}{
		{"empty file", "empty.txt", "text/plain", nil, "document_empty"},
		{"whitespace only", "blank.txt", "text/plain", []byte(" \n\t "), "document_empty"},
		{"NUL byte", "nul.txt", "text/plain", []byte("text\x00more"), "unsupported_document"},
		{"invalid UTF-8", "bad.txt", "text/plain", []byte{0xff, 0xfe, 0xfd}, "unsupported_document"},
		{"binary MIME", "scan.pdf", "application/pdf", []byte("plain looking text"), "unsupported_document"},
		{"oversized text", "big.txt", "text/plain", bytes.Repeat([]byte("a"), int(bot.DocumentMaxBytes)+1), "document_too_large"},
	} {
		payload, contentType := uploadMultipart(t, test.filename, test.contentType, test.data, nil)
		status, body := uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
		if status != http.StatusBadRequest || uploadErrorCode(t, body) != test.wantCode {
			t.Errorf("%s status=%d code=%s body=%s", test.name, status, uploadErrorCode(t, body), body)
		}
	}
	if got := webappDocumentCount(t, app, testChatID); got != 0 {
		t.Fatalf("rejected uploads stored %d documents", got)
	}
}

func TestDocumentUploadBodyOverTheCapReturns413(t *testing.T) {
	app, server := newTestAppWithRaw(t, map[string]string{"MEDIA_MAX_MB": "1"})
	bearer := launchSession(t, app, server)
	capBytes := documentUploadBodyLimit(app.cfg)
	// The file itself is larger than the whole request cap, so reading the multipart body must hit 413 before
	// the per-document text limit can turn it into a 400.
	payload, contentType := uploadMultipart(t, "huge.txt", "text/plain", bytes.Repeat([]byte("a"), int(capBytes)), nil)
	if int64(payload.Len()) <= capBytes {
		t.Fatalf("test body=%d must exceed the cap=%d", payload.Len(), capBytes)
	}
	status, body := uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
	if status != http.StatusRequestEntityTooLarge || uploadErrorCode(t, body) != "body_too_large" {
		t.Fatalf("oversized body status=%d code=%s body=%s", status, uploadErrorCode(t, body), body)
	}
}

func TestDocumentUploadEnforcesThePerChatLimit(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	for index := 0; index < int(app.cfg.RAGMaxDocuments); index++ {
		if _, _, err := store.PutRAGDocument(context.Background(), app.db, testChatID, testUserID,
			fmt.Sprintf("existing-%d.txt", index), "existing document"); err != nil {
			t.Fatal(err)
		}
	}
	payload, contentType := uploadMultipart(t, "one-too-many.txt", "text/plain", []byte("new document"), nil)
	status, body := uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
	if status != http.StatusConflict || uploadErrorCode(t, body) != "document_limit" {
		t.Fatalf("limit status=%d code=%s body=%s", status, uploadErrorCode(t, body), body)
	}
	if got := webappDocumentCount(t, app, testChatID); got != int(app.cfg.RAGMaxDocuments) {
		t.Fatalf("documents=%d want=%d", got, app.cfg.RAGMaxDocuments)
	}
	// Replacing an existing name is not a new document, so it stays allowed at the limit (the store rule).
	payload, contentType = uploadMultipart(t, "existing-0.txt", "text/plain", []byte("replacement text"), nil)
	status, body = uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
	if status != http.StatusCreated {
		t.Fatalf("replace at limit status=%d body=%s", status, body)
	}
	if got := webappDocumentCount(t, app, testChatID); got != int(app.cfg.RAGMaxDocuments) {
		t.Fatalf("replace at limit documents=%d want=%d", got, app.cfg.RAGMaxDocuments)
	}
}

func TestDocumentUploadSameNameReplacesTheDocumentPerStoreRules(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	first, contentType := uploadMultipart(t, "handbook.txt", "text/plain", []byte("short first version"), nil)
	status, body := uploadRequest(t, server, bearer, bytes.NewReader(first.Bytes()), contentType)
	if status != http.StatusCreated {
		t.Fatalf("first upload status=%d body=%s", status, body)
	}
	firstResult := decodeUpload(t, body)

	secondContent := strings.Repeat("longer replacement sentence. ", 120)
	second, contentType := uploadMultipart(t, "handbook.txt", "text/plain", []byte(secondContent), nil)
	status, body = uploadRequest(t, server, bearer, bytes.NewReader(second.Bytes()), contentType)
	if status != http.StatusCreated {
		t.Fatalf("second upload status=%d body=%s", status, body)
	}
	secondResult := decodeUpload(t, body)
	if secondResult.ID != firstResult.ID {
		t.Fatalf("replacing by name changed the id: first=%d second=%d", firstResult.ID, secondResult.ID)
	}
	if webappDocumentCount(t, app, testChatID) != 1 {
		t.Fatalf("replace stored %d documents", webappDocumentCount(t, app, testChatID))
	}
	if got := webappDocumentChunkCount(t, app, secondResult.ID); got != secondResult.Chunks || got < 2 {
		t.Fatalf("replace chunks=%d response=%d want at least 2", got, secondResult.Chunks)
	}
	var content string
	if err := app.db.QueryRowContext(context.Background(), `SELECT content FROM rag_chunks WHERE document_id = ? ORDER BY ordinal LIMIT 1`, secondResult.ID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "longer replacement") {
		t.Fatalf("the stored chunk still holds the old text: %q", content)
	}
}

func TestDocumentUploadReportsEmbeddedAndKeepsTheDocumentLexicalOnFailure(t *testing.T) {
	t.Run("no embeddings client", func(t *testing.T) {
		app, server := newTestApp(t)
		bearer := launchSession(t, app, server)
		payload, contentType := uploadMultipart(t, "plain.txt", "text/plain", []byte("plain lexical document"), nil)
		status, body := uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
		if status != http.StatusCreated || decodeUpload(t, body).Embedded {
			t.Fatalf("status=%d body=%s", status, body)
		}
	})
	t.Run("fake embeddings server", func(t *testing.T) {
		app, server := newTestApp(t)
		fake := &fakeUploadEmbeddingClient{}
		app.SetEmbeddings(fake)
		bearer := launchSession(t, app, server)
		payload, contentType := uploadMultipart(t, "embedded.txt", "text/plain", []byte("document with a vector"), nil)
		status, body := uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
		if status != http.StatusCreated {
			t.Fatalf("status=%d body=%s", status, body)
		}
		result := decodeUpload(t, body)
		if !result.Embedded || fake.calls == 0 {
			t.Fatalf("embedded=%v calls=%d", result.Embedded, fake.calls)
		}
		embedded, total, err := store.EmbeddingStats(context.Background(), app.db, testChatID, fake.Model())
		if err != nil || embedded != total || embedded != result.Chunks {
			t.Fatalf("embedding stats=%d/%d chunks=%d err=%v", embedded, total, result.Chunks, err)
		}
	})
	t.Run("failing embeddings server", func(t *testing.T) {
		app, server := newTestApp(t)
		app.SetEmbeddings(&fakeUploadEmbeddingClient{err: errors.New("embedding server is down")})
		bearer := launchSession(t, app, server)
		payload, contentType := uploadMultipart(t, "fallback.txt", "text/plain", []byte("still searchable lexically"), nil)
		status, body := uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
		if status != http.StatusCreated || decodeUpload(t, body).Embedded {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if webappDocumentCount(t, app, testChatID) != 1 {
			t.Fatal("a failed embedding removed the document")
		}
	})
}

func TestDocumentUploadNameOverrideAndRequestContract(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)

	payload, contentType := uploadMultipart(t, "original.txt", "text/plain", []byte("name override body"),
		map[string]string{"name": "renamed.md"})
	status, body := uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
	if status != http.StatusCreated || decodeUpload(t, body).Name != "renamed.md" {
		t.Fatalf("name override status=%d body=%s", status, body)
	}

	// The list endpoint is unchanged and GET still works after adding POST.
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/documents?limit=10", bearer)
	if status != http.StatusOK || !strings.Contains(string(body), "renamed.md") {
		t.Fatalf("GET documents status=%d body=%s", status, body)
	}
	status, _, headers := apiRequest(t, server, http.MethodPut, "/api/documents", bearer)
	if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodGet+", "+http.MethodPost {
		t.Fatalf("PUT documents status=%d Allow=%q", status, headers.Get("Allow"))
	}
	status, body, _ = apiRequest(t, server, http.MethodPost, "/api/documents?chat_id=9999", bearer)
	if status != http.StatusBadRequest || uploadErrorCode(t, body) != "invalid_body" {
		t.Fatalf("query status=%d body=%s", status, body)
	}

	status, body = uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), "application/json")
	if status != http.StatusUnsupportedMediaType || uploadErrorCode(t, body) != "content_type" {
		t.Fatalf("wrong content type status=%d body=%s", status, body)
	}

	empty, emptyType := uploadMultipart(t, "", "text/plain", []byte("x"), nil)
	status, body = uploadRequest(t, server, bearer, bytes.NewReader(empty.Bytes()), emptyType)
	if status != http.StatusBadRequest || uploadErrorCode(t, body) != "invalid_body" {
		t.Fatalf("missing filename status=%d body=%s", status, body)
	}

	var duplicates bytes.Buffer
	writer := multipart.NewWriter(&duplicates)
	for index := 0; index < 2; index++ {
		part, err := writer.CreateFormFile("file", fmt.Sprintf("d%d.txt", index))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("duplicate")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	status, body = uploadRequest(t, server, bearer, bytes.NewReader(duplicates.Bytes()), writer.FormDataContentType())
	if status != http.StatusBadRequest || uploadErrorCode(t, body) != "invalid_body" {
		t.Fatalf("duplicate file status=%d body=%s", status, body)
	}
}
