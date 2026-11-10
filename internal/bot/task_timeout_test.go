package bot

import (
	"context"
	"testing"
	"time"

	"bot/internal/model"
)

func TestTaskRequestTimeoutSurvivesToolRounds(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TOOLS_ENABLED": "true"})
	var call = model.ToolCall{ID: "call-1", Name: "calculator", Arguments: `{"expression":"1+1"}`}
	var m = &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{call}, Attempts: 1},
		{Content: "готово", Attempts: 1},
	}}
	var d = newTestDispatcher(t, cfg, m, &fakeReplier{}, nil)
	if _, err := d.completeWithTools(context.Background(), 11, model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: "q"}},
		Timeout:  300 * time.Second,
	}, nil, nil); err != nil {
		t.Fatalf("completeWithTools: %v", err)
	}
	if len(m.requests) != 2 {
		t.Fatalf("model calls=%d", len(m.requests))
	}
	for index, request := range m.requests {
		if request.Timeout != 300*time.Second {
			t.Fatalf("call %d Timeout=%s, want 300s", index, request.Timeout)
		}
	}
}

func TestTaskAutoApprovalIsScopedToTaskSteps(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TASK_AUTO_APPROVE_NETWORK": "true"})
	var d = newTestDispatcher(t, cfg, &scriptedToolModel{}, &fakeReplier{}, nil)
	var taskState = &toolLoopContinuation{metadata: toolLoopMetadata{taskID: 7}}
	if !d.taskAutoApprovesNetwork(taskState) {
		t.Fatal("a task step was not auto-approved")
	}
	var chatState = &toolLoopContinuation{metadata: toolLoopMetadata{}}
	if d.taskAutoApprovesNetwork(chatState) {
		t.Fatal("a chat answer was auto-approved")
	}
}
