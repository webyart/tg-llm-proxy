package bot

import (
	"context"
	"errors"
	"testing"

	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
)

// taskLimitConfig — the plan is one model call of one chat, so the test pins both budgets explicitly
// instead of inheriting the defaults of the loader.
func taskLimitConfig(t *testing.T, perMinute string) config.Config {
	t.Helper()
	var overrides = map[string]string{}
	overrides["TELEGRAM_ALLOWED_USERS"] = "11"
	overrides["RATE_LIMIT_PER_MINUTE"] = perMinute
	overrides["MAX_CONCURRENT_MODEL_REQUESTS"] = "1"
	return testConfig(t, overrides)
}

// TestCreateTaskObeysTheModelRateLimit is the regression test for the Mini App bypass: planning a task is
// a model call, and it must consume the same per-minute budget as /ask does through the middleware chain.
func TestCreateTaskObeysTheModelRateLimit(t *testing.T) {
	var cfg = taskLimitConfig(t, "1")
	var fake = &fakeModel{response: model.Response{Content: `{"steps":[{"description":"прочитать вопрос"}]}`}}
	var d = newTestDispatcher(t, cfg, fake, &fakeReplier{}, nil)

	// The user's single allowed request for this minute is already spent (that is what a /ask would do).
	if !d.deps.Limit.Allow(11) {
		t.Fatal("the first request of the minute must be allowed")
	}

	_, err := d.CreateTask(context.Background(), 900, 11, "безопасная задача")
	if !errors.Is(err, ErrTaskRateLimited) {
		t.Fatalf("CreateTask over the per-minute budget = %v, want ErrTaskRateLimited", err)
	}
	if got := fake.callCount(); got != 0 {
		t.Fatalf("model calls = %d, want 0: the plan was requested over the limit", got)
	}
	if _, found, currentErr := store.CurrentTask(context.Background(), d.deps.DB, 900); currentErr != nil || found {
		t.Fatalf("a refused plan created a task row: found=%v err=%v", found, currentErr)
	}
}

// TestCreateTaskStoresPlanWithinBudget keeps the positive case: an allowed user still gets a task, and the
// budget is consumed by the successful call.
func TestCreateTaskStoresPlanWithinBudget(t *testing.T) {
	var cfg = taskLimitConfig(t, "20")
	var fake = &fakeModel{response: model.Response{Content: `{"steps":[{"description":"прочитать вопрос"}]}`}}
	var d = newTestDispatcher(t, cfg, fake, &fakeReplier{}, nil)

	created, err := d.CreateTask(context.Background(), 902, 11, "безопасная задача")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.ID == 0 || created.Status != store.TaskStatusPlanning {
		t.Fatalf("CreateTask returned task=%+v, want an immediate planning row", created)
	}
	waitTaskPlanningSettled(t, d)
	if got := fake.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want 1", got)
	}
	if allowed := d.deps.Limit.Count(11); allowed != 1 {
		t.Fatalf("per-minute counter = %d, want 1: the successful plan must be counted", allowed)
	}
	current, found, readErr := store.CurrentTask(context.Background(), d.deps.DB, 902)
	if readErr != nil || !found || current.Status != store.TaskStatusReady {
		t.Fatalf("planned task=%+v found=%v err=%v", current, found, readErr)
	}
}
