package timefmt

import (
	"testing"
	"time"
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return location
}

func TestDurationBoundariesRUAndEN(t *testing.T) {
	cases := []struct {
		name string
		date time.Duration
		ru   string
		en   string
	}{
		{name: "zero", date: 0, ru: "0:00", en: "0:00"},
		{name: "negative", date: -time.Second, ru: "0:00", en: "0:00"},
		{name: "59 seconds", date: 59 * time.Second, ru: "~<1 мин", en: "~<1 min"},
		{name: "one minute", date: time.Minute, ru: "1:00", en: "1:00"},
		{name: "59 minutes", date: 59 * time.Minute, ru: "59:00", en: "59:00"},
		{name: "23 hours", date: 23 * time.Hour, ru: "23 ч", en: "23 h"},
		{name: "hour and minutes", date: 2*time.Hour + 5*time.Minute, ru: "2 ч 5 мин", en: "2 h 5 min"},
		{name: "six minutes twelve", date: 6*time.Minute + 12*time.Second, ru: "6:12", en: "6:12"},
	}
	for _, test := range cases {
		if got := Duration(test.date, RU); got != test.ru {
			t.Errorf("%s: RU Duration = %q, want %q", test.name, got, test.ru)
		}
		if got := Duration(test.date, EN); got != test.en {
			t.Errorf("%s: EN Duration = %q, want %q", test.name, got, test.en)
		}
	}
}

func TestETABoundariesRUAndEN(t *testing.T) {
	cases := []struct {
		date time.Duration
		ru   string
		en   string
	}{
		{date: 0, ru: "—", en: "—"},
		{date: -time.Minute, ru: "—", en: "—"},
		{date: 30 * time.Second, ru: "~<1 мин", en: "~<1 min"},
		{date: 7 * time.Minute, ru: "~7 мин", en: "~7 min"},
		{date: time.Hour + 59*time.Minute + 40*time.Second, ru: "~2 ч", en: "~2 h"},
		{date: 2*time.Hour + 5*time.Minute, ru: "~2 ч 5 мин", en: "~2 h 5 min"},
	}
	for _, test := range cases {
		if got := ETA(test.date, RU); got != test.ru {
			t.Errorf("RU ETA(%s) = %q, want %q", test.date, got, test.ru)
		}
		if got := ETA(test.date, EN); got != test.en {
			t.Errorf("EN ETA(%s) = %q, want %q", test.date, got, test.en)
		}
	}
}

func TestRelativeBoundariesRUAndEN(t *testing.T) {
	location := mustLocation(t, "Asia/Yekaterinburg")
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, location)
	cases := []struct {
		name string
		at   time.Time
		ru   string
		en   string
	}{
		{name: "now", at: now, ru: "только что", en: "just now"},
		{name: "future 12 min", at: now.Add(12 * time.Minute), ru: "через 12 мин", en: "in 12 min"},
		{name: "past 2h5m", at: now.Add(-2*time.Hour - 5*time.Minute), ru: "2 ч 5 мин назад", en: "2 h 5 min ago"},
		{name: "yesterday evening", at: time.Date(2026, 11, 10, 18, 40, 0, 0, location), ru: "вчера 18:40", en: "yesterday 18:40"},
		{name: "tomorrow morning", at: time.Date(2026, 11, 12, 9, 0, 0, 0, location), ru: "завтра 09:00", en: "tomorrow 09:00"},
		{name: "three days ago", at: now.AddDate(0, 0, -3), ru: "3 дня назад", en: "3 days ago"},
		{name: "five days ahead", at: now.AddDate(0, 0, 5), ru: "через 5 дней", en: "in 5 days"},
	}
	for _, test := range cases {
		if got := Relative(test.at, now, location, RU); got != test.ru {
			t.Errorf("%s: RU Relative = %q, want %q", test.name, got, test.ru)
		}
		if got := Relative(test.at, now, location, EN); got != test.en {
			t.Errorf("%s: EN Relative = %q, want %q", test.name, got, test.en)
		}
	}
	if got := Relative(now.Add(59*time.Second), now, location, RU); got != "через <1 мин" {
		t.Errorf("sub-minute future = %q", got)
	}
	if got := Relative(now.Add(-11*24*time.Hour), now, location, RU); got != DateTime(now.Add(-11*24*time.Hour), location, RU) {
		t.Errorf("old timestamp = %q, want the absolute form", got)
	}
	if got := Relative(time.Time{}, now, location, RU); got != "" {
		t.Errorf("zero time = %q, want empty", got)
	}
}

func TestPluralRU(t *testing.T) {
	cases := []struct {
		value int64
		want  string
	}{
		{1, "1 день"}, {2, "2 дня"}, {4, "4 дня"}, {5, "5 дней"}, {11, "11 дней"},
		{12, "12 дней"}, {14, "14 дней"}, {21, "21 день"}, {22, "22 дня"}, {25, "25 дней"}, {0, "0 дней"},
	}
	for _, test := range cases {
		if got := PluralRU(test.value, "день", "дня", "дней"); got != test.want {
			t.Errorf("PluralRU(%d) = %q, want %q", test.value, got, test.want)
		}
	}
}

// TestDateTimeInChatZone is the acceptance example of the backlog: one UTC instant is 11 ноя, 02:00 in
// Yekaterinburg and 21:00 in UTC. Storage never changes; only the rendering location does.
func TestDateTimeInChatZone(t *testing.T) {
	instant := time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC)
	yekaterinburg := mustLocation(t, "Asia/Yekaterinburg")
	if got := DateTime(instant, yekaterinburg, RU); got != "11 ноя, 02:00" {
		t.Errorf("RU Yekaterinburg = %q, want 11 ноя, 02:00", got)
	}
	if got := DateTime(instant, time.UTC, EN); got != "10 Nov, 21:00" {
		t.Errorf("EN UTC = %q, want 10 Nov, 21:00", got)
	}
	if got := DateTimeZoned(instant, yekaterinburg, RU); got != "11 ноя, 02:00 YEKT" {
		t.Errorf("zoned = %q, want 11 ноя, 02:00 YEKT", got)
	}
	if got := Date(instant, yekaterinburg, RU); got != "11 ноя" {
		t.Errorf("date = %q, want 11 ноя", got)
	}
	if got := Clock(instant, yekaterinburg); got != "02:00" {
		t.Errorf("clock = %q, want 02:00", got)
	}
	if got := DateTimeZoned(instant, time.UTC, EN); got != "10 Nov, 21:00 UTC" {
		t.Errorf("EN zoned UTC = %q", got)
	}
}

func TestDateTimeAcrossDSTEuropeBerlin(t *testing.T) {
	berlin := mustLocation(t, "Europe/Berlin")
	summer := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	winter := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	if got := DateTimeZoned(summer, berlin, RU); got != "1 июл, 14:00 CEST" {
		t.Errorf("summer = %q, want 1 июл, 14:00 CEST", got)
	}
	if got := DateTimeZoned(winter, berlin, RU); got != "15 янв, 13:00 CET" {
		t.Errorf("winter = %q, want 15 янв, 13:00 CET", got)
	}
	// The spring-forward night: 01:30 UTC is 03:30 CEST, so «вчера» must not appear for the same
	// calendar day in Berlin.
	now := time.Date(2026, 3, 29, 4, 0, 0, 0, berlin)
	at := time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC)
	if got := Relative(at, now, berlin, RU); got != "30 мин назад" {
		t.Errorf("DST relative = %q, want 30 мин назад", got)
	}
}

func TestParseStoredTimestamps(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Time
	}{
		{raw: "2026-11-10T21:00:00Z", want: time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC)},
		{raw: "2026-11-10T21:00:00.112588140Z", want: time.Date(2026, 11, 10, 21, 0, 0, 112588140, time.UTC)},
		{raw: "2026-11-10 21:00:00", want: time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC)},
		{raw: "2026-11-10", want: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)},
	}
	for _, test := range cases {
		got, ok := Parse(test.raw)
		if !ok || !got.Equal(test.want) {
			t.Errorf("Parse(%q) = %s ok=%v, want %s", test.raw, got, ok, test.want)
		}
	}
	for _, raw := range []string{"", "not-a-time", "11.11.2026"} {
		if _, ok := Parse(raw); ok {
			t.Errorf("Parse(%q) accepted a malformed value", raw)
		}
	}
}
