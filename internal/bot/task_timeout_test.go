package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
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

// Task test budgets. They are set on the parsed config (the loader floors the ENV rows at 30/60 s), so a test
// can prove the deadline propagation in seconds instead of minutes.
const (
	taskTestModelTimeout = time.Second
	taskTestStepDeadline = 3 * time.Second
)

func taskBudgetTestConfig(t *testing.T, modelTimeout time.Duration, stepDeadline time.Duration, extra map[string]string) config.Config {
	t.Helper()
	var env = map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TOOLS_ENABLED": "true"}
	for key, value := range extra {
		env[key] = value
	}
	var cfg = testConfig(t, env)
	cfg.TaskModelTimeoutSeconds = int64(modelTimeout / time.Second)
	cfg.TaskStepDeadlineSeconds = int64(stepDeadline / time.Second)
	cfg.TimeoutSeconds = 1
	cfg.ReplyDeadlineSeconds = 2
	return cfg
}

// blockingTaskModel answers the planner at once and then blocks for the smaller of Request.Timeout and the
// caller's remaining deadline, returning the same error the real client returns when the budget is spent.
type blockingTaskModel struct {
	mu           sync.Mutex
	requests     []model.Request
	deadlineLeft []time.Duration
	plan         string
	// stepResult, when set, answers the first non-planner call at once: the synthesis test needs a completed
	// step before the call that is expected to block.
	stepResult string
}

func (m *blockingTaskModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	call := len(m.requests)
	left := time.Duration(-1)
	if deadline, ok := ctx.Deadline(); ok {
		left = time.Until(deadline)
	}
	m.deadlineLeft = append(m.deadlineLeft, left)
	m.mu.Unlock()
	if call == 1 {
		return model.Response{Content: m.plan, Model: "selected-model", Attempts: 1}, nil
	}
	if call == 2 && m.stepResult != "" {
		return model.Response{Content: m.stepResult, Model: "selected-model", Attempts: 1}, nil
	}
	budget := req.Timeout
	if budget <= 0 {
		budget = time.Minute
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}
	if budget < time.Millisecond {
		budget = time.Millisecond
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return model.Response{}, fmt.Errorf("%w: %s", ctx.Err(), model.TelegramBudgetNotice)
	case <-timer.C:
		return model.Response{}, fmt.Errorf("%w: %s", context.DeadlineExceeded, model.TelegramBudgetNotice)
	}
}

func (m *blockingTaskModel) Models(context.Context) ([]string, error) { return nil, nil }

func (m *blockingTaskModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *blockingTaskModel) snapshot() ([]model.Request, []time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.Request{}, m.requests...), append([]time.Duration{}, m.deadlineLeft...)
}

func TestTaskStepModelTimeoutIsRetriedOnceAndNamesTheStepBudget(t *testing.T) {
	var cfg = taskBudgetTestConfig(t, taskTestModelTimeout, taskTestStepDeadline, nil)
	var m = &blockingTaskModel{plan: validTaskPlanTwo}
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))
	d.taskStepRetryDelay = 0
	if err := dispatch(t, d, testEvent(100, 11, 2101, "/task a slow research step")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 2)

	current, found, err := store.CurrentTask(context.Background(), d.deps.DB, 100)
	if err != nil || !found || current.Status != store.TaskStatusFailed {
		t.Fatalf("task=%+v found=%v err=%v", current, found, err)
	}
	// Planner + the step call + its single retry: the spent model budget must trigger the retry, not a
	// straight failure.
	if got := m.callCount(); got != 3 {
		t.Fatalf("model calls=%d, want planner and the step with one retry", got)
	}
	if retries, err := store.CountEvents(context.Background(), d.deps.DB, store.EventTaskStepRetry); err != nil || retries != 1 {
		t.Fatalf("task_step_retry events=%d err=%v, want exactly one", retries, err)
	}
	var reply = r.joined()
	if !strings.Contains(reply, "TASK_STEP_DEADLINE_SECONDS") {
		t.Fatalf("the failure does not name the task step budget: %q", reply)
	}
	if strings.Contains(reply, "STRATA_TIMEOUT_SECONDS") {
		t.Fatalf("the task failure leaked the chat budget name: %q", reply)
	}
	if strings.Contains(current.Error, "STRATA_TIMEOUT_SECONDS") {
		t.Fatalf("tasks.error leaked the chat budget name: %q", current.Error)
	}
	requests, deadlines := m.snapshot()
	for index, request := range requests[1:] {
		if request.Timeout != taskTestModelTimeout {
			t.Fatalf("call %d Timeout=%s, want %s", index+1, request.Timeout, taskTestModelTimeout)
		}
		if deadlines[index+1] <= 0 || deadlines[index+1] > taskTestStepDeadline {
			t.Fatalf("call %d saw a step deadline of %s, want (0, %s]", index+1, deadlines[index+1], taskTestStepDeadline)
		}
	}
}

func TestTaskSynthesisTimeoutIsRetriedOnceAndNamesTheStepBudget(t *testing.T) {
	var cfg = taskBudgetTestConfig(t, taskTestModelTimeout, taskTestStepDeadline, nil)
	var m = &blockingTaskModel{plan: validTaskPlanOne, stepResult: "completed step result"}
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))
	d.taskStepRetryDelay = 0
	if err := dispatch(t, d, testEvent(100, 11, 2102, "/task a slow final answer")); err != nil {
		t.Fatal(err)
	}
	// Pass 1 stores the only step; pass 2 spends the synthesis budget and its retry.
	processTasks(t, d, 3)

	current, found, err := store.CurrentTask(context.Background(), d.deps.DB, 100)
	if err != nil || !found || current.Status != store.TaskStatusFailed {
		t.Fatalf("task=%+v found=%v err=%v", current, found, err)
	}
	if got := m.callCount(); got != 4 {
		t.Fatalf("model calls=%d, want planner + step + synthesis + its retry", got)
	}
	if retries, err := store.CountEvents(context.Background(), d.deps.DB, store.EventTaskStepRetry); err != nil || retries != 1 {
		t.Fatalf("task_step_retry events=%d err=%v, want exactly one synthesis retry", retries, err)
	}
	var reply = r.joined()
	if !strings.Contains(reply, "TASK_STEP_DEADLINE_SECONDS") || strings.Contains(reply, "STRATA_TIMEOUT_SECONDS") {
		t.Fatalf("the synthesis failure did not name the task budget: %q", reply)
	}
	requests, deadlines := m.snapshot()
	for index, request := range requests[1:] {
		if request.Timeout != taskTestModelTimeout {
			t.Fatalf("call %d Timeout=%s, want %s", index+1, request.Timeout, taskTestModelTimeout)
		}
		if deadlines[index+1] <= 0 {
			t.Fatalf("call %d had no step deadline", index+1)
		}
	}
}

// toolRoundTaskModel scripts one full research step: the planner, every tool round, the final answer after
// the rounds, and the synthesis.
type toolRoundTaskModel struct {
	mu           sync.Mutex
	requests     []model.Request
	deadlineLeft []time.Duration
}

func (m *toolRoundTaskModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	call := len(m.requests)
	left := time.Duration(-1)
	if deadline, ok := ctx.Deadline(); ok {
		left = time.Until(deadline)
	}
	m.deadlineLeft = append(m.deadlineLeft, left)
	m.mu.Unlock()
	switch {
	case call == 1:
		return model.Response{Content: validTaskPlanOne, Model: "selected-model", Attempts: 1}, nil
	case call <= 1+toolRoundLimit:
		// toolRoundLimit rounds, each with one calculator call.
		return model.Response{ToolCalls: []model.ToolCall{{
			ID: fmt.Sprintf("call-%d", call), Name: "calculator", Arguments: `{"expression":"2+2"}`,
		}}, Model: "selected-model", Attempts: 1}, nil
	case call == 2+toolRoundLimit:
		return model.Response{Content: "step result after every round", Model: "selected-model", Attempts: 1}, nil
	default:
		return model.Response{Content: "final answer", Model: "selected-model", Attempts: 1}, nil
	}
}

func (m *toolRoundTaskModel) Models(context.Context) ([]string, error) { return nil, nil }

func (m *toolRoundTaskModel) snapshot() ([]model.Request, []time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.Request{}, m.requests...), append([]time.Duration{}, m.deadlineLeft...)
}

func TestTaskStepWithEveryToolRoundFitsTheStepDeadline(t *testing.T) {
	// Five calls per step in the worst case: four tool rounds plus the final answer. This test runs all five
	// inside one step context and proves each one still sees the task budget, never the chat deadline.
	var cfg = taskBudgetTestConfig(t, taskTestModelTimeout, taskTestStepDeadline, nil)
	var m = &toolRoundTaskModel{}
	var d = newTestDispatcher(t, cfg, m, &fakeReplier{}, testStore(t))
	if err := dispatch(t, d, testEvent(100, 11, 2103, "/task research every round")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 3)
	current, found, err := store.CurrentTask(context.Background(), d.deps.DB, 100)
	if err != nil || !found || current.Status != store.TaskStatusCompleted {
		t.Fatalf("task=%+v found=%v err=%v", current, found, err)
	}
	requests, deadlines := m.snapshot()
	if len(requests) != 2+toolRoundLimit+1 {
		t.Fatalf("model calls=%d, want planner + %d step calls + synthesis", len(requests), 1+toolRoundLimit)
	}
	for index, request := range requests {
		if request.Timeout != taskTestModelTimeout {
			t.Fatalf("call %d Timeout=%s, want %s", index, request.Timeout, taskTestModelTimeout)
		}
		if deadlines[index] <= 0 || deadlines[index] > taskTestStepDeadline {
			t.Fatalf("call %d saw %s left on its context, want (0, %s]", index, deadlines[index], taskTestStepDeadline)
		}
	}
	// The step spent all five calls and still finished: its result is what the synthesis consumed.
	results, err := store.ParseTaskResults(current.ResultsJSON)
	if err != nil || len(results) != 1 || !strings.Contains(results[0].Content, "step result") {
		t.Fatalf("step results=%+v err=%v", results, err)
	}
}

func TestTaskModelFailureLogNamesEveryBudgetWithoutPromptContent(t *testing.T) {
	var logs bytes.Buffer
	var logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	var cfg = taskBudgetTestConfig(t, taskTestModelTimeout, taskTestStepDeadline, nil)
	var m = &blockingTaskModel{plan: validTaskPlanOne}
	var d = newTestDispatcher(t, cfg, m, &fakeReplier{}, testStore(t), logger)
	d.taskStepRetryDelay = 0
	var goal = "diagnose the failure budgets"
	if err := dispatch(t, d, testEvent(100, 11, 2104, "/task "+goal)); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 2)

	var seen bool
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if !strings.Contains(line, "task model call failed") {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("log line is not JSON: %v: %q", err, line)
		}
		seen = true
		if fields["phase"] != taskPhaseTool {
			t.Fatalf("phase=%v, want %q", fields["phase"], taskPhaseTool)
		}
		for _, name := range []string{"request_timeout_seconds", "ctx_deadline_seconds", "step_deadline_seconds", "strata_timeout_seconds"} {
			if _, ok := fields[name]; !ok {
				t.Fatalf("the failure log misses %q: %q", name, line)
			}
		}
		if got := fields["request_timeout_seconds"]; got != float64(int64(taskTestModelTimeout/time.Second)) {
			t.Fatalf("request_timeout_seconds=%v, want %v", got, int64(taskTestModelTimeout/time.Second))
		}
		if got := fields["step_deadline_seconds"]; got != float64(int64(taskTestStepDeadline/time.Second)) {
			t.Fatalf("step_deadline_seconds=%v, want %v", got, int64(taskTestStepDeadline/time.Second))
		}
		if got := fields["strata_timeout_seconds"]; got != float64(cfg.TimeoutSeconds) {
			t.Fatalf("strata_timeout_seconds=%v, want %d", got, cfg.TimeoutSeconds)
		}
		if got, ok := fields["ctx_deadline_seconds"].(float64); !ok || got <= 0 {
			t.Fatalf("ctx_deadline_seconds=%v, want a positive remaining step budget", fields["ctx_deadline_seconds"])
		}
	}
	if !seen {
		t.Fatalf("no task model failure line was logged:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), goal) {
		t.Fatalf("the diagnostic log leaked the prompt: %s", logs.String())
	}
}

func TestTaskModelAndStepBudgetsNeverFallBackToTheChatBudget(t *testing.T) {
	var d = &Dispatcher{deps: Deps{Cfg: config.Config{}}}
	if got := d.taskModelTimeout(); got != time.Duration(config.DefaultTaskModelTimeoutSeconds)*time.Second {
		t.Fatalf("taskModelTimeout with an empty config = %s, want the task default", got)
	}
	if got := d.taskStepDeadline(); got != time.Duration(config.DefaultTaskStepDeadlineSeconds)*time.Second {
		t.Fatalf("taskStepDeadline with an empty config = %s, want the task default", got)
	}
}
