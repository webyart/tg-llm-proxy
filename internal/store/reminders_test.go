package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reminderTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenMemory(context.Background(), strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestReminderMigrationPreservesSchema6DataAndIndexes(t *testing.T) {
	ctx := context.Background()
	db := reminderTestDB(t)
	migrations := MigrationSQL
	MigrationSQL = append([]string(nil), migrations[:6]...)
	defer func() { MigrationSQL = migrations }()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureChat(ctx, db, 100, 11, "kept"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO events (user_id, kind, detail, created_at) VALUES (11, 'usage', 'schema6', '2026-01-02 03:04:05')`); err != nil {
		t.Fatal(err)
	}
	MigrationSQL = migrations
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != 7 || version != len(MigrationSQL) {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var title, detail string
	if err := db.QueryRowContext(ctx, `SELECT title FROM chats WHERE chat_id = 100`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT detail FROM events WHERE user_id = 11`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if title != "kept" || detail != "schema6" {
		t.Fatalf("schema 6 data changed: %q/%q", title, detail)
	}
	for _, index := range []string{"idx_reminders_due", "idx_reminders_lease", "idx_reminders_chat_active"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s count=%d err=%v", index, count, err)
		}
	}
}

func TestReminderCRUDClaimCompletionAndChatIsolation(t *testing.T) {
	ctx := context.Background()
	db := reminderTestDB(t)
	for _, chatID := range []int64{100, 101} {
		if err := EnsureChat(ctx, db, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	once, err := CreateReminder(ctx, db, Reminder{ChatID: 100, UserID: 11, Text: "once", Cadence: ReminderCadenceOnce, NextRunAt: now.Add(time.Minute), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	daily, err := CreateReminder(ctx, db, Reminder{ChatID: 100, UserID: 11, Text: "daily", Cadence: ReminderCadenceDaily, DailyTime: "13:00", NextRunAt: now.Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	other, err := CreateReminder(ctx, db, Reminder{ChatID: 101, UserID: 11, Text: "other", Cadence: ReminderCadenceOnce, NextRunAt: now.Add(3 * time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := ListReminders(ctx, db, 100, 20)
	if err != nil || len(rows) != 2 || rows[0].ID != once.ID || rows[1].ID != daily.ID {
		t.Fatalf("ListReminders=%+v err=%v", rows, err)
	}
	if _, found, err := ClaimNextReminder(ctx, db, now, time.Minute); err != nil || found {
		t.Fatalf("future claim found=%v err=%v", found, err)
	}
	claimed, found, err := ClaimNextReminder(ctx, db, now.Add(time.Minute), time.Minute)
	if err != nil || !found || claimed.ID != once.ID || claimed.Attempts != 1 || claimed.Status != ReminderStatusSending {
		t.Fatalf("claim=%+v found=%v err=%v", claimed, found, err)
	}
	if _, found, err := ClaimNextReminder(ctx, db, now.Add(time.Minute), time.Minute); err != nil || found {
		t.Fatalf("duplicate claim found=%v err=%v", found, err)
	}
	completed, err := CompleteReminder(ctx, db, claimed, now.Add(time.Minute), time.Time{})
	if err != nil || !completed {
		t.Fatalf("complete=%v err=%v", completed, err)
	}
	completed, err = CompleteReminder(ctx, db, claimed, now.Add(time.Minute), time.Time{})
	if err != nil || completed {
		t.Fatalf("idempotent completion=%v err=%v", completed, err)
	}
	claimedDaily, found, err := ClaimNextReminder(ctx, db, now.Add(time.Hour), time.Minute)
	if err != nil || !found || claimedDaily.ID != daily.ID {
		t.Fatalf("daily claim=%+v found=%v err=%v", claimedDaily, found, err)
	}
	nextDaily := now.Add(25 * time.Hour)
	if ok, err := CompleteReminder(ctx, db, claimedDaily, now.Add(time.Hour), nextDaily); err != nil || !ok {
		t.Fatalf("daily completion ok=%v err=%v", ok, err)
	}
	rows, _ = ListReminders(ctx, db, 100, 20)
	if len(rows) != 1 || !rows[0].NextRunAt.Equal(nextDaily) || rows[0].Attempts != 0 {
		t.Fatalf("daily advance rows=%+v", rows)
	}
	if ok, err := CancelReminder(ctx, db, 100, other.ID, now); err != nil || ok {
		t.Fatalf("cross-chat cancel ok=%v err=%v", ok, err)
	}
	if ok, err := CancelReminder(ctx, db, 101, other.ID, now); err != nil || !ok {
		t.Fatalf("owner cancel ok=%v err=%v", ok, err)
	}
	if ok, err := CancelReminder(ctx, db, 101, other.ID, now); err != nil || ok {
		t.Fatalf("stale cancel ok=%v err=%v", ok, err)
	}
}

func TestReminderLimitAndValidation(t *testing.T) {
	ctx := context.Background()
	db := reminderTestDB(t)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < ReminderMaxActivePerChat; i++ {
		if _, err := CreateReminder(ctx, db, Reminder{ChatID: 100, UserID: 11, Text: "bounded", Cadence: ReminderCadenceOnce, NextRunAt: now.Add(time.Duration(i+1) * time.Hour), CreatedAt: now}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	extra := Reminder{ChatID: 100, UserID: 11, Text: "extra", Cadence: ReminderCadenceOnce, NextRunAt: now.Add(24 * time.Hour), CreatedAt: now}
	if _, err := CreateReminder(ctx, db, extra); !errors.Is(err, ErrReminderLimit) {
		t.Fatalf("21st reminder err=%v", err)
	}
	if ok, err := CancelReminder(ctx, db, 100, 1, now); err != nil || !ok {
		t.Fatalf("cancel for capacity ok=%v err=%v", ok, err)
	}
	if _, err := CreateReminder(ctx, db, extra); err != nil {
		t.Fatalf("cancel did not release capacity: %v", err)
	}
	for _, test := range []struct {
		name     string
		reminder Reminder
		want     error
	}{
		{"empty", Reminder{ChatID: 100, UserID: 11, Text: " ", Cadence: ReminderCadenceOnce, NextRunAt: now}, ErrReminderText},
		{"long", Reminder{ChatID: 100, UserID: 11, Text: strings.Repeat("я", 501), Cadence: ReminderCadenceOnce, NextRunAt: now}, ErrReminderText},
		{"nul", Reminder{ChatID: 100, UserID: 11, Text: "a\x00b", Cadence: ReminderCadenceOnce, NextRunAt: now}, ErrReminderText},
		{"interval", Reminder{ChatID: 100, UserID: 11, Text: "bad", Cadence: "hourly", NextRunAt: now}, ErrReminderCadence},
		{"daily clock", Reminder{ChatID: 100, UserID: 11, Text: "bad", Cadence: ReminderCadenceDaily, DailyTime: "24:00", NextRunAt: now}, ErrReminderTime},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CreateReminder(ctx, db, test.reminder); !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
		})
	}
}

func TestReminderRestartRecoversLeaseWithoutDuplicateAfterCompletion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reminders.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	once, err := CreateReminder(ctx, db, Reminder{ChatID: 100, UserID: 11, Text: "persist once", Cadence: ReminderCadenceOnce, NextRunAt: now, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateReminder(ctx, db, Reminder{ChatID: 100, UserID: 11, Text: "persist daily", Cadence: ReminderCadenceDaily, DailyTime: "09:00", NextRunAt: now.Add(24 * time.Hour), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	_, found, err := ClaimNextReminder(ctx, db, now, time.Minute)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := ListReminders(ctx, db, 100, 20)
	if err != nil || len(rows) != 2 {
		t.Fatalf("restart rows=%+v err=%v", rows, err)
	}
	if _, found, err := ClaimNextReminder(ctx, db, now.Add(30*time.Second), time.Minute); err != nil || found {
		t.Fatalf("unexpired lease recovered found=%v err=%v", found, err)
	}
	recovered, found, err := ClaimNextReminder(ctx, db, now.Add(61*time.Second), time.Minute)
	if err != nil || !found || recovered.ID != once.ID || recovered.Attempts != 2 {
		t.Fatalf("recovered=%+v found=%v err=%v", recovered, found, err)
	}
	if ok, err := CompleteReminder(ctx, db, recovered, now.Add(61*time.Second), time.Time{}); err != nil || !ok {
		t.Fatalf("complete recovered ok=%v err=%v", ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := ClaimNextReminder(ctx, db, now.Add(61*time.Second), time.Minute); err != nil || found {
		t.Fatalf("completed reminder duplicated after restart found=%v err=%v", found, err)
	}
}
