package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/model"
)

func TestToolLimitFinalAnswerRetriesWithoutTools(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TOOLS_ENABLED": "true"})
	var call = model.ToolCall{ID: "call-1", Name: "calculator", Arguments: `{"expression":"1+1"}`}
	var markupOnly = "<tool_call>\n<function=web_search>\n<parameter=query>\nignored\n</parameter>\n</function>\n</tool_call>"
	var m = &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{call}, Attempts: 1},
		{ToolCalls: []model.ToolCall{call}, Attempts: 1},
		{Content: markupOnly, Attempts: 1},
		{Content: "Финальный ответ", Attempts: 1},
	}}
	var d = newTestDispatcher(t, cfg, m, &fakeReplier{}, nil)
	var response, err = d.completeWithTools(context.Background(), 11, model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "q"}}}, nil, nil)
	if err != nil {
		t.Fatalf("completeWithTools: %v", err)
	}
	if response.Content != "Финальный ответ" || strings.Contains(response.Content, "<tool_call") {
		t.Fatalf("response=%q", response.Content)
	}
	if len(m.requests) != 4 {
		t.Fatalf("model calls=%d, want two tool rounds + final + corrective final", len(m.requests))
	}
	var corrective bool
	for _, message := range m.requests[3].Messages {
		if message.Role == model.RoleSystem && strings.Contains(message.Content, "Tools are no longer available") {
			corrective = true
		}
	}
	if !corrective {
		t.Fatal("the corrective final-answer instruction was not sent")
	}
}
