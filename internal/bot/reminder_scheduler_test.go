package bot

import (
	"context"
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
