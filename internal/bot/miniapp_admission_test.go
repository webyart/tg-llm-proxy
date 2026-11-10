package bot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"bot/internal/model"
)

// TestMiniAppQueueOverflowUsesTheTelegramOverloadedAnswer — the Mini App now takes the same
// bounded per-chat queue as Telegram. Overflow must produce Telegram's msgOverloaded, not a
// generic model failure, and the overflow must not reach the model.
func TestMiniAppQueueOverflowUsesTheTelegramOverloadedAnswer(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11",
		"STREAMING_ENABLED":      "false",
		"RATE_LIMIT_PER_MINUTE":  "0",
	})
	m := newBlockingAdmissionModel()
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, testStore(t))
	t.Cleanup(m.unblock)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- d.AnswerForMiniApp(context.Background(), 11, 11, "вопрос 1", func(MiniAppStreamEvent) error { return nil })
	}()
	awaitAdmissionSignal(t, m.started)

	queued := make(chan error, perChatQueueCapacity)
	for index := 0; index < perChatQueueCapacity; index++ {
		go func(index int) {
			text := fmt.Sprintf("вопрос %d", index+2)
			queued <- d.AnswerForMiniApp(context.Background(), 11, 11, text, func(MiniAppStreamEvent) error { return nil })
		}(index)
		waitAdmission(t, func() bool { return d.deps.Locks.Waiting(11) == index+1 })
	}

	log := &miniAppEventLog{}
	err := d.AnswerForMiniApp(context.Background(), 11, 11, "лишний вопрос", log.emit)
	if !errors.Is(err, errMiniAppOverloaded) {
		t.Fatalf("queue overflow error = %v, want errMiniAppOverloaded", err)
	}
	if len(log.events) != 1 || log.events[0].Error != msgOverloaded {
		t.Fatalf("queue overflow events = %+v, want the Telegram overloaded text %q", log.events, msgOverloaded)
	}
	if got := m.callCount(); got != 1 {
		t.Fatalf("model calls while the queue is full = %d, want 1 (the running answer only)", got)
	}

	m.unblock()
	if err := <-firstDone; err != nil {
		t.Fatalf("first AnswerForMiniApp: %v", err)
	}
	for index := 0; index < perChatQueueCapacity; index++ {
		if err := <-queued; err != nil {
			t.Fatalf("queued AnswerForMiniApp %d: %v", index, err)
		}
	}
}

// firstCallGateModel blocks its first Complete call until release and returns firstErr. Later calls
// succeed immediately: they are used to prove that the execution slot was released.
type firstCallGateModel struct {
	mu       sync.Mutex
	calls    int
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
	firstErr error
	response model.Response
}

func (m *firstCallGateModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	m.mu.Lock()
	call := m.calls
	m.calls++
	m.mu.Unlock()
	if call == 0 {
		m.once.Do(func() { close(m.started) })
		select {
		case <-m.release:
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
		if m.firstErr != nil {
			return model.Response{}, m.firstErr
		}
	}
	return m.response, nil
}

func (m *firstCallGateModel) Models(context.Context) ([]string, error) { return nil, nil }

func (m *firstCallGateModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// TestMiniAppExecutionSlotIsReleasedOnErrorAndCancel — the Mini App answer owns Telegram's global
// execution slot and active_handlers counter. A second chat is overloaded while the slot is held,
// and the slot is returned on both the ordinary error path and the cancelled path.
func TestMiniAppExecutionSlotIsReleasedOnErrorAndCancel(t *testing.T) {
	for _, test := range []struct {
		name     string
		firstErr error
	}{
		{name: "model error", firstErr: errors.New("provider failed")},
		{name: "cancelled model call", firstErr: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(t, map[string]string{
				"TELEGRAM_ALLOWED_USERS":        "11",
				"STREAMING_ENABLED":             "false",
				"RATE_LIMIT_PER_MINUTE":         "0",
				"MAX_CONCURRENT_MODEL_REQUESTS": "1",
			})
			m := &firstCallGateModel{
				started:  make(chan struct{}),
				release:  make(chan struct{}),
				firstErr: test.firstErr,
				response: model.Response{Content: "готово", Model: "fake-model", Latency: time.Millisecond, Attempts: 1},
			}
			d := newTestDispatcher(t, cfg, m, &fakeReplier{}, testStore(t))
			d.chatQueueTimeout = 100 * time.Millisecond

			firstDone := make(chan error, 1)
			go func() {
				firstDone <- d.AnswerForMiniApp(context.Background(), 11, 11, "первый", func(MiniAppStreamEvent) error { return nil })
			}()
			awaitAdmissionSignal(t, m.started)
			if got := d.activeHandlers.Load(); got != 1 {
				t.Fatalf("active_handlers while the first answer runs = %d, want 1", got)
			}

			log := &miniAppEventLog{}
			err := d.AnswerForMiniApp(context.Background(), 12, 11, "второй", log.emit)
			if !errors.Is(err, errMiniAppOverloaded) {
				t.Fatalf("second chat error = %v, want errMiniAppOverloaded while the only slot is held", err)
			}
			if len(log.events) != 1 || log.events[0].Error != msgOverloaded {
				t.Fatalf("second chat events = %+v, want the Telegram overloaded text", log.events)
			}
			if got := d.activeHandlers.Load(); got != 1 {
				t.Fatalf("active_handlers after the rejected chat = %d, want 1", got)
			}

			close(m.release)
			if err := <-firstDone; !errors.Is(err, test.firstErr) {
				t.Fatalf("first answer error = %v, want %v", err, test.firstErr)
			}
			waitAdmission(t, func() bool { return d.activeHandlers.Load() == 0 })

			if err := d.AnswerForMiniApp(context.Background(), 13, 11, "третий", (&miniAppEventLog{}).emit); err != nil {
				t.Fatalf("answer after the slot was released: %v", err)
			}
			if got := m.callCount(); got != 2 {
				t.Fatalf("model calls = %d, want 2 (the failed first call and the successful third)", got)
			}
		})
	}
}
