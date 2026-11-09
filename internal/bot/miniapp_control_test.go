package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/store"
)

func TestMiniAppControlReportsAndResetsChatOverride(t *testing.T) {
	replier := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS":    "11",
		"MINIAPP_TELEGRAM_FALLBACK": "true",
	}), nil, replier, db)

	if err := dispatch(t, d, testEvent(100, 11, 4000, "/miniapp")); err != nil {
		t.Fatalf("/miniapp: %v", err)
	}
	text := replier.joined()
	if !strings.Contains(text, "включена") || !strings.Contains(text, "общий MINIAPP_TELEGRAM_FALLBACK") {
		t.Fatalf("default /miniapp status is wrong: %q", text)
	}

	if err := dispatch(t, d, testEvent(100, 11, 4001, "/miniapp on")); err != nil {
		t.Fatalf("/miniapp on: %v", err)
	}
	if got := replier.texts()[len(replier.texts())-1]; !strings.Contains(got, "включена") || !strings.Contains(got, "Настройка сохранена") {
		t.Fatalf("/miniapp on confirmation is wrong: %q", got)
	}
	enabled, found, err := store.ChatMiniAppFallback(context.Background(), db, 100)
	if err != nil || !found || !enabled {
		t.Fatalf("override=%v found=%v err=%v, want on", enabled, found, err)
	}

	// The override must be visible on the next status call instead of the environment row.
	if err := dispatch(t, d, testEvent(100, 11, 4002, "/miniapp")); err != nil {
		t.Fatalf("/miniapp after on: %v", err)
	}
	text = replier.texts()[len(replier.texts())-1]
	if !strings.Contains(text, "включена") || !strings.Contains(text, "настройка этого чата") {
		t.Fatalf("override status is wrong: %q", text)
	}

	if err := dispatch(t, d, testEvent(100, 11, 4003, "/miniapp off")); err != nil {
		t.Fatalf("/miniapp off: %v", err)
	}
	enabled, found, err = store.ChatMiniAppFallback(context.Background(), db, 100)
	if err != nil || !found || enabled {
		t.Fatalf("override=%v found=%v err=%v, want off", enabled, found, err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 4004, "/miniapp")); err != nil {
		t.Fatalf("/miniapp after off: %v", err)
	}
	text = replier.texts()[len(replier.texts())-1]
	if !strings.Contains(text, "выключена") || !strings.Contains(text, "настройка этого чата") {
		t.Fatalf("off override status is wrong: %q", text)
	}

	if err := dispatch(t, d, testEvent(100, 11, 4005, "/miniapp reset")); err != nil {
		t.Fatalf("/miniapp reset: %v", err)
	}
	if _, found, err := store.ChatMiniAppFallback(context.Background(), db, 100); err != nil || found {
		t.Fatalf("reset left an override, found=%v err=%v", found, err)
	}
	text = replier.texts()[len(replier.texts())-1]
	if !strings.Contains(text, "Настройка этого чата сброшена") || !strings.Contains(text, "общий MINIAPP_TELEGRAM_FALLBACK") {
		t.Fatalf("reset status is wrong: %q", text)
	}
}

func TestMiniAppControlGlobalDefaultAndUsageError(t *testing.T) {
	replier := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS":    "11",
		"MINIAPP_TELEGRAM_FALLBACK": "false",
	}), nil, replier, db)

	if err := dispatch(t, d, testEvent(101, 11, 4010, "/miniapp")); err != nil {
		t.Fatalf("/miniapp: %v", err)
	}
	text := replier.texts()[len(replier.texts())-1]
	if !strings.Contains(text, "выключена") || !strings.Contains(text, "общий MINIAPP_TELEGRAM_FALLBACK") {
		t.Fatalf("global default status is wrong: %q", text)
	}

	if err := dispatch(t, d, testEvent(101, 11, 4011, "/miniapp maybe")); err != nil {
		t.Fatalf("/miniapp maybe: %v", err)
	}
	text = replier.texts()[len(replier.texts())-1]
	if !strings.Contains(text, "Использование: /miniapp") {
		t.Fatalf("unknown argument did not return usage: %q", text)
	}
	if _, found, err := store.ChatMiniAppFallback(context.Background(), db, 101); err != nil || found {
		t.Fatalf("usage error changed the override, found=%v err=%v", found, err)
	}
}

func TestMiniAppControlCanOverrideEveryGlobalValue(t *testing.T) {
	for _, test := range []struct {
		name   string
		global string
		action string
		want   bool
	}{
		{name: "on overrides global off", global: "false", action: "on", want: true},
		{name: "off overrides global on", global: "true", action: "off", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			replier := &fakeReplier{}
			db := testStore(t)
			d := newTestDispatcher(t, testConfig(t, map[string]string{
				"TELEGRAM_ALLOWED_USERS":    "11",
				"MINIAPP_TELEGRAM_FALLBACK": test.global,
			}), nil, replier, db)
			if err := dispatch(t, d, testEvent(102, 11, 4020, "/miniapp "+test.action)); err != nil {
				t.Fatalf("/miniapp %s: %v", test.action, err)
			}
			if got := d.effectiveMiniAppTelegramFallback(context.Background(), 102); got != test.want {
				t.Fatalf("effective fallback=%v, want %v", got, test.want)
			}
		})
	}
}
