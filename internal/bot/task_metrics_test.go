package bot

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"bot/internal/metrics"
	"bot/internal/model"
	"bot/internal/store"
)

func TestTaskStepTimeoutMetricLabelsThePhase(t *testing.T) {
	t.Run("tool", func(t *testing.T) {
		reg := metrics.NewRegistry()
		cfg := taskBudgetTestConfig(t, taskTestModelTimeout, taskTestStepDeadline, nil)
		d := newTestDispatcher(t, cfg, &blockingTaskModel{plan: validTaskPlanTwo}, &fakeReplier{}, testStore(t))
		d.SetMetrics(reg)
		d.taskStepRetryDelay = 0
		if err := dispatch(t, d, testEvent(100, 11, 4101, "/task a slow research step")); err != nil {
			t.Fatal(err)
		}
		processTasks(t, d, 2)

		body := taskMetricsExposition(t, reg)
		if !strings.Contains(body, "bot_task_step_timeouts_total{phase=\"tool\"} 1") {
			t.Errorf("the tool-round timeout was not counted in the tool phase:\n%s", body)
		}
		for _, other := range []string{"final", "synthesis"} {
			if strings.Contains(body, "bot_task_step_timeouts_total{phase=\""+other+"\"}") {
				t.Errorf("a tool-round timeout was also counted as %q:\n%s", other, body)
			}
		}
		if count := taskStepTimeoutCount(t, body); count != 1 {
			t.Errorf("bot_task_step_timeouts_total sums to %v for one failed step, want exactly 1:\n%s", count, body)
		}
	})

	t.Run("final", func(t *testing.T) {
		reg := metrics.NewRegistry()
		cfg := taskBudgetTestConfig(t, taskTestModelTimeout, taskTestStepDeadline, nil)
		d := newTestDispatcher(t, cfg, &finalPhaseTaskModel{plan: validTaskPlanOne}, &fakeReplier{}, testStore(t))
		d.SetMetrics(reg)
		d.taskStepRetryDelay = 0
		if err := dispatch(t, d, testEvent(100, 11, 4103, "/task a slow final answer after research")); err != nil {
			t.Fatal(err)
		}
		processTasks(t, d, 2)

		body := taskMetricsExposition(t, reg)
		if !strings.Contains(body, "bot_task_step_timeouts_total{phase=\"final\"} 1") {
			t.Errorf("the final-answer timeout was not counted in the final phase:\n%s", body)
		}
		for _, other := range []string{"tool", "synthesis"} {
			if strings.Contains(body, "bot_task_step_timeouts_total{phase=\""+other+"\"}") {
				t.Errorf("a final-answer timeout was also counted as %q:\n%s", other, body)
			}
		}
		if count := taskStepTimeoutCount(t, body); count != 1 {
			t.Errorf("bot_task_step_timeouts_total sums to %v for one failed step, want exactly 1:\n%s", count, body)
		}
	})

	t.Run("synthesis", func(t *testing.T) {
		reg := metrics.NewRegistry()
		cfg := taskBudgetTestConfig(t, taskTestModelTimeout, taskTestStepDeadline, nil)
		d := newTestDispatcher(t, cfg, &blockingTaskModel{plan: validTaskPlanOne, stepResult: "completed step result"}, &fakeReplier{}, testStore(t))
		d.SetMetrics(reg)
		d.taskStepRetryDelay = 0
		if err := dispatch(t, d, testEvent(100, 11, 4102, "/task a slow final answer")); err != nil {
			t.Fatal(err)
		}
		processTasks(t, d, 3)

		body := taskMetricsExposition(t, reg)
		if !strings.Contains(body, "bot_task_step_timeouts_total{phase=\"synthesis\"} 1") {
			t.Errorf("the synthesis timeout was not counted in the synthesis phase:\n%s", body)
		}
		for _, other := range []string{"tool", "final"} {
			if strings.Contains(body, "bot_task_step_timeouts_total{phase=\""+other+"\"}") {
				t.Errorf("a synthesis timeout was also counted as %q:\n%s", other, body)
			}
		}
		if count := taskStepTimeoutCount(t, body); count != 1 {
			t.Errorf("bot_task_step_timeouts_total sums to %v for one failed step, want exactly 1:\n%s", count, body)
		}
	})
}

// taskStepTimeoutCount sums every sample of bot_task_step_timeouts_total: a retry that counted the same failed
// step twice cannot hide behind two phase labels this way.
func taskStepTimeoutCount(t *testing.T, body string) float64 {
	t.Helper()
	var total float64
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, taskMetricStepTimeouts+"{") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("unexpected %s sample %q", taskMetricStepTimeouts, line)
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			t.Fatalf("parse %s sample %q: %v", taskMetricStepTimeouts, line, err)
		}
		total += value
	}
	return total
}

func TestTaskPlanningFailureMetricClassifiesTheCause(t *testing.T) {
	tests := []struct {
		name  string
		model model.Client
		want  string
	}{
		{name: "invalid", model: taskTextModel(`{"steps":[{"description":"Read https://attacker.example"}]}`), want: "invalid"},
		{name: "model", model: &taskScriptModel{results: []taskModelResult{{err: &model.APIError{Status: 503}}}}, want: "model"},
		{name: "timeout", model: &taskScriptModel{results: []taskModelResult{{err: context.DeadlineExceeded}}}, want: "timeout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reg := metrics.NewRegistry()
			d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), test.model, &fakeReplier{}, testStore(t))
			d.SetMetrics(reg)
			if err := dispatch(t, d, testEvent(100, 11, 4200, "/task classify the planning failure")); err != nil {
				t.Fatal(err)
			}
			waitTaskPlanningSettled(t, d)

			body := taskMetricsExposition(t, reg)
			sample := "bot_tasks_planning_failures_total{reason=\"" + test.want + "\"} 1"
			if !strings.Contains(body, sample) {
				t.Errorf("planning failure was not counted by %q:\n%s", test.want, body)
			}
			for _, other := range []string{"model", "timeout", "invalid", "other"} {
				if other == test.want {
					continue
				}
				if strings.Contains(body, "bot_tasks_planning_failures_total{reason=\""+other+"\"}") {
					t.Errorf("planning failure also appeared under %q:\n%s", other, body)
				}
			}
		})
	}
	if got := taskPlanningFailureClass(nil); got != "other" {
		t.Errorf("taskPlanningFailureClass(nil)=%q, want other", got)
	}
	if got := taskPlanningFailureClass(errors.New("plain")); got != "other" {
		t.Errorf("taskPlanningFailureClass(plain)=%q, want other", got)
	}
}

func TestTaskQueueDepthGaugeCountsPlanningAndReady(t *testing.T) {
	reg := metrics.NewRegistry()
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), taskTextModel(validTaskPlanOne), &fakeReplier{}, testStore(t))
	d.SetMetrics(reg)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, chatID := range []int64{100, 101} {
		if err := store.EnsureChat(ctx, d.deps.DB, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreatePlanningTask(ctx, d.deps.DB, store.Task{
		ChatID: 100, UserID: 11, Goal: "still planning", Model: "m",
		DueAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(ctx, d.deps.DB, store.Task{
		ChatID: 101, UserID: 11, Goal: "ready to run", Model: "m", PlanJSON: validTaskPlanOne,
		DueAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ProcessDueTasks(ctx, now, TaskBatchLimit); err != nil {
		t.Fatal(err)
	}
	body := taskMetricsExposition(t, reg)
	if !strings.Contains(body, "bot_task_queue_depth 2") {
		t.Errorf("queue depth did not count the planning and ready rows:\n%s", body)
	}
}

func TestTaskTimeoutPhaseNameIsBounded(t *testing.T) {
	cases := []struct {
		phase string
		want  string
	}{
		{phase: taskPhaseTool, want: "tool"},
		{phase: taskPhaseFinal, want: "final"},
		{phase: taskPhaseSynthesis, want: "synthesis"},
		{phase: taskPhaseStep, want: "tool"},
		{phase: "unexpected", want: "tool"},
	}
	for _, test := range cases {
		if got := taskTimeoutPhaseName(test.phase); got != test.want {
			t.Errorf("taskTimeoutPhaseName(%q)=%q, want %q", test.phase, got, test.want)
		}
	}
}

func TestTaskPlanningFailureClassIsBounded(t *testing.T) {
	cases := []struct {
		name  string
		cause error
		want  string
	}{
		{name: "invalid plan", cause: ErrTaskPlan, want: "invalid"},
		{name: "wrapped invalid", cause: errors.Join(errors.New("plan"), ErrTaskPlan), want: "invalid"},
		{name: "store state", cause: store.ErrTaskInvalid, want: "other"},
		{name: "deadline", cause: context.DeadlineExceeded, want: "timeout"},
		{name: "api", cause: &model.APIError{Status: 500}, want: "model"},
		{name: "empty", cause: model.ErrEmptyAnswer, want: "model"},
		{name: "rate limited", cause: ErrModelRateLimited, want: "other"},
		{name: "nil", cause: nil, want: "other"},
		{name: "plain", cause: errors.New("unexpected"), want: "other"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := taskPlanningFailureClass(test.cause); got != test.want {
				t.Fatalf("taskPlanningFailureClass(%v)=%q, want %q", test.cause, got, test.want)
			}
		})
	}
}
