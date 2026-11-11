package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// planningInstanceDB opens one more handle on the same file: two *sql.DB values are the two "instances" of
// the lease tests, the same way two bot processes share one bot.db.
func planningInstanceDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedPlanningTask(t *testing.T, db *sql.DB, chatID int64, now time.Time) Task {
	t.Helper()
	ctx := context.Background()
	if err := EnsureChat(ctx, db, chatID, 11, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	created, err := CreatePlanningTask(ctx, db, Task{ChatID: chatID, UserID: 11, Goal: "plan once", Model: "m",
		DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatalf("CreatePlanningTask: %v", err)
	}
	return created
}

// TestClaimPlanningTaskElectsOneOwner is the two-instance race: both handles claim at the same moment and
// exactly one wins. The loser must not steal the lease by retrying immediately either.
func TestClaimPlanningTaskElectsOneOwner(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claim-race.db")
	first := planningInstanceDB(t, path)
	second := planningInstanceDB(t, path)
	now := time.Now().UTC()
	created := seedPlanningTask(t, first, 100, now)

	type claimResult struct {
		owner string
		task  Task
		found bool
		err   error
	}
	owners := []string{"instance-first", "instance-second"}
	results := make([]claimResult, len(owners))
	var start sync.WaitGroup
	start.Add(1)
	var workers sync.WaitGroup
	for index, db := range []*sql.DB{first, second} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			start.Wait()
			task, found, err := ClaimPlanningTask(ctx, db, now, owners[index], time.Minute)
			results[index] = claimResult{owner: owners[index], task: task, found: found, err: err}
		}()
	}
	start.Done()
	workers.Wait()

	var winners []claimResult
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("claim of %s: %v", result.owner, result.err)
		}
		if result.found {
			winners = append(winners, result)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("concurrent claims produced %d winners, want exactly one: %+v", len(winners), results)
	}
	winner := winners[0]
	if winner.task.ID != created.ID || winner.task.PlanningOwner != winner.owner || winner.task.PlanningLeaseUntil == nil {
		t.Fatalf("winning claim=%+v", winner.task)
	}
	stored, found, err := CurrentTask(ctx, first, 100)
	if err != nil || !found || stored.PlanningOwner != winner.owner || stored.PlanningLeaseUntil == nil {
		t.Fatalf("stored owner=%+v found=%v err=%v, want %q", stored, found, err, winner.owner)
	}
	// The live lease is not claimable by the loser, now or a moment later.
	if _, found, err := ClaimPlanningTask(ctx, second, now.Add(time.Second), "instance-third", time.Minute); err != nil || found {
		t.Fatalf("a live lease was claimed: found=%v err=%v", found, err)
	}
}

// TestClaimPlanningTaskSkipsALiveLeaseAndReclaimsAnExpiredOne drives the two states of the lease: a deadline
// in the future owns the row, a deadline in the past does not.
func TestClaimPlanningTaskSkipsALiveLeaseAndReclaimsAnExpiredOne(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Now().UTC()
	created := seedPlanningTask(t, db, 100, now)

	claimed, found, err := ClaimPlanningTask(ctx, db, now, "instance-a", 120*time.Millisecond)
	if err != nil || !found || claimed.ID != created.ID {
		t.Fatalf("first claim=%+v found=%v err=%v", claimed, found, err)
	}
	if _, found, err := ClaimPlanningTask(ctx, db, now.Add(10*time.Millisecond), "instance-b", time.Minute); err != nil || found {
		t.Fatalf("a lease with 110 ms left was claimed: found=%v err=%v", found, err)
	}
	time.Sleep(150 * time.Millisecond)
	reclaimed, found, err := ClaimPlanningTask(ctx, db, time.Now().UTC(), "instance-b", time.Minute)
	if err != nil || !found || reclaimed.ID != created.ID || reclaimed.PlanningOwner != "instance-b" {
		t.Fatalf("expired lease reclaim=%+v found=%v err=%v", reclaimed, found, err)
	}
	if reclaimed.PlanningLeaseUntil == nil || !reclaimed.PlanningLeaseUntil.After(time.Now().UTC()) {
		t.Fatalf("reclaimed lease deadline=%v, want a future time", reclaimed.PlanningLeaseUntil)
	}
}

// TestRenewPlanningLeaseExtendsOnlyTheOwner: a renewal by another instance is a no-op, the owner's renewal
// keeps the row unclaimable even after the original TTL.
func TestRenewPlanningLeaseExtendsOnlyTheOwner(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Now().UTC()
	created := seedPlanningTask(t, db, 100, now)

	claimed, found, err := ClaimPlanningTask(ctx, db, now, "instance-a", 100*time.Millisecond)
	if err != nil || !found {
		t.Fatalf("claim=%+v found=%v err=%v", claimed, found, err)
	}
	if renewed, err := RenewPlanningLease(ctx, db, created.ID, "instance-b", now.Add(time.Hour)); err != nil || renewed {
		t.Fatalf("a foreign renewal changed=%v err=%v", renewed, err)
	}
	if renewed, err := RenewPlanningLease(ctx, db, created.ID, "instance-a", now.Add(time.Hour)); err != nil || !renewed {
		t.Fatalf("the owner's renewal changed=%v err=%v", renewed, err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, found, err := ClaimPlanningTask(ctx, db, time.Now().UTC(), "instance-b", time.Minute); err != nil || found {
		t.Fatalf("a renewed lease was claimed after the original TTL: found=%v err=%v", found, err)
	}
	stored, _, err := CurrentTask(ctx, db, 100)
	if err != nil || stored.PlanningLeaseUntil == nil || !stored.PlanningLeaseUntil.Equal(now.Add(time.Hour)) {
		t.Fatalf("renewed deadline=%v err=%v, want the owner's %v", stored.PlanningLeaseUntil, err, now.Add(time.Hour))
	}
}

// TestReleasePlanningLeaseAllowsReplan: only the owner can release, and the released row is claimable again —
// the path that lets a failed or cancelled row be replanned by any instance.
func TestReleasePlanningLeaseAllowsReplan(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Now().UTC()
	created := seedPlanningTask(t, db, 100, now)

	if _, found, err := ClaimPlanningTask(ctx, db, now, "instance-a", time.Minute); err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if released, err := ReleasePlanningLease(ctx, db, created.ID, "instance-b"); err != nil || released {
		t.Fatalf("a foreign release changed=%v err=%v", released, err)
	}
	if released, err := ReleasePlanningLease(ctx, db, created.ID, "instance-a"); err != nil || !released {
		t.Fatalf("the owner's release changed=%v err=%v", released, err)
	}
	stored, _, err := CurrentTask(ctx, db, 100)
	if err != nil || stored.PlanningOwner != "" || stored.PlanningLeaseUntil != nil {
		t.Fatalf("released row owner=%q lease=%v err=%v", stored.PlanningOwner, stored.PlanningLeaseUntil, err)
	}
	replanned, found, err := ClaimPlanningTask(ctx, db, now, "instance-b", time.Minute)
	if err != nil || !found || replanned.PlanningOwner != "instance-b" {
		t.Fatalf("claim after release=%+v found=%v err=%v", replanned, found, err)
	}
}

// TestTerminalPlanningTransitionsClearTheLease: cancel and a stored planning failure must not leave a claim
// behind, even when the planner process never released it.
func TestTerminalPlanningTransitionsClearTheLease(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Now().UTC()

	cancelledRow := seedPlanningTask(t, db, 100, now)
	if _, found, err := ClaimPlanningTask(ctx, db, now, "instance-a", time.Minute); err != nil || !found {
		t.Fatalf("claim before cancel found=%v err=%v", found, err)
	}
	if _, ok, err := CancelTask(ctx, db, 100, now); err != nil || !ok {
		t.Fatalf("CancelTask ok=%v err=%v", ok, err)
	}
	cancelled, _, err := CurrentTask(ctx, db, 100)
	if err != nil || cancelled.ID != cancelledRow.ID || cancelled.PlanningOwner != "" || cancelled.PlanningLeaseUntil != nil {
		t.Fatalf("cancelled row=%+v err=%v", cancelled, err)
	}

	failedRow := seedPlanningTask(t, db, 101, now)
	if _, found, err := ClaimPlanningTask(ctx, db, now, "instance-a", time.Minute); err != nil || !found {
		t.Fatalf("claim before failure found=%v err=%v", found, err)
	}
	if failed, err := FailTaskPlanning(ctx, db, failedRow.ID, "не удалось составить план", now); err != nil || !failed {
		t.Fatalf("FailTaskPlanning changed=%v err=%v", failed, err)
	}
	failed, _, err := CurrentTask(ctx, db, 101)
	if err != nil || failed.PlanningOwner != "" || failed.PlanningLeaseUntil != nil {
		t.Fatalf("failed planning row=%+v err=%v", failed, err)
	}
	if released, err := ReleasePlanningLease(ctx, db, failedRow.ID, "instance-a"); err != nil || released {
		t.Fatalf("release after the terminal transition changed=%v err=%v", released, err)
	}
}

// TestResetTaskPlanningBumpsTheAttemptOnce: two recoverers that listed the same row must not both increment
// the counter. The second reset is refused by the updated_at condition that the first reset moved forward.
func TestResetTaskPlanningBumpsTheAttemptOnce(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Now().UTC()
	created := seedPlanningTask(t, db, 100, now)
	staleBefore := now

	if reset, err := ResetTaskPlanning(ctx, db, created.ID, staleBefore, now.Add(time.Second), time.Hour); err != nil || !reset {
		t.Fatalf("first reset changed=%v err=%v", reset, err)
	}
	reset, _, err := CurrentTask(ctx, db, 100)
	if err != nil || reset.PlanningAttempts != 2 {
		t.Fatalf("attempts after first reset=%d err=%v, want 2", reset.PlanningAttempts, err)
	}
	if reset, err := ResetTaskPlanning(ctx, db, created.ID, staleBefore, now.Add(2*time.Second), time.Hour); err != nil || reset {
		t.Fatalf("second reset changed=%v err=%v, want a refused no-op", reset, err)
	}
	stored, _, err := CurrentTask(ctx, db, 100)
	if err != nil || stored.PlanningAttempts != 2 {
		t.Fatalf("attempts after the refused reset=%d err=%v, want 2", stored.PlanningAttempts, err)
	}
}

// TestMigration027ToleratesAnExistingLeaseColumn reproduces a database that already carries the columns but
// whose version-27 row is missing (an interrupted pre-release, or an operator's repair). The guarded runner
// must skip only the ALTER and still create the index instead of failing with "duplicate column".
func TestMigration027ToleratesAnExistingLeaseColumn(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 26)
	for _, statement := range []string{
		`ALTER TABLE tasks ADD COLUMN planning_owner TEXT`,
		`ALTER TABLE tasks ADD COLUMN planning_lease_until TEXT`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("pre-add the column: %v", err)
		}
	}
	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema over existing planning lease columns: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) {
		t.Fatalf("SchemaVersion=%d err=%v, want %d", version, err, len(MigrationSQL))
	}
	var index int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_tasks_planning_lease'`).Scan(&index); err != nil || index != 1 {
		t.Fatalf("the index statement of 027 did not run: present=%d err=%v", index, err)
	}
}

// TestMigration027PreservesTasksAndAddsNullableLeaseColumns upgrades a schema-26 database that already has a
// planning row: the row survives untouched, both new columns are NULL (which means "unleased"), and the old
// row is immediately claimable by the new protocol.
func TestMigration027PreservesTasksAndAddsNullableLeaseColumns(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 26)
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks
		(chat_id, user_id, goal, model, plan_json, results_json, next_step, status, attempts, planning_attempts,
		 due_at, expires_at, created_at, updated_at)
		VALUES (100, 11, 'old planning goal', 'old-model', '', '[]', 0, 'planning', 0, 1, ?, ?, ?, ?)`,
		formatReminderTime(now), formatReminderTime(now.Add(time.Hour)),
		formatReminderTime(now), formatReminderTime(now.Add(-3*time.Minute))); err != nil {
		t.Fatalf("seed schema-26 planning row: %v", err)
	}

	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("upgrade schema 26 to 27: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	// The 027 upgrade is measured by the full compiled history, not by the literal 27: later additive
	// migrations (028 provider budgets) must not require editing this test to prove what 027 did.
	if err != nil || version != len(MigrationSQL) || version < 27 {
		t.Fatalf("SchemaVersion=%d err=%v, want %d (schema 27 or newer)", version, err, len(MigrationSQL))
	}
	for _, column := range []string{"planning_owner", "planning_lease_until"} {
		var present int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name = ?`, column).Scan(&present); err != nil {
			t.Fatalf("pragma_table_info(%s): %v", column, err)
		}
		if present != 1 {
			t.Fatalf("column %s is missing after migration 027", column)
		}
	}
	var index int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_tasks_planning_lease'`).Scan(&index); err != nil || index != 1 {
		t.Fatalf("idx_tasks_planning_lease present=%d err=%v", index, err)
	}
	stored, found, err := CurrentTask(ctx, db, 100)
	if err != nil || !found || stored.Goal != "old planning goal" || stored.Status != TaskStatusPlanning ||
		stored.PlanningAttempts != 1 || stored.PlanningOwner != "" || stored.PlanningLeaseUntil != nil {
		t.Fatalf("upgraded row=%+v found=%v err=%v", stored, found, err)
	}
	if claimed, found, err := ClaimPlanningTask(ctx, db, now, "instance-new", time.Minute); err != nil || !found ||
		claimed.ID != stored.ID || claimed.PlanningOwner != "instance-new" {
		t.Fatalf("old unleased row claim=%+v found=%v err=%v", claimed, found, err)
	}
}
