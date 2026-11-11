package bot

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"bot/internal/metrics"
	"bot/internal/model"
	"bot/internal/store"
)

func TestTaskStepRetriesOnceAfterARetryableProviderError(t *testing.T) {
	m := &taskScriptModel{results: []taskModelResult{
		{response: model.Response{Content: validTaskPlanOne}},
		{err: &model.APIError{Status: 503}},
		{response: model.Response{Content: "recovered step result"}},
		{response: model.Response{Content: "recovered final answer"}},
	}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	d.taskStepRetryDelay = 0
	if err := dispatch(t, d, testEvent(100, 11, 1201, "/task recover one step")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 3)
	current, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || current.Status != store.TaskStatusCompleted {
		t.Fatalf("task=%+v found=%v err=%v replies=%q", current, found, err, r.joined())
	}
	if m.callCount() != 4 {
		t.Fatalf("model calls=%d, want planner + failed step + retried step + synthesis", m.callCount())
	}
	if !strings.Contains(r.joined(), "recovered final answer") {
		t.Fatalf("the retried step did not reach the final answer: %q", r.joined())
	}
	retries, err := store.CountEvents(context.Background(), db, store.EventTaskStepRetry)
	if err != nil || retries != 1 {
		t.Fatalf("task_step_retry events=%d err=%v, want exactly one", retries, err)
	}
}

func TestTaskStepFailsAfterTheSingleRetry(t *testing.T) {
	m := &taskScriptModel{results: []taskModelResult{
		{response: model.Response{Content: validTaskPlanOne}},
		{err: &model.APIError{Status: 502}},
		{err: &model.APIError{Status: 502}},
	}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	d.taskStepRetryDelay = 0
	if err := dispatch(t, d, testEvent(100, 11, 1202, "/task fail twice")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 2)
	current, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || current.Status != store.TaskStatusFailed {
		t.Fatalf("task=%+v found=%v err=%v", current, found, err)
	}
	if m.callCount() != 3 {
		t.Fatalf("model calls=%d, want planner + the step and its single retry", m.callCount())
	}
	if !strings.Contains(r.joined(), "Задача #1 остановлена") {
		t.Fatalf("the user was not told about the failure: %q", r.joined())
	}
	retries, err := store.CountEvents(context.Background(), db, store.EventTaskStepRetry)
	if err != nil || retries != 1 {
		t.Fatalf("task_step_retry events=%d err=%v, want exactly one", retries, err)
	}
	// The failed task is terminal: another scheduler pass must not spend a second retry budget.
	processTasks(t, d, 2)
	if m.callCount() != 3 {
		t.Fatalf("a failed task was re-executed: calls=%d", m.callCount())
	}
}

func TestTaskStepRetriesOnceAfterAnEmptyAnswer(t *testing.T) {
	m := &taskScriptModel{results: []taskModelResult{
		{response: model.Response{Content: validTaskPlanOne}},
		{response: model.Response{}},
		{response: model.Response{Content: "recovered from empty"}},
		{response: model.Response{Content: "empty recovery answer"}},
	}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	d.taskStepRetryDelay = 0
	if err := dispatch(t, d, testEvent(100, 11, 1203, "/task answer after empty")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 3)
	current, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || current.Status != store.TaskStatusCompleted {
		t.Fatalf("task=%+v found=%v err=%v", current, found, err)
	}
	if m.callCount() != 4 {
		t.Fatalf("model calls=%d, want planner + empty + correction + synthesis", m.callCount())
	}
	retries, err := store.CountEvents(context.Background(), db, store.EventTaskStepRetry)
	if err != nil || retries != 1 {
		t.Fatalf("task_step_retry events=%d err=%v", retries, err)
	}
	retryRequest := m.requests[2]
	correction := false
	for _, message := range retryRequest.Messages {
		if strings.Contains(message.Content, "previous step answer was empty") {
			correction = true
		}
	}
	if !correction {
		t.Fatalf("the empty-answer retry did not carry the correction prompt: %+v", retryRequest.Messages)
	}
}

func TestTaskStepDoesNotRetryACancelledStep(t *testing.T) {
	m := &taskScriptModel{results: []taskModelResult{
		{response: model.Response{Content: validTaskPlanOne}},
		{err: context.Canceled},
	}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	d.taskStepRetryDelay = 0
	if err := dispatch(t, d, testEvent(100, 11, 1204, "/task cancel the step")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 2)
	if m.callCount() != 2 {
		t.Fatalf("a cancelled step was retried: model calls=%d", m.callCount())
	}
	current, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || current.Status != store.TaskStatusFailed {
		t.Fatalf("task=%+v found=%v err=%v", current, found, err)
	}
	if retries, err := store.CountEvents(context.Background(), db, store.EventTaskStepRetry); err != nil || retries != 0 {
		t.Fatalf("cancel recorded %d task_step_retry events, err=%v", retries, err)
	}
}

func TestTaskInvalidPlanIsNotRetried(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	m := &taskScriptModel{}
	r := &fakeReplier{}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	now := d.now().UTC()
	if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, db, store.Task{
		ChatID: 100, UserID: 11, Goal: "invalid plan", Model: "selected-model",
		PlanJSON: `{"steps":[]}`, DueAt: now, ExpiresAt: now.Add(TaskLifetime), CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 2)
	if m.callCount() != 0 {
		t.Fatalf("an invalid plan reached the model %d time(s)", m.callCount())
	}
	current, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || current.ID != task.ID || current.Status != store.TaskStatusFailed {
		t.Fatalf("task=%+v found=%v err=%v", current, found, err)
	}
	if retries, err := store.CountEvents(ctx, db, store.EventTaskStepRetry); err != nil || retries != 0 {
		t.Fatalf("invalid plan recorded %d task_step_retry events, err=%v", retries, err)
	}
}

func TestTaskFailureReasonClassification(t *testing.T) {
	cases := []struct {
		name  string
		cause error
		want  string
	}{
		{name: "model", cause: &model.APIError{Status: 503}, want: "model"},
		{name: "non-retryable provider", cause: &model.APIError{Status: 401}, want: "model"},
		{name: "empty", cause: model.ErrEmptyAnswer, want: "empty"},
		{name: "invalid", cause: store.ErrTaskInvalid, want: "invalid"},
		{name: "wrapped invalid", cause: errors.Join(errors.New("plan"), store.ErrTaskInvalid), want: "invalid"},
		{name: "timeout", cause: context.DeadlineExceeded, want: "timeout"},
		{name: "budget per minute", cause: ErrModelRateLimited, want: "budget"},
		{name: "budget daily", cause: ErrModelDailyLimited, want: "budget"},
		{name: "budget chat daily", cause: ErrChatModelDailyLimited, want: "budget"},
		{name: "cancel", cause: context.Canceled, want: "other"},
		{name: "no room", cause: model.ErrNoRoom, want: "other"},
		{name: "plain", cause: errors.New("execution failed safely"), want: "other"},
		{name: "nil", cause: nil, want: "other"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := taskFailureReason(test.cause); got != test.want {
				t.Fatalf("taskFailureReason(%v)=%q, want %q", test.cause, got, test.want)
			}
		})
	}
}

func TestTaskMetricsRecordOutcomes(t *testing.T) {
	reg := metrics.NewRegistry()
	// One dispatcher and one registry: SetMetrics registers process-wide gauges and must be called once,
	// exactly as main does. Two chats give the two outcomes without a second registry.
	m := &taskScriptModel{results: []taskModelResult{
		{response: model.Response{Content: validTaskPlanOne}},
		{response: model.Response{Content: "step result"}},
		{response: model.Response{Content: "final answer"}},
		{response: model.Response{Content: validTaskPlanOne}},
		{response: model.Response{}},
		{response: model.Response{}},
	}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	d.SetMetrics(reg)
	d.taskStepRetryDelay = 0
	if err := dispatch(t, d, testEvent(100, 11, 1301, "/task complete")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 2)
	if err := dispatch(t, d, testEvent(200, 11, 1302, "/task fail empty")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 2)

	completed, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || completed.Status != store.TaskStatusCompleted {
		t.Fatalf("completed task=%+v found=%v err=%v", completed, found, err)
	}
	failed, found, err := store.CurrentTask(context.Background(), db, 200)
	if err != nil || !found || failed.Status != store.TaskStatusFailed {
		t.Fatalf("failed task=%+v found=%v err=%v", failed, found, err)
	}

	body := taskMetricsExposition(t, reg)
	for _, sample := range []string{
		"bot_tasks_started_total 2",
		"bot_tasks_completed_total 1",
		`bot_tasks_failed_total{reason="empty"} 1`,
		"bot_task_step_retries_total 1",
		"bot_task_duration_seconds_count 2",
		"bot_tasks_planning_seconds_count 2",
	} {
		if !strings.Contains(body, sample+"\n") {
			t.Errorf("metrics miss %q:\n%s", sample, body)
		}
	}
	for _, forbidden := range []string{"chat_id", "user_id", "task_id"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("task metrics expose %q:\n%s", forbidden, body)
		}
	}
}

func taskMetricsExposition(t *testing.T, reg *metrics.Registry) string {
	t.Helper()
	var buffer bytes.Buffer
	if _, err := reg.WriteTo(&buffer); err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}
