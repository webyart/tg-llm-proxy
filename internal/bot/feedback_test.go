package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"

	"bot/internal/model"
)

type typingReplier struct {
	fakeReplier
	typingMu      sync.Mutex
	typingCalls   int
	typingErr     error
	typingSignals chan struct{}
}

func (r *typingReplier) SendTyping(ctx context.Context, chatID int64) error {
	r.typingMu.Lock()
	r.typingCalls++
	err := r.typingErr
	signals := r.typingSignals
	r.typingMu.Unlock()
	if signals != nil {
		select {
		case signals <- struct{}{}:
		default:
		}
	}
	return err
}

func (r *typingReplier) typingCount() int {
	r.typingMu.Lock()
	defer r.typingMu.Unlock()
	return r.typingCalls
}

func waitForTypingCalls(t *testing.T, r *typingReplier, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if r.typingCount() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("typing calls=%d, want at least %d", r.typingCount(), want)
}

func TestTypingHeartbeatStartStopAndCancellation(t *testing.T) {
	t.Run("stop is prompt and idempotent", func(t *testing.T) {
		r := &typingReplier{}
		d := newTestDispatcher(t, testConfig(t, nil), nil, r, testStore(t))
		stop := d.startTypingHeartbeatWithInterval(context.Background(), 100, 5*time.Millisecond)
		waitForTypingCalls(t, r, 3)
		stop()
		stop()
		calls := r.typingCount()
		time.Sleep(25 * time.Millisecond)
		if got := r.typingCount(); got != calls {
			t.Fatalf("typing continued after stop: before=%d after=%d", calls, got)
		}
	})

	t.Run("parent cancellation stops heartbeat", func(t *testing.T) {
		r := &typingReplier{}
		d := newTestDispatcher(t, testConfig(t, nil), nil, r, testStore(t))
		ctx, cancel := context.WithCancel(context.Background())
		stop := d.startTypingHeartbeatWithInterval(ctx, 100, 5*time.Millisecond)
		waitForTypingCalls(t, r, 2)
		cancel()
		stop()
		calls := r.typingCount()
		time.Sleep(25 * time.Millisecond)
		if got := r.typingCount(); got != calls {
			t.Fatalf("typing continued after parent cancellation: before=%d after=%d", calls, got)
		}
	})

	t.Run("Telegram error stops retries", func(t *testing.T) {
		r := &typingReplier{typingErr: errors.New("rate limited")}
		d := newTestDispatcher(t, testConfig(t, nil), nil, r, testStore(t))
		stop := d.startTypingHeartbeatWithInterval(context.Background(), 100, time.Millisecond)
		stop()
		time.Sleep(10 * time.Millisecond)
		if got := r.typingCount(); got != 1 {
			t.Fatalf("failed typing action was retried %d times", got)
		}
	})
}

func TestOrdinaryAskTypingPrecedesModelAndStopsAfterFinalAnswer(t *testing.T) {
	r := &typingReplier{}
	m := &fakeModel{response: model.Response{Content: "готовый ответ"}}
	m.contentFor = func(req model.Request) string {
		if r.typingCount() == 0 {
			t.Error("model started before the first typing action")
		}
		return "готовый ответ"
	}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "false"}), m, r, testStore(t))
	if err := dispatch(t, d, testEvent(100, 11, 8000, "/ask вопрос")); err != nil {
		t.Fatal(err)
	}
	calls := r.typingCount()
	if calls != 1 {
		t.Fatalf("fast ordinary request produced %d typing actions, want one", calls)
	}
	time.Sleep(10 * time.Millisecond)
	if got := r.typingCount(); got != calls {
		t.Fatalf("typing continued after final answer: before=%d after=%d", calls, got)
	}
	texts := r.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "готовый ответ") || !strings.Contains(texts[0], "⏱") {
		t.Fatalf("ordinary final response = %q", texts)
	}
}

func TestNonStreamingToolRequestHasTypingButNoStageMessages(t *testing.T) {
	const (
		sensitiveQuery  = "private-query-do-not-display"
		sensitiveResult = "private-result-do-not-display"
	)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"` + sensitiveResult + `","url":"https://example.invalid","content":"secret"}]}`))
	}))
	defer provider.Close()
	call := model.ToolCall{ID: "search-private", Name: "web_search", Arguments: `{"query":"` + sensitiveQuery + `"}`}
	m := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{call}, Attempts: 1},
		{Content: "Итог без раскрытия запроса", Model: "fake-model", Latency: 5 * time.Millisecond, Attempts: 1},
	}}
	r := &typingReplier{}
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11",
		"STREAMING_ENABLED":      "false",
		"WEB_SEARCH_ENABLED":     "true",
		"WEB_SEARCH_BASE_URL":    provider.URL,
	})
	d := newTestDispatcher(t, cfg, m, r, testStore(t))
	if err := dispatch(t, d, testEvent(100, 11, 8001, "найди информацию")); err != nil {
		t.Fatal(err)
	}
	if r.typingCount() == 0 {
		t.Fatal("tool-using ordinary request did not send typing feedback")
	}
	text := strings.Join(r.texts(), "\n")
	if len(r.texts()) != 1 || !strings.Contains(text, "Итог без раскрытия запроса") {
		t.Fatalf("non-streaming tool request produced stage messages: %q", text)
	}
	for _, sensitive := range []string{sensitiveQuery, sensitiveResult, provider.URL, streamStageWebSearch, call.Arguments} {
		if strings.Contains(text, sensitive) {
			t.Fatalf("non-streaming feedback leaked %q: %q", sensitive, text)
		}
	}
}

func TestStreamingToolStagesArePrivateDeduplicatedAndFinalized(t *testing.T) {
	for _, tc := range []struct {
		name          string
		call          model.ToolCall
		duplicate     bool
		wantStage     string
		sensitive     []string
		webSearchBase string
	}{
		{
			name:      "read_url",
			call:      model.ToolCall{ID: "read-private", Name: "read_url", Arguments: `{"url":"https://127.0.0.1/private-path?token=do-not-display"}`},
			wantStage: streamStageReadURL,
			sensitive: []string{"https://127.0.0.1/private-path?token=do-not-display", "private-path", "do-not-display"},
		},
		{
			name:      "web_search duplicate",
			call:      model.ToolCall{ID: "search-private", Name: "web_search", Arguments: `{"query":"private-search-query"}`},
			duplicate: true,
			wantStage: streamStageWebSearch,
			sensitive: []string{"private-search-query", "private-search-result"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var provider *httptest.Server
			if tc.call.Name == "web_search" {
				provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"results":[{"title":"private-search-result","url":"https://example.invalid","content":"secret"}]}`))
				}))
				defer provider.Close()
				tc.webSearchBase = provider.URL
				tc.sensitive = append(tc.sensitive, provider.URL)
			}
			calls := []model.ToolCall{tc.call}
			if tc.duplicate {
				second := tc.call
				second.ID += "-second"
				calls = append(calls, second)
			}
			m := &scriptedStreamToolModel{scriptedToolModel: scriptedToolModel{responses: []model.Response{
				{ToolCalls: calls, Attempts: 1},
				{Content: "Финальный ответ", Model: "fake-model", Latency: 5 * time.Millisecond, Attempts: 1},
			}}}
			r := &streamReplier{}
			raw := map[string]string{"URL_READER_ENABLED": "true"}
			if tc.webSearchBase != "" {
				raw["WEB_SEARCH_ENABLED"] = "true"
				raw["WEB_SEARCH_BASE_URL"] = tc.webSearchBase
			}
			cfg := streamConfig(t, raw)
			d := newTestDispatcher(t, cfg, m, r, testStore(t))
			if err := dispatch(t, d, testEvent(100, 11, 8002, "используй инструмент")); err != nil {
				t.Fatal(err)
			}
			if r.postCount() != 1 || r.count() != 0 {
				t.Fatalf("tool stages created extra messages: placeholder posts=%d ordinary sends=%d", r.postCount(), r.count())
			}
			feedback := strings.Join(append(r.postTexts(), r.editTexts()...), "\n")
			if strings.Count(feedback, tc.wantStage) != 1 {
				t.Fatalf("stage feedback was missing or duplicated: %q", feedback)
			}
			for _, sensitive := range append(tc.sensitive, tc.call.Arguments) {
				if strings.Contains(feedback, sensitive) {
					t.Fatalf("streaming stage leaked %q: %q", sensitive, feedback)
				}
			}
			last := r.lastEdit()
			if !strings.Contains(last, "Финальный ответ") || !strings.Contains(last, "⏱") || strings.Contains(last, tc.wantStage) {
				t.Fatalf("stage was not replaced by the normal final response: %q", last)
			}
			rows := assistantRows(t, d.deps.DB, 100)
			if len(rows) != 1 || rows[0].Content != "Финальный ответ" {
				t.Fatalf("stored final answer = %+v", rows)
			}
		})
	}
}

func TestTelegramSendTypingUsesTypingChatAction(t *testing.T) {
	var action, chatID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasSuffix(req.URL.Path, "/sendChatAction") {
			t.Errorf("Telegram method path = %q", req.URL.Path)
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		action = req.FormValue("action")
		chatID = req.FormValue("chat_id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	tg, err := tgbot.New("12345:test-token-not-a-secret", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe(), tgbot.WithHTTPClient(time.Second, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	d := newTestDispatcher(t, testConfig(t, nil), nil, &fakeReplier{}, testStore(t))
	r := &telegramReplier{d: d, bot: tg, cfg: d.deps.Cfg, logger: d.deps.Logger}
	if err := r.SendTyping(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if action != "typing" || chatID != "100" {
		t.Fatalf("sendChatAction action=%q chat_id=%q", action, chatID)
	}
}
