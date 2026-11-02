// Two measured facts shape this package:
//
//	answer "allowed" without touching its map — otherwise a configured-off limit would still
//	allocate state per user.
//	TG_REPLY_DEADLINE_SECONDS, and a lock that ignores ctx would keep a goroutine parked after
//	size 1 selected together with ctx.Done(), which also means nothing is ever sent into the
//	channel when ctx wins — no lost slot, no permanently locked chat.
package ratelimit

import (
	"context"
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

// and reused, so the answers of one chat never interleave while different chats run in parallel
type Locks struct {
	mu    sync.Mutex
	chats map[int64]chan struct{}
}

// NewLocks — an empty set of per-chat locks.
func NewLocks() *Locks {
	return &Locks{chats: map[int64]chan struct{}{}}
}

// forChat — the lock channel of one chat, created on the first use.
func (l *Locks) forChat(chatID int64) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	var ch, ok = l.chats[chatID]
	if !ok {
		ch = make(chan struct{}, 1)
		l.chats[chatID] = ch
	}
	return ch
}

// WithChat — runs fn while the chat is held. ctx cancels the WAIT, never an acquired run: the
func (l *Locks) WithChat(ctx context.Context, chatID int64, fn func() error) error {
	var ch = l.forChat(chatID)
	select {
	case ch <- struct{}{}:
		defer func() { <-ch }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return fn()
}

// Held — reports whether some goroutine currently owns the chat lock (the concurrency test of
func (l *Locks) Held(chatID int64) bool {
	var ch = l.forChat(chatID)
	select {
	case ch <- struct{}{}:
		<-ch
		return false
	default:
		return true
	}
}
