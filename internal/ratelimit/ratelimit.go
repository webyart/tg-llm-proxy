// Package ratelimit — the two §12 performance/reliability guards of §4: the token bucket of
// RATE_LIMIT_PER_MINUTE per user (§7 row) and the per-chat lock that keeps the answers of one chat
// in order (§12 «per-chat lock гарантирует порядок ответов в одном чате»).
//
// Two measured facts shape this package:
//  1. RATE_LIMIT_PER_MINUTE=0 means NO limit (§7 row comment "0 = no limit"), so the limiter must
//     answer "allowed" without touching its map — otherwise a configured-off limit would still
//     allocate state per user.
//  2. A per-chat lock has to be cancellable: the reply of §10 День 4 runs inside
//     TG_REPLY_DEADLINE_SECONDS, and a lock that ignores ctx would keep a goroutine parked after
//     the budget expired (§12.1.4 MsgStuckShutdown). The lock is therefore a buffered channel of
//     size 1 selected together with ctx.Done(), which also means nothing is ever sent into the
//     channel when ctx wins — no lost slot, no permanently locked chat.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Window — the §7 window of RATE_LIMIT_PER_MINUTE, one minute.
const Window = time.Minute

// Limiter — a sliding-window counter per user (§12 reliability: one user must not exhaust the GPU).
// The clock is injectable so the tests of §11 verify the expiry without sleeping a minute.
type Limiter struct {
	perMinute int
	mu        sync.Mutex
	hits      map[int64][]time.Time
	now       func() time.Time
}

// New — the limiter of §7. perMinute <= 0 means "no limit" (§7: 0 = no limit).
func New(perMinute int) *Limiter {
	return &Limiter{perMinute: perMinute, hits: map[int64][]time.Time{}, now: time.Now}
}

// WithClock — the test seam for the window expiry (§11 unit level: "rate limit").
func (l *Limiter) WithClock(now func() time.Time) *Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
	return l
}

// PerMinute — the configured limit, 0 meaning unlimited.
func (l *Limiter) PerMinute() int { return l.perMinute }

// Allow — records one answer for the user and reports whether it fits into the window (§7).
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

// RetryAfter — how long the user has to wait for the next free slot (§12 UX: the refusal says the
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

// Count — the hits currently inside the window (the /stats view of §6 needs no more).
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

// Locks — the per-chat locks of §12. A chat gets its own buffered channel of size 1, created once
// and reused, so the answers of one chat never interleave while different chats run in parallel
// (§10 День 6 "нет перепутанных ответов", §12 производительность).
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
// budget of §10 День 3 ends the queueing, the answer that already started still finishes and is
// written once (§10 День 3 write invariant).
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
// §10 День 6 checks the queue through this).
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
