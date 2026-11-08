package store

import (
	"context"
	"testing"
	"time"
)

const pruneClassOldStamp = "2020-01-01T00:00:00.000000000Z"

// TestPruneCoversQuotaTasksRemindersAndLinks — the four tables that grew without a retention path. Terminal
// rows older than the cutoff go, active rows and fresh rows stay, and the dry-run counts the same set.
func TestPruneCoversQuotaTasksRemindersAndLinks(t *testing.T) {
	var ctx = context.Background()
	var db, err = OpenMemory(ctx, "prune-classes")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	// The four tables cascade from chats, so the chats have to exist first.
	for _, chatID := range []int64{100, 101, 102} {
		if err := EnsureChat(ctx, db, chatID, 11, "chat"); err != nil {
			t.Fatalf("EnsureChat(%d): %v", chatID, err)
		}
	}

	// Two quota days: one old, one that is not older than the cutoff.
	if _, err := db.ExecContext(ctx, `INSERT INTO daily_quota_reservations (user_id, day, class, used)
		VALUES (11, '2020-01-01', 'model', 3), (11, '2030-01-01', 'model', 1)`); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	// Tasks: terminal old (deleted), terminal fresh (kept), active old (kept — it is not terminal).
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks
		(chat_id, user_id, goal, model, plan_json, status, due_at, expires_at, created_at, updated_at, finished_at)
		VALUES
		(100, 11, 'old done', 'm', '[]', 'completed', ?, ?, ?, ?, ?),
		(101, 11, 'fresh done', 'm', '[]', 'completed', ?, ?, ?, ?, ?),
		(102, 11, 'old active', 'm', '[]', 'running', ?, ?, ?, ?, NULL)`,
		pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp,
		pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp,
		pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp); err != nil {
		t.Fatalf("seed tasks: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET finished_at = '2030-01-01T00:00:00.000000000Z',
		updated_at = '2030-01-01T00:00:00.000000000Z' WHERE goal = 'fresh done'`); err != nil {
		t.Fatalf("age fresh task: %v", err)
	}
	// Reminders: sent old (deleted), active old (kept).
	if _, err := db.ExecContext(ctx, `INSERT INTO reminders
		(chat_id, user_id, text, cadence, next_run_at, status, created_at, updated_at, finished_at)
		VALUES
		(100, 11, 'old sent', 'once', ?, 'sent', ?, ?, ?),
		(101, 11, 'old active', 'once', ?, 'active', ?, ?, NULL)`,
		pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp,
		pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp); err != nil {
		t.Fatalf("seed reminders: %v", err)
	}
	// Links: old (deleted), fresh (kept).
	if _, err := db.ExecContext(ctx, `INSERT INTO saved_links
		(chat_id, user_id, title, canonical_url, content, created_at, updated_at)
		VALUES
		(100, 11, 'old link', 'https://example.test/old', 'body', ?, ?),
		(100, 11, 'fresh link', 'https://example.test/fresh', 'body', ?, ?)`,
		pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp, pruneClassOldStamp); err != nil {
		t.Fatalf("seed links: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE saved_links SET created_at = '2030-01-01T00:00:00.000000000Z',
		updated_at = '2030-01-01T00:00:00.000000000Z' WHERE title = 'fresh link'`); err != nil {
		t.Fatalf("age fresh link: %v", err)
	}

	var cutoff = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var options = PruneOptions{QuotaBefore: cutoff, TasksBefore: cutoff, RemindersBefore: cutoff, LinksBefore: cutoff}

	var preview, previewErr = PrunePreview(ctx, db, options)
	if previewErr != nil {
		t.Fatalf("PrunePreview: %v", previewErr)
	}
	if preview.Quota != 1 || preview.Tasks != 1 || preview.Reminders != 1 || preview.Links != 1 {
		t.Fatalf("preview = %+v, want one candidate of each class", preview)
	}

	var report, pruneErr = Prune(ctx, db, options)
	if pruneErr != nil {
		t.Fatalf("Prune: %v", pruneErr)
	}
	if report.Quota != 1 || report.Tasks != 1 || report.Reminders != 1 || report.Links != 1 {
		t.Fatalf("report = %+v, want one deleted row of each class", report)
	}
	if report.Empty() {
		t.Fatal("a non-empty report must not be Empty()")
	}

	var checks = []struct {
		name  string
		query string
		want  int64
	}{
		{"quota", `SELECT COUNT(*) FROM daily_quota_reservations`, 1},
		{"tasks", `SELECT COUNT(*) FROM tasks`, 2},
		{"reminders", `SELECT COUNT(*) FROM reminders`, 1},
		{"links", `SELECT COUNT(*) FROM saved_links`, 1},
	}
	for _, check := range checks {
		var count int64
		if err := db.QueryRowContext(ctx, check.query).Scan(&count); err != nil {
			t.Fatalf("%s count: %v", check.name, err)
		}
		if count != check.want {
			t.Fatalf("%s rows = %d, want %d", check.name, count, check.want)
		}
	}
	// The active task and the active reminder survived: retention deletes history, never live work.
	var activeTasks, activeReminders int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE status = 'running'`).Scan(&activeTasks); err != nil {
		t.Fatalf("active tasks: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reminders WHERE status = 'active'`).Scan(&activeReminders); err != nil {
		t.Fatalf("active reminders: %v", err)
	}
	if activeTasks != 1 || activeReminders != 1 {
		t.Fatalf("active work was pruned: tasks=%d reminders=%d", activeTasks, activeReminders)
	}
}

// TestPruneRejectsAnEmptyPlan keeps the guard: a prune that selects nothing must fail instead of reporting
// success that deletes nothing.
func TestPruneRejectsAnEmptyPlan(t *testing.T) {
	var ctx = context.Background()
	var db, err = OpenMemory(ctx, "prune-empty-plan")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, err := Prune(ctx, db, PruneOptions{}); err == nil {
		t.Fatal("Prune with no cutoff must be refused")
	}
}

// TestVacuumReturnsFreedPages checks that Vacuum runs on a WAL database and leaves it usable.
func TestVacuumReturnsFreedPages(t *testing.T) {
	var ctx = context.Background()
	var db, err = OpenMemory(ctx, "prune-vacuum")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `INSERT INTO events (user_id, kind, detail) VALUES (11, 'command', 'x')`); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if err := Vacuum(ctx, db); err != nil {
		t.Fatalf("Vacuum: %v", err)
	}
	var count int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("the database is not usable after VACUUM: count=%d err=%v", count, err)
	}
}
