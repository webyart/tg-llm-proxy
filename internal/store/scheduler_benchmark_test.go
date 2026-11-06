package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func schedulerBenchmarkDB(b *testing.B, name string) *sql.DB {
	b.Helper()
	db, err := OpenMemory(context.Background(), name)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	return db
}

func schedulerBenchmarkTime() time.Time {
	return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
}

func BenchmarkClaimNextTaskIdle(b *testing.B) {
	ctx := context.Background()
	db := schedulerBenchmarkDB(b, "scheduler_claim_task_idle")
	now := schedulerBenchmarkTime()
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		b.Fatal(err)
	}
	task := newStoreTask(100, now)
	task.DueAt = now.Add(time.Hour)
	if _, err := CreateTask(ctx, db, task); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, found, err := ClaimNextTask(ctx, db, now, time.Minute); err != nil || found {
			b.Fatalf("found=%v err=%v", found, err)
		}
	}
}

func BenchmarkClaimNextTaskDue(b *testing.B) {
	ctx := context.Background()
	db := schedulerBenchmarkDB(b, "scheduler_claim_task_due")
	now := schedulerBenchmarkTime()
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		b.Fatal(err)
	}
	created, err := CreateTask(ctx, db, newStoreTask(100, now))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		b.StopTimer()
		if _, err := db.ExecContext(ctx, `UPDATE tasks SET status = 'ready', attempts = 0, claim_token = NULL, lease_until = NULL, updated_at = ? WHERE id = ?`, formatReminderTime(now), created.ID); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		claimed, found, err := ClaimNextTask(ctx, db, now, time.Minute)
		if err != nil || !found || claimed.ID != created.ID {
			b.Fatalf("claimed=%+v found=%v err=%v", claimed, found, err)
		}
	}
}

func BenchmarkExpireTasksIdle(b *testing.B) {
	ctx := context.Background()
	db := schedulerBenchmarkDB(b, "scheduler_expire_tasks_idle")
	now := schedulerBenchmarkTime()
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		b.Fatal(err)
	}
	if _, err := CreateTask(ctx, db, newStoreTask(100, now)); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		expired, err := ExpireTasks(ctx, db, now, 20)
		if err != nil || len(expired) != 0 {
			b.Fatalf("expired=%+v err=%v", expired, err)
		}
	}
}

func BenchmarkExpireTasksDue(b *testing.B) {
	ctx := context.Background()
	db := schedulerBenchmarkDB(b, "scheduler_expire_tasks_due")
	now := schedulerBenchmarkTime()
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		b.Fatal(err)
	}
	task := newStoreTask(100, now)
	task.ExpiresAt = now
	created, err := CreateTask(ctx, db, task)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		b.StopTimer()
		if _, err := db.ExecContext(ctx, `UPDATE tasks SET status = 'ready', error = NULL, claim_token = NULL, lease_until = NULL,
			continuation_json = NULL, approval_token = NULL, approval_message_id = NULL, approval_expires_at = NULL,
			updated_at = ?, finished_at = NULL WHERE id = ?`, formatReminderTime(now), created.ID); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		expired, err := ExpireTasks(ctx, db, now, 20)
		if err != nil || len(expired) != 1 || expired[0].ID != created.ID {
			b.Fatalf("expired=%+v err=%v", expired, err)
		}
	}
}

func BenchmarkClaimNextReminderIdle(b *testing.B) {
	ctx := context.Background()
	db := schedulerBenchmarkDB(b, "scheduler_claim_reminder_idle")
	now := schedulerBenchmarkTime()
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		b.Fatal(err)
	}
	reminder := Reminder{ChatID: 100, UserID: 11, Text: "future", Cadence: ReminderCadenceOnce, NextRunAt: now.Add(time.Hour), CreatedAt: now}
	if _, err := CreateReminder(ctx, db, reminder); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, found, err := ClaimNextReminder(ctx, db, now, time.Minute); err != nil || found {
			b.Fatalf("found=%v err=%v", found, err)
		}
	}
}

func BenchmarkClaimNextReminderDue(b *testing.B) {
	ctx := context.Background()
	db := schedulerBenchmarkDB(b, "scheduler_claim_reminder_due")
	now := schedulerBenchmarkTime()
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		b.Fatal(err)
	}
	reminder := Reminder{ChatID: 100, UserID: 11, Text: "due", Cadence: ReminderCadenceOnce, NextRunAt: now, CreatedAt: now}
	created, err := CreateReminder(ctx, db, reminder)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		b.StopTimer()
		if _, err := db.ExecContext(ctx, `UPDATE reminders SET status = 'active', attempts = 0, claim_token = NULL, lease_until = NULL, updated_at = ? WHERE id = ?`, formatReminderTime(now), created.ID); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		claimed, found, err := ClaimNextReminder(ctx, db, now, time.Minute)
		if err != nil || !found || claimed.ID != created.ID {
			b.Fatalf("claimed=%+v found=%v err=%v", claimed, found, err)
		}
	}
}
