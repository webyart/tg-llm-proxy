package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot/internal/i18n"
	"bot/internal/store"
)

func TestTimezoneCommandShowsSetsAndResetsForThePrivateOwner(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11,12",
		"TELEGRAM_ADMIN_USERS":   "11",
	}), nil, &fakeReplier{}, db)
	r := d.deps.Replier.(*fakeReplier)
	if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 1, "/timezone")); err != nil {
		t.Fatal(err)
	}
	if got := r.joined(); !strings.Contains(got, "Asia/Yekaterinburg") || !strings.Contains(got, "по умолчанию") {
		t.Fatalf("default /timezone = %q", got)
	}
	if err := dispatch(t, d, testEvent(100, 11, 2, "/timezone Europe/Berlin")); err != nil {
		t.Fatal(err)
	}
	if got := r.joined(); !strings.Contains(got, "Europe/Berlin") {
		t.Fatalf("set /timezone = %q", got)
	}
	timezone, found, err := store.GetChatTimezone(ctx, db, 100)
	if err != nil || !found || timezone != "Europe/Berlin" {
		t.Fatalf("stored timezone=%q found=%v err=%v", timezone, found, err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 3, "/timezone reset")); err != nil {
		t.Fatal(err)
	}
	if timezone, found, err := store.GetChatTimezone(ctx, db, 100); err != nil || found || timezone != "" {
		t.Fatalf("reset left timezone=%q found=%v err=%v", timezone, found, err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 4, "/timezone Mars/Olympus_Mons")); err != nil {
		t.Fatal(err)
	}
	if got := r.joined(); !strings.Contains(got, "Неизвестный часовой пояс") {
		t.Fatalf("invalid /timezone = %q", got)
	}
}

func TestTimezoneCommandPermissionsPrivateOwnerGroupAdminOnly(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11,12",
		"TELEGRAM_ADMIN_USERS":   "11",
	}), nil, &fakeReplier{}, db)
	if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureChat(ctx, db, -100500, 11, "group"); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, d, testEvent(100, 12, 1, "/timezone UTC")); err != nil {
		t.Fatal(err)
	}
	if timezone, found, _ := store.GetChatTimezone(ctx, db, 100); found || timezone != "" {
		t.Fatalf("another private user changed the zone to %q", timezone)
	}
	groupMember := testEvent(-100500, 12, 2, "/timezone UTC")
	groupMember.GroupAccepted = true
	if err := dispatch(t, d, groupMember); err != nil {
		t.Fatal(err)
	}
	if timezone, found, _ := store.GetChatTimezone(ctx, db, -100500); found || timezone != "" {
		t.Fatalf("a group member changed the zone to %q", timezone)
	}
	groupAdmin := testEvent(-100500, 11, 3, "/timezone UTC")
	groupAdmin.GroupAccepted = true
	if err := dispatch(t, d, groupAdmin); err != nil {
		t.Fatal(err)
	}
	if timezone, found, _ := store.GetChatTimezone(ctx, db, -100500); !found || timezone != "UTC" {
		t.Fatalf("administrator group change: timezone=%q found=%v", timezone, found)
	}
}

func TestReminderScheduleAndTaskStatusUseTheChatZone(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, &fakeReplier{}, db)
	if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatTimezone(ctx, db, 100, "Europe/Berlin"); err != nil {
		t.Fatal(err)
	}

	next := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC) // 09:00 CET
	schedule := reminderScheduleFor(ctx, i18n.RU, d, store.Reminder{
		ChatID: 100, Cadence: store.ReminderCadenceDaily, DailyTime: "09:00", NextRunAt: next,
	})
	for _, want := range []string{"09:00", "CET", "Europe/Berlin"} {
		if !strings.Contains(schedule, want) {
			t.Errorf("reminder schedule %q misses %q", schedule, want)
		}
	}
	if strings.Contains(schedule, "2026-11-11T") || strings.Contains(schedule, "08:00:00") {
		t.Errorf("reminder schedule still looks like ISO: %q", schedule)
	}

	created := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	if _, err := store.CreateTask(ctx, db, store.Task{
		ChatID: 100, UserID: 11, Goal: "goal", Model: "m", PlanJSON: validTaskPlanOne,
		DueAt: created, ExpiresAt: created.Add(time.Hour), CreatedAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	text, _, err := d.taskStatusText(ctx, testEvent(100, 11, 1, "/task"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Создана: 11 ноя, 13:00 CET") {
		t.Fatalf("task status did not render the created time in Europe/Berlin: %q", text)
	}
	if strings.Contains(text, "T12:00:00") || strings.Contains(text, "Z") {
		t.Fatalf("task status still contains a raw timestamp: %q", text)
	}
}
