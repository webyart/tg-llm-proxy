package webapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bot/internal/bot"
	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
)

// approvalNetworkModel asks for one web_search and then answers. It has no Stream method, so the
// Mini App answer is produced without streaming and the approval still travels over SSE.
type approvalNetworkModel struct {
	mu    sync.Mutex
	calls int
}

func (m *approvalNetworkModel) Complete(context.Context, model.Request) (model.Response, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		return model.Response{ToolCalls: []model.ToolCall{{
			ID: "approval-search", Name: "web_search", Arguments: `{"query":"web approval"}`,
		}}}, nil
	}
	return model.Response{Content: "Финальный ответ после подтверждения", Model: "fake-model", Latency: time.Millisecond}, nil
}

func (m *approvalNetworkModel) Models(context.Context) ([]string, error) {
	return []string{"fake-model"}, nil
}

func (m *approvalNetworkModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func newApprovalApp(t *testing.T, providerURL string, client model.Client) (*App, *httptest.Server) {
	t.Helper()
	raw := map[string]string{
		"TELEGRAM_BOT_TOKEN":     "123:test",
		"TELEGRAM_ALLOWED_USERS": strconv.FormatInt(testUserID, 10),
		"MINIAPP_ENABLED":        "true",
		"MINIAPP_PUBLIC_URL":     "https://example.test/app",
		"STREAMING_ENABLED":      "false",
		"WEB_SEARCH_ENABLED":     "true",
		"WEB_SEARCH_BASE_URL":    providerURL,
	}
	cfg, problems := config.LoadFrom(raw)
	if len(problems) != 0 {
		t.Fatalf("config: %v", problems)
	}
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "approval.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dispatcher := bot.New(bot.Deps{Cfg: cfg, DB: db, Model: client, Logger: logger, Token: cfg.BotToken})
	app := New(cfg, db, client, logger)
	app.SetChatBackend(dispatcher)
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	return app, server
}

func newApprovalProvider(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if got := req.URL.Query().Get("q"); got != "web approval" {
			t.Errorf("provider query=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Результат","url":"https://example.com","content":"Содержимое"}]}`))
	}))
	t.Cleanup(provider.Close)
	return provider, &calls
}

func newApprovalFakeApp(t *testing.T) (*App, *httptest.Server, *fakeChatBackend, string) {
	t.Helper()
	app, server := newTestApp(t)
	backend := &fakeChatBackend{}
	app.SetChatBackend(backend)
	return app, server, backend, launchSession(t, app, server)
}

// readChatEvent reads one SSE block from a live response, so a test can send the approval decision
// while /api/messages is still blocked inside the waiter.
func readChatEvent(t *testing.T, reader *bufio.Reader) (chatSSEEvent, bool) {
	t.Helper()
	name := "message"
	data := []string{}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return chatSSEEvent{}, false
			}
			t.Fatalf("read SSE: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if name == "message" && len(data) == 0 {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &payload); err != nil {
				t.Fatalf("SSE data %q: %v", strings.Join(data, "\n"), err)
			}
			return chatSSEEvent{name: name, data: payload}, true
		}
		if strings.HasPrefix(line, "event:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

func startChatStream(t *testing.T, server *httptest.Server, bearer string, text string) (*bufio.Reader, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/messages", strings.NewReader(`{"text":`+strconv.Quote(text)+`}`))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	response, err := server.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		cancel()
		t.Fatalf("chat stream status=%d body=%s", response.StatusCode, body)
	}
	cleanup := func() {
		response.Body.Close()
		cancel()
	}
	return bufio.NewReader(response.Body), cleanup
}

func waitChatEvent(t *testing.T, reader *bufio.Reader, name string) chatSSEEvent {
	t.Helper()
	for {
		event, ok := readChatEvent(t, reader)
		if !ok {
			t.Fatalf("stream ended before event %q", name)
		}
		if event.name == name {
			return event
		}
	}
}

func TestChatApprovalDecisionResumesTheBlockedStream(t *testing.T) {
	provider, providerCalls := newApprovalProvider(t)
	client := &approvalNetworkModel{}
	app, server := newApprovalApp(t, provider.URL, client)
	bearer := launchSession(t, app, server)
	reader, cleanup := startChatStream(t, server, bearer, "Подготовь обзор")
	defer cleanup()

	approval := waitChatEvent(t, reader, "approval")
	token, _ := approval.data["token"].(string)
	if token == "" {
		t.Fatalf("approval event has no token: %+v", approval.data)
	}
	if approval.data["tool"] != "web_search" {
		t.Fatalf("approval tool=%v", approval.data["tool"])
	}
	if summary, _ := approval.data["summary"].(string); !strings.Contains(summary, "web approval") {
		t.Fatalf("approval summary=%q", summary)
	}
	if expires, _ := approval.data["expires_in_seconds"].(float64); expires < 1 {
		t.Fatalf("approval expires_in_seconds=%v", approval.data["expires_in_seconds"])
	}
	if providerCalls.Load() != 0 || client.callCount() != 1 {
		t.Fatalf("tool/model ran before the decision: provider=%d model=%d", providerCalls.Load(), client.callCount())
	}

	status, body, _ := chatRequest(t, server, http.MethodPost, "/api/chat/approval", bearer,
		`{"token":"`+token+`","approve":true}`, "application/json")
	if status != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("approval decision status=%d body=%s", status, body)
	}

	done := waitChatEvent(t, reader, "done")
	doneText, _ := done.data["text"].(string)
	if !strings.HasPrefix(doneText, "Финальный ответ после подтверждения") || !strings.Contains(doneText, "⏱") {
		t.Fatalf("done text=%v, want the answer plus the latency footer", done.data["text"])
	}
	if providerCalls.Load() != 1 || client.callCount() != 2 {
		t.Fatalf("provider=%d model=%d after approval", providerCalls.Load(), client.callCount())
	}
	history, err := store.HistoryOfChat(context.Background(), app.db, testChatID, 10)
	if err != nil || len(history) != 2 || history[1].Content != "Финальный ответ после подтверждения" {
		t.Fatalf("history=%+v err=%v", history, err)
	}

	// The token is single-use: replaying the same decision is a used token, not another wake-up.
	status, body, _ = chatRequest(t, server, http.MethodPost, "/api/chat/approval", bearer,
		`{"token":"`+token+`","approve":true}`, "application/json")
	if status != http.StatusGone {
		t.Fatalf("replayed token status=%d body=%s, want 410", status, body)
	}
}

func TestChatApprovalWithoutDecisionTimesOut(t *testing.T) {
	oldTTL := bot.MiniAppApprovalTimeout
	bot.MiniAppApprovalTimeout = 150 * time.Millisecond
	t.Cleanup(func() { bot.MiniAppApprovalTimeout = oldTTL })

	provider, providerCalls := newApprovalProvider(t)
	client := &approvalNetworkModel{}
	app, server := newApprovalApp(t, provider.URL, client)
	bearer := launchSession(t, app, server)
	reader, cleanup := startChatStream(t, server, bearer, "Подготовь обзор")
	defer cleanup()

	approval := waitChatEvent(t, reader, "approval")
	token, _ := approval.data["token"].(string)
	if token == "" {
		t.Fatal("approval event has no token")
	}
	errorEvent := waitChatEvent(t, reader, "error")
	message, _ := errorEvent.data["message"].(string)
	if message == "" || !strings.Contains(message, "истекло") {
		t.Fatalf("timeout error message=%q", message)
	}
	if providerCalls.Load() != 0 || client.callCount() != 1 {
		t.Fatalf("a timed-out action ran: provider=%d model=%d", providerCalls.Load(), client.callCount())
	}
	history, err := store.HistoryOfChat(context.Background(), app.db, testChatID, 10)
	if err != nil || len(history) != 1 || history[0].Role != store.RoleUser {
		t.Fatalf("a timed-out request stored an answer: history=%+v err=%v", history, err)
	}
	status, body, _ := chatRequest(t, server, http.MethodPost, "/api/chat/approval", bearer,
		`{"token":"`+token+`","approve":true}`, "application/json")
	if status != http.StatusGone {
		t.Fatalf("late decision status=%d body=%s, want 410", status, body)
	}
}

func TestChatApprovalEndpointStatuses(t *testing.T) {
	t.Run("requires a session", func(t *testing.T) {
		_, server, _, _ := newApprovalFakeApp(t)
		status, body, _ := chatRequest(t, server, http.MethodPost, "/api/chat/approval", "",
			`{"token":"t","approve":true}`, "application/json")
		if status != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", status, body)
		}
	})

	for _, test := range []struct {
		name string
		body string
	}{
		{"malformed JSON", `{"token":`},
		{"unknown field", `{"token":"t","approve":true,"chat_id":1}`},
		{"missing token", `{"approve":true}`},
		{"missing approve", `{"token":"t"}`},
		{"trailing JSON", `{"token":"t","approve":true}{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, server, backend, bearer := newApprovalFakeApp(t)
			status, body, _ := chatRequest(t, server, http.MethodPost, "/api/chat/approval", bearer, test.body, "application/json")
			if status != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", status, body)
			}
			if len(backend.approvals) != 0 {
				t.Fatalf("a rejected body reached the backend: %+v", backend.approvals)
			}
		})
	}

	t.Run("query parameters", func(t *testing.T) {
		_, server, _, bearer := newApprovalFakeApp(t)
		status, body, _ := chatRequest(t, server, http.MethodPost, "/api/chat/approval?chat_id=1", bearer,
			`{"token":"t","approve":true}`, "application/json")
		if status != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", status, body)
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		_, server, _, bearer := newApprovalFakeApp(t)
		status, body, _ := chatRequest(t, server, http.MethodGet, "/api/chat/approval", bearer, "", "")
		if status != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d body=%s", status, body)
		}
	})

	t.Run("unknown token", func(t *testing.T) {
		app, server, backend, bearer := newApprovalFakeApp(t)
		backend.approveErr = bot.ErrMiniAppApprovalNotFound
		_ = app
		status, body, _ := chatRequest(t, server, http.MethodPost, "/api/chat/approval", bearer,
			`{"token":"t","approve":false}`, "application/json")
		if status != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", status, body)
		}
	})

	t.Run("expired token", func(t *testing.T) {
		_, server, backend, bearer := newApprovalFakeApp(t)
		backend.approveErr = bot.ErrMiniAppApprovalExpired
		status, body, _ := chatRequest(t, server, http.MethodPost, "/api/chat/approval", bearer,
			`{"token":"t","approve":false}`, "application/json")
		if status != http.StatusGone {
			t.Fatalf("status=%d body=%s", status, body)
		}
	})

	t.Run("accepted decision", func(t *testing.T) {
		_, server, backend, bearer := newApprovalFakeApp(t)
		status, body, _ := chatRequest(t, server, http.MethodPost, "/api/chat/approval", bearer,
			`{"token":"token-1","approve":true}`, "application/json")
		if status != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if len(backend.approvals) != 1 {
			t.Fatalf("backend approvals=%+v", backend.approvals)
		}
		got := backend.approvals[0]
		if got.token != "token-1" || got.chatID != testChatID || got.userID != testUserID || !got.approve {
			t.Fatalf("backend got %+v, want the session scope and approve=true", got)
		}
	})
}

func TestChatApprovalDecisionRateLimitIsPerUser(t *testing.T) {
	app, server, backend, bearer := newApprovalFakeApp(t)
	for index := 0; index < approvalRateLimit; index++ {
		if allowed, _ := app.rateLimits.allowApproval(testUserID); !allowed {
			t.Fatalf("prefill allowance %d was rejected", index)
		}
	}
	status, body, headers := chatRequest(t, server, http.MethodPost, "/api/chat/approval", bearer,
		`{"token":"t","approve":true}`, "application/json")
	if status != http.StatusTooManyRequests || headers.Get("Retry-After") == "" {
		t.Fatalf("status=%d Retry-After=%q body=%s", status, headers.Get("Retry-After"), body)
	}
	if len(backend.approvals) != 0 {
		t.Fatal("a rate-limited decision reached the backend")
	}
}
