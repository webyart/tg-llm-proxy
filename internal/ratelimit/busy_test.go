package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestBusyChats(t *testing.T) {
	locks := NewLocks()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_ = locks.WithChat(context.Background(), 42, func() error {
			close(started)
			<-release
			return nil
		})
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("chat lock did not start")
	}
	if got := locks.BusyChats(); got != 1 {
		t.Fatalf("busy=%d, want 1", got)
	}
	close(release)
	<-done
	if got := locks.BusyChats(); got != 0 {
		t.Fatalf("busy after release=%d", got)
	}
}
