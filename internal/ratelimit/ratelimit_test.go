// ratelimit_test.go — the §11 unit level for the two guards of §12: the token bucket of
// RATE_LIMIT_PER_MINUTE and the per-chat lock. The clock is injected (§11 "rate limit" must not
// sleep a real minute), and the lock tests assert the ORDER, which is what §10 День 6 DoD
// ("нет перепутанных ответов") actually depends on.
package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock — a clock the test moves by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestAllowUpToLimitThenBlocks(t *testing.T) {
	var clock = &fakeClock{t: time.Unix(1700000000, 0)}
	var l = New(3).WithClock(clock.Now)

	if !l.Allow(1) || !l.Allow(1) || !l.Allow(1) {
		t.Fatalf("the first three answers of the window must pass (§7 RATE_LIMIT_PER_MINUTE=3)")
	}
	if l.Allow(1) {
		t.Fatalf("the fourth answer inside one window must be refused (§12 reliability)")
	}
	var wait = l.RetryAfter(1)
	if wait <= 0 || wait > Window {
		t.Fatalf("RetryAfter must name the wait inside the window, got %v", wait)
	}
	if got := l.Count(1); got != 3 {
		t.Fatalf("Count must report the hits of the window, got %d want 3", got)
	}
}

func TestWindowExpires(t *testing.T) {
	var clock = &fakeClock{t: time.Unix(1700000000, 0)}
	var l = New(2).WithClock(clock.Now)

	if !l.Allow(7) {
		t.Fatalf("the first answer must pass")
	}
	clock.Advance(30 * time.Second)
	if !l.Allow(7) {
		t.Fatalf("the second answer inside the window must pass")
	}
	if l.Allow(7) {
		t.Fatalf("the third answer inside one window must be refused")
	}

	// The oldest hit expires at exactly t+60s (live() keeps a hit while its age is < Window), so
	// one minute minus a second still holds both, and t+61s keeps one.
	clock.Advance(29 * time.Second)
	if got := l.Count(7); got != 2 {
		t.Fatalf("both hits are still inside the window at t+59s, got %d want 2", got)
	}
	clock.Advance(2 * time.Second)
	if got := l.Count(7); got != 1 {
		t.Fatalf("the hit at t must be out of the window at t+62s, got %d want 1", got)
	}
	if !l.Allow(7) {
		t.Fatalf("after the oldest hit expired a new answer must pass (§7 per-minute window)")
	}
}

func TestZeroMeansUnlimited(t *testing.T) {
	var clock = &fakeClock{t: time.Unix(1700000000, 0)}
	var l = New(0).WithClock(clock.Now)

	var i = 0
	for i < 1000 {
		if !l.Allow(42) {
			t.Fatalf("RATE_LIMIT_PER_MINUTE=0 means no limit (§7), the %d-th answer was refused", i)
		}
		i++
	}
	if wait := l.RetryAfter(42); wait != 0 {
		t.Fatalf("an unlimited limiter must not ask anyone to wait, got %v", wait)
	}
}

func TestUsersAreIndependent(t *testing.T) {
	var clock = &fakeClock{t: time.Unix(1700000000, 0)}
	var l = New(1).WithClock(clock.Now)

	if !l.Allow(1) {
		t.Fatalf("the first user must get their first answer")
	}
	if l.Allow(1) {
		t.Fatalf("the same user must be limited by their own window")
	}
	if !l.Allow(2) {
		t.Fatalf("another user has their own window (§12: the limit is per user, not global)")
	}
}

func TestConcurrentAllowIsNotOverTheLimit(t *testing.T) {
	var clock = &fakeClock{t: time.Unix(1700000000, 0)}
	var l = New(5).WithClock(clock.Now)

	var wg sync.WaitGroup
	var passed int32
	var i = 0
	for i < 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow(9) {
				atomic.AddInt32(&passed, 1)
			}
		}()
		i++
	}
	wg.Wait()
	if passed != 5 {
		t.Fatalf("exactly the limit may pass under concurrency, got %d want 5 (§12 reliability)", passed)
	}
}

func TestWithChatKeepsOneChatSerialized(t *testing.T) {
	var locks = NewLocks()
	var inside int32
	var maxInside int32
	var order = []string{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	var run = func(label string, wait time.Duration) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err = locks.WithChat(context.Background(), 100, func() error {
				var now = atomic.AddInt32(&inside, 1)
				for {
					var prev = atomic.LoadInt32(&maxInside)
					if now <= prev || atomic.CompareAndSwapInt32(&maxInside, prev, now) {
						break
					}
				}
				time.Sleep(wait)
				mu.Lock()
				order = append(order, label)
				mu.Unlock()
				atomic.AddInt32(&inside, -1)
				return nil
			})
			if err != nil {
				t.Errorf("WithChat: %v", err)
			}
		}()
	}

	run("long", 120*time.Millisecond)
	time.Sleep(20 * time.Millisecond) // the second request queues behind the first
	run("short", 10*time.Millisecond)
	wg.Wait()

	if maxInside != 1 {
		t.Fatalf("two runs of one chat must never overlap, the peak was %d (§12 per-chat lock)", maxInside)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "long" || order[1] != "short" {
		t.Fatalf("the answers of one chat must stay in arrival order, got %v (§10 День 6 DoD)", order)
	}
}

func TestDifferentChatsRunInParallel(t *testing.T) {
	var locks = NewLocks()
	var arrive = sync.WaitGroup{}
	arrive.Add(2)
	var hold = sync.WaitGroup{}
	hold.Add(2)
	var wg sync.WaitGroup

	var run = func(chatID int64) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err = locks.WithChat(context.Background(), chatID, func() error {
				arrive.Done()
				arrive.Wait() // both chats reach their own lock at the same moment
				hold.Done()
				hold.Wait() // and keep it while the other chat is inside as well
				return nil
			})
			if err != nil {
				t.Errorf("WithChat chat %d: %v", chatID, err)
			}
		}()
	}

	run(1)
	run(2)

	var done = make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("two different chats must not block each other (§12 performance)")
	}
}

func TestWithChatCancelDoesNotLockTheChatForever(t *testing.T) {
	var locks = NewLocks()
	var release = make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = locks.WithChat(context.Background(), 55, func() error {
			<-release
			return nil
		})
	}()

	for !locks.Held(55) {
		time.Sleep(5 * time.Millisecond)
	}
	var ctx, cancel = context.WithCancel(context.Background())
	cancel()
	var err = locks.WithChat(ctx, 55, func() error {
		t.Error("a cancelled waiter must not run its fn")
		return nil
	})
	if err != context.Canceled {
		t.Fatalf("a cancelled wait must return context.Canceled, got %v (§12.1.4 MsgStuckShutdown)", err)
	}

	close(release)
	wg.Wait()
	if locks.Held(55) {
		t.Fatalf("a cancelled wait must not leave the chat locked forever (§10 День 3 budget must free the queue)")
	}
	if err := locks.WithChat(context.Background(), 55, func() error { return nil }); err != nil {
		t.Fatalf("the chat must be usable again after the release, got %v", err)
	}
}
