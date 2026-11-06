package bot

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bot/internal/model"
	"bot/internal/ratelimit"
	"bot/internal/store"
)

type scriptedToolModel struct {
	responses []model.Response
	requests  []model.Request
}

func (m *scriptedToolModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	m.requests = append(m.requests, req)
	if len(m.requests) > len(m.responses) {
		return model.Response{}, nil
	}
	return m.responses[len(m.requests)-1], nil
}

func (m *scriptedToolModel) Models(ctx context.Context) ([]string, error) { return nil, nil }

func explicitSearchToolRequest() model.Request {
	return model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "Найди в интернете актуальную информацию"}}}
}

func TestCalculatorToolLoopStoresOnlyFinalAnswer(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TOOLS_ENABLED": "true"})
	call := model.ToolCall{ID: "call-1", Name: "calculator", Arguments: `{"expression":"2+2*3"}`}
	m := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{call}, Attempts: 1},
		{Content: "Результат: 8", Attempts: 1},
	}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	if err := dispatch(t, d, testEvent(220, 11, 1700, "Сколько 2+2*3?")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(m.requests) != 2 || len(m.requests[0].Tools) != 1 {
		t.Fatalf("model requests=%d", len(m.requests))
	}
	last := m.requests[1].Messages[len(m.requests[1].Messages)-1]
	if last.Role != model.RoleTool || last.ToolCallID != "call-1" || !strings.Contains(last.Content, `"result":"8"`) {
		t.Fatalf("tool result=%+v", last)
	}
	rows, err := store.HistoryOfChat(context.Background(), db, 220, 10)
	if err != nil || len(rows) != 2 || rows[1].Content != "Результат: 8" {
		t.Fatalf("stored rows=%+v err=%v", rows, err)
	}
	uses, err := store.CountEvents(context.Background(), db, store.EventUsage)
	if err != nil || uses != 1 {
		t.Fatalf("calculator usage events=%d err=%v", uses, err)
	}
}

func TestToolLoopRequiresNativeToolCalls(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TOOLS_ENABLED": "true"})
	m := &scriptedToolModel{responses: []model.Response{{Content: "ok"}}}
	d := newTestDispatcher(t, cfg, m, nil, nil)
	if _, err := d.completeWithTools(context.Background(), 11, model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "question"}}}, nil, nil); err != nil {
		t.Fatalf("completeWithTools: %v", err)
	}
	messages := m.requests[0].Messages
	if len(messages) != 2 || messages[0].Role != model.RoleSystem || messages[0].Content != nativeToolInstruction || messages[1].Content != "question" {
		t.Fatalf("native tool instruction messages=%+v", messages)
	}
}

func TestURLReaderDisabledByDefault(t *testing.T) {
	cfg := testConfig(t, nil)
	m := &scriptedToolModel{responses: []model.Response{{Content: "ok"}}}
	d := newTestDispatcher(t, cfg, m, nil, nil)
	if _, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil); err != nil {
		t.Fatalf("completeWithTools: %v", err)
	}
	if len(m.requests) != 1 || len(m.requests[0].Tools) != 1 || m.requests[0].Tools[0].Name != "calculator" {
		t.Fatalf("default tools=%+v", m.requests[0].Tools)
	}
}

func TestURLReaderToolSchema(t *testing.T) {
	cfg := testConfig(t, map[string]string{"URL_READER_ENABLED": "true"})
	m := &scriptedToolModel{responses: []model.Response{{Content: "ok"}}}
	d := newTestDispatcher(t, cfg, m, nil, nil)
	if _, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil); err != nil {
		t.Fatalf("completeWithTools: %v", err)
	}
	if len(m.requests) != 1 || len(m.requests[0].Tools) != 2 {
		t.Fatalf("enabled tools=%+v", m.requests[0].Tools)
	}
	tool := m.requests[0].Tools[1]
	if tool.Name != "read_url" || tool.Description == "" {
		t.Fatalf("read_url schema=%+v", tool)
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("read_url properties=%+v", tool.Parameters)
	}
	url, ok := properties["url"].(map[string]any)
	if !ok || url["type"] != "string" {
		t.Fatalf("read_url url parameter=%+v", properties["url"])
	}
}

func TestURLReaderToolLoopDispatch(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "URL_READER_ENABLED": "true"})
	call := model.ToolCall{ID: "call-url", Name: "read_url", Arguments: `{"url":"https://127.0.0.1/"}`}
	m := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{call}, Attempts: 1},
		{Content: "Страница недоступна", Attempts: 1},
	}}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, db)
	if err := dispatch(t, d, testEvent(221, 11, 1701, "Прочитай https://127.0.0.1/")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(m.requests) != 2 {
		t.Fatalf("model requests=%d", len(m.requests))
	}
	last := m.requests[1].Messages[len(m.requests[1].Messages)-1]
	if last.Role != model.RoleTool || last.ToolName != "read_url" || last.ToolCallID != "call-url" || !json.Valid([]byte(last.Content)) {
		t.Fatalf("tool result=%+v", last)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(last.Content), &payload); err != nil || payload["error"] == nil {
		t.Fatalf("read error payload=%s err=%v", last.Content, err)
	}
	events, err := store.RecentEvents(context.Background(), db, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	uses := 0
	for _, event := range events {
		if event.Kind == store.EventUsage {
			uses++
			if event.Detail != "read_url" {
				t.Fatalf("usage detail=%q", event.Detail)
			}
		}
	}
	if uses != 1 {
		t.Fatalf("read_url usage events=%d", uses)
	}
}

type scriptedStreamToolModel struct {
	scriptedToolModel
}

func (m *scriptedStreamToolModel) Stream(ctx context.Context, req model.Request, handler model.StreamHandler) (model.Response, error) {
	resp, err := m.Complete(ctx, req)
	if err == nil && resp.Content != "" {
		if deltaErr := handler.Delta(resp.Content); deltaErr != nil {
			return resp, deltaErr
		}
	}
	return resp, err
}

func TestToolCallAfterStreamedTextExecutesAndReturnsFinalAnswer(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"STREAMING_ENABLED":      "true",
		"TELEGRAM_ALLOWED_USERS": "11",
		"TOOLS_ENABLED":          "true",
	})
	m := &scriptedStreamToolModel{scriptedToolModel: scriptedToolModel{responses: []model.Response{
		{Content: "Выполняю запрос.", ToolCalls: []model.ToolCall{{ID: "call-1", Name: "calculator", Arguments: `{"expression":"1+1"}`}}, Attempts: 1},
		{Content: "Результат: 2", Attempts: 1},
	}}}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, db)
	session := d.newStreamSession(context.Background(), 203, &streamReplier{})
	resp, err := d.completeWithTools(context.Background(), 11, model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "сочти"}}}, m, session)
	if err != nil || resp.Content != "Результат: 2" || len(m.requests) != 2 {
		t.Fatalf("completeWithTools response=%+v requests=%d err=%v", resp, len(m.requests), err)
	}
	assistant := m.requests[1].Messages[len(m.requests[1].Messages)-2]
	if assistant.Role != model.RoleAssistant || assistant.Content != "Выполняю запрос." || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant tool message=%+v", assistant)
	}
	uses, countErr := store.CountEvents(context.Background(), db, store.EventUsage)
	if countErr != nil || uses != 1 {
		t.Fatalf("tool usage events=%d err=%v", uses, countErr)
	}
}

type countingAcceptListener struct {
	net.Listener
	dials *atomic.Int64
}

func (l *countingAcceptListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.dials.Add(1)
	}
	return conn, err
}

func TestToolLoopRejectsDisabledAndUnsolicitedCalls(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tool    string
		args    string
		raw     map[string]string
		network string
		wantErr string
	}{
		{name: "calculator globally disabled", tool: "calculator", args: `{"expression":"2+2"}`, raw: map[string]string{"TOOLS_ENABLED": "false"}, wantErr: "tool disabled"},
		{name: "read_url feature disabled", tool: "read_url", args: `{"url":"https://example.com"}`, network: "read_url", wantErr: "tool disabled"},
		{name: "read_url globally disabled", tool: "read_url", args: `{"url":"https://example.com"}`, raw: map[string]string{"TOOLS_ENABLED": "false", "URL_READER_ENABLED": "true"}, network: "read_url", wantErr: "tool disabled"},
		{name: "web_search feature disabled", tool: "web_search", args: `{"query":"test"}`, network: "web_search", wantErr: "tool disabled"},
		{name: "web_search globally disabled", tool: "web_search", args: `{"query":"test"}`, raw: map[string]string{"TOOLS_ENABLED": "false"}, network: "web_search", wantErr: "tool disabled"},
		{name: "unsolicited unknown tool", tool: "shell", args: `{}`, raw: map[string]string{"URL_READER_ENABLED": "true", "WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": "https://searx.internal.example"}, wantErr: "unknown tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := make(map[string]string, len(tc.raw)+2)
			for key, value := range tc.raw {
				raw[key] = value
			}
			args := tc.args
			var dials, requests atomic.Int64
			if tc.network != "" {
				target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"results":[]}`))
				}))
				target.Listener = &countingAcceptListener{Listener: target.Listener, dials: &dials}
				if tc.network == "read_url" {
					target.StartTLS()
					args = `{"url":"` + target.URL + `"}`
				} else {
					target.Start()
					raw["WEB_SEARCH_BASE_URL"] = target.URL
				}
				t.Cleanup(target.Close)
			}
			call := model.ToolCall{ID: "forbidden-call", Name: tc.tool, Arguments: args}
			m := &scriptedToolModel{responses: []model.Response{{ToolCalls: []model.ToolCall{call}}}}
			db := testStore(t)
			cfg := testConfig(t, raw)
			if tc.tool == "web_search" && !cfg.ToolsEnabled {
				cfg.WebSearchEnabled = true
			}
			d := newTestDispatcher(t, cfg, m, nil, db)
			_, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("completeWithTools error=%v want=%q", err, tc.wantErr)
			}
			if len(m.requests) != 1 {
				t.Fatalf("model requests=%d, want only the initial request", len(m.requests))
			}
			for _, tool := range m.requests[0].Tools {
				if tool.Name == tc.tool {
					t.Fatalf("disabled tool was offered: %+v", m.requests[0].Tools)
				}
			}
			if len(m.requests[0].Tools) == 0 && len(m.requests[0].Messages) != 0 {
				t.Fatalf("no-tools request contains native tool instruction: %+v", m.requests[0].Messages)
			}
			uses, countErr := store.CountEvents(context.Background(), db, store.EventUsage)
			if countErr != nil || uses != 0 {
				t.Fatalf("usage events=%d err=%v", uses, countErr)
			}
			if dials.Load() != 0 || requests.Load() != 0 || d.networkWork.Count(11) != 0 || len(d.outboundTools) != 0 {
				t.Fatalf("tool side effects: dials=%d requests=%d network work=%d in flight=%d", dials.Load(), requests.Load(), d.networkWork.Count(11), len(d.outboundTools))
			}
		})
	}
}

func TestToolLoopAuthorizationPreflightsWholeRound(t *testing.T) {
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(target.Close)
	cfg := testConfig(t, map[string]string{"WEB_SEARCH_BASE_URL": target.URL})
	m := &scriptedToolModel{responses: []model.Response{{ToolCalls: []model.ToolCall{
		{ID: "allowed", Name: "calculator", Arguments: `{"expression":"2+2"}`},
		{ID: "disabled", Name: "web_search", Arguments: `{"query":"test"}`},
	}}}}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, nil, db)
	_, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil)
	if err == nil || err.Error() != "tool disabled" {
		t.Fatalf("completeWithTools error=%v", err)
	}
	uses, countErr := store.CountEvents(context.Background(), db, store.EventUsage)
	if countErr != nil || uses != 0 || requests.Load() != 0 || len(m.requests) != 1 {
		t.Fatalf("usage=%d count error=%v provider requests=%d model requests=%d", uses, countErr, requests.Load(), len(m.requests))
	}
}

func TestToolLoopCallErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		call model.ToolCall
		want string
	}{
		{name: "unknown tool", call: model.ToolCall{Name: "shell", Arguments: `{}`}, want: "unknown tool"},
		{name: "malformed calculator", call: model.ToolCall{Name: "calculator", Arguments: `{"expression":`}},
		{name: "malformed read_url", call: model.ToolCall{Name: "read_url", Arguments: `{"url":`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, map[string]string{"URL_READER_ENABLED": "true"})
			m := &scriptedToolModel{responses: []model.Response{{ToolCalls: []model.ToolCall{tc.call}}}}
			db := testStore(t)
			d := newTestDispatcher(t, cfg, m, nil, db)
			_, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil)
			if err == nil || tc.want != "" && err.Error() != tc.want {
				t.Fatalf("completeWithTools error=%v want=%q", err, tc.want)
			}
			uses, countErr := store.CountEvents(context.Background(), db, store.EventUsage)
			if countErr != nil || uses != 0 {
				t.Fatalf("usage events=%d err=%v", uses, countErr)
			}
		})
	}
}

func TestToolLoopLimits(t *testing.T) {
	t.Run("calls per round", func(t *testing.T) {
		call := model.ToolCall{Name: "calculator", Arguments: `{"expression":"1+1"}`}
		m := &scriptedToolModel{responses: []model.Response{
			{ToolCalls: []model.ToolCall{call, call, call}},
			{Content: "Готово"},
		}}
		db := testStore(t)
		d := newTestDispatcher(t, testConfig(t, nil), m, nil, db)
		resp, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil)
		if err != nil || resp.Content != "Готово" {
			t.Fatalf("completeWithTools response=%+v error=%v", resp, err)
		}
		messages := m.requests[1].Messages
		last := messages[len(messages)-1]
		if last.Role != model.RoleTool || last.Content != toolRoundCallLimitErrorJSON {
			t.Fatalf("skipped tool response=%+v", last)
		}
		uses, countErr := store.CountEvents(context.Background(), db, store.EventUsage)
		if countErr != nil || uses != 2 {
			t.Fatalf("executed tool usage=%d err=%v", uses, countErr)
		}
	})

	t.Run("final answer after the round limit", func(t *testing.T) {
		call := model.ToolCall{Name: "calculator", Arguments: `{"expression":"1+1"}`}
		m := &scriptedToolModel{responses: []model.Response{
			{ToolCalls: []model.ToolCall{call}},
			{ToolCalls: []model.ToolCall{call}},
			{Content: "Итог: 2"},
		}}
		d := newTestDispatcher(t, testConfig(t, nil), m, nil, nil)
		resp, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil)
		if err != nil || resp.Content != "Итог: 2" {
			t.Fatalf("completeWithTools response=%+v error=%v", resp, err)
		}
		if len(m.requests) != 3 || len(m.requests[2].Tools) != 0 {
			t.Fatalf("final request must disable tools, requests=%+v", m.requests)
		}
	})
}

func TestCalculatorValidation(t *testing.T) {
	for input, want := range map[string]string{"2+2*3": "8", "(10-2)/4": "2", "-3.5+1": "-2.5", "10%3": "1"} {
		result, err := executeCalculator(model.ToolCall{Name: "calculator", Arguments: `{"expression":` + strconvQuote(input) + `}`})
		if err != nil || !strings.Contains(result, `"result":"`+want+`"`) {
			t.Errorf("%s result=%q err=%v want=%s", input, result, err, want)
		}
	}
	for _, input := range []string{"1/0", "2;system()", "abc", "(2+3"} {
		if _, err := executeCalculator(model.ToolCall{Name: "calculator", Arguments: `{"expression":` + strconvQuote(input) + `}`}); err == nil {
			t.Errorf("%q must be rejected", input)
		}
	}
	if _, err := executeCalculator(model.ToolCall{Name: "shell", Arguments: `{}`}); err == nil {
		t.Fatal("unknown tool must be rejected")
	}
}

func strconvQuote(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"")
	return "\"" + replacer.Replace(value) + "\""
}

func TestModelWorkBudgetIncludesToolRoundsAndFinalSynthesis(t *testing.T) {
	call := model.ToolCall{ID: "budget-call", Name: "calculator", Arguments: `{"expression":"1+1"}`}
	responses := make([]model.Response, 0, 60)
	for i := 0; i < 20; i++ {
		responses = append(responses,
			model.Response{ToolCalls: []model.ToolCall{call}},
			model.Response{ToolCalls: []model.ToolCall{call}},
			model.Response{Content: "done"},
		)
	}
	m := &scriptedToolModel{responses: responses}
	d := newTestDispatcher(t, testConfig(t, nil), m, nil, nil)
	now := time.Unix(1700000000, 0)
	d.modelWork.WithClock(func() time.Time { return now })
	if got := d.modelWork.PerMinute(); got != 60 {
		t.Fatalf("default model work limit=%d, want 60", got)
	}
	for i := 0; i < 20; i++ {
		resp, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil)
		if err != nil || resp.Content != "done" {
			t.Fatalf("question %d response=%+v err=%v", i, resp, err)
		}
	}
	if len(m.requests) != 60 || d.modelWork.Count(11) != 60 {
		t.Fatalf("model requests=%d budget count=%d, want 60", len(m.requests), d.modelWork.Count(11))
	}
	resp, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil)
	if err != ErrModelRateLimited || resp.Content != "" {
		t.Fatalf("over-budget response=%+v err=%v", resp, err)
	}
	if len(m.requests) != 60 {
		t.Fatalf("over-budget question reached the model: requests=%d", len(m.requests))
	}
	if _, err := d.completeWithTools(context.Background(), 12, model.Request{}, nil, nil); err != nil {
		t.Fatalf("independent user: %v", err)
	}
	if len(m.requests) != 61 || d.modelWork.Count(12) != 1 || d.modelWork.Count(11) != 60 {
		t.Fatalf("per-user counters leaked: requests=%d user11=%d user12=%d", len(m.requests), d.modelWork.Count(11), d.modelWork.Count(12))
	}
	now = now.Add(ratelimit.Window)
	if _, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil); err != nil {
		t.Fatalf("after budget window: %v", err)
	}
	if len(m.requests) != 62 || d.modelWork.Count(11) != 1 {
		t.Fatalf("budget did not reset: requests=%d count=%d", len(m.requests), d.modelWork.Count(11))
	}
}

func TestNetworkToolWorkBudgetReturnsJSONForSynthesis(t *testing.T) {
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(target.Close)
	cfg := testConfig(t, map[string]string{
		"WEB_SEARCH_ENABLED":  "true",
		"WEB_SEARCH_BASE_URL": target.URL,
	})
	calls := []model.ToolCall{
		{ID: "search-1", Name: "web_search", Arguments: `{"query":"one"}`},
		{ID: "search-2", Name: "web_search", Arguments: `{"query":"two"}`},
	}
	responses := make([]model.Response, 0, 9)
	for i := 0; i < 3; i++ {
		responses = append(responses,
			model.Response{ToolCalls: calls},
			model.Response{ToolCalls: calls},
			model.Response{Content: "synthesized"},
		)
	}
	m := &scriptedToolModel{responses: responses}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, nil, db)
	for i := 0; i < 3; i++ {
		resp, err := d.completeWithTools(context.Background(), 11, explicitSearchToolRequest(), nil, nil)
		if err != nil || resp.Content != "synthesized" {
			t.Fatalf("question %d response=%+v err=%v", i, resp, err)
		}
	}
	if providerCalls.Load() != 8 || d.networkWork.Count(11) != 8 {
		t.Fatalf("provider calls=%d network count=%d, want 8", providerCalls.Load(), d.networkWork.Count(11))
	}
	budgetErrors := 0
	for _, message := range m.requests[8].Messages {
		if message.Role == model.RoleTool && message.Content == networkWorkBudgetErrorJSON {
			budgetErrors++
			if !json.Valid([]byte(message.Content)) {
				t.Fatalf("invalid budget error JSON: %q", message.Content)
			}
		}
	}
	if budgetErrors != 4 {
		t.Fatalf("final synthesis received %d budget errors, want 4", budgetErrors)
	}
	uses, err := store.CountEvents(context.Background(), db, store.EventUsage)
	if err != nil || uses != 8 {
		t.Fatalf("actual network usage events=%d err=%v, want 8", uses, err)
	}
	result, executed, err := d.executeNetworkTool(context.Background(), 12, calls[0])
	if err != nil || !executed || !strings.Contains(result, `"results"`) {
		t.Fatalf("independent user result=%q executed=%v err=%v", result, executed, err)
	}
	if providerCalls.Load() != 9 || d.networkWork.Count(11) != 8 || d.networkWork.Count(12) != 1 {
		t.Fatalf("network counters leaked: calls=%d user11=%d user12=%d", providerCalls.Load(), d.networkWork.Count(11), d.networkWork.Count(12))
	}
}

func TestCalculatorDoesNotConsumeNetworkWork(t *testing.T) {
	call := model.ToolCall{ID: "local", Name: "calculator", Arguments: `{"expression":"2+2"}`}
	m := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{call}},
		{Content: "4"},
	}}
	d := newTestDispatcher(t, testConfig(t, nil), m, nil, nil)
	resp, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil)
	if err != nil || resp.Content != "4" {
		t.Fatalf("calculator response=%+v err=%v", resp, err)
	}
	if d.networkWork.Count(11) != 0 || len(d.outboundTools) != 0 {
		t.Fatalf("calculator consumed network work: count=%d in flight=%d", d.networkWork.Count(11), len(d.outboundTools))
	}
}

type roundAwareToolModel struct {
	mu          sync.Mutex
	requests    []model.Request
	call        model.ToolCall
	synthesized chan struct{}
}

func (m *roundAwareToolModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	for _, message := range req.Messages {
		if message.Role == model.RoleTool {
			if m.synthesized != nil {
				m.synthesized <- struct{}{}
			}
			return model.Response{Content: "synthesized"}, nil
		}
	}
	return model.Response{ToolCalls: []model.ToolCall{m.call}}, nil
}

func (m *roundAwareToolModel) Models(ctx context.Context) ([]string, error) { return nil, nil }

func (m *roundAwareToolModel) snapshot() []model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.Request(nil), m.requests...)
}

func TestNetworkToolGlobalConcurrencyLimit(t *testing.T) {
	const callers = 12
	var active, maxActive, providerCalls atomic.Int64
	started := make(chan struct{}, callers)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(target.Close)
	cfg := testConfig(t, map[string]string{
		"WEB_SEARCH_ENABLED":  "true",
		"WEB_SEARCH_BASE_URL": target.URL,
	})
	m := &roundAwareToolModel{
		call:        model.ToolCall{ID: "concurrent-search", Name: "web_search", Arguments: `{"query":"test"}`},
		synthesized: make(chan struct{}, callers),
	}
	d := newTestDispatcher(t, cfg, m, nil, nil)
	begin := make(chan struct{})
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func(userID int64) {
			<-begin
			_, err := d.completeWithTools(context.Background(), userID, explicitSearchToolRequest(), nil, nil)
			errs <- err
		}(int64(100 + i))
	}
	close(begin)
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for i := 0; i < globalOutboundToolLimit; i++ {
		select {
		case <-started:
		case <-timeout.C:
			t.Fatalf("only %d outbound calls started", i)
		}
	}
	for i := 0; i < callers-globalOutboundToolLimit; i++ {
		select {
		case <-m.synthesized:
		case <-timeout.C:
			t.Fatalf("only %d busy callers reached synthesis", i)
		}
	}
	if got := providerCalls.Load(); got != globalOutboundToolLimit {
		t.Fatalf("provider calls while first batch is blocked=%d, want 4", got)
	}
	releaseAll()
	for i := 0; i < callers; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("concurrent completeWithTools: %v", err)
			}
		case <-timeout.C:
			t.Fatal("concurrent callers did not finish")
		}
	}
	if providerCalls.Load() != globalOutboundToolLimit || maxActive.Load() != globalOutboundToolLimit {
		t.Fatalf("provider calls=%d max active=%d, want 4", providerCalls.Load(), maxActive.Load())
	}
	busyErrors := 0
	for _, req := range m.snapshot() {
		for _, message := range req.Messages {
			if message.Role == model.RoleTool && message.Content == outboundToolBusyErrorJSON {
				busyErrors++
				if !json.Valid([]byte(message.Content)) {
					t.Fatalf("invalid busy error JSON: %q", message.Content)
				}
			}
		}
	}
	if busyErrors != callers-globalOutboundToolLimit {
		t.Fatalf("busy JSON results=%d, want %d", busyErrors, callers-globalOutboundToolLimit)
	}
	networkWork := 0
	for i := 0; i < callers; i++ {
		networkWork += d.networkWork.Count(int64(100 + i))
	}
	if networkWork != globalOutboundToolLimit || len(d.outboundTools) != 0 {
		t.Fatalf("network work=%d outbound slots held=%d, want 4 and 0", networkWork, len(d.outboundTools))
	}
}
