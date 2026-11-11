package bot

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bot/internal/model"
	"bot/internal/store"
	"bot/internal/testutil"
)

// planningLeaseReminderLayout mirrors the store's fixed reminder timestamp layout. A bot test backdates a row
// with it to reproduce "the crash happened three minutes ago" without sleeping for the recovery age.
const planningLeaseReminderLayout = "2006-01-02T15:04:05.000000000Z"

// countingPlanModel answers every planning call with one valid plan after an optional delay and counts the
// calls, so a test can prove that exactly one instance reached the model.
type countingPlanModel struct {
	mu    sync.Mutex
	delay time.Duration
	plan  string
	calls int
}

func (m *countingPlanModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
	return model.Response{Content: m.plan, Model: "selected-model", Attempts: 1}, nil
}

func (m *countingPlanModel) Models(context.Context) ([]string, error) { return nil, nil }

func (m *countingPlanModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// openPlanningInstance opens one more handle on the same database file: it is the second bot instance of the
// two-instance lease tests.
func openPlanningInstance(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// planningLeaseTask creates the planning row directly, without waking any planner: the test drives the
// workers itself.
func planningLeaseTask(t *testing.T, db *sql.DB, chatID int64) store.Task {
	t.Helper()
	ctx := context.Background()
	if err := store.EnsureChat(ctx, db, chatID, 11, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	now := time.Now().UTC()
	created, err := store.CreatePlanningTask(ctx, db, store.Task{
		ChatID: chatID, UserID: 11, Goal: "one planner across instances", Model: "m",
		DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreatePlanningTask: %v", err)
	}
	return created
}

// waitTaskPlanningIdle waits until no local planner worker is starting or running. Unlike
// waitTaskPlanningSettled it does not require the row to leave 'planning': a worker that skipped a live lease
// leaves the row planning on purpose.
func waitTaskPlanningIdle(t *testing.T, d *Dispatcher) {
	t.Helper()
	testutil.Eventually(t, 5*time.Second, func() bool {
		d.planningMu.Lock()
		defer d.planningMu.Unlock()
		return len(d.planningActive) == 0
	}, func() string { return "local planning workers did not stop" })
}

// TestTaskPlanningLeaseElectsOnePlannerAcrossInstances is the multi-instance guarantee: two dispatchers on
// one database are woken for the same row at the same time, the lease lets exactly one of them plan, and the
// other idles instead of producing a second plan.
func TestTaskPlanningLeaseElectsOnePlannerAcrossInstances(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "two-instances.db")
	firstDB := openPlanningInstance(t, path)
	secondDB := openPlanningInstance(t, path)
	created := planningLeaseTask(t, firstDB, 100)
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	firstModel := &countingPlanModel{delay: 300 * time.Millisecond, plan: validTaskPlanOne}
	secondModel := &countingPlanModel{delay: 300 * time.Millisecond, plan: validTaskPlanOne}
	first := newTestDispatcher(t, cfg, firstModel, &fakeReplier{}, firstDB)
	second := newTestDispatcher(t, cfg, secondModel, &fakeReplier{}, secondDB)

	first.startTaskPlanning(created)
	second.startTaskPlanning(created)
	waitTaskPlanningSettled(t, first)
	waitTaskPlanningIdle(t, first)
	waitTaskPlanningIdle(t, second)

	if got := firstModel.callCount() + secondModel.callCount(); got != 1 {
		t.Fatalf("planner calls=%d across two instances, want exactly one", got)
	}
	current, found, err := store.CurrentTask(ctx, firstDB, 100)
	if err != nil || !found || current.Status != store.TaskStatusReady || current.PlanJSON != validTaskPlanOne {
		t.Fatalf("planned task=%+v found=%v err=%v", current, found, err)
	}
	if current.PlanningOwner != "" || current.PlanningLeaseUntil != nil {
		t.Fatalf("released task owner=%q lease=%v, want a free row", current.PlanningOwner, current.PlanningLeaseUntil)
	}
}

// TestTaskPlanningLeaseSkipsALiveOwnerAndRecoversAnExpiredOne: a lease held by another instance (a live
// planner, or a process that has not timed out yet) is skipped by the recovery scan; once its deadline is in
// the past the row is reclaimed, planned and released.
func TestTaskPlanningLeaseSkipsALiveOwnerAndRecoversAnExpiredOne(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	created := planningLeaseTask(t, db, 100)
	crashedAt := time.Now().UTC().Add(-3 * time.Minute)
	claimed, found, err := store.ClaimPlanningTask(ctx, db, time.Now().UTC(), "instance-dead", 5*time.Minute)
	if err != nil || !found || claimed.ID != created.ID {
		t.Fatalf("simulated crash claim=%+v found=%v err=%v", claimed, found, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET updated_at = ? WHERE id = ?`,
		crashedAt.Format(planningLeaseReminderLayout), created.ID); err != nil {
		t.Fatalf("backdate the crashed row: %v", err)
	}
	m := &countingPlanModel{delay: 5 * time.Millisecond, plan: validTaskPlanOne}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)

	if _, err := d.ProcessDueTasks(ctx, time.Now().UTC(), TaskBatchLimit); err != nil {
		t.Fatalf("scheduler pass with a live lease: %v", err)
	}
	waitTaskPlanningIdle(t, d)
	if got := m.callCount(); got != 0 {
		t.Fatalf("a live lease reached the model %d time(s)", got)
	}
	live, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || live.Status != store.TaskStatusPlanning || live.PlanningOwner != "instance-dead" || live.PlanningAttempts != 1 {
		t.Fatalf("live row=%+v found=%v err=%v", live, found, err)
	}

	// The dead instance never released; its lease expires and any instance may take the row.
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET planning_lease_until = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute).Format(planningLeaseReminderLayout), created.ID); err != nil {
		t.Fatalf("expire the dead lease: %v", err)
	}
	if _, err := d.ProcessDueTasks(ctx, time.Now().UTC(), TaskBatchLimit); err != nil {
		t.Fatalf("scheduler pass with an expired lease: %v", err)
	}
	waitTaskPlanningSettled(t, d)
	recovered, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || recovered.Status != store.TaskStatusReady || recovered.PlanJSON != validTaskPlanOne {
		t.Fatalf("recovered task=%+v found=%v err=%v", recovered, found, err)
	}
	if recovered.PlanningAttempts != 2 {
		t.Fatalf("planning attempts=%d, want the crash re-enqueue counted once", recovered.PlanningAttempts)
	}
	if recovered.PlanningOwner != "" || recovered.PlanningLeaseUntil != nil {
		t.Fatalf("recovered task owner=%q lease=%v, want a free row", recovered.PlanningOwner, recovered.PlanningLeaseUntil)
	}
	if got := m.callCount(); got != 1 {
		t.Fatalf("recovery reached the model %d time(s), want one", got)
	}
}

// TestTaskPlanningLeaseDoesNotFailALiveOwnerAtTheAttemptCap: the attempt cap is applied only to a row whose
// lease is free. A plan that another instance is still running must not be failed as "interrupted" just
// because the counter is at the cap.
func TestTaskPlanningLeaseDoesNotFailALiveOwnerAtTheAttemptCap(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	created := planningLeaseTask(t, db, 100)
	if _, found, err := store.ClaimPlanningTask(ctx, db, time.Now().UTC(), "instance-live", 5*time.Minute); err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET planning_attempts = ?, updated_at = ? WHERE id = ?`,
		taskPlanningAttemptsCap, time.Now().UTC().Add(-3*time.Minute).Format(planningLeaseReminderLayout), created.ID); err != nil {
		t.Fatalf("seed the capped live row: %v", err)
	}
	m := &countingPlanModel{plan: validTaskPlanOne}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)
	if _, err := d.ProcessDueTasks(ctx, time.Now().UTC(), TaskBatchLimit); err != nil {
		t.Fatalf("scheduler pass: %v", err)
	}
	waitTaskPlanningIdle(t, d)
	live, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || live.Status != store.TaskStatusPlanning || live.Error != "" {
		t.Fatalf("capped live row=%+v found=%v err=%v", live, found, err)
	}
	if live.PlanningOwner != "instance-live" || live.PlanningAttempts != taskPlanningAttemptsCap {
		t.Fatalf("live row owner=%q attempts=%d, want the live owner and the untouched cap", live.PlanningOwner, live.PlanningAttempts)
	}
}

// TestTaskPlanningHeartbeatKeepsTheLeaseWhileThePlanRuns: the TTL is much shorter than the plan, so the row
// stays unclaimable only because the heartbeat renews it. Without the renewal the direct claim below would win
// the row while the first instance is still waiting for the model.
func TestTaskPlanningHeartbeatKeepsTheLeaseWhileThePlanRuns(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	created := planningLeaseTask(t, db, 100)
	m := &slowPlanningModel{delay: 1200 * time.Millisecond}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)
	d.planningLeaseTTL = 500 * time.Millisecond

	d.startTaskPlanning(created)
	testutil.Eventually(t, 5*time.Second, func() bool {
		current, found, err := store.CurrentTask(ctx, db, 100)
		return err == nil && found && current.PlanningOwner == d.planningOwner && current.PlanningLeaseUntil != nil
	}, func() string { return "the planner did not claim the row" })
	claimedAt, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || claimedAt.PlanningLeaseUntil == nil {
		t.Fatalf("claimed row=%+v found=%v err=%v", claimedAt, found, err)
	}
	originalDeadline := *claimedAt.PlanningLeaseUntil
	// The plan outlives the original TTL: only a heartbeat can keep a lease in the future after it.
	testutil.Eventually(t, 5*time.Second, func() bool {
		current, found, err := store.CurrentTask(ctx, db, 100)
		return err == nil && found && current.PlanningLeaseUntil != nil &&
			time.Now().After(originalDeadline) && current.PlanningLeaseUntil.Sub(time.Now()) >= 300*time.Millisecond
	}, func() string { return "the planning lease was not renewed past its original TTL" })
	if _, found, err := store.ClaimPlanningTask(ctx, db, time.Now().UTC(), "instance-other", time.Minute); err != nil || found {
		t.Fatalf("a heartbeating lease was claimed by another instance: found=%v err=%v", found, err)
	}
	if got := m.callCount(); got != 1 {
		t.Fatalf("planner calls=%d, want one", got)
	}
	waitTaskPlanningSettled(t, d)
	current, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || current.Status != store.TaskStatusReady || current.PlanningOwner != "" || current.PlanningLeaseUntil != nil {
		t.Fatalf("heartbeat task=%+v found=%v err=%v", current, found, err)
	}
}
