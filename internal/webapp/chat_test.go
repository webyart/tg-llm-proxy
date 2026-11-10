package webapp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"bot/internal/bot"
	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
)

type recordedApproval struct {
	token   string
	chatID  int64
	userID  int64
	approve bool
}

type fakeChatBackend struct {
	mu sync.Mutex

	deltas    []string
	stage     string
	final     string
	err       error
	emitError bool

	calls  int
	chatID int64
	userID int64
	text   string

	resets     int
	resetErr   error
	skips      []bool
	skipSet    bool
	skipErr    error
	approvals  []recordedApproval
	approveErr error

	transcribeText  string
	transcribeErr   error
	transcribeCalls int
	transcribeData  []byte
	transcribeName  string
	transcribeMIME  string
}

func (f *fakeChatBackend) AnswerForMiniApp(ctx context.Context, chatID, userID int64, text string, emit func(bot.MiniAppStreamEvent) error) error {
	f.mu.Lock()
	f.calls++
	f.chatID, f.userID, f.text = chatID, userID, text
	deltas := append([]string{}, f.deltas...)
	stage, final := f.stage, f.final
	err, emitError := f.err, f.emitError
	f.mu.Unlock()

	if stage != "" {
		if emitErr := emit(bot.MiniAppStreamEvent{Stage: stage}); emitErr != nil {
			return emitErr
		}
	}
	for _, delta := range deltas {
		if emitErr := emit(bot.MiniAppStreamEvent{Delta: delta}); emitErr != nil {
			return emitErr
		}
	}
	if err != nil {
		if emitError {
			if emitErr := emit(bot.MiniAppStreamEvent{Error: "safe failure"}); emitErr != nil {
				return emitErr
			}
		}
		return err
	}
	return emit(bot.MiniAppStreamEvent{Delta: final, Done: true})
}

func (f *fakeChatBackend) ResolveMiniAppApproval(ctx context.Context, token string, chatID, userID int64, approve bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approvals = append(f.approvals, recordedApproval{token: token, chatID: chatID, userID: userID, approve: approve})
	return f.approveErr
}

func (f *fakeChatBackend) ResetContextForMiniApp(ctx context.Context, chatID, userID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resets++
	return f.resetErr
}

func (f *fakeChatBackend) SkipPendingForMiniApp(ctx context.Context, chatID, userID int64, skip bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.skips = append(f.skips, skip)
	return f.skipSet, f.skipErr
}

func (f *fakeChatBackend) TranscribeForMiniApp(ctx context.Context, chatID, userID int64, data []byte, filename, mime string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transcribeCalls++
	f.chatID, f.userID = chatID, userID
	f.transcribeData = append([]byte{}, data...)
	f.transcribeName = filename
	f.transcribeMIME = mime
	return f.transcribeText, f.transcribeErr
}

func (f *fakeChatBackend) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeChatBackend) skipCalls() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool{}, f.skips...)
}

func chatRequest(t *testing.T, server *httptest.Server, method string, path string, bearer string, body string, contentType string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
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
	responseBody, err := readAll(response)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, responseBody, response.Header
}

func readAll(response *http.Response) ([]byte, error) {
	return io.ReadAll(response.Body)
}

type chatSSEEvent struct {
	name string
	data map[string]any
}

func parseChatSSE(t *testing.T, body []byte) []chatSSEEvent {
	t.Helper()
	events := []chatSSEEvent{}
	for _, block := range strings.Split(string(body), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		event := chatSSEEvent{name: "message"}
		lines := []string{}
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				event.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if len(lines) == 0 {
			continue
		}
		if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &event.data); err != nil {
			t.Fatalf("SSE data is not JSON: %q (%v)", strings.Join(lines, "\n"), err)
		}
		events = append(events, event)
	}
	return events
}

func TestChatEndpointsRequireAuthentication(t *testing.T) {
	app, server := newTestApp(t)
	app.SetChatBackend(&fakeChatBackend{})
	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/messages", `{"text":"hi"}`},
		{http.MethodPost, "/api/context/reset", ""},
		{http.MethodPost, "/api/pending/skip", `{"skip":true}`},
		{http.MethodGet, "/api/whoami", ""},
	} {
		status, body, _ := chatRequest(t, server, test.method, test.path, "", test.body, "application/json")
		if status != http.StatusUnauthorized {
			t.Errorf("%s %s without a bearer token status=%d body=%s", test.method, test.path, status, body)
		}
	}
}

func TestChatMessageRequiresStrictJSONAndBoundedText(t *testing.T) {
	app, server := newTestApp(t)
	backend := &fakeChatBackend{final: "answer"}
	app.SetChatBackend(backend)
	bearer := launchSession(t, app, server)

	for _, test := range []struct {
		name        string
		method      string
		path        string
		body        string
		contentType string
		want        int
	}{
		{"unknown field chat_id", http.MethodPost, "/api/messages", `{"text":"hi","chat_id":123}`, "application/json", http.StatusBadRequest},
		{"empty text", http.MethodPost, "/api/messages", `{"text":""}`, "application/json", http.StatusBadRequest},
		{"whitespace text", http.MethodPost, "/api/messages", `{"text":"   "}`, "application/json", http.StatusBadRequest},
		{"too long", http.MethodPost, "/api/messages", `{"text":"` + strings.Repeat("я", bot.MiniAppMaxTextRunes+1) + `"}`, "application/json", http.StatusBadRequest},
		{"NUL", http.MethodPost, "/api/messages", `{"text":"a\u0000b"}`, "application/json", http.StatusBadRequest},
		{"trailing JSON", http.MethodPost, "/api/messages", `{"text":"hi"}{"text":"again"}`, "application/json", http.StatusBadRequest},
		{"malformed JSON", http.MethodPost, "/api/messages", `{"text":`, "application/json", http.StatusBadRequest},
		{"wrong content type", http.MethodPost, "/api/messages", `{"text":"hi"}`, "text/plain", http.StatusUnsupportedMediaType},
		{"query chat id", http.MethodPost, "/api/messages?chat_id=999", `{"text":"hi"}`, "application/json", http.StatusBadRequest},
		{"wrong method", http.MethodGet, "/api/messages", "", "", http.StatusMethodNotAllowed},
		{"body too large", http.MethodPost, "/api/messages", `{"text":"` + strings.Repeat("a", maxBodyBytes+1024) + `"}`, "application/json", http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, body, _ := chatRequest(t, server, test.method, test.path, bearer, test.body, test.contentType)
			if status != test.want {
				t.Fatalf("status=%d want=%d body=%s", status, test.want, body)
			}
			if backend.callCount() != 0 {
				t.Fatalf("a rejected request reached the chat backend (%d calls)", backend.callCount())
			}
		})
	}
}

func TestChatMessageStreamsDeltaStageAndDoneForSessionScope(t *testing.T) {
	app, server := newTestApp(t)
	backend := &fakeChatBackend{stage: "thinking", deltas: []string{"При", "вет"}, final: "Привет"}
	app.SetChatBackend(backend)
	bearer := launchSession(t, app, server)

	status, body, headers := chatRequest(t, server, http.MethodPost, "/api/messages", bearer, `{"text":"  привет  "}`, "application/json")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if got := headers.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type=%q, want text/event-stream", got)
	}
	if got := headers.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q", got)
	}
	events := parseChatSSE(t, body)
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.name)
	}
	if got := strings.Join(names, ","); got != "stage,delta,delta,done" {
		t.Fatalf("event order = %s, want stage,delta,delta,done (%s)", got, body)
	}
	if events[0].data["stage"] != "thinking" {
		t.Errorf("stage data = %+v", events[0].data)
	}
	if events[1].data["text"] != "При" || events[2].data["text"] != "вет" {
		t.Errorf("delta data = %+v %+v", events[1].data, events[2].data)
	}
	if events[3].data["text"] != "Привет" {
		t.Errorf("done data = %+v", events[3].data)
	}

	backend.mu.Lock()
	gotChat, gotUser, gotText := backend.chatID, backend.userID, backend.text
	backend.mu.Unlock()
	if gotChat != testChatID || gotUser != testUserID {
		t.Fatalf("backend scope chat=%d user=%d, want the session scope %d/%d", gotChat, gotUser, testChatID, testUserID)
	}
	if gotText != "привет" {
		t.Fatalf("backend text = %q, want the trimmed session message", gotText)
	}
}

func TestChatMutationRateLimitAppliesBeforeTheBackend(t *testing.T) {
	app, server := newTestApp(t)
	backend := &fakeChatBackend{final: "answer"}
	app.SetChatBackend(backend)
	bearer := launchSession(t, app, server)

	for index := 0; index < mutationRateLimit; index++ {
		if allowed, _ := app.rateLimits.allow(testUserID, false); !allowed {
			t.Fatalf("prefill allowance %d was rejected", index)
		}
	}
	status, body, headers := chatRequest(t, server, http.MethodPost, "/api/messages", bearer, `{"text":"hi"}`, "application/json")
	if status != http.StatusTooManyRequests {
		t.Fatalf("status=%d want 429 body=%s", status, body)
	}
	if headers.Get("Retry-After") == "" {
		t.Error("429 response misses Retry-After")
	}
	if backend.callCount() != 0 {
		t.Fatalf("rate-limited request reached the backend %d times", backend.callCount())
	}
}

func TestChatAdmissionFailuresUseHTTPStatuses(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{"denied", bot.ErrMiniAppDenied, http.StatusForbidden},
		{"rate limited", &bot.MiniAppRateLimitError{RetryAfter: 1500 * time.Millisecond}, http.StatusTooManyRequests},
		{"text too long", bot.ErrMiniAppTextTooLong, http.StatusBadRequest},
		{"unavailable", bot.ErrMiniAppUnavailable, http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, server := newTestApp(t)
			app.SetChatBackend(&fakeChatBackend{err: test.err})
			bearer := launchSession(t, app, server)
			status, body, headers := chatRequest(t, server, http.MethodPost, "/api/messages", bearer, `{"text":"hi"}`, "application/json")
			if status != test.want {
				t.Fatalf("status=%d want=%d body=%s", status, test.want, body)
			}
			if test.want == http.StatusTooManyRequests {
				if got := headers.Get("Retry-After"); got != "2" {
					t.Errorf("Retry-After=%q, want 2", got)
				}
			}
		})
	}
}

func TestChatErrorEventIsSingleAndSanitized(t *testing.T) {
	app, server := newTestApp(t)
	backend := &fakeChatBackend{deltas: []string{"часть"}, err: context.DeadlineExceeded, emitError: true}
	app.SetChatBackend(backend)
	bearer := launchSession(t, app, server)

	status, body, headers := chatRequest(t, server, http.MethodPost, "/api/messages", bearer, `{"text":"hi"}`, "application/json")
	if status != http.StatusOK {
		t.Fatalf("a failure after the stream started must stay 200, got %d body=%s", status, body)
	}
	if got := headers.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type=%q", got)
	}
	events := parseChatSSE(t, body)
	errorsSeen := 0
	for _, event := range events {
		if event.name == "done" {
			t.Fatal("error stream must not end with done")
		}
		if event.name == "error" {
			errorsSeen++
		}
	}
	if errorsSeen != 1 {
		t.Fatalf("error events = %d, want exactly one (the backend already emitted it): %s", errorsSeen, body)
	}

	// A backend that fails without emitting an error gets the web fallback, which must strip the
	// bot token from the message.
	backend2 := &fakeChatBackend{deltas: []string{"часть"}, err: errChatBoom{message: "boom with 123:test token"}}
	app.SetChatBackend(backend2)
	status, body, _ = chatRequest(t, server, http.MethodPost, "/api/messages", bearer, `{"text":"hi"}`, "application/json")
	if status != http.StatusOK {
		t.Fatalf("fallback stream status=%d body=%s", status, body)
	}
	events = parseChatSSE(t, body)
	errorText := ""
	for _, event := range events {
		if event.name == "error" {
			errorText, _ = event.data["message"].(string)
		}
		if event.name == "done" {
			t.Fatal("fallback error stream must not end with done")
		}
	}
	if errorText == "" {
		t.Fatalf("fallback stream has no error event: %s", body)
	}
	if strings.Contains(errorText, "123:test") {
		t.Fatalf("SSE error leaked the bot token: %q", errorText)
	}
}

type errChatBoom struct{ message string }

func (e errChatBoom) Error() string { return e.message }

// streamingMiniAppModel is the model client used by the end-to-end test: it streams a few deltas
// and returns the same text as the final Response.
type streamingMiniAppModel struct {
	mu      sync.Mutex
	answer  string
	streams int
}

func (m *streamingMiniAppModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	return model.Response{Content: m.answer, Model: "test-model", Attempts: 1, Latency: time.Millisecond}, nil
}

func (m *streamingMiniAppModel) Models(ctx context.Context) ([]string, error) {
	return []string{"test-model"}, nil
}

func (m *streamingMiniAppModel) Stream(ctx context.Context, req model.Request, handler model.StreamHandler) (model.Response, error) {
	m.mu.Lock()
	m.streams++
	m.mu.Unlock()
	if handler.Progress != nil {
		handler.Progress(model.StreamInfo{Model: "test-model"})
	}
	for _, chunk := range []string{"При", "вет"} {
		if handler.Delta != nil {
			if err := handler.Delta(chunk); err != nil {
				return model.Response{}, err
			}
		}
	}
	return model.Response{Content: m.answer, Model: "test-model", Attempts: 1, Latency: time.Millisecond}, nil
}

func TestChatMessagesRunThroughTheRealDispatcher(t *testing.T) {
	raw := map[string]string{
		"TELEGRAM_BOT_TOKEN":     "123:test",
		"TELEGRAM_ALLOWED_USERS": strconv.FormatInt(testUserID, 10),
		"MINIAPP_ENABLED":        "true",
		"MINIAPP_PUBLIC_URL":     "https://example.test/app",
		"STREAMING_ENABLED":      "true",
		"DB_PATH":                "./data/test.db",
	}
	cfg, problems := config.LoadFrom(raw)
	if len(problems) != 0 {
		t.Fatalf("config: %v", problems)
	}
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	client := &streamingMiniAppModel{answer: "Привет"}
	app := New(cfg, db, client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)

	bearer := launchSession(t, app, server)
	status, body, headers := chatRequest(t, server, http.MethodPost, "/api/messages", bearer, `{"text":"привет"}`, "application/json")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if got := headers.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type=%q", got)
	}
	events := parseChatSSE(t, body)
	deltaText := strings.Builder{}
	doneText := ""
	for _, event := range events {
		switch event.name {
		case "delta":
			if text, ok := event.data["text"].(string); ok {
				deltaText.WriteString(text)
			}
		case "done":
			doneText, _ = event.data["text"].(string)
		}
	}
	if deltaText.String() != "Привет" || doneText != "Привет" {
		t.Fatalf("deltas=%q done=%q, want Привет/Привет (%s)", deltaText.String(), doneText, body)
	}
	client.mu.Lock()
	streams := client.streams
	client.mu.Unlock()
	if streams != 1 {
		t.Fatalf("model stream calls = %d, want 1", streams)
	}
	rows, err := store.HistoryOfChat(context.Background(), db, testChatID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Role != store.RoleUser || rows[0].Content != "привет" ||
		rows[1].Role != store.RoleAssistant || rows[1].Content != "Привет" {
		t.Fatalf("history = %+v, want the question and the streamed answer", rows)
	}
}

func TestChatResetSkipAndWhoamiEndpoints(t *testing.T) {
	app, server := newTestApp(t)
	backend := &fakeChatBackend{skipSet: true}
	app.SetChatBackend(backend)
	bearer := launchSession(t, app, server)

	status, body, _ := chatRequest(t, server, http.MethodPost, "/api/context/reset", bearer, "", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("reset status=%d body=%s", status, body)
	}
	backend.mu.Lock()
	resets := backend.resets
	backend.mu.Unlock()
	if resets != 1 {
		t.Fatalf("reset backend calls = %d, want 1", resets)
	}
	if status, _, _ := chatRequest(t, server, http.MethodPost, "/api/context/reset", bearer, `{"unexpected":true}`, "application/json"); status != http.StatusBadRequest {
		t.Fatalf("reset with a body status=%d, want 400", status)
	}

	status, body, _ = chatRequest(t, server, http.MethodPost, "/api/pending/skip", bearer, `{"skip":true}`, "application/json")
	if status != http.StatusOK || !strings.Contains(string(body), `"changed":true`) {
		t.Fatalf("skip status=%d body=%s", status, body)
	}
	status, body, _ = chatRequest(t, server, http.MethodPost, "/api/pending/skip", bearer, `{"skip":false}`, "application/json")
	if status != http.StatusOK || !strings.Contains(string(body), `"changed":true`) {
		t.Fatalf("unskip status=%d body=%s", status, body)
	}
	if got := backend.skipCalls(); len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("skip calls = %v, want [true false]", got)
	}
	if status, _, _ := chatRequest(t, server, http.MethodPost, "/api/pending/skip", bearer, `{}`, "application/json"); status != http.StatusBadRequest {
		t.Fatalf("skip without the field status=%d, want 400", status)
	}
	if status, _, _ := chatRequest(t, server, http.MethodPost, "/api/pending/skip", bearer, `{"skip":true,"chat_id":9}`, "application/json"); status != http.StatusBadRequest {
		t.Fatalf("skip with chat_id status=%d, want 400", status)
	}

	status, body, _ = chatRequest(t, server, http.MethodGet, "/api/whoami", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("whoami status=%d body=%s", status, body)
	}
	var whoami struct {
		UserID  int64 `json:"user_id"`
		ChatID  int64 `json:"chat_id"`
		Allowed bool  `json:"allowed"`
	}
	if err := json.Unmarshal(body, &whoami); err != nil {
		t.Fatalf("whoami JSON: %v (%s)", err, body)
	}
	if whoami.UserID != testUserID || whoami.ChatID != testChatID || !whoami.Allowed {
		t.Fatalf("whoami = %+v, want the session scope", whoami)
	}
	if status, _, _ := chatRequest(t, server, http.MethodGet, "/api/whoami?chat_id=1", bearer, "", ""); status != http.StatusBadRequest {
		t.Fatalf("whoami with a query status=%d, want 400", status)
	}
}

func TestChatUnavailableWithoutBackend(t *testing.T) {
	app, server := newTestApp(t)
	app.SetChatBackend(nil)
	bearer := launchSession(t, app, server)
	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/messages", `{"text":"hi"}`},
		{http.MethodPost, "/api/context/reset", ""},
		{http.MethodPost, "/api/pending/skip", `{"skip":true}`},
	} {
		status, body, _ := chatRequest(t, server, test.method, test.path, bearer, test.body, "application/json")
		if test.path == "/api/messages" {
			// The body is validated before the backend is consulted, so this one is a 503 only
			// for valid input.
			if status != http.StatusServiceUnavailable {
				t.Errorf("%s status=%d body=%s", test.path, status, body)
			}
		} else if status != http.StatusServiceUnavailable {
			t.Errorf("%s status=%d body=%s", test.path, status, body)
		}
	}
}
