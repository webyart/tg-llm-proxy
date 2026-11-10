package webapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"bot/internal/bot"
)

// gatedStreamBackend blocks only the FIRST chat answer until release, so a test can hold one
// user's SSE stream open while another request is admitted (or refused) against the same server.
type gatedStreamBackend struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedStreamBackend() *gatedStreamBackend {
	return &gatedStreamBackend{started: make(chan struct{}), release: make(chan struct{})}
}

func (b *gatedStreamBackend) AnswerForMiniApp(ctx context.Context, chatID, userID int64, text string, emit func(bot.MiniAppStreamEvent) error) error {
	b.mu.Lock()
	first := b.calls == 0
	b.calls++
	b.mu.Unlock()
	if first {
		b.once.Do(func() { close(b.started) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return emit(bot.MiniAppStreamEvent{Delta: "готово", Done: true})
}

func (b *gatedStreamBackend) ResolveMiniAppApproval(context.Context, string, int64, int64, bool) error {
	return nil
}

func (b *gatedStreamBackend) ResetContextForMiniApp(context.Context, int64, int64) error {
	return nil
}

func (b *gatedStreamBackend) SkipPendingForMiniApp(context.Context, int64, int64, bool) (bool, error) {
	return false, nil
}

func (b *gatedStreamBackend) TranscribeForMiniApp(context.Context, int64, int64, []byte, string, string) (string, error) {
	return "", nil
}

func (b *gatedStreamBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

type rawChatResult struct {
	status int
	body   []byte
	header http.Header
	err    error
}

// rawChat does not touch *testing.T so it is safe inside a goroutine that serves the held stream.
func rawChat(server *httptest.Server, bearer string, text string) rawChatResult {
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/messages", strings.NewReader(`{"text":"`+text+`"}`))
	if err != nil {
		return rawChatResult{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(req)
	if err != nil {
		return rawChatResult{err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return rawChatResult{status: response.StatusCode, body: body, header: response.Header, err: err}
}

func awaitStreamStart(t *testing.T, backend *gatedStreamBackend) {
	t.Helper()
	select {
	case <-backend.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first Mini App stream never reached the backend")
	}
}

// TestChatStreamsAreCappedPerUser — one user may hold at most one active stream; the second gets
// 429/stream_limit without reaching the backend, while another user's stream still passes.
func TestChatStreamsAreCappedPerUser(t *testing.T) {
	app, server := newTestApp(t)
	backend := newGatedStreamBackend()
	app.SetChatBackend(backend)
	bearerA := launchSession(t, app, server)
	bearerB := launchSessionFor(t, app, server, otherTestUserID, otherTestUserID)

	firstDone := make(chan rawChatResult, 1)
	go func() { firstDone <- rawChat(server, bearerA, "первый") }()
	awaitStreamStart(t, backend)

	status, body, headers := chatRequest(t, server, http.MethodPost, "/api/messages", bearerA, `{"text":"второй"}`, "application/json")
	if status != http.StatusTooManyRequests {
		t.Fatalf("second concurrent stream status=%d body=%s, want 429", status, body)
	}
	if got := headers.Get("Retry-After"); got == "" {
		t.Error("stream_limit response misses Retry-After")
	}
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &failure); err != nil || failure.Error.Code != "stream_limit" {
		t.Fatalf("second concurrent stream code=%q err=%v body=%s, want stream_limit", failure.Error.Code, err, body)
	}
	if got := backend.callCount(); got != 1 {
		t.Fatalf("model backend calls after the refused stream = %d, want 1", got)
	}

	// Another user is not affected by the first user's active stream.
	status, body, _ = chatRequest(t, server, http.MethodPost, "/api/messages", bearerB, `{"text":"другой"}`, "application/json")
	if status != http.StatusOK {
		t.Fatalf("another user's stream status=%d body=%s, want 200", status, body)
	}
	if !strings.Contains(string(body), `event: done`) {
		t.Fatalf("another user's stream has no done event: %s", body)
	}

	close(backend.release)
	first := <-firstDone
	if first.err != nil || first.status != http.StatusOK {
		t.Fatalf("first stream status=%d err=%v body=%s", first.status, first.err, first.body)
	}
	if !strings.Contains(string(first.body), `event: done`) {
		t.Fatalf("first stream has no done event: %s", first.body)
	}

	// The slot is released after done: the same user may start the next answer.
	status, body, _ = chatRequest(t, server, http.MethodPost, "/api/messages", bearerA, `{"text":"третий"}`, "application/json")
	if status != http.StatusOK {
		t.Fatalf("stream after the first finished status=%d body=%s, want 200", status, body)
	}
}

// TestUploadSlotsAreIndependentFromStreamSlots — a held upload/transcription slot must not consume
// the JSON/SSE slot; the upload class itself is bounded separately.
func TestUploadSlotsAreIndependentFromStreamSlots(t *testing.T) {
	entered := make(chan string, 1)
	release := make(chan struct{})
	handler := limitInFlightSplit(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if isUploadRoute(req) {
			entered <- req.URL.Path
			<-release
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}), 1, 1, 0, 0)

	uploadDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/documents", nil))
		close(uploadDone)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow upload never acquired its slot")
	}

	// The upload class is full: a second large-body request is refused.
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/transcribe", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("second upload status=%d body=%s, want 503", response.Code, response.Body.String())
	}

	// The JSON/SSE class still has its own slot even though the upload is holding one.
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/messages", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s while an upload is in flight, want 200", response.Code, response.Body.String())
	}

	close(release)
	<-uploadDone
}
