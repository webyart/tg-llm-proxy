package bot

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bot/internal/store"
)

type taskPassResult struct {
	processed int
	err       error
}

func createSchedulerTask(t *testing.T, d *Dispatcher, chatID int64, now time.Time) store.Task {
	t.Helper()
	ctx := context.Background()
	if err := store.EnsureChat(ctx, d.deps.DB, chatID, 11, ""); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, d.deps.DB, store.Task{
		ChatID: chatID, UserID: 11, Goal: "scheduler goal", Model: "selected-model",
		PlanJSON: validTaskPlanOne, DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func holdSchedulerChatLock(t *testing.T, d *Dispatcher, chatID int64) (release func(), stopped <-chan struct{}) {
	t.Helper()
	held := make(chan struct{})
	unblock := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_ = d.deps.Locks.WithChat(context.Background(), chatID, func() error {
			close(held)
			<-unblock
			return nil
		})
		close(done)
	}()
	select {
	case <-held:
	case <-time.After(2 * time.Second):
		t.Fatal("chat lock was not acquired")
	}
	var once atomic.Bool
	return func() {
		if once.CompareAndSwap(false, true) {
			close(unblock)
		}
	}, done
}

func waitForClaimedTask(t *testing.T, d *Dispatcher, chatID int64) store.Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		task, found, err := store.CurrentTask(context.Background(), d.deps.DB, chatID)
		if err != nil {
			t.Fatal(err)
		}
		if found && task.Status == store.TaskStatusRunning && task.ClaimToken != "" {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("task was not claimed")
	return store.Task{}
}

func waitForTaskModelCalls(t *testing.T, model *taskScriptModel, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if model.callCount() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("model calls=%d, want %d", model.callCount(), want)
}

func waitForTaskReadyStep(t *testing.T, d *Dispatcher, chatID int64, step int) store.Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		task, found, err := store.CurrentTask(context.Background(), d.deps.DB, chatID)
		if err != nil {
			t.Fatal(err)
		}
		if found && task.NextStep == step && task.Status == store.TaskStatusReady {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task did not reach ready step %d", step)
	return store.Task{}
}

func TestTaskSchedulerBlockedChatAllowsOtherChatProgress(t *testing.T) {
	m := taskTextModel("first result", "second result")
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, testStore(t))
	now := time.Now().UTC()
	createSchedulerTask(t, d, 100, now)
	createSchedulerTask(t, d, 200, now)
	release, lockStopped := holdSchedulerChatLock(t, d, 100)
	defer func() {
		release()
		<-lockStopped
	}()

	done := make(chan taskPassResult, 1)
	go func() {
		processed, err := d.ProcessDueTasks(context.Background(), now, TaskBatchLimit)
		done <- taskPassResult{processed: processed, err: err}
	}()
	waitForTaskModelCalls(t, m, 1)
	_ = waitForTaskReadyStep(t, d, 200, 1)
	select {
	case result := <-done:
		t.Fatalf("pass returned while a chat lock was still blocked: %+v", result)
	default:
	}

	release()
	select {
	case result := <-done:
		if result.err != nil || result.processed != 2 {
			t.Fatalf("pass result=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pass did not finish after releasing the chat lock")
	}
	if m.callCount() != 2 {
		t.Fatalf("model calls=%d, want both chats to execute", m.callCount())
	}
}

func TestTaskSchedulerRevalidatesLeaseAfterBlockedChatLock(t *testing.T) {
	m := taskTextModel("must not execute")
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, testStore(t))
	now := time.Now().UTC()
	createSchedulerTask(t, d, 100, now)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	d.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	release, lockStopped := holdSchedulerChatLock(t, d, 100)
	defer func() {
		release()
		<-lockStopped
	}()

	done := make(chan taskPassResult, 1)
	go func() {
		processed, err := d.ProcessDueTasks(context.Background(), now, 1)
		done <- taskPassResult{processed: processed, err: err}
	}()
	claimed := waitForClaimedTask(t, d, 100)
	clock.Store(claimed.LeaseUntil.Add(time.Second).UnixNano())
	release()
	select {
	case result := <-done:
		if result.err != nil || result.processed != 1 {
			t.Fatalf("stale claim pass result=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale claim pass did not finish")
	}
	if m.callCount() != 0 {
		t.Fatalf("expired claim reached the model %d times", m.callCount())
	}
}

func TestTaskSchedulerRevalidatesClaimOnShutdown(t *testing.T) {
	m := taskTextModel("must not execute")
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, testStore(t))
	now := time.Now().UTC()
	createSchedulerTask(t, d, 100, now)
	release, lockStopped := holdSchedulerChatLock(t, d, 100)
	defer func() {
		release()
		<-lockStopped
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan taskPassResult, 1)
	go func() {
		processed, err := d.ProcessDueTasks(ctx, now, 1)
		done <- taskPassResult{processed: processed, err: err}
	}()
	waitForClaimedTask(t, d, 100)
	cancel()
	release()
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) || result.processed != 1 {
			t.Fatalf("shutdown pass result=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown pass did not finish")
	}
	if m.callCount() != 0 {
		t.Fatalf("cancelled claim reached the model %d times", m.callCount())
	}
}

func TestTaskClaimRevalidationRejectsChangedState(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name  string
		query string
		args  []any
	}{
		{name: "status", query: "UPDATE tasks SET status = 'cancelled' WHERE id = ?"},
		{name: "claim token", query: "UPDATE tasks SET claim_token = 'replacement-token' WHERE id = ?"},
		{name: "expiry", query: "UPDATE tasks SET expires_at = ? WHERE id = ?", args: []any{now.Add(-time.Second).Format("2006-01-02T15:04:05.000000000Z")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := taskTextModel("must not execute")
			d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, testStore(t))
			created := createSchedulerTask(t, d, 100, now)
			claimed, found, err := store.ClaimNextTask(context.Background(), d.deps.DB, now, d.taskLease())
			if err != nil || !found {
				t.Fatalf("claim found=%v err=%v", found, err)
			}
			args := append([]any{}, test.args...)
			args = append(args, created.ID)
			if _, err := d.deps.DB.ExecContext(context.Background(), test.query, args...); err != nil {
				t.Fatal(err)
			}
			err = d.deps.Locks.WithChat(context.Background(), created.ChatID, func() error {
				return d.revalidateAndExecuteClaimedTask(context.Background(), claimed)
			})
			if !errors.Is(err, errTaskStale) {
				t.Fatalf("revalidation err=%v, want errTaskStale", err)
			}
			if m.callCount() != 0 {
				t.Fatalf("stale %s reached the model", test.name)
			}
		})
	}
}

func requireTaskSchedulerBlocked(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	stable := 0
	var stack string
	buffer := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		n := runtime.Stack(buffer, true)
		stack = string(buffer[:n])
		passRunning := strings.Contains(stack, "bot/internal/bot.(*Dispatcher).processDueTasks(")
		blocked := false
		for _, goroutine := range strings.Split(stack, "\n\n") {
			if !strings.Contains(goroutine, "bot/internal/bot.(*Dispatcher).RunTaskScheduler(") {
				continue
			}
			header, _, _ := strings.Cut(goroutine, "\n")
			blocked = !passRunning && strings.Contains(header, "[select")
		}
		if blocked {
			stable++
			if stable == 5 {
				return
			}
		} else {
			stable = 0
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task scheduler did not stay blocked after its pass:\n%s", stack)
}

func TestTaskSchedulerBlocksAfterPassUntilWake(t *testing.T) {
	m := taskTextModel("wake result", "wake synthesis")
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, testStore(t))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.RunTaskScheduler(ctx)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("task scheduler did not stop")
		}
	}()

	requireTaskSchedulerBlocked(t)
	createSchedulerTask(t, d, 100, time.Now().UTC())
	d.wakeTasks()
	deadline := time.Now().Add(2 * time.Second)
	for m.callCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.callCount() < 1 {
		t.Fatal("task scheduler did not run after its wake")
	}
}

func TestTaskSchedulerRunsPendingPassImmediately(t *testing.T) {
	m := taskTextModel("first result", "first synthesis", "pending result", "pending synthesis")
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, testStore(t))
	now := time.Now().UTC()
	createSchedulerTask(t, d, 100, now)
	release, lockStopped := holdSchedulerChatLock(t, d, 100)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.RunTaskScheduler(ctx)
	}()
	defer func() {
		release()
		<-lockStopped
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("task scheduler did not stop")
		}
	}()

	_ = waitForClaimedTask(t, d, 100)
	createSchedulerTask(t, d, 200, now)
	d.wakeTasks()
	wakeDeadline := time.Now().Add(2 * time.Second)
	for len(d.taskWake) != 0 && time.Now().Before(wakeDeadline) {
		time.Sleep(time.Millisecond)
	}
	if len(d.taskWake) != 0 {
		t.Fatal("task wake was not consumed while the first pass was blocked")
	}
	release()
	pendingDeadline := time.Now().Add(500 * time.Millisecond)
	pendingStarted := false
	for !pendingStarted && time.Now().Before(pendingDeadline) {
		task, found, err := store.CurrentTask(context.Background(), d.deps.DB, 200)
		if err != nil {
			t.Fatal(err)
		}
		pendingStarted = found && task.Attempts > 0
		time.Sleep(time.Millisecond)
	}
	if !pendingStarted {
		t.Fatal("pending task pass waited for another tick")
	}
}
