package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

func TestDailyModelLimitDefaultsToUnlimited(t *testing.T) {
	m := &fakeModel{response: model.Response{Content: "ok"}}
	d := newTestDispatcher(t, testConfig(t, nil), m, nil, nil)
	for i := 0; i < 2; i++ {
		resp, err := d.callModel(context.Background(), 11, model.Request{}, nil, nil)
		if err != nil || resp.Content != "ok" {
			t.Fatalf("callModel %d response=%+v err=%v", i, resp, err)
		}
	}
	if m.callCount() != 2 {
		t.Fatalf("provider calls=%d, want 2 with unlimited defaults", m.callCount())
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
	if err != nil || resp.Content != modelDailyLimitErrorText {
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

func TestDailyModelLimitRefusalIsNotStoredAsUsage(t *testing.T) {
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
	d.logEvent(context.Background(), 11, store.EventUsage, "web_search")
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
	if err != nil || resp.Content != modelDailyLimitErrorText {
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
