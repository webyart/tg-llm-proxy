package bot

import (
	"context"
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
