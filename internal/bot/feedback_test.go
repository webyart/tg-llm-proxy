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
	"bot/internal/store"
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
	if err := dispatch(t, d, testEvent(100, 11, 8001, "Найди в интернете информацию: private-query-do-not-display")); err != nil {
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
			question := "Найди в интернете информацию: private-search-query"
			if tc.call.Name == "read_url" {
				question = "Прочитай https://127.0.0.1/private-path?token=do-not-display"
			}
			if err := dispatch(t, d, testEvent(100, 11, 8002, question)); err != nil {
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

// The command rates the newest assistant answer, the short alias reaches the same handler, and a
// second click replaces the first rating.
func TestFeedbackRatesTheNewestAssistantMessage(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, r, db)
	var ctx = context.Background()

	if err := dispatch(t, d, testEvent(100, 11, 500, "/feedback up")); err != nil {
		t.Fatalf("/feedback without an assistant answer: %v", err)
	}
	if !strings.Contains(r.joined(), "нет ответа модели") {
		t.Fatalf("/feedback of a fresh chat must say there is nothing to rate, got %q", r.joined())
	}

	if _, err := store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "вопрос"}); err != nil {
		t.Fatalf("Append user: %v", err)
	}
	first, err := store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleAssistant, Content: "первый ответ"})
	if err != nil {
		t.Fatalf("Append first assistant: %v", err)
	}

	if err := dispatch(t, d, testEvent(100, 11, 501, "/feedback up")); err != nil {
		t.Fatalf("/feedback up: %v", err)
	}
	if !strings.Contains(r.joined(), "Спасибо! Оценка сохранена: 👍") {
		t.Fatalf("/feedback up must confirm with 👍, got %q", r.joined())
	}
	var ratings map[int64]int
	if ratings, err = store.MessageFeedbackFor(ctx, db, 100, 11, []int64{first}); err != nil || ratings[first] != 1 {
		t.Fatalf("rating after /feedback up = %v err=%v, want 1", ratings, err)
	}

	if _, err := store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "ещё вопрос"}); err != nil {
		t.Fatalf("Append second user: %v", err)
	}
	second, err := store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleAssistant, Content: "второй ответ"})
	if err != nil {
		t.Fatalf("Append second assistant: %v", err)
	}

	// The alias `/fb` with the emoji argument must rate the NEWEST answer.
	if err := dispatch(t, d, testEvent(100, 11, 502, "/fb 👎")); err != nil {
		t.Fatalf("/fb 👎: %v", err)
	}
	if !strings.Contains(r.joined(), "Спасибо! Оценка сохранена: 👎") {
		t.Fatalf("/fb 👎 must confirm with 👎, got %q", r.joined())
	}
	if ratings, err = store.MessageFeedbackFor(ctx, db, 100, 11, []int64{first, second}); err != nil {
		t.Fatalf("MessageFeedbackFor: %v", err)
	}
	if ratings[first] != 1 || ratings[second] != -1 {
		t.Fatalf("ratings = %v, want the first 👍 and the second 👎", ratings)
	}

	// A repeated click replaces the value and says so.
	if err := dispatch(t, d, testEvent(100, 11, 503, "/feedback up")); err != nil {
		t.Fatalf("/feedback up replacement: %v", err)
	}
	if !strings.Contains(r.joined(), "Спасибо! Оценка сохранена: 👍") {
		t.Fatalf("replacement must confirm with 👍, got %q", r.joined())
	}
	if ratings, err = store.MessageFeedbackFor(ctx, db, 100, 11, []int64{second}); err != nil || ratings[second] != 1 {
		t.Fatalf("rating after replacement = %v err=%v, want 1", ratings, err)
	}
	// The same value again is not an error, it just confirms the stored rating.
	if err := dispatch(t, d, testEvent(100, 11, 504, "/feedback up")); err != nil {
		t.Fatalf("/feedback up repeated: %v", err)
	}
	if !strings.Contains(r.joined(), "Оценка уже стоит: 👍") {
		t.Fatalf("repeated rating must say it is already stored, got %q", r.joined())
	}
}

// An unknown argument is a usage error: it must not be reported as a rating and must not write one.
func TestFeedbackUnknownArgumentIsUsageError(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, r, db)
	var ctx = context.Background()

	if _, err := store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleAssistant, Content: "ответ"}); err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 510, "/feedback sideways")); err != nil {
		t.Fatalf("/feedback with an unknown argument: %v", err)
	}
	var text = r.joined()
	for _, want := range []string{"/feedback up", "/feedback down"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the usage error must show %q, got %q", want, text)
		}
	}
	if strings.Contains(text, "сохранена") || strings.Contains(text, "уже стоит") {
		t.Fatalf("an unknown argument must not be reported as a rating, got %q", text)
	}
	if up, down, err := store.MessageFeedbackCounts(ctx, db, 100); err != nil || up != 0 || down != 0 {
		t.Fatalf("unknown argument wrote feedback %d/%d err=%v", up, down, err)
	}
}

// `/feedback` without an argument prints both counters and the usage line.
func TestFeedbackWithoutArgumentPrintsCounts(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, r, db)
	var ctx = context.Background()

	if _, err := store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleAssistant, Content: "первый ответ"}); err != nil {
		t.Fatalf("Append first assistant: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 520, "/feedback up")); err != nil {
		t.Fatalf("/feedback up: %v", err)
	}
	if _, err := store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleAssistant, Content: "второй ответ"}); err != nil {
		t.Fatalf("Append second assistant: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 521, "/feedback down")); err != nil {
		t.Fatalf("/feedback down: %v", err)
	}

	if err := dispatch(t, d, testEvent(100, 11, 522, "/feedback")); err != nil {
		t.Fatalf("/feedback without an argument: %v", err)
	}
	var text = r.joined()
	for _, want := range []string{"Оценки: 👍 1 / 👎 1", "/feedback up", "/feedback down"} {
		if !strings.Contains(text, want) {
			t.Fatalf("/feedback without an argument must show %q, got %q", want, text)
		}
	}
}

// /stats keeps the feedback visible: the DoD of the sprint reads «фидбэк виден в /stats».
func TestStatsIncludesFeedbackCounts(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, r, db)
	var ctx = context.Background()

	var messageID, err = store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleAssistant, Content: "ответ"})
	if err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	if _, err := store.SetMessageFeedback(ctx, db, 100, 11, messageID, 1); err != nil {
		t.Fatalf("SetMessageFeedback 👍: %v", err)
	}
	if _, err := store.SetMessageFeedback(ctx, db, 100, 12, messageID, -1); err != nil {
		t.Fatalf("SetMessageFeedback 👎: %v", err)
	}

	if err := dispatch(t, d, testEvent(100, 11, 530, "/stats")); err != nil {
		t.Fatalf("/stats: %v", err)
	}
	if !strings.Contains(r.joined(), "Оценки: 👍 1 / 👎 1") {
		t.Fatalf("/stats must show the feedback counts, got %q", r.joined())
	}
}
