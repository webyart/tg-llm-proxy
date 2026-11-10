package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"bot/internal/testutil"
)

func TestCoalescerWindowsAndKeyBound(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1700000000, 0)}
	coalescer := NewCoalescer(time.Minute, 2).WithClock(clock.Now)
	first := CoalesceKey{UserID: 1, ChatID: 10, Action: "denied"}
	second := CoalesceKey{UserID: 2, ChatID: 20, Action: "denied"}
	third := CoalesceKey{UserID: 3, ChatID: 30, Action: "denied"}
	if !coalescer.Allow(first) || coalescer.Allow(first) {
		t.Fatal("a key must pass once and then coalesce inside its window")
	}
	if !coalescer.Allow(second) || coalescer.Allow(third) {
		t.Fatal("a full coalescer must reject new state instead of growing without bound")
	}
	if got := coalescer.Len(); got != 2 {
		t.Fatalf("coalescer keys = %d, want 2", got)
	}
	clock.Advance(time.Minute)
	if !coalescer.Allow(third) {
		t.Fatal("an expired window must make room for a new key")
	}
}

func TestWithChatBoundedRejectsExcessAndRunsAcceptedFIFO(t *testing.T) {
	locks := NewLocks()
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	order := []int{}
	run := func(id int, block bool) {
		if err := locks.WithChatBounded(context.Background(), 42, 2, func() error {
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
			if block {
				close(started)
				<-release
			}
			return nil
		}); err != nil {
			t.Errorf("accepted WithChatBounded(%d): %v", id, err)
		}
	}
	go run(1, true)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first chat handler did not start")
	}
	var wg sync.WaitGroup
	for id := 2; id <= 3; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			run(id, false)
		}(id)
		testutil.Eventually(t, time.Second, func() bool { return locks.Waiting(42) == id-1 },
			func() string { return fmt.Sprintf("waiter %d was not queued", id) })
	}
	if err := locks.WithChatBounded(context.Background(), 42, 2, func() error {
		t.Fatal("an over-capacity waiter must not run")
		return nil
	}); !errors.Is(err, ErrChatQueueFull) {
		t.Fatalf("over-capacity error = %v, want ErrChatQueueFull", err)
	}
	close(release)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("accepted chat handlers did not finish within 1s")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Fatalf("FIFO order = %v, want [1 2 3]", order)
	}
}
