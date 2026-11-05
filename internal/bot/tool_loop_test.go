package bot

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"bot/internal/model"
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
	if err := dispatch(t, d, testEvent(221, 11, 1701, "Прочитай страницу")); err != nil {
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
			if dials.Load() != 0 || requests.Load() != 0 {
				t.Fatalf("tool side effects: dials=%d requests=%d", dials.Load(), requests.Load())
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
		m := &scriptedToolModel{responses: []model.Response{{ToolCalls: []model.ToolCall{call, call, call}}}}
		d := newTestDispatcher(t, testConfig(t, nil), m, nil, nil)
		_, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil)
		if err == nil || err.Error() != "model requested more than two tool calls in one round" {
			t.Fatalf("completeWithTools error=%v", err)
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
