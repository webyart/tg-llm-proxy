package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultTimezoneLoadsAndDefaultsToYekaterinburg(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "1:secret"})
	if len(problems) != 0 {
		t.Fatalf("LoadFrom: %v", problems)
	}
	if cfg.Timezone != DefaultTimezone || cfg.TimezoneLocation == nil || cfg.TimezoneLocation.String() != DefaultTimezone {
		t.Fatalf("timezone = %q location=%v, want %s", cfg.Timezone, cfg.TimezoneLocation, DefaultTimezone)
	}
	_, offset := time.Date(2026, 11, 11, 12, 0, 0, 0, cfg.TimezoneLocation).Zone()
	if offset != 5*60*60 {
		t.Fatalf("Yekaterinburg offset = %d, want 5h", offset)
	}
}

func TestDefaultTimezoneLoadsEuropeBerlinAndHandlesDST(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:secret",
		"DEFAULT_TIMEZONE":   "Europe/Berlin",
	})
	if len(problems) != 0 {
		t.Fatalf("LoadFrom: %v", problems)
	}
	if cfg.Timezone != "Europe/Berlin" || cfg.TimezoneLocation == nil {
		t.Fatalf("timezone = %q/%v", cfg.Timezone, cfg.TimezoneLocation)
	}
	summer := time.Date(2026, 7, 1, 12, 0, 0, 0, cfg.TimezoneLocation)
	winter := time.Date(2026, 1, 15, 12, 0, 0, 0, cfg.TimezoneLocation)
	_, summerOffset := summer.Zone()
	_, winterOffset := winter.Zone()
	if summerOffset != 2*60*60 || winterOffset != 1*60*60 {
		t.Fatalf("Berlin offsets summer=%d winter=%d, want 2h/1h", summerOffset, winterOffset)
	}
}

func TestDefaultTimezoneRejectsAnInvalidZone(t *testing.T) {
	for _, invalid := range []string{"Mars/Olympus_Mons", "Local", "../etc/passwd", "/etc/passwd"} {
		cfg, problems := LoadFrom(map[string]string{
			"TELEGRAM_BOT_TOKEN": "1:secret",
			"DEFAULT_TIMEZONE":   invalid,
		})
		if len(problems) != 1 || !strings.HasPrefix(problems[0], "ENV DEFAULT_TIMEZONE") {
			t.Fatalf("%q problems = %v, want one ENV DEFAULT_TIMEZONE problem", invalid, problems)
		}
		if cfg.Timezone != DefaultTimezone || cfg.TimezoneLocation == nil || cfg.TimezoneLocation.String() != DefaultTimezone {
			t.Fatalf("%q fallback timezone = %q/%v, want %s", invalid, cfg.Timezone, cfg.TimezoneLocation, DefaultTimezone)
		}
	}
}

// TestReminderTimezoneStillSuppliesTheLegacyDefault keeps an existing deployment working: before
// DEFAULT_TIMEZONE existed, REMINDER_TIMEZONE was the only configured zone.
func TestReminderTimezoneStillSuppliesTheLegacyDefault(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:secret",
		"REMINDER_TIMEZONE":  "America/New_York",
	})
	if len(problems) != 0 {
		t.Fatalf("LoadFrom: %v", problems)
	}
	if cfg.Timezone != "America/New_York" || cfg.TimezoneLocation == nil || cfg.TimezoneLocation.String() != "America/New_York" {
		t.Fatalf("legacy timezone = %q/%v", cfg.Timezone, cfg.TimezoneLocation)
	}

	both, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:secret",
		"DEFAULT_TIMEZONE":   "Europe/Berlin",
		"REMINDER_TIMEZONE":  "America/New_York",
	})
	if len(problems) != 0 {
		t.Fatalf("LoadFrom both: %v", problems)
	}
	if both.Timezone != "Europe/Berlin" {
		t.Fatalf("DEFAULT_TIMEZONE did not win: %q", both.Timezone)
	}
}
