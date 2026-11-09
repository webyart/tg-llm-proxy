package webapp

import (
	"archive/zip"
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
// part MIME is explicit so ExtractDocumentText sees the same declared type a browser would send.
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
		{"binary MIME", "scan.pdf", "application/pdf", []byte("plain looking text"), "invalid_document"},
		{"legacy doc", "old.doc", "application/msword", []byte{0xD0, 0xCF, 0x11, 0xE0}, "unsupported_document"},
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

// uploadTestPDF builds a minimal one-page PDF with an uncompressed text stream. padding > 0 emits a PDF comment
// of that many bytes, which lets a test cross the 64 KiB text limit without committing a binary fixture.
func uploadTestPDF(content string, padding int) []byte {
	var buffer bytes.Buffer
	buffer.WriteString("%PDF-1.4\n")
	if padding > 0 {
		buffer.WriteByte('%')
		buffer.Write(bytes.Repeat([]byte("p"), padding))
		buffer.WriteByte('\n')
	}
	offsets := make([]int, 6)
	object := func(number int, body string) {
		offsets[number] = buffer.Len()
		fmt.Fprintf(&buffer, "%d 0 obj\n%s\nendobj\n", number, body)
	}
	object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>")
	object(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	object(5, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	xref := buffer.Len()
	buffer.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for number := 1; number <= 5; number++ {
		fmt.Fprintf(&buffer, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&buffer, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return buffer.Bytes()
}

// uploadTestDOCX builds a real zip package with word/document.xml and one paragraph per argument.
func uploadTestDOCX(paragraphs ...string) []byte {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("word/document.xml")
	if err != nil {
		panic(err)
	}
	var document strings.Builder
	document.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	document.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, paragraph := range paragraphs {
		document.WriteString(`<w:p><w:r><w:t>`)
		document.WriteString(paragraph)
		document.WriteString(`</w:t></w:r></w:p>`)
	}
	document.WriteString(`</w:body></w:document>`)
	if _, err := io.WriteString(entry, document.String()); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

// repeatedByteReader generates one byte up to a fixed total length, so an over-limit request can be sent without
// allocating the body twice in the test process.
type repeatedByteReader struct{ remaining int64 }

func (r *repeatedByteReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	for index := range p {
		p[index] = 'a'
	}
	r.remaining -= int64(len(p))
	return len(p), nil
}

func streamingUploadBody(filename string, contentType string, size int64) (io.Reader, string) {
	const boundary = "----documentUploadTestBoundary"
	prefix := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"%s\"\r\nContent-Type: %s\r\n\r\n",
		boundary, filename, contentType)
	suffix := fmt.Sprintf("\r\n--%s--\r\n", boundary)
	return io.MultiReader(strings.NewReader(prefix), &repeatedByteReader{remaining: size}, strings.NewReader(suffix)),
		"multipart/form-data; boundary=" + boundary
}

func TestDocumentUploadAcceptsPDFAndStoresExtractedText(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	// The 1 MiB comment makes the file larger than the 64 KiB text limit: it is accepted only because a PDF gets
	// the 20 MiB binary limit.
	data := uploadTestPDF("BT /F1 18 Tf 72 720 Td (Mini App Aurora PDF) Tj ET", 1<<20)
	if int64(len(data)) <= bot.DocumentMaxBytes {
		t.Fatalf("fixture is %d bytes, it must exceed the text limit %d", len(data), bot.DocumentMaxBytes)
	}
	payload, contentType := uploadMultipart(t, "manual.pdf", "application/pdf", data, nil)
	status, body := uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
	if status != http.StatusCreated {
		t.Fatalf("pdf upload status=%d body=%s", status, body)
	}
	result := decodeUpload(t, body)
	if result.Name != "manual.pdf" || result.Chunks < 1 || result.Embedded {
		t.Fatalf("pdf upload result=%+v", result)
	}
	var content string
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT content FROM rag_chunks WHERE document_id = ? ORDER BY ordinal LIMIT 1`, result.ID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Mini App Aurora PDF") || strings.Contains(content, "%PDF") {
		t.Fatalf("stored chunk = %q, want the extracted text rather than the PDF bytes", content)
	}
}

func TestDocumentUploadAcceptsDOCXAndStoresExtractedText(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	payload, contentType := uploadMultipart(t, "notes.docx",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		uploadTestDOCX("DOCX first line", "DOCX second line"), nil)
	status, body := uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
	if status != http.StatusCreated {
		t.Fatalf("docx upload status=%d body=%s", status, body)
	}
	result := decodeUpload(t, body)
	var content string
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT content FROM rag_chunks WHERE document_id = ? ORDER BY ordinal LIMIT 1`, result.ID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "DOCX first line") || !strings.Contains(content, "DOCX second line") {
		t.Fatalf("stored chunk = %q", content)
	}
}

func TestDocumentUploadRejectsAScanOnlyPDFAndStoresNothing(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	payload, contentType := uploadMultipart(t, "scan.pdf", "application/pdf",
		uploadTestPDF("q 0 0 612 792 re f Q", 0), nil)
	status, body := uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
	if status != http.StatusBadRequest || uploadErrorCode(t, body) != "document_no_text" {
		t.Fatalf("scan-only pdf status=%d code=%s body=%s", status, uploadErrorCode(t, body), body)
	}
	if got := webappDocumentCount(t, app, testChatID); got != 0 {
		t.Fatalf("scan-only pdf stored %d documents", got)
	}
}

// TestDocumentUploadRejectsABinaryDocumentOverTheLimit streams a real multipart body just over
// bot.DocumentBinaryMaxBytes: the request stays under the body cap (20 MiB + 1 MiB overhead), so the handler must
// read it and let the shared ingest helper answer 400 document_too_large. A 5 MiB .txt then proves that raising
// the binary limit did not raise the text limit. Streaming avoids allocating a second 20 MiB buffer in the test.
func TestDocumentUploadRejectsABinaryDocumentOverTheLimit(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	body, contentType := streamingUploadBody("big.pdf", "application/pdf", bot.DocumentBinaryMaxBytes+1)
	status, payload := uploadRequest(t, server, bearer, body, contentType)
	if status != http.StatusBadRequest || uploadErrorCode(t, payload) != "document_too_large" {
		t.Fatalf("over-limit pdf status=%d code=%s body=%s", status, uploadErrorCode(t, payload), payload)
	}
	body, contentType = streamingUploadBody("big.txt", "text/plain", 5<<20)
	status, payload = uploadRequest(t, server, bearer, body, contentType)
	if status != http.StatusBadRequest || uploadErrorCode(t, payload) != "document_too_large" {
		t.Fatalf("5 MiB text status=%d code=%s body=%s", status, uploadErrorCode(t, payload), payload)
	}
	if got := webappDocumentCount(t, app, testChatID); got != 0 {
		t.Fatalf("rejected uploads stored %d documents", got)
	}
}

func TestDocumentUploadBodyOverTheCapReturns413(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	capBytes := documentUploadBodyLimit(app.cfg)
	// The file itself is larger than the whole request cap, so reading the multipart body must hit 413 before
	// the per-document binary limit can turn it into a 400. The body is generated on the fly; no large fixture
	// is committed and the test process never holds the whole request.
	body, contentType := streamingUploadBody("huge.pdf", "application/pdf", capBytes+1)
	status, payload := uploadRequest(t, server, bearer, body, contentType)
	if status != http.StatusRequestEntityTooLarge || uploadErrorCode(t, payload) != "body_too_large" {
		t.Fatalf("oversized body status=%d code=%s body=%s", status, uploadErrorCode(t, payload), payload)
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

// TestDocumentUploadAcceptsAVendorMIMEWithATextualExtension — a phone often labels .csv as
// application/vnd.ms-excel and .md as application/x-markdown; the explicit extension plus valid UTF-8 must be
// enough for the upload to succeed.
func TestDocumentUploadAcceptsAVendorMIMEWithATextualExtension(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	for _, test := range []struct {
		name    string
		mime    string
		content string
	}{
		{name: "report.csv", mime: "application/vnd.ms-excel", content: "name,value\none,1\n"},
		{name: "notes.md", mime: "application/x-markdown", content: "# notes\nbody\n"},
	} {
		payload, contentType := uploadMultipart(t, test.name, test.mime, []byte(test.content), nil)
		status, body := uploadRequest(t, server, bearer, bytes.NewReader(payload.Bytes()), contentType)
		if status != http.StatusCreated {
			t.Fatalf("%s upload status=%d body=%s", test.name, status, body)
		}
	}
}
