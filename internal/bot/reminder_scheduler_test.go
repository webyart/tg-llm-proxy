package bot

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"bot/internal/store"
)

type blockedReminderReplier struct {
	fakeReplier
	started chan int64
	release chan struct{}
}

func (r *blockedReminderReplier) Send(ctx context.Context, chatID int64, text string) error {
	r.started <- chatID
	select {
	case <-r.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.fakeReplier.Send(ctx, chatID, text)
}

func TestReminderSchedulerSlowDeliveriesRunConcurrently(t *testing.T) {
	r := &blockedReminderReplier{started: make(chan int64, 6), release: make(chan struct{})}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, testStore(t))
	now := time.Now().UTC()
	for _, chatID := range []int64{100, 200, 300, 400, 500, 600} {
		if err := store.EnsureChat(context.Background(), d.deps.DB, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateReminder(context.Background(), d.deps.DB, store.Reminder{
			ChatID: chatID, UserID: 11, Text: "slow delivery", Cadence: store.ReminderCadenceOnce, NextRunAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(r.release) }) }
	defer release()
	done := make(chan taskPassResult, 1)
	go func() {
		processed, err := d.ProcessDueReminders(context.Background(), now, ReminderBatchLimit)
		done <- taskPassResult{processed: processed, err: err}
	}()

	for delivery := 0; delivery < reminderWorkerLimit; delivery++ {
		select {
		case <-r.started:
		case <-time.After(2 * time.Second):
			release()
			<-done
			t.Fatalf("only %d slow deliveries started before one was released", delivery)
		}
	}
	select {
	case chatID := <-r.started:
		release()
		<-done
		t.Fatalf("delivery to chat %d exceeded the %d-worker bound", chatID, reminderWorkerLimit)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case result := <-done:
		if result.err != nil || result.processed != 6 {
			t.Fatalf("reminder pass result=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reminder pass did not finish")
	}
	if r.count() != 6 {
		t.Fatalf("delivered reminders=%d, want 6", r.count())
	}
}

func requireReminderSchedulerBlocked(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	stable := 0
	var stack string
	buffer := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		n := runtime.Stack(buffer, true)
		stack = string(buffer[:n])
		passRunning := strings.Contains(stack, "bot/internal/bot.(*Dispatcher).processDueReminders(")
		blocked := false
		for _, goroutine := range strings.Split(stack, "\n\n") {
			if !strings.Contains(goroutine, "bot/internal/bot.(*Dispatcher).RunReminderScheduler(") {
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
	t.Fatalf("reminder scheduler did not stay blocked after its pass:\n%s", stack)
}

func TestReminderSchedulerBlocksAfterPassUntilNextTick(t *testing.T) {
	r := &blockedReminderReplier{started: make(chan int64, 1), release: make(chan struct{})}
	close(r.release)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, testStore(t))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.RunReminderScheduler(ctx)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("reminder scheduler did not stop")
		}
	}()

	requireReminderSchedulerBlocked(t)
	now := time.Now().UTC()
	if err := store.EnsureChat(context.Background(), d.deps.DB, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateReminder(context.Background(), d.deps.DB, store.Reminder{
		ChatID: 100, UserID: 11, Text: "next tick", Cadence: store.ReminderCadenceOnce, NextRunAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case chatID := <-r.started:
		if chatID != 100 {
			t.Fatalf("delivered chat=%d, want 100", chatID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reminder scheduler did not run on the next tick")
	}
}

func TestReminderSchedulerRunsPendingPassImmediately(t *testing.T) {
	r := &blockedReminderReplier{started: make(chan int64, 2), release: make(chan struct{})}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, testStore(t))
	now := time.Now().UTC()
	if err := store.EnsureChat(context.Background(), d.deps.DB, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateReminder(context.Background(), d.deps.DB, store.Reminder{
		ChatID: 100, UserID: 11, Text: "first pass", Cadence: store.ReminderCadenceOnce, NextRunAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(r.release) }) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.RunReminderScheduler(ctx)
	}()
	defer func() {
		release()
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("reminder scheduler did not stop")
		}
	}()

	select {
	case chatID := <-r.started:
		if chatID != 100 {
			t.Fatalf("first delivery chat=%d, want 100", chatID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first reminder pass did not start")
	}
	if err := store.EnsureChat(context.Background(), d.deps.DB, 200, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateReminder(context.Background(), d.deps.DB, store.Reminder{
		ChatID: 200, UserID: 11, Text: "pending pass", Cadence: store.ReminderCadenceOnce, NextRunAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(ReminderPollInterval + 200*time.Millisecond)
	release()
	select {
	case chatID := <-r.started:
		if chatID != 200 {
			t.Fatalf("pending delivery chat=%d, want 200", chatID)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pending reminder pass waited for another tick")
	}
}
