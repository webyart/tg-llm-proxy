package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

func TestDailyModelLimitDefaultsToUnlimited(t *testing.T) {
	m := &fakeModel{response: model.Response{Content: "ok"}}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, nil), m, nil, db)
	for i := 0; i < 2; i++ {
		resp, err := d.callModel(context.Background(), 11, model.Request{}, nil, nil)
		if err != nil || resp.Content != "ok" {
			t.Fatalf("callModel %d response=%+v err=%v", i, resp, err)
		}
	}
	if m.callCount() != 2 {
		t.Fatalf("provider calls=%d, want 2 with unlimited defaults", m.callCount())
	}
	calls, err := store.CountEvents(context.Background(), db, store.EventModelCall)
	if err != nil || calls != 2 {
		t.Fatalf("unlimited model_call events=%d err=%v, want 2", calls, err)
	}
	used, err := store.DailyQuotaUsed(context.Background(), db, 11, d.now(), store.DailyQuotaModel)
	if err != nil || used != 0 {
		t.Fatalf("unlimited reservation used=%d err=%v, want 0", used, err)
	}
}

func TestDailyModelLimitBelowAtOverAndPerUser(t *testing.T) {
	cfg := testConfig(t, map[string]string{"USER_DAILY_MODEL_LIMIT": "2"})
	m := &fakeModel{response: model.Response{Content: "ok"}}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, nil, db)
	for i := 0; i < 2; i++ {
		resp, err := d.callModel(context.Background(), 11, model.Request{}, nil, nil)
		if err != nil || resp.Content != "ok" {
			t.Fatalf("below-limit call %d response=%+v err=%v", i, resp, err)
		}
		if _, err := store.Append(context.Background(), db, store.Message{
			ChatID: 100, UserID: 11, Role: store.RoleAssistant, Content: resp.Content, Model: "fake-model",
		}); err != nil {
			t.Fatalf("Append actual response: %v", err)
		}
	}
	resp, err := d.callModel(context.Background(), 11, model.Request{}, nil, nil)
	if err != ErrModelDailyLimited || resp.Content != "" {
		t.Fatalf("at-limit response=%+v err=%v", resp, err)
	}
	if m.callCount() != 2 {
		t.Fatalf("over-limit request reached provider: calls=%d", m.callCount())
	}
	resp, err = d.callModel(context.Background(), 12, model.Request{}, nil, nil)
	if err != nil || resp.Content != "ok" || m.callCount() != 3 {
		t.Fatalf("independent user response=%+v calls=%d err=%v", resp, m.callCount(), err)
	}
}

func TestDailyModelLimitRefusalIsNotStoredAsUsageOrAssistantAnswer(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "USER_DAILY_MODEL_LIMIT": "1"})
	m := &fakeModel{response: model.Response{Content: "actual answer"}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	if err := dispatch(t, d, testEvent(100, 11, 700, "first")); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 701, "second")); err != nil {
		t.Fatalf("second Dispatch: %v", err)
	}
	if m.callCount() != 1 || !strings.Contains(r.joined(), modelDailyLimitErrorText) {
		t.Fatalf("calls=%d replies=%q", m.callCount(), r.joined())
	}
	calls, err := store.DailyModelCalls(context.Background(), db, 11, d.now())
	if err != nil || calls != 1 {
		t.Fatalf("DailyModelCalls=%d err=%v, want only the actual answer", calls, err)
	}
	rows, err := store.HistoryOfChat(context.Background(), db, 100, 10)
	if err != nil || len(rows) != 3 || rows[1].Role != store.RoleAssistant || rows[1].Content != "actual answer" {
		t.Fatalf("limited dispatch stored an assistant notice: rows=%+v err=%v", rows, err)
	}
}

func TestDailyNetworkToolLimitBelowAtOverAndPerUser(t *testing.T) {
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer target.Close()
	cfg := testConfig(t, map[string]string{
		"USER_DAILY_NETWORK_TOOL_LIMIT": "1",
		"WEB_SEARCH_ENABLED":            "true",
		"WEB_SEARCH_BASE_URL":           target.URL,
	})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, nil, nil, db)
	call := model.ToolCall{ID: "search", Name: "web_search", Arguments: `{"query":"private search text"}`}
	result, executed, err := d.executeNetworkTool(context.Background(), 11, call)
	if err != nil || !executed || !strings.Contains(result, `"results"`) {
		t.Fatalf("below-limit result=%q executed=%v err=%v", result, executed, err)
	}
	result, executed, err = d.executeNetworkTool(context.Background(), 11, call)
	if err != nil || executed || result != networkDailyLimitErrorJSON || !json.Valid([]byte(result)) {
		t.Fatalf("at-limit result=%q executed=%v err=%v", result, executed, err)
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("over-limit tool reached provider: calls=%d", providerCalls.Load())
	}
	result, executed, err = d.executeNetworkTool(context.Background(), 12, call)
	if err != nil || !executed || !strings.Contains(result, `"results"`) || providerCalls.Load() != 2 {
		t.Fatalf("independent user result=%q executed=%v calls=%d err=%v", result, executed, providerCalls.Load(), err)
	}
}

func TestDisabledNetworkToolFailsBeforeDailyLimit(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"USER_DAILY_NETWORK_TOOL_LIMIT": "1",
		"WEB_SEARCH_ENABLED":            "false",
	})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, nil, nil, db)
	d.logEvent(context.Background(), 11, store.EventUsage, "web_search")
	result, executed, err := d.executeNetworkTool(context.Background(), 11, model.ToolCall{
		Name: "web_search", Arguments: `{"query":"must not run"}`,
	})
	if err == nil || err.Error() != "tool disabled" || executed || result != "" {
		t.Fatalf("disabled result=%q executed=%v err=%v", result, executed, err)
	}
	if d.networkWork.Count(11) != 0 || len(d.outboundTools) != 0 {
		t.Fatalf("disabled tool consumed work=%d slots=%d", d.networkWork.Count(11), len(d.outboundTools))
	}
}

func TestUsageCommandAndMenuRenderOnlyAggregates(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS":        "11,12",
		"USER_DAILY_MODEL_LIMIT":        "5",
		"USER_DAILY_NETWORK_TOOL_LIMIT": "4",
		"USER_LIMITS_JSON":              `{"11":{"model":3,"network_tools":2}}`,
	})
	r := &menuEditReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, nil, r, db)
	for _, row := range []struct {
		model     string
		tokensIn  int64
		tokensOut int64
	}{
		{model: "model-alpha", tokensIn: 10, tokensOut: 20},
		{model: "model-beta", tokensIn: 1, tokensOut: 2},
	} {
		if _, err := store.Append(context.Background(), db, store.Message{
			ChatID: 100, UserID: 11, Role: store.RoleAssistant, Content: "private-answer-body", Model: row.model,
			TokensIn: &row.tokensIn, TokensOut: &row.tokensOut,
		}); err != nil {
			t.Fatalf("Append assistant: %v", err)
		}
	}
	if _, err := store.Append(context.Background(), db, store.Message{
		ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "private-query-do-not-render", TelegramMessageID: intPtr(800),
	}); err != nil {
		t.Fatalf("Append user: %v", err)
	}
	if _, err := store.Append(context.Background(), db, store.Message{
		ChatID: 200, UserID: 12, Role: store.RoleAssistant, Content: "other-user-answer", Model: "other-model",
	}); err != nil {
		t.Fatalf("Append other user: %v", err)
	}
	for _, detail := range []string{"read_url", "web_search", "calculator", "rag"} {
		d.logEvent(context.Background(), 11, store.EventUsage, detail)
	}
	d.logEvent(context.Background(), 12, store.EventUsage, "read_url")
	// The daily limits are enforced on daily_quota_reservations, and /usage reports that table. The fixture
	// therefore reserves what two model calls and two network tools would have reserved.
	for _, class := range []struct {
		name  string
		limit int64
	}{
		{name: store.DailyQuotaModel, limit: 3},
		{name: store.DailyQuotaNetworkTools, limit: 2},
	} {
		for i := 0; i < 2; i++ {
			if _, err := store.ReserveDailyQuota(context.Background(), db, 11, d.now(), class.name, class.limit); err != nil {
				t.Fatalf("ReserveDailyQuota(%s): %v", class.name, err)
			}
		}
	}

	if err := dispatch(t, d, testEvent(100, 11, 801, "/usage")); err != nil {
		t.Fatalf("/usage: %v", err)
	}
	text := r.texts()[len(r.texts())-1]
	for _, want := range []string{
		"model-alpha: вызовов 1; токены 10 входящих / 20 исходящих",
		"model-beta: вызовов 1; токены 1 входящих / 2 исходящих",
		"read_url: 1", "web_search: 1", "calculator: 1", "RAG: 1",
		"Модель: лимит 3; использовано 2; осталось 1",
		"Сетевые инструменты: лимит 2; использовано 2; осталось 0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/usage misses %q: %q", want, text)
		}
	}
	for _, forbidden := range []string{"private-query-do-not-render", "private-answer-body", "other-model", "other-user-answer"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("/usage contains non-aggregate content %q: %q", forbidden, text)
		}
	}
	if !hasMenuCallback(statusMenuMarkup(), "menu:usage") {
		t.Fatal("status menu has no usage entry")
	}
	if err := dispatchMenuData(t, d, 100, 11, 802, "menu:usage"); err != nil {
		t.Fatalf("menu:usage: %v", err)
	}
	edit := r.lastEdit()
	if edit.text != text || !hasMenuCallback(edit.markup, "menu:usage") || !hasMenuCallback(edit.markup, "menu:status") {
		t.Fatalf("usage menu edit=%+v, command text=%q", edit, text)
	}
}

func TestDailyModelLimitCountsEveryToolLoopProviderCall(t *testing.T) {
	cfg := testConfig(t, map[string]string{"USER_DAILY_MODEL_LIMIT": "2"})
	m := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{{ID: "calc", Name: "calculator", Arguments: `{"expression":"1+1"}`}}},
		{Content: "done"},
	}}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, nil, db)
	resp, err := d.completeWithTools(context.Background(), 11, model.Request{Model: "test-model"}, nil, nil)
	if err != nil || resp.Content != "done" {
		t.Fatalf("completeWithTools response=%+v err=%v", resp, err)
	}
	resp, err = d.completeWithTools(context.Background(), 11, model.Request{Model: "test-model"}, nil, nil)
	if err != ErrModelDailyLimited || resp.Content != "" {
		t.Fatalf("at-limit tool-loop response=%+v err=%v", resp, err)
	}
	if len(m.requests) != 2 {
		t.Fatalf("provider requests=%d, want exactly the daily limit", len(m.requests))
	}
	calls, err := store.CountEvents(context.Background(), db, store.EventModelCall)
	if err != nil || calls != 2 {
		t.Fatalf("model_call events=%d err=%v, want 2", calls, err)
	}
}

func TestDailyModelLimitConcurrentSameUserAcrossChats(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS":        "11",
		"USER_DAILY_MODEL_LIMIT":        "5",
		"RATE_LIMIT_PER_MINUTE":         "0",
		"MAX_CONCURRENT_MODEL_REQUESTS": "12",
	})
	m := &fakeModel{response: model.Response{Content: "ok"}}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, nil, db)
	const attempts = 12
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for attempt := 0; attempt < attempts; attempt++ {
		chatID := int64(1000 + attempt)
		telegramMessageID := int64(3000 + attempt)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- d.Dispatch(context.Background(), testEvent(chatID, 11, telegramMessageID, "question"))
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Dispatch: %v", err)
		}
	}
	if m.callCount() != 5 {
		t.Fatalf("provider calls=%d, want daily cap 5", m.callCount())
	}
	used, err := store.DailyQuotaUsed(context.Background(), db, 11, d.now(), store.DailyQuotaModel)
	if err != nil || used != 5 {
		t.Fatalf("reserved calls=%d err=%v, want 5", used, err)
	}
	var answers int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM messages WHERE user_id = 11 AND role = 'assistant'`).Scan(&answers); err != nil || answers != 5 {
		t.Fatalf("assistant answers=%d err=%v, want 5", answers, err)
	}
	calls, err := store.CountEvents(context.Background(), db, store.EventModelCall)
	if err != nil || calls != 5 {
		t.Fatalf("model_call events=%d err=%v, want 5", calls, err)
	}
}

func TestDailyNetworkLimitConcurrentSameUserCannotExceedCap(t *testing.T) {
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer target.Close()
	cfg := testConfig(t, map[string]string{
		"USER_DAILY_NETWORK_TOOL_LIMIT": "3",
		"WEB_SEARCH_ENABLED":            "true",
		"WEB_SEARCH_BASE_URL":           target.URL,
	})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, nil, nil, db)
	call := model.ToolCall{ID: "search", Name: "web_search", Arguments: `{"query":"concurrent"}`}
	const attempts = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for attempt := 0; attempt < attempts; attempt++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := d.executeNetworkTool(context.Background(), 11, call)
			if err != nil {
				t.Errorf("executeNetworkTool: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if providerCalls.Load() != 3 {
		t.Fatalf("outbound calls=%d, want daily cap 3", providerCalls.Load())
	}
	used, err := store.DailyQuotaUsed(context.Background(), db, 11, d.now(), store.DailyQuotaNetworkTools)
	if err != nil || used != 3 {
		t.Fatalf("reserved network calls=%d err=%v, want 3", used, err)
	}
}

func TestNetworkReservationWriteFailurePreventsOutboundIO(t *testing.T) {
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer target.Close()
	cfg := testConfig(t, map[string]string{
		"USER_DAILY_NETWORK_TOOL_LIMIT": "1",
		"WEB_SEARCH_ENABLED":            "true",
		"WEB_SEARCH_BASE_URL":           target.URL,
	})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, nil, nil, db)
	if _, err := db.ExecContext(context.Background(), `DROP TABLE daily_quota_reservations`); err != nil {
		t.Fatal(err)
	}
	result, executed, err := d.executeNetworkTool(context.Background(), 11, model.ToolCall{
		ID: "search", Name: "web_search", Arguments: `{"query":"must not leave the process"}`,
	})
	if err == nil || executed || result != "" {
		t.Fatalf("failed reservation result=%q executed=%v err=%v", result, executed, err)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("reservation failure allowed %d outbound calls", providerCalls.Load())
	}
	if d.networkWork.Count(11) != 0 || len(d.outboundTools) != 0 {
		t.Fatalf("failed reservation consumed work=%d slots=%d", d.networkWork.Count(11), len(d.outboundTools))
	}
}

func TestUnlimitedNetworkToolDoesNotNeedReservationTable(t *testing.T) {
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer target.Close()
	cfg := testConfig(t, map[string]string{
		"WEB_SEARCH_ENABLED":  "true",
		"WEB_SEARCH_BASE_URL": target.URL,
	})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, nil, nil, db)
	if _, err := db.ExecContext(context.Background(), `DROP TABLE daily_quota_reservations`); err != nil {
		t.Fatal(err)
	}
	result, executed, err := d.executeNetworkTool(context.Background(), 11, model.ToolCall{
		ID: "search", Name: "web_search", Arguments: `{"query":"unlimited"}`,
	})
	if err != nil || !executed || !strings.Contains(result, `"results"`) || providerCalls.Load() != 1 {
		t.Fatalf("unlimited result=%q executed=%v calls=%d err=%v", result, executed, providerCalls.Load(), err)
	}
}

func TestGroupUsageAttributionSurvivesMiniAppSettingsMutation(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12", "STRATA_MODEL": "model-a"})
	tokensIn, tokensOut := int64(7), int64(9)
	m := &fakeModel{response: model.Response{Content: "answer", Model: "model-a", TokensIn: &tokensIn, TokensOut: &tokensOut}}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, nil, db)
	const groupID int64 = -100500
	first := testEvent(groupID, 11, 5001, "first user question")
	first.ChatType = "group"
	if err := dispatch(t, d, first); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureChat(context.Background(), db, groupID, 12, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatModel(context.Background(), db, groupID, "model-a"); err != nil {
		t.Fatal(err)
	}
	second := testEvent(groupID, 12, 5002, "second user question")
	second.ChatType = "group"
	if err := dispatch(t, d, second); err != nil {
		t.Fatal(err)
	}
	rows, err := store.HistoryOfChat(context.Background(), db, groupID, 10)
	if err != nil || len(rows) != 4 {
		t.Fatalf("group history=%+v err=%v", rows, err)
	}
	if rows[1].Role != store.RoleAssistant || rows[1].UserID != 11 || rows[3].Role != store.RoleAssistant || rows[3].UserID != 12 {
		t.Fatalf("assistant attribution=%+v", rows)
	}
	for _, userID := range []int64{11, 12} {
		usage, err := store.UserDailyUsage(context.Background(), db, userID, d.now())
		if err != nil || usage.ModelCalls() != 1 || len(usage.Models) != 1 || usage.Models[0].TokensIn != 7 || usage.Models[0].TokensOut != 9 {
			t.Fatalf("user %d usage=%+v err=%v", userID, usage, err)
		}
	}
}
