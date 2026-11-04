package bot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdminShowsActiveModelCallsAndBusyChats(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TELEGRAM_ADMIN_USERS": "11"})
	cfg.DBPath = filepath.Join(t.TempDir(), "bot.db")
	if err := os.WriteFile(cfg.DBPath, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("stat fixture: %v", err)
	}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, nil, r, testStore(t))
	d.activeModelRequests.Add(2)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_ = d.deps.Locks.WithChat(context.Background(), 201, func() error {
			close(started)
			<-release
			return nil
		})
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("lock fixture did not start")
	}
	if err := dispatch(t, d, testEvent(200, 11, 1800, "/admin")); err != nil {
		t.Fatalf("admin: %v", err)
	}
	if !strings.Contains(r.joined(), "Активных model calls: 2") || !strings.Contains(r.joined(), "чатов с lock: 2") {
		t.Fatalf("admin response=%q", r.joined())
	}
	close(release)
	<-done
	d.activeModelRequests.Add(-2)
}
