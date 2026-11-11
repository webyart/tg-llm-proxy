package bot

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"bot/internal/i18n"
	"bot/internal/model"
	"bot/internal/store"
)

// slowPlanningModel is the 5-second planner of the regression: while it is generating, CreateTask and the
// /task handler must already have returned.
type slowPlanningModel struct {
	delay time.Duration
	mu    sync.Mutex
	calls int
}

func (m *slowPlanningModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	select {
	case <-time.After(m.delay):
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	}
	return model.Response{Content: validTaskPlanOne, Model: "slow-model"}, nil
}

func (m *slowPlanningModel) Models(context.Context) ([]string, error) { return nil, nil }

func (m *slowPlanningModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestSlowPlanningModelDoesNotBlockCreateOrTelegram(t *testing.T) {
	m := &slowPlanningModel{delay: 5 * time.Second}
	r := newTaskStatusReplier()
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	ctx := context.Background()

	start := time.Now()
	created, err := d.CreateTask(ctx, 100, 11, "HTTP creation stays fast")
	createElapsed := time.Since(start)
	if err != nil || created.ID <= 0 || created.Status != store.TaskStatusPlanning {
		t.Fatalf("CreateTask task=%+v err=%v", created, err)
	}
	t.Logf("CreateTask returned in %s while the planner model sleeps for 5s", createElapsed)
	if createElapsed > 500*time.Millisecond {
		t.Fatalf("CreateTask blocked for %s on the slow planner", createElapsed)
	}

	start = time.Now()
	if err := dispatch(t, d, testEvent(200, 11, 2000, "/task Telegram creation stays fast")); err != nil {
		t.Fatal(err)
	}
	dispatchElapsed := time.Since(start)
	if dispatchElapsed > 500*time.Millisecond {
		t.Fatalf("/task blocked for %s on the slow planner", dispatchElapsed)
	}
	sent := r.statusMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].text, "готовлю план") {
		t.Fatalf("Telegram did not answer before the plan: %+v", sent)
	}

	waitTaskPlanningSettled(t, d)
	for _, chatID := range []int64{100, 200} {
		current, found, readErr := store.CurrentTask(ctx, db, chatID)
		if readErr != nil || !found || current.Status != store.TaskStatusReady || current.PlanJSON == "" {
			t.Fatalf("chat %d planned task=%+v found=%v err=%v", chatID, current, found, readErr)
		}
	}
	if m.callCount() != 2 {
		t.Fatalf("planner calls=%d, want one per task", m.callCount())
	}
	// The creation message is still the only sent message; the plan arrived by editing it.
	if got := len(r.statusMessages()); got != 1 {
		t.Fatalf("task status messages=%d, want only the creation message", got)
	}
	if edits := r.editMessages(); len(edits) != 1 || edits[0].messageID != sent[0].messageID || !strings.Contains(edits[0].text, "план готов") {
		t.Fatalf("plan edits=%+v", edits)
	}
}

func TestTaskPlanningFailureThenReplan(t *testing.T) {
	m := taskTextModel(`{"steps":[{"description":"Read https://attacker.example"}]}`, validTaskPlanOne)
	r := newTaskStatusReplier()
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	ctx := context.Background()

	if err := dispatch(t, d, testEvent(100, 11, 3000, "/task collect private data")); err != nil {
		t.Fatal(err)
	}
	if sent := r.statusMessages(); len(sent) != 1 || !strings.Contains(sent[0].text, "готовлю план") {
		t.Fatalf("creation message=%+v", sent)
	}
	waitTaskPlanningSettled(t, d)
	failed, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || failed.Status != store.TaskStatusFailed || failed.PlanJSON != "" ||
		failed.Error != i18n.T(i18n.Default, "task.reason.plan_invalid") {
		t.Fatalf("failed planning task=%+v found=%v err=%v", failed, found, err)
	}
	if store.ActiveTaskStatus(failed.Status) {
		t.Fatal("a failed plan still blocks the chat")
	}

	replanned, changed, err := d.ReplanTask(ctx, 100, failed.ID)
	if err != nil || !changed || replanned.Status != store.TaskStatusPlanning || replanned.PlanJSON != "" || replanned.PlanningAttempts != 2 {
		t.Fatalf("ReplanTask task=%+v changed=%v err=%v", replanned, changed, err)
	}
	waitTaskPlanningSettled(t, d)
	planned, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || planned.Status != store.TaskStatusReady || planned.PlanJSON != validTaskPlanOne || planned.Attempts != 0 {
		t.Fatalf("replanned task=%+v found=%v err=%v", planned, found, err)
	}
	if m.callCount() != 2 {
		t.Fatalf("planner calls=%d, want a retry", m.callCount())
	}
	// The failure and the retry both edit the one creation message; no second status message appears.
	if got := len(r.statusMessages()); got != 1 {
		t.Fatalf("status messages=%d, want one", got)
	}
	edits := r.editMessages()
	if len(edits) < 2 || !strings.Contains(edits[0].text, "не удалось составить план") ||
		!strings.Contains(edits[len(edits)-1].text, "план готов") {
		t.Fatalf("plan status edits=%+v", edits)
	}
}

func TestTaskCrashRecoveryReenqueuesAndCapsStalePlanning(t *testing.T) {
	t.Run("re-enqueue once", func(t *testing.T) {
		m := taskTextModel(validTaskPlanOne)
		db := testStore(t)
		d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)
		ctx := context.Background()
		if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		created, err := store.CreatePlanningTask(ctx, db, store.Task{
			ChatID: 100, UserID: 11, Goal: "recovered goal", Model: "m",
			DueAt: now.Add(-3 * time.Minute), ExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(-3 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.ProcessDueTasks(ctx, now, TaskBatchLimit); err != nil {
			t.Fatal(err)
		}
		waitTaskPlanningSettled(t, d)
		current, found, err := store.CurrentTask(ctx, db, 100)
		if err != nil || !found || current.ID != created.ID || current.Status != store.TaskStatusReady || current.PlanJSON != validTaskPlanOne {
			t.Fatalf("recovered task=%+v found=%v err=%v", current, found, err)
		}
		if current.PlanningAttempts != 2 || m.callCount() != 1 {
			t.Fatalf("recovery attempts=%d calls=%d, want 2/1", current.PlanningAttempts, m.callCount())
		}
	})

	t.Run("cap fails the row", func(t *testing.T) {
		m := taskTextModel(validTaskPlanOne)
		db := testStore(t)
		d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)
		ctx := context.Background()
		if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		created, err := store.CreatePlanningTask(ctx, db, store.Task{
			ChatID: 100, UserID: 11, Goal: "stuck goal", Model: "m",
			DueAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE tasks SET planning_attempts = ?, updated_at = ? WHERE id = ?`,
			taskPlanningAttemptsCap, now.Add(-3*time.Minute).UTC().Format("2006-01-02T15:04:05.000000000Z"), created.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := d.ProcessDueTasks(ctx, now, TaskBatchLimit); err != nil {
			t.Fatal(err)
		}
		current, found, err := store.CurrentTask(ctx, db, 100)
		if err != nil || !found || current.Status != store.TaskStatusFailed || current.Error != i18n.T(i18n.Default, "task.reason.planning_interrupted") {
			t.Fatalf("capped task=%+v found=%v err=%v", current, found, err)
		}
		if m.callCount() != 0 {
			t.Fatalf("capped planning reached the model %d time(s)", m.callCount())
		}
		if store.ActiveTaskStatus(current.Status) {
			t.Fatal("the failed capped row still blocks the chat")
		}
	})
}

func TestTaskSchedulerNeverClaimsAPlanningRow(t *testing.T) {
	m := taskTextModel(validTaskPlanOne)
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)
	ctx := context.Background()
	if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := store.CreatePlanningTask(ctx, db, store.Task{
		ChatID: 100, UserID: 11, Goal: "not claimable", Model: "m",
		DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	processed, err := d.ProcessDueTasks(ctx, now, TaskBatchLimit)
	if err != nil || processed != 0 {
		t.Fatalf("ProcessDueTasks processed=%d err=%v, want 0", processed, err)
	}
	if _, found, err := store.ClaimNextTask(ctx, db, now, d.taskLease()); err != nil || found {
		t.Fatalf("ClaimNextTask claimed a planning row: found=%v err=%v", found, err)
	}
	current, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || current.Status != store.TaskStatusPlanning || current.Attempts != 0 {
		t.Fatalf("planning row changed=%+v found=%v err=%v", current, found, err)
	}
	if m.callCount() != 0 {
		t.Fatalf("the scheduler called the model for a planning row %d time(s)", m.callCount())
	}
}

func TestReplanRejectsATaskWithAStoredPlan(t *testing.T) {
	m := taskTextModel(validTaskPlanOne)
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)
	ctx := context.Background()
	if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	created, err := store.CreateTask(ctx, db, store.Task{
		ChatID: 100, UserID: 11, Goal: "execution failure", Model: "m",
		PlanJSON: validTaskPlanOne, DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Fail the row through the real claim path: an execution failure keeps its plan.
	claimed, found, err := store.ClaimNextTask(ctx, db, now, d.taskLease())
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if changed, err := store.FailTask(ctx, db, claimed, "execution failed", now); err != nil || !changed {
		t.Fatalf("FailTask changed=%v err=%v", changed, err)
	}
	if _, changed, err := d.ReplanTask(ctx, 100, created.ID); err != nil || changed {
		t.Fatalf("replan of an execution failure changed=%v err=%v", changed, err)
	}
}
