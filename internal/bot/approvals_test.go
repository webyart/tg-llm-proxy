package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bot/internal/model"
	"bot/internal/store"
)

// miniAppAnswer runs one Mini App answer in the background and returns its events and final error.
// The event channel is buffered generously: the tests read the approval event first and the answer
// tail after the decision, so the producer must never block on a full channel.
func miniAppAnswer(t *testing.T, d *Dispatcher, ctx context.Context, chatID, userID int64, text string) (<-chan error, <-chan MiniAppStreamEvent) {
	t.Helper()
	events := make(chan MiniAppStreamEvent, 64)
	done := make(chan error, 1)
	go func() {
		done <- d.AnswerForMiniApp(ctx, chatID, userID, text, func(event MiniAppStreamEvent) error {
			events <- event
			return nil
		})
	}()
	return done, events
}

func waitMiniAppApprovalEvent(t *testing.T, events <-chan MiniAppStreamEvent) MiniAppApprovalEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Approval != nil {
				return *event.Approval
			}
		case <-deadline:
			t.Fatal("the Mini App answer never requested a network approval")
		}
	}
}

func waitMiniAppAnswer(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the Mini App answer did not finish")
		return nil
	}
}

// drainMiniAppEvents collects whatever events the answer already produced. The producer is finished,
// so a non-blocking read returns the whole tail.
func drainMiniAppEvents(events <-chan MiniAppStreamEvent) []MiniAppStreamEvent {
	out := []MiniAppStreamEvent{}
	for {
		select {
		case event := <-events:
			out = append(out, event)
		default:
			return out
		}
	}
}

func miniAppDone(events []MiniAppStreamEvent) (MiniAppStreamEvent, bool) {
	for _, event := range events {
		if event.Done {
			return event, true
		}
	}
	return MiniAppStreamEvent{}, false
}

func TestMiniAppApprovedNetworkActionExecutesAndResumesTheSameAnswer(t *testing.T) {
	const query = "approved query"
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		providerCalls.Add(1)
		if got := req.URL.Query().Get("q"); got != query {
			t.Errorf("provider query=%q, want %q", got, query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Результат поиска","url":"https://example.com","content":"Содержимое"}]}`))
	}))
	defer provider.Close()

	call := model.ToolCall{ID: "approved", Name: "web_search", Arguments: `{"query":"` + query + `"}`}
	m := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{call}},
		{Content: "Ответ: Результат поиска"},
	}}
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "false",
		"WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": provider.URL,
	})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, db)

	done, events := miniAppAnswer(t, d, context.Background(), 100, 11, "Подготовь обзор")
	approval := waitMiniAppApprovalEvent(t, events)
	if approval.Token == "" || approval.Tool != "web_search" || !strings.Contains(approval.Summary, query) {
		t.Fatalf("approval event=%+v", approval)
	}
	if approval.ExpiresInSeconds <= 0 {
		t.Fatalf("expires_in_seconds=%d", approval.ExpiresInSeconds)
	}
	if providerCalls.Load() != 0 || len(m.requests) != 1 {
		t.Fatal("the tool ran before the user approved it")
	}
	if err := d.ResolveMiniAppApproval(context.Background(), approval.Token, 100, 11, true); err != nil {
		t.Fatalf("ResolveMiniAppApproval: %v", err)
	}
	if err := waitMiniAppAnswer(t, done); err != nil {
		t.Fatalf("AnswerForMiniApp: %v", err)
	}
	tail := drainMiniAppEvents(events)
	final, ok := miniAppDone(tail)
	if !ok || final.Delta != "Ответ: Результат поиска" || final.MessageID <= 0 {
		t.Fatalf("done event=%+v events=%+v", final, tail)
	}
	if providerCalls.Load() != 1 || len(m.requests) != 2 {
		t.Fatalf("provider=%d model=%d, want 1/2", providerCalls.Load(), len(m.requests))
	}
	last := m.requests[1].Messages[len(m.requests[1].Messages)-1]
	if last.Role != model.RoleTool || !strings.Contains(last.Content, "Результат поиска") {
		t.Fatalf("the model did not receive the tool result: %+v", last)
	}
	rows, err := store.HistoryOfChat(context.Background(), db, 100, 10)
	if err != nil || len(rows) != 2 || rows[1].Role != store.RoleAssistant || rows[1].Content != "Ответ: Результат поиска" {
		t.Fatalf("history=%+v err=%v", rows, err)
	}
	if activeMiniAppApprovals(d) != 0 {
		t.Fatal("an approved token stayed pending")
	}
	if err := d.ResolveMiniAppApproval(context.Background(), approval.Token, 100, 11, true); !errors.Is(err, ErrMiniAppApprovalExpired) {
		t.Fatalf("replayed token error=%v, want ErrMiniAppApprovalExpired", err)
	}
	if networkUsageCount(t, d, "web_search") != 1 {
		t.Fatalf("web_search usage count=%d", networkUsageCount(t, d, "web_search"))
	}
}

func TestMiniAppDeniedNetworkActionStoresRefusalWithoutExecuting(t *testing.T) {
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer provider.Close()

	call := model.ToolCall{ID: "denied", Name: "web_search", Arguments: `{"query":"denied"}`}
	m := &scriptedToolModel{responses: []model.Response{{ToolCalls: []model.ToolCall{call}}}}
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "false",
		"WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": provider.URL,
	})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, db)

	done, events := miniAppAnswer(t, d, context.Background(), 100, 11, "Подготовь обзор")
	approval := waitMiniAppApprovalEvent(t, events)
	if err := d.ResolveMiniAppApproval(context.Background(), approval.Token, 100, 11, false); err != nil {
		t.Fatalf("ResolveMiniAppApproval: %v", err)
	}
	if err := waitMiniAppAnswer(t, done); err != nil {
		t.Fatalf("AnswerForMiniApp: %v", err)
	}
	final, ok := miniAppDone(drainMiniAppEvents(events))
	if !ok || final.Delta != miniAppApprovalRefusal {
		t.Fatalf("denied answer done=%+v ok=%v", final, ok)
	}
	if providerCalls.Load() != 0 || len(m.requests) != 1 {
		t.Fatalf("a denied action ran: provider=%d model=%d", providerCalls.Load(), len(m.requests))
	}
	rows, err := store.HistoryOfChat(context.Background(), db, 100, 10)
	if err != nil || len(rows) != 2 || rows[1].Role != store.RoleAssistant || rows[1].Content != miniAppApprovalRefusal {
		t.Fatalf("refusal was not stored: rows=%+v err=%v", rows, err)
	}
	if activeMiniAppApprovals(d) != 0 {
		t.Fatal("a denied token stayed pending")
	}
	if err := d.ResolveMiniAppApproval(context.Background(), approval.Token, 100, 11, true); !errors.Is(err, ErrMiniAppApprovalExpired) {
		t.Fatalf("replayed denied token error=%v, want ErrMiniAppApprovalExpired", err)
	}
	if networkUsageCount(t, d, "web_search") != 0 {
		t.Fatal("a denied action was counted as a network tool use")
	}
}

func TestMiniAppApprovalTimeoutEndsWithClearErrorAndNoAnswer(t *testing.T) {
	oldTTL := MiniAppApprovalTimeout
	MiniAppApprovalTimeout = 50 * time.Millisecond
	t.Cleanup(func() { MiniAppApprovalTimeout = oldTTL })

	call := model.ToolCall{ID: "timeout", Name: "web_search", Arguments: `{"query":"timeout"}`}
	m := &scriptedToolModel{responses: []model.Response{{ToolCalls: []model.ToolCall{call}}}}
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "false", "WEB_SEARCH_ENABLED": "true",
		"WEB_SEARCH_BASE_URL": "http://127.0.0.1:1/",
	})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, db)

	done, events := miniAppAnswer(t, d, context.Background(), 100, 11, "Подготовь обзор")
	approval := waitMiniAppApprovalEvent(t, events)
	err := waitMiniAppAnswer(t, done)
	if !errors.Is(err, errMiniAppApprovalTimeout) {
		t.Fatalf("timeout error=%v, want errMiniAppApprovalTimeout", err)
	}
	tail := drainMiniAppEvents(events)
	if _, ok := miniAppDone(tail); ok {
		t.Fatal("a timed-out approval produced a done event")
	}
	sawError := false
	for _, event := range tail {
		if event.Error != "" {
			sawError = true
			if !strings.Contains(event.Error, "истекло") {
				t.Fatalf("timeout error text=%q", event.Error)
			}
		}
	}
	if !sawError {
		t.Fatalf("timeout produced no error event: %+v", tail)
	}
	rows, err := store.HistoryOfChat(context.Background(), db, 100, 10)
	if err != nil || len(rows) != 1 || rows[0].Role != store.RoleUser {
		t.Fatalf("a timed-out request stored an answer: rows=%+v err=%v", rows, err)
	}
	if activeMiniAppApprovals(d) != 0 {
		t.Fatal("a timed-out token stayed pending")
	}
	if err := d.ResolveMiniAppApproval(context.Background(), approval.Token, 100, 11, true); !errors.Is(err, ErrMiniAppApprovalExpired) {
		t.Fatalf("late decision after timeout error=%v, want ErrMiniAppApprovalExpired", err)
	}
}

func TestMiniAppApprovalRejectsWrongChatAndUserWithoutConsuming(t *testing.T) {
	call := model.ToolCall{ID: "scope", Name: "web_search", Arguments: `{"query":"scope"}`}
	m := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{call}},
		{Content: "готово"},
	}}
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11,12", "STREAMING_ENABLED": "false", "WEB_SEARCH_ENABLED": "true",
		"WEB_SEARCH_BASE_URL": "http://127.0.0.1:1/",
	})
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, testStore(t))

	done, events := miniAppAnswer(t, d, context.Background(), 100, 11, "Подготовь обзор")
	approval := waitMiniAppApprovalEvent(t, events)
	if err := d.ResolveMiniAppApproval(context.Background(), approval.Token, 100, 12, true); !errors.Is(err, ErrMiniAppApprovalNotFound) {
		t.Fatalf("wrong user error=%v, want ErrMiniAppApprovalNotFound", err)
	}
	if err := d.ResolveMiniAppApproval(context.Background(), approval.Token, 101, 11, true); !errors.Is(err, ErrMiniAppApprovalNotFound) {
		t.Fatalf("wrong chat error=%v, want ErrMiniAppApprovalNotFound", err)
	}
	if activeMiniAppApprovals(d) != 1 {
		t.Fatal("a mismatched decision consumed the token")
	}
	if err := d.ResolveMiniAppApproval(context.Background(), approval.Token, 100, 11, true); err != nil {
		t.Fatalf("correct decision: %v", err)
	}
	if err := waitMiniAppAnswer(t, done); err != nil {
		t.Fatalf("AnswerForMiniApp: %v", err)
	}
}

func TestMiniAppApprovalCancellationCleansTheWaiter(t *testing.T) {
	call := model.ToolCall{ID: "cancel", Name: "web_search", Arguments: `{"query":"cancel"}`}
	m := &scriptedToolModel{responses: []model.Response{{ToolCalls: []model.ToolCall{call}}}}
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "false", "WEB_SEARCH_ENABLED": "true",
		"WEB_SEARCH_BASE_URL": "http://127.0.0.1:1/",
	})
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, testStore(t))

	ctx, cancel := context.WithCancel(context.Background())
	done, events := miniAppAnswer(t, d, ctx, 100, 11, "Подготовь обзор")
	approval := waitMiniAppApprovalEvent(t, events)
	cancel()
	if err := waitMiniAppAnswer(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled answer error=%v, want context.Canceled", err)
	}
	if activeMiniAppApprovals(d) != 0 {
		t.Fatal("cancellation left the approval pending")
	}
	// A decision that arrives after the client is gone must not panic and must not be accepted.
	if err := d.ResolveMiniAppApproval(context.Background(), approval.Token, 100, 11, true); !errors.Is(err, ErrMiniAppApprovalExpired) {
		t.Fatalf("decision after cancellation error=%v, want ErrMiniAppApprovalExpired", err)
	}
}

func TestMiniAppApprovalCapsAreEnforced(t *testing.T) {
	d := newTestDispatcher(t, testConfig(t, nil), nil, &fakeReplier{}, testStore(t))
	for index := 0; index < miniAppApprovalUserCap; index++ {
		pending := &miniAppApproval{
			ChatID: int64(100 + index), UserID: 11, ExpiresAt: time.Now().Add(time.Minute),
			decision: make(chan bool, 1),
		}
		if err := d.registerMiniAppApproval(string(rune('a'+index)), pending); err != nil {
			t.Fatalf("registration %d: %v", index, err)
		}
	}
	extra := &miniAppApproval{ChatID: 200, UserID: 11, ExpiresAt: time.Now().Add(time.Minute), decision: make(chan bool, 1)}
	if err := d.registerMiniAppApproval("extra", extra); !errors.Is(err, errMiniAppApprovalBusy) {
		t.Fatalf("per-user cap error=%v, want errMiniAppApprovalBusy", err)
	}
	if activeMiniAppApprovals(d) != miniAppApprovalUserCap {
		t.Fatalf("active approvals=%d, want %d", activeMiniAppApprovals(d), miniAppApprovalUserCap)
	}
}

func TestMiniAppApprovalSummaryIsBoundedAndSanitized(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_BOT_TOKEN": "12345:super-secret-token"})
	d := newTestDispatcher(t, cfg, nil, &fakeReplier{}, testStore(t))
	arguments := `{"query":"` + strings.Repeat("я", miniAppApprovalSummary+50) + ` ` + cfg.BotToken + "\nsecond"
	summary := d.miniAppApprovalSummaryText(model.ToolCall{Name: "web_search", Arguments: arguments})
	if strings.Contains(summary, cfg.BotToken) {
		t.Fatal("the approval summary leaked a configured secret")
	}
	if strings.ContainsAny(summary, "\n\r\t") {
		t.Fatal("the approval summary contains control characters")
	}
	if Runes(summary) > miniAppApprovalSummary+1 {
		t.Fatalf("summary length=%d", Runes(summary))
	}
}
