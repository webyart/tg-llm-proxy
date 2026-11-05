package ratelimit

import (
	"sync"
	"time"
)

type CoalesceKey struct {
	UserID int64
	ChatID int64
	Action string
}

type Coalescer struct {
	window  time.Duration
	maxKeys int
	mu      sync.Mutex
	seen    map[CoalesceKey]time.Time
	now     func() time.Time
}

func NewCoalescer(window time.Duration, maxKeys int) *Coalescer {
	if maxKeys <= 0 {
		maxKeys = 1
	}
	return &Coalescer{window: window, maxKeys: maxKeys, seen: map[CoalesceKey]time.Time{}, now: time.Now}
}

func (c *Coalescer) WithClock(now func() time.Time) *Coalescer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
	return c
}

func (c *Coalescer) Allow(key CoalesceKey) bool {
	if c.window <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if last, ok := c.seen[key]; ok && now.Sub(last) < c.window {
		return false
	}
	if _, ok := c.seen[key]; !ok && len(c.seen) >= c.maxKeys {
		for candidate, last := range c.seen {
			if now.Sub(last) >= c.window {
				delete(c.seen, candidate)
			}
		}
		if len(c.seen) >= c.maxKeys {
			return false
		}
	}
	c.seen[key] = now
	return true
}

func (c *Coalescer) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}
