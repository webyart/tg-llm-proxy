package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"bot/internal/store"
)

func TestParseReminderDurationsAndValidation(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, test := range []struct {
		input string
		delay time.Duration
	}{
		{"через 1 секунду test", time.Second},
		{"через 2 секунды test", 2 * time.Second},
		{"через 1 минуту test", time.Minute},
		{"через 2 часа test", 2 * time.Hour},
		{"через 3 дня test", 72 * time.Hour},
	} {
		reminder, err := ParseReminder(test.input, now, time.UTC)
		if err != nil || reminder.Cadence != store.ReminderCadenceOnce || !reminder.NextRunAt.Equal(now.Add(test.delay)) || reminder.Text != "test" {
			t.Errorf("ParseReminder(%q)=%+v err=%v", test.input, reminder, err)
		}
	}
	long, err := ParseReminder("через 1 секунду "+strings.Repeat("я", 500), now, time.UTC)
	if err != nil || len([]rune(long.Text)) != 500 {
		t.Fatalf("500-rune text err=%v", err)
	}
	for _, input := range []string{
		"", "через 1 минуту", "через 0 минут test", "через -1 час test", "через 1 неделю test",
		"через 999999999 дней test", "каждую неделю 09:00 test", "каждый день 9:00 test", "каждый день 24:00 test",
		"через 1 секунду " + strings.Repeat("я", 501), "через 1 секунду a\x00b",
	} {
		if _, err := ParseReminder(input, now, time.UTC); err == nil {
			t.Errorf("ParseReminder(%q) succeeded", input)
		}
	}
}

func TestDailyReminderUsesWallClockAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, location)
	first, err := NextDailyReminder(now, "09:00", location)
	if err != nil {
		t.Fatal(err)
	}
	if got := first.In(location).Format("2006-01-02 15:04 MST"); got != "2026-03-07 09:00 EST" {
		t.Fatalf("first=%s", got)
	}
	second, err := AdvanceDailyReminder(first, first, "09:00", location)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.In(location).Format("2006-01-02 15:04 MST"); got != "2026-03-08 09:00 EDT" || second.Sub(first) != 23*time.Hour {
		t.Fatalf("spring next=%s delta=%s", got, second.Sub(first))
	}

	fall := time.Date(2026, 10, 31, 9, 0, 0, 0, location)
	next, err := AdvanceDailyReminder(fall, fall, "09:00", location)
	if err != nil {
		t.Fatal(err)
	}
	if got := next.In(location).Format("2006-01-02 15:04 MST"); got != "2026-11-01 09:00 EST" || next.Sub(fall) != 25*time.Hour {
		t.Fatalf("fall next=%s delta=%s", got, next.Sub(fall))
	}

	late := first.Add(48 * time.Hour)
	catchUp, err := AdvanceDailyReminder(first, late, "09:00", location)
	if err != nil || !catchUp.After(late) || catchUp.In(location).Hour() != 9 {
		t.Fatalf("catch-up=%s err=%v", catchUp, err)
	}
}

func TestReminderCommandsMenuIsolationAndLimit(t *testing.T) {
	r := &menuEditReplier{}
	m := &fakeModel{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12", "REMINDER_TIMEZONE": "UTC"}), m, r, db)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	d.now = func() time.Time { return now }
	if err := dispatch(t, d, testEvent(100, 11, 500, "/remind через 1 минуту купить молоко")); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 501, "/remind каждый день 09:00 зарядка")); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListReminders(context.Background(), db, 100, 20)
	if err != nil || len(rows) != 2 || !rows[0].NextRunAt.Equal(now.Add(time.Minute)) || rows[1].DailyTime != "09:00" {
		t.Fatalf("created=%+v err=%v", rows, err)
	}
	if m.callCount() != 0 {
		t.Fatal("reminder commands called the model")
	}
	if err := dispatch(t, d, testEvent(100, 11, 502, "/reminders")); err != nil {
		t.Fatal(err)
	}
	lastMenu := r.menus[len(r.menus)-1]
	cancelData := fmt.Sprintf("menu:reminder:cancel:%d", rows[0].ID)
	if !hasMenuCallback(lastMenu, cancelData) || !hasMenuCallback(lastMenu, "menu:more") {
		t.Fatalf("reminder list markup=%+v", lastMenu)
	}
	if err := dispatch(t, d, testEvent(101, 11, 503, fmt.Sprintf("/reminders cancel %d", rows[0].ID))); err != nil {
		t.Fatal(err)
	}
	if left, _ := store.ListReminders(context.Background(), db, 100, 20); len(left) != 2 {
		t.Fatal("cross-chat cancellation removed a reminder")
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, cancelData); err != nil {
		t.Fatal(err)
	}
	if left, _ := store.ListReminders(context.Background(), db, 100, 20); len(left) != 1 {
		t.Fatal("inline cancellation did not remove exactly one reminder")
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, cancelData); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "истекло") {
		t.Fatalf("stale inline cancellation=%q", r.lastEdit().text)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:more"); err != nil {
		t.Fatal(err)
	}
	if !hasMenuCallback(r.lastEdit().markup, "menu:reminders") {
		t.Fatal("More menu has no reminders leaf")
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:reminders"); err != nil {
		t.Fatal(err)
	}
	if !hasMenuCallback(r.lastEdit().markup, "menu:more") {
		t.Fatal("reminders leaf has no Back navigation")
	}
	for _, invalid := range []string{"menu:reminder:cancel:0", "menu:reminder:cancel:-1", "menu:reminder:cancel:text", "menu:reminder:cancel:999999999999999999999999"} {
		if _, ok := d.callbackText(invalid); ok {
			t.Errorf("unsafe callback accepted %q", invalid)
		}
	}

	for i := 0; i < store.ReminderMaxActivePerChat-1; i++ {
		if _, err := store.CreateReminder(context.Background(), db, store.Reminder{ChatID: 100, UserID: 11, Text: "limit", Cadence: store.ReminderCadenceOnce, NextRunAt: now.Add(time.Duration(i+2) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := dispatch(t, d, testEvent(100, 11, 504, "/remind через 1 минуту лишнее")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.joined(), "20 активных") {
		t.Fatalf("limit reply=%q", r.joined())
	}
}

func TestReminderSchedulerRetryDeliveryAndDailyAdvance(t *testing.T) {
	r := &fakeReplier{err: errors.New("temporary")}
	m := &fakeModel{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.EnsureChat(context.Background(), db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	once, err := store.CreateReminder(context.Background(), db, store.Reminder{ChatID: 100, UserID: 11, Text: "доставить", Cadence: store.ReminderCadenceOnce, NextRunAt: now})
	if err != nil {
		t.Fatal(err)
	}
	daily, err := store.CreateReminder(context.Background(), db, store.Reminder{ChatID: 100, UserID: 11, Text: "ежедневно", Cadence: store.ReminderCadenceDaily, DailyTime: "09:00", NextRunAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ProcessDueReminders(context.Background(), now, 20); err != nil {
		t.Fatal(err)
	}
	if r.count() != 0 {
		t.Fatal("failed sends reached the transcript")
	}
	rows, _ := store.ListReminders(context.Background(), db, 100, 20)
	if len(rows) != 2 || rows[0].Attempts != 1 || !rows[0].NextRunAt.Equal(now.Add(2*time.Second)) {
		t.Fatalf("backoff rows=%+v", rows)
	}
	if processed, err := d.ProcessDueReminders(context.Background(), now, 20); err != nil || processed != 0 {
		t.Fatalf("retry ran before backoff processed=%d err=%v", processed, err)
	}
	r.mu.Lock()
	r.err = nil
	r.mu.Unlock()
	if _, err := d.ProcessDueReminders(context.Background(), now.Add(2*time.Second), 20); err != nil {
		t.Fatal(err)
	}
	if r.count() != 2 || !strings.Contains(r.joined(), "Напоминание: доставить") || !strings.Contains(r.joined(), "Напоминание: ежедневно") {
		t.Fatalf("deliveries=%q", r.joined())
	}
	rows, _ = store.ListReminders(context.Background(), db, 100, 20)
	if len(rows) != 1 || rows[0].ID != daily.ID || !rows[0].NextRunAt.Equal(time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)) || rows[0].Attempts != 0 {
		t.Fatalf("daily rows=%+v", rows)
	}
	if _, err := d.ProcessDueReminders(context.Background(), now.Add(3*time.Second), 20); err != nil {
		t.Fatal(err)
	}
	if r.count() != 2 || m.callCount() != 0 {
		t.Fatalf("duplicate or model call sends=%d model=%d once=%d", r.count(), m.callCount(), once.ID)
	}
	for attempt, want := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second} {
		if got := ReminderRetryDelay(attempt + 1); got != want {
			t.Errorf("delay(%d)=%s want=%s", attempt+1, got, want)
		}
	}
}

func TestReminderSchedulerExhaustionAndDeniedCancellation(t *testing.T) {
	for _, test := range []struct {
		name    string
		allowed string
		want    string
	}{
		{"exhausted", "11", store.ReminderStatusFailed},
		{"denied", "12", store.ReminderStatusCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &fakeReplier{err: errors.New("offline")}
			db := testStoreNamed(t, t.Name())
			d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": test.allowed}), nil, r, db)
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := store.EnsureChat(context.Background(), db, 100, 11, ""); err != nil {
				t.Fatal(err)
			}
			created, err := store.CreateReminder(context.Background(), db, store.Reminder{ChatID: 100, UserID: 11, Text: "secret", Cadence: store.ReminderCadenceOnce, NextRunAt: now})
			if err != nil {
				t.Fatal(err)
			}
			attempts := 1
			if test.name == "exhausted" {
				attempts = ReminderMaxSendAttempts
			}
			for attempt := 0; attempt < attempts; attempt++ {
				at := now
				if attempt > 0 {
					at = rowsNextRetryAt(t, db, created.ID)
				}
				if _, err := d.ProcessDueReminders(context.Background(), at, 20); err != nil {
					t.Fatal(err)
				}
			}
			var status string
			if err := db.QueryRowContext(context.Background(), `SELECT status FROM reminders WHERE id = ?`, created.ID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != test.want || r.count() != 0 {
				t.Fatalf("status=%s sends=%d", status, r.count())
			}
		})
	}
}

func rowsNextRetryAt(t *testing.T, db *sql.DB, id int64) time.Time {
	t.Helper()
	var value string
	if err := db.QueryRowContext(context.Background(), `SELECT next_run_at FROM reminders WHERE id = ?`, id).Scan(&value); err != nil {
		t.Fatal(err)
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.000000000Z", value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
