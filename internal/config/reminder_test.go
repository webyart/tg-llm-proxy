package config

import (
	"strings"
	"testing"
)

func TestReminderTimezoneLoadsLocationAndRejectsBadZone(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:secret",
		"REMINDER_TIMEZONE":  "America/New_York",
	})
	if len(problems) != 0 {
		t.Fatalf("LoadFrom: %v", problems)
	}
	if cfg.ReminderTimezone != "America/New_York" || cfg.ReminderLocation == nil || cfg.ReminderLocation.String() != "America/New_York" {
		t.Fatalf("timezone = %q location=%v", cfg.ReminderTimezone, cfg.ReminderLocation)
	}

	bad, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:secret",
		"REMINDER_TIMEZONE":  "Mars/Olympus_Mons",
	})
	if len(problems) != 1 || !strings.HasPrefix(problems[0], "ENV REMINDER_TIMEZONE") {
		t.Fatalf("bad timezone problems = %v", problems)
	}
	if bad.ReminderTimezone != "UTC" || bad.ReminderLocation == nil {
		t.Fatalf("bad timezone fallback = %q/%v", bad.ReminderTimezone, bad.ReminderLocation)
	}
}
