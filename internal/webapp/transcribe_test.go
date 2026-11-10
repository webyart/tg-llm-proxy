package webapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"bot/internal/bot"
	"bot/internal/model"
	"bot/internal/store"
)

// transcribeVoiceModel is a model.Client that can also transcribe: the webapp test wires a real
// bot.Dispatcher with it, so the endpoint exercises the same validation the Telegram voice handler uses.
type transcribeVoiceModel struct {
	transcript string
	err        error
	calls      int
	request    model.TranscriptionRequest
}

func (m *transcribeVoiceModel) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, fmt.Errorf("the transcription test must not call Complete")
}

func (m *transcribeVoiceModel) Models(context.Context) ([]string, error) {
	return []string{"fake-model"}, nil
}

func (m *transcribeVoiceModel) Transcribe(ctx context.Context, req model.TranscriptionRequest) (string, error) {
	m.calls++
	m.request = req
	if m.err != nil {
		return "", m.err
	}
	return m.transcript, nil
}

func newTranscribeApp(t *testing.T, voice *transcribeVoiceModel) (*App, *httptest.Server) {
	t.Helper()
	app, server := newTestAppWithRaw(t, map[string]string{"MEDIA_MAX_MB": "1"})
	app.SetChatBackend(bot.New(bot.Deps{Cfg: app.cfg, DB: app.db, Model: voice, Logger: app.logger, Token: app.cfg.BotToken}))
	return app, server
}

// transcribeMultipart builds one multipart body whose single part carries the recorded audio. The field
// name is explicit so the "wrong field" case can be built with the same helper.
func transcribeMultipart(t *testing.T, field, filename, contentType string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": field, "filename": filename}))
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
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

func transcribePost(t *testing.T, server *httptest.Server, bearer string, body io.Reader, contentType string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/transcribe", body)
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

// webappWebM is a WebM container prefix plus a marker string the "not stored" assertion can search for.
func webappWebM(marker string) []byte {
	data := []byte{0x1a, 0x45, 0xdf, 0xa3}
	data = append(data, []byte(marker)...)
	return append(data, make([]byte, 512)...)
}

func TestTranscribeReturnsTranscriptAndStoresNoAudio(t *testing.T) {
	voice := &transcribeVoiceModel{transcript: "  привет из Mini App  "}
	app, server := newTranscribeApp(t, voice)
	bearer := launchSession(t, app, server)
	const marker = "AUDIO-MARKER-MUST-NOT-BE-STORED"
	payload := webappWebM(marker)
	body, contentType := transcribeMultipart(t, "file", "voice.webm", "audio/webm;codecs=opus", payload)

	status, response := transcribePost(t, server, bearer, body, contentType)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, response)
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatalf("response=%s err=%v", response, err)
	}
	if decoded.Text != "привет из Mini App" {
		t.Fatalf("text=%q, want the trimmed transcript", decoded.Text)
	}
	if voice.calls != 1 || voice.request.FileName != "voice.webm" || voice.request.MIME != "audio/webm" || !bytes.Equal(voice.request.Data, payload) {
		t.Fatalf("transcription request = %+v", voice.request)
	}

	// The audio is not a message, not a RAG document and not an event detail.
	var messages, documents int
	if err := app.db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := app.db.QueryRow("SELECT COUNT(*) FROM rag_documents").Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || documents != 0 {
		t.Fatalf("audio was stored: messages=%d documents=%d", messages, documents)
	}
	rows, err := app.db.Query("SELECT detail FROM events")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var detail string
		if err := rows.Scan(&detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, marker) {
			t.Fatalf("the audio marker reached the audit: %q", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Successful mutations write the same metadata-only miniapp_mutation row as the other routes.
	if audited, err := store.CountEvents(context.Background(), app.db, "miniapp_mutation"); err != nil || audited != 1 {
		t.Fatalf("miniapp_mutation rows = %d err=%v, want 1", audited, err)
	}
	var detail string
	if err := app.db.QueryRow("SELECT detail FROM events WHERE kind = 'miniapp_mutation'").Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "operation=transcribe") || strings.Contains(detail, marker) {
		t.Fatalf("mutation audit = %q", detail)
	}
}

func TestTranscribeAuthenticationAndMethod(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)

	if status, body := transcribePost(t, server, "", nil, ""); status != http.StatusUnauthorized {
		t.Fatalf("missing bearer status=%d body=%s", status, body)
	}
	status, body, headers := apiRequest(t, server, http.MethodGet, "/api/transcribe", bearer)
	if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodPost {
		t.Fatalf("GET status=%d Allow=%q body=%s", status, headers.Get("Allow"), body)
	}
}

func TestTranscribeRejectsBadBodies(t *testing.T) {
	voice := &transcribeVoiceModel{transcript: "no"}
	app, server := newTranscribeApp(t, voice)
	bearer := launchSession(t, app, server)

	// Wrong Content-Type: 415 before the body is parsed.
	if status, body := transcribePost(t, server, bearer, strings.NewReader(`{"data":"x"}`), "application/json"); status != http.StatusUnsupportedMediaType {
		t.Fatalf("json body status=%d body=%s", status, body)
	} else if code := uploadErrorCode(t, body); code != "content_type" {
		t.Fatalf("json body code=%q", code)
	}

	// No file part at all.
	var empty bytes.Buffer
	writer := multipart.NewWriter(&empty)
	if err := writer.WriteField("name", "voice"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if status, body := transcribePost(t, server, bearer, &empty, writer.FormDataContentType()); status != http.StatusBadRequest {
		t.Fatalf("no-file status=%d body=%s", status, body)
	} else if code := uploadErrorCode(t, body); code != "invalid_body" {
		t.Fatalf("no-file code=%q", code)
	}

	// A file part under the wrong field name is not the audio part.
	body, contentType := transcribeMultipart(t, "audio", "voice.webm", "audio/webm", webappWebM("wrong-field"))
	if status, response := transcribePost(t, server, bearer, body, contentType); status != http.StatusBadRequest {
		t.Fatalf("wrong-field status=%d body=%s", status, response)
	} else if code := uploadErrorCode(t, response); code != "invalid_body" {
		t.Fatalf("wrong-field code=%q", code)
	}

	// A declared non-audio type and a mismatched container both map to unsupported_audio.
	for _, test := range []struct {
		name     string
		filename string
		mime     string
		data     []byte
	}{
		{name: "text", filename: "note.txt", mime: "text/plain", data: []byte(strings.Repeat("hello", 200))},
		{name: "bad signature", filename: "voice.webm", mime: "audio/webm", data: []byte(strings.Repeat("not-webm", 100))},
	} {
		body, contentType := transcribeMultipart(t, "file", test.filename, test.mime, test.data)
		status, response := transcribePost(t, server, bearer, body, contentType)
		if status != http.StatusBadRequest || uploadErrorCode(t, response) != "unsupported_audio" {
			t.Fatalf("%s status=%d body=%s", test.name, status, response)
		}
	}
	if voice.calls != 0 {
		t.Fatalf("the transcriber was called for a rejected file")
	}
}

func TestTranscribeRejectsTooShortAudio(t *testing.T) {
	voice := &transcribeVoiceModel{transcript: "no"}
	app, server := newTranscribeApp(t, voice)
	bearer := launchSession(t, app, server)
	body, contentType := transcribeMultipart(t, "file", "voice.webm", "audio/webm", []byte{0x1a, 0x45, 0xdf, 0xa3})
	status, response := transcribePost(t, server, bearer, body, contentType)
	if status != http.StatusBadRequest || uploadErrorCode(t, response) != "audio_too_short" {
		t.Fatalf("short audio status=%d body=%s", status, response)
	}
	if voice.calls != 0 {
		t.Fatalf("the transcriber was called for short audio")
	}
}

func TestTranscribeBodyOverMediaLimitIsTooLarge(t *testing.T) {
	voice := &transcribeVoiceModel{transcript: "no"}
	app, server := newTranscribeApp(t, voice)
	bearer := launchSession(t, app, server)
	// MEDIA_MAX_MB is 1 in this app: the body cap is 1 MiB + overhead, so 1.5 MiB is refused before parsing.
	body, contentType := transcribeMultipart(t, "file", "voice.webm", "audio/webm", webappWebM(strings.Repeat("x", 3<<19)))
	status, response := transcribePost(t, server, bearer, body, contentType)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d body=%s", status, response)
	}
	if code := uploadErrorCode(t, response); code != "body_too_large" {
		t.Fatalf("oversize code=%q", code)
	}
	if voice.calls != 0 {
		t.Fatalf("the transcriber was called for an oversize body")
	}
}

func TestTranscribeBackendErrorIsFiveHundred(t *testing.T) {
	voice := &transcribeVoiceModel{err: fmt.Errorf("whisper answered 502 with %s", "123:test")}
	app, server := newTranscribeApp(t, voice)
	bearer := launchSession(t, app, server)
	body, contentType := transcribeMultipart(t, "file", "voice.webm", "audio/webm", webappWebM("backend failure"))
	status, response := transcribePost(t, server, bearer, body, contentType)
	if status != http.StatusInternalServerError {
		t.Fatalf("backend error status=%d body=%s", status, response)
	}
	if code := uploadErrorCode(t, response); code != "transcription_failed" {
		t.Fatalf("backend error code=%q", code)
	}
	if strings.Contains(string(response), "123:test") || strings.Contains(string(response), "whisper answered") {
		t.Fatalf("provider detail leaked: %s", response)
	}
}

func TestTranscribeRejectsQueryAndEmptyTranscript(t *testing.T) {
	voice := &transcribeVoiceModel{transcript: "   "}
	app, server := newTranscribeApp(t, voice)
	bearer := launchSession(t, app, server)

	body, contentType := transcribeMultipart(t, "file", "voice.webm", "audio/webm", webappWebM("query"))
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/transcribe?chat_id=1", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", contentType)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusBadRequest || uploadErrorCode(t, payload) != "invalid_body" {
		t.Fatalf("query status=%d body=%s", response.StatusCode, payload)
	}

	body, contentType = transcribeMultipart(t, "file", "voice.webm", "audio/webm", webappWebM("empty transcript"))
	status, payload := transcribePost(t, server, bearer, body, contentType)
	if status != http.StatusBadRequest || uploadErrorCode(t, payload) != "audio_too_short" {
		t.Fatalf("empty transcript status=%d body=%s", status, payload)
	}
}

func TestTranscribeUsesTheMutationRateLimit(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	for index := 0; index < mutationRateLimit; index++ {
		if status, body := transcribePost(t, server, bearer, strings.NewReader(`{}`), "application/json"); status == http.StatusTooManyRequests {
			t.Fatalf("request %d was limited before the budget was spent: %s", index, body)
		}
	}
	// The 31st mutation in the minute is refused by the shared bucket before the handler sees the body.
	status, body, headers := apiRequest(t, server, http.MethodPost, "/api/transcribe", bearer)
	if status != http.StatusTooManyRequests || uploadErrorCode(t, body) != "rate_limited" || headers.Get("Retry-After") == "" {
		t.Fatalf("rate limit status=%d code=%q Retry-After=%q", status, uploadErrorCode(t, body), headers.Get("Retry-After"))
	}
}
