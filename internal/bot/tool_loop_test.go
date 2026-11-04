package bot

import (
	"context"
	"encoding/json"
	"strings"
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

	t.Run("rounds", func(t *testing.T) {
		call := model.ToolCall{Name: "calculator", Arguments: `{"expression":"1+1"}`}
		m := &scriptedToolModel{responses: []model.Response{
			{ToolCalls: []model.ToolCall{call}},
			{ToolCalls: []model.ToolCall{call}},
		}}
		d := newTestDispatcher(t, testConfig(t, nil), m, nil, nil)
		_, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil)
		if err == nil || err.Error() != "model exceeded the tool round limit" {
			t.Fatalf("completeWithTools error=%v", err)
		}
		if len(m.requests) != 2 {
			t.Fatalf("model requests=%d", len(m.requests))
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
