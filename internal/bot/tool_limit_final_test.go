package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/model"
)

func TestToolLimitFinalAnswerRetriesWithoutTools(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TOOLS_ENABLED": "true"})
	var markupOnly = "<tool_call>\n<function=web_search>\n<parameter=query>\nignored\n</parameter>\n</function>\n</tool_call>"
	var responses []model.Response
	for index := 0; index < toolRoundLimit; index++ {
		responses = append(responses, model.Response{ToolCalls: []model.ToolCall{{
			ID: "call-1", Name: "calculator", Arguments: `{"expression":"1+1"}`,
		}}, Attempts: 1})
	}
	responses = append(responses,
		model.Response{Content: markupOnly, Attempts: 1},
		model.Response{Content: "Финальный ответ", Attempts: 1},
	)
	var m = &scriptedToolModel{responses: responses}
	var d = newTestDispatcher(t, cfg, m, &fakeReplier{}, nil)
	var response, err = d.completeWithTools(context.Background(), 11, model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "q"}}}, nil, nil)
	if err != nil {
		t.Fatalf("completeWithTools: %v", err)
	}
	if response.Content != "Финальный ответ" || strings.Contains(response.Content, "<tool_call") {
		t.Fatalf("response=%q", response.Content)
	}
	if len(m.requests) != toolRoundLimit+2 {
		t.Fatalf("model calls=%d, want tool rounds + final + corrective final", len(m.requests))
	}
	var corrective bool
	for _, message := range m.requests[len(m.requests)-1].Messages {
		if message.Role == model.RoleSystem && strings.Contains(message.Content, "Tools are no longer available") {
			corrective = true
		}
	}
	if !corrective {
		t.Fatal("the corrective final-answer instruction was not sent")
	}
}
