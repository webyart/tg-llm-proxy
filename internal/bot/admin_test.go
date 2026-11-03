package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminRequiresDedicatedAllowlist(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11,12",
		"TELEGRAM_ADMIN_USERS":   "12",
	})
	cfg.DBPath = filepath.Join(t.TempDir(), "bot.db")
	if err := os.WriteFile(cfg.DBPath, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write stat fixture: %v", err)
	}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, nil, r, testStore(t))

	if err := dispatch(t, d, testEvent(100, 11, 5000, "/admin")); err != nil {
		t.Fatalf("non-admin /admin: %v", err)
	}
	if !strings.Contains(r.joined(), "только администраторам") || strings.Contains(r.joined(), "Размер SQLite") {
		t.Fatalf("non-admin received administrative data: %q", r.joined())
	}
	if err := dispatch(t, d, testEvent(100, 12, 5001, "/admin")); err != nil {
		t.Fatalf("admin /admin: %v", err)
	}
	for _, want := range []string{"Администрирование:", "Чатов:", "Сообщений:", "Размер SQLite:", "Модель по умолчанию:"} {
		if !strings.Contains(r.joined(), want) {
			t.Errorf("admin response misses %q: %q", want, r.joined())
		}
	}
}
