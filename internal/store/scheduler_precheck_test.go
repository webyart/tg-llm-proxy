package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

type schedulerStatementCounter struct {
	taskUpdates     atomic.Int64
	reminderUpdates atomic.Int64
}

func (counter *schedulerStatementCounter) observe(query string) {
	normalized := strings.ToUpper(strings.TrimSpace(query))
	if strings.HasPrefix(normalized, "UPDATE TASKS") {
		counter.taskUpdates.Add(1)
	}
	if strings.HasPrefix(normalized, "UPDATE REMINDERS") {
		counter.reminderUpdates.Add(1)
	}
}

func (counter *schedulerStatementCounter) reset() {
	counter.taskUpdates.Store(0)
	counter.reminderUpdates.Store(0)
}

type schedulerStatementDriver struct {
	counter *schedulerStatementCounter
}

func (d *schedulerStatementDriver) Open(name string) (driver.Conn, error) {
	conn, err := new(sqlite.Driver).Open(name)
	if err != nil {
		return nil, err
	}
	return &schedulerStatementConn{Conn: conn, counter: d.counter}, nil
}

type schedulerStatementConn struct {
	driver.Conn
	counter *schedulerStatementCounter
}

func (conn *schedulerStatementConn) Prepare(query string) (driver.Stmt, error) {
	conn.counter.observe(query)
	return conn.Conn.Prepare(query)
}

func (conn *schedulerStatementConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	conn.counter.observe(query)
	if preparer, ok := conn.Conn.(driver.ConnPrepareContext); ok {
		return preparer.PrepareContext(ctx, query)
	}
	return conn.Conn.Prepare(query)
}

var schedulerStatementDriverID atomic.Uint64

func schedulerStatementTestDB(t *testing.T) (*sql.DB, *schedulerStatementCounter) {
	t.Helper()
	counter := new(schedulerStatementCounter)
	id := schedulerStatementDriverID.Add(1)
	driverName := fmt.Sprintf("scheduler_statements_%d", id)
	sql.Register(driverName, &schedulerStatementDriver{counter: counter})
	dsn, err := MemoryDSN(fmt.Sprintf("scheduler_statements_%d", id))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(MaxOpenConns)
	db.SetMaxIdleConns(MaxIdleConns)
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := ApplySchema(context.Background(), db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	counter.reset()
	t.Cleanup(func() { _ = db.Close() })
	return db, counter
}

func requireSchedulerUpdateCounts(t *testing.T, counter *schedulerStatementCounter, tasks, reminders int64) {
	t.Helper()
	if got := counter.taskUpdates.Load(); got != tasks {
		t.Fatalf("task UPDATE statements=%d want=%d", got, tasks)
	}
	if got := counter.reminderUpdates.Load(); got != reminders {
		t.Fatalf("reminder UPDATE statements=%d want=%d", got, reminders)
	}
}

func TestTaskSchedulerPrecheckIdleDueAndLeaseRecovery(t *testing.T) {
	ctx := context.Background()
	db, counter := schedulerStatementTestDB(t)
	now := time.Date(2026, 11, 6, 17, 0, 0, 123456789, time.FixedZone("UTC+5", 5*60*60))
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	task := newStoreTask(100, now)
	task.DueAt = now.Add(time.Hour)
	task.ExpiresAt = now.Add(2 * time.Hour)
	if _, err := CreateTask(ctx, db, task); err != nil {
		t.Fatal(err)
	}
	counter.reset()

	if _, found, err := ClaimNextTask(ctx, db, now, time.Minute); err != nil || found {
		t.Fatalf("future claim found=%v err=%v", found, err)
	}
	if expired, err := ExpireTasks(ctx, db, now, 20); err != nil || len(expired) != 0 {
		t.Fatalf("future expiry expired=%+v err=%v", expired, err)
	}
	requireSchedulerUpdateCounts(t, counter, 0, 0)

	if _, err := db.ExecContext(ctx, `UPDATE tasks SET due_at = ?`, formatReminderTime(now)); err != nil {
		t.Fatal(err)
	}
	counter.reset()
	claimed, found, err := ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found || claimed.Attempts != 1 {
		t.Fatalf("due claim=%+v found=%v err=%v", claimed, found, err)
	}
	requireSchedulerUpdateCounts(t, counter, 1, 0)

	counter.reset()
	if _, found, err := ClaimNextTask(ctx, db, now.Add(30*time.Second), time.Minute); err != nil || found {
		t.Fatalf("unexpired lease claim found=%v err=%v", found, err)
	}
	if expired, err := ExpireTasks(ctx, db, now.Add(30*time.Second), 20); err != nil || len(expired) != 0 {
		t.Fatalf("unexpired task expired=%+v err=%v", expired, err)
	}
	requireSchedulerUpdateCounts(t, counter, 0, 0)

	recovered, found, err := ClaimNextTask(ctx, db, now.Add(time.Minute), time.Minute)
	if err != nil || !found || recovered.ID != claimed.ID || recovered.Attempts != 2 {
		t.Fatalf("recovered=%+v found=%v err=%v", recovered, found, err)
	}
	requireSchedulerUpdateCounts(t, counter, 1, 0)
}

func TestExpireTasksPrecheckMatchesExpiryAndApprovalPredicates(t *testing.T) {
	ctx := context.Background()
	db, counter := schedulerStatementTestDB(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, chatID := range []int64{100, 101} {
		if err := EnsureChat(ctx, db, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
	}
	expiring := newStoreTask(100, now)
	expiring.ExpiresAt = now
	if _, err := CreateTask(ctx, db, expiring); err != nil {
		t.Fatal(err)
	}
	approvalTask := newStoreTask(101, now.Add(-time.Minute))
	if _, err := CreateTask(ctx, db, approvalTask); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found || claimed.ChatID != 101 {
		t.Fatalf("approval claim=%+v found=%v err=%v", claimed, found, err)
	}
	if saved, err := SaveTaskApproval(ctx, db, claimed, "0123456789abcdef0123456789abcdef", 700, now, `{"request":{}}`, now); err != nil || !saved {
		t.Fatalf("SaveTaskApproval saved=%v err=%v", saved, err)
	}
	counter.reset()

	expired, err := ExpireTasks(ctx, db, now, 20)
	if err != nil || len(expired) != 2 {
		t.Fatalf("expired=%+v err=%v", expired, err)
	}
	requireSchedulerUpdateCounts(t, counter, 1, 0)
	for _, task := range expired {
		if task.Status != TaskStatusFailed {
			t.Fatalf("expired task=%+v", task)
		}
	}

	counter.reset()
	if expired, err := ExpireTasks(ctx, db, now, 20); err != nil || len(expired) != 0 {
		t.Fatalf("terminal expiry expired=%+v err=%v", expired, err)
	}
	requireSchedulerUpdateCounts(t, counter, 0, 0)
}

func TestReminderSchedulerPrecheckIdleDueAndLeaseRecovery(t *testing.T) {
	ctx := context.Background()
	db, counter := schedulerStatementTestDB(t)
	now := time.Date(2026, 11, 6, 17, 0, 0, 123456789, time.FixedZone("UTC+5", 5*60*60))
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateReminder(ctx, db, Reminder{ChatID: 100, UserID: 11, Text: "bounded", Cadence: ReminderCadenceOnce, NextRunAt: now.Add(time.Hour), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	counter.reset()

	if _, found, err := ClaimNextReminder(ctx, db, now, time.Minute); err != nil || found {
		t.Fatalf("future claim found=%v err=%v", found, err)
	}
	requireSchedulerUpdateCounts(t, counter, 0, 0)

	if _, err := db.ExecContext(ctx, `UPDATE reminders SET next_run_at = ?`, formatReminderTime(now)); err != nil {
		t.Fatal(err)
	}
	counter.reset()
	claimed, found, err := ClaimNextReminder(ctx, db, now, time.Minute)
	if err != nil || !found || claimed.Attempts != 1 {
		t.Fatalf("due claim=%+v found=%v err=%v", claimed, found, err)
	}
	requireSchedulerUpdateCounts(t, counter, 0, 1)

	counter.reset()
	if _, found, err := ClaimNextReminder(ctx, db, now.Add(30*time.Second), time.Minute); err != nil || found {
		t.Fatalf("unexpired lease claim found=%v err=%v", found, err)
	}
	requireSchedulerUpdateCounts(t, counter, 0, 0)

	recovered, found, err := ClaimNextReminder(ctx, db, now.Add(time.Minute), time.Minute)
	if err != nil || !found || recovered.ID != claimed.ID || recovered.Attempts != 2 {
		t.Fatalf("recovered=%+v found=%v err=%v", recovered, found, err)
	}
	requireSchedulerUpdateCounts(t, counter, 0, 1)
}

func TestClaimNextTaskConcurrentInserts(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	const total = 12
	for chatID := int64(100); chatID < 100+total; chatID++ {
		if err := EnsureChat(ctx, db, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	claimedIDs := make(chan int64, total)
	errs := make(chan error, total)
	var wg sync.WaitGroup
	for chatID := int64(100); chatID < 100+total; chatID++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := CreateTask(ctx, db, newStoreTask(chatID, now)); err != nil {
				errs <- fmt.Errorf("create chat %d: %w", chatID, err)
				return
			}
			claimed, found, err := ClaimNextTask(ctx, db, now, time.Minute)
			if err != nil {
				errs <- fmt.Errorf("claim chat %d: %w", chatID, err)
				return
			}
			if found {
				claimedIDs <- claimed.ID
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for {
		claimed, found, err := ClaimNextTask(ctx, db, now, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		claimedIDs <- claimed.ID
	}
	close(claimedIDs)
	unique := make(map[int64]struct{}, total)
	for id := range claimedIDs {
		if _, exists := unique[id]; exists {
			t.Fatalf("task %d claimed more than once", id)
		}
		unique[id] = struct{}{}
	}
	if len(unique) != total {
		t.Fatalf("claimed tasks=%d want=%d", len(unique), total)
	}
}

func TestClaimNextReminderConcurrentInserts(t *testing.T) {
	ctx := context.Background()
	db := reminderTestDB(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	const total = 12
	for chatID := int64(100); chatID < 100+total; chatID++ {
		if err := EnsureChat(ctx, db, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	claimedIDs := make(chan int64, total)
	errs := make(chan error, total)
	var wg sync.WaitGroup
	for chatID := int64(100); chatID < 100+total; chatID++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			reminder := Reminder{ChatID: chatID, UserID: 11, Text: "concurrent", Cadence: ReminderCadenceOnce, NextRunAt: now, CreatedAt: now}
			if _, err := CreateReminder(ctx, db, reminder); err != nil {
				errs <- fmt.Errorf("create chat %d: %w", chatID, err)
				return
			}
			claimed, found, err := ClaimNextReminder(ctx, db, now, time.Minute)
			if err != nil {
				errs <- fmt.Errorf("claim chat %d: %w", chatID, err)
				return
			}
			if found {
				claimedIDs <- claimed.ID
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for {
		claimed, found, err := ClaimNextReminder(ctx, db, now, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		claimedIDs <- claimed.ID
	}
	close(claimedIDs)
	unique := make(map[int64]struct{}, total)
	for id := range claimedIDs {
		if _, exists := unique[id]; exists {
			t.Fatalf("reminder %d claimed more than once", id)
		}
		unique[id] = struct{}{}
	}
	if len(unique) != total {
		t.Fatalf("claimed reminders=%d want=%d", len(unique), total)
	}
}
