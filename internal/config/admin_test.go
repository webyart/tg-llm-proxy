package config

import "testing"

func TestAdminAllowlistIsDedicated(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":     "123456:fake",
		"TELEGRAM_ALLOWED_USERS": "11,12",
		"TELEGRAM_ADMIN_USERS":   "12,22",
	})
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if cfg.IsAdmin(11) || !cfg.IsAdmin(12) || !cfg.IsAdmin(22) || cfg.IsAdmin(99) {
		t.Fatalf("admin allowlist is wrong: %+v", cfg.AdminIDs)
	}
}

func TestAdminAllowlistDefaultsClosed(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123456:fake"})
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(cfg.AdminIDs) != 0 || cfg.IsAdmin(11) {
		t.Fatal("empty admin allowlist must deny everyone")
	}
}
