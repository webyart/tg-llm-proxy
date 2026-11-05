// Two measured facts shape this package:
//
//	answer "allowed" without touching its map — otherwise a configured-off limit would still
//	allocate state per user.
//	TG_REPLY_DEADLINE_SECONDS, and a lock that ignores ctx would keep a goroutine parked after
//	the reply budget expired. Chat waiters are explicitly queued, cancellable, and removed when idle.
package ratelimit

import (
	"context"
	"errors"
	"sync"
	"time"
)

const Window = time.Minute

type Limiter struct {
	perMinute int
	mu        sync.Mutex
	hits      map[int64][]time.Time
	now       func() time.Time
}

func New(perMinute int) *Limiter {
	return &Limiter{perMinute: perMinute, hits: map[int64][]time.Time{}, now: time.Now}
}

func (l *Limiter) WithClock(now func() time.Time) *Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
	return l
}

// PerMinute — the configured limit, 0 meaning unlimited.
func (l *Limiter) PerMinute() int { return l.perMinute }

func (l *Limiter) Allow(userID int64) bool {
	if l.perMinute <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var moment = l.now()
	l.hits[userID] = l.live(l.hits[userID], moment)
	if len(l.hits[userID]) >= l.perMinute {
		return false
	}
	l.hits[userID] = append(l.hits[userID], moment)
	return true
}

// wait instead of silently dropping the message). Zero means "allowed now".
func (l *Limiter) RetryAfter(userID int64) time.Duration {
	if l.perMinute <= 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var moment = l.now()
	var live = l.live(l.hits[userID], moment)
	if len(live) < l.perMinute {
		return 0
	}
	return live[0].Add(Window).Sub(moment)
}

func (l *Limiter) Count(userID int64) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.live(l.hits[userID], l.now()))
}

// live — the timestamps still inside the window, oldest first. The caller holds the lock.
func (l *Limiter) live(hits []time.Time, moment time.Time) []time.Time {
	var i = 0
	for i < len(hits) {
		if moment.Sub(hits[i]) < Window {
			break
		}
		i++
	}
	return hits[i:]
}

var ErrChatQueueFull = errors.New("ratelimit: per-chat queue is full")

type chatWaiter struct {
	ready chan struct{}
}

type chatLock struct {
	held    bool
	waiters []*chatWaiter
}

type Locks struct {
	mu    sync.Mutex
	chats map[int64]*chatLock
}

// NewLocks — an empty set of per-chat locks.
func NewLocks() *Locks {
	return &Locks{chats: map[int64]*chatLock{}}
}

func (l *Locks) WithChat(ctx context.Context, chatID int64, fn func() error) error {
	return l.WithChatBounded(ctx, chatID, 0, fn)
}

// WithChatBounded rejects a new waiter once maxWaiting waiters are already queued.
// A zero maxWaiting retains the unbounded waiting behavior used by legacy internal callers.
func (l *Locks) WithChatBounded(ctx context.Context, chatID int64, maxWaiting int, fn func() error) error {
	l.mu.Lock()
	chat := l.chats[chatID]
	if chat == nil {
		chat = &chatLock{}
		l.chats[chatID] = chat
	}
	if !chat.held {
		chat.held = true
		l.mu.Unlock()
		defer l.release(chatID)
		return fn()
	}
	if maxWaiting > 0 && len(chat.waiters) >= maxWaiting {
		l.mu.Unlock()
		return ErrChatQueueFull
	}
	waiter := &chatWaiter{ready: make(chan struct{})}
	chat.waiters = append(chat.waiters, waiter)
	l.mu.Unlock()

	select {
	case <-waiter.ready:
		defer l.release(chatID)
		return fn()
	case <-ctx.Done():
		l.mu.Lock()
		chat = l.chats[chatID]
		for i, queued := range chat.waiters {
			if queued != waiter {
				continue
			}
			chat.waiters = append(chat.waiters[:i], chat.waiters[i+1:]...)
			l.mu.Unlock()
			return ctx.Err()
		}
		l.mu.Unlock()
		l.release(chatID)
		return ctx.Err()
	}
}

func (l *Locks) release(chatID int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	chat := l.chats[chatID]
	if len(chat.waiters) == 0 {
		delete(l.chats, chatID)
		return
	}
	waiter := chat.waiters[0]
	chat.waiters = chat.waiters[1:]
	close(waiter.ready)
}

// Held — reports whether some goroutine currently owns the chat lock (the concurrency test of
func (l *Locks) BusyChats() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	busy := 0
	for _, chat := range l.chats {
		if chat.held {
			busy++
		}
	}
	return busy
}

func (l *Locks) Held(chatID int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	chat := l.chats[chatID]
	return chat != nil && chat.held
}

func (l *Locks) Waiting(chatID int64) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	chat := l.chats[chatID]
	if chat == nil {
		return 0
	}
	return len(chat.waiters)
}
