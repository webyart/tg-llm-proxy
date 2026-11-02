package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/store"
)

func TestPersonaPresetCustomScopeAndReset(t *testing.T) {
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, db)
	ctx := context.Background()

	if err := dispatch(t, d, testEvent(100, 11, 4000, "/persona")); err != nil {
		t.Fatalf("/persona list: %v", err)
	}
	joined := r.joined()
	for _, want := range []string{"default", "/persona coder", "/persona translator", "/persona custom"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("persona list misses %q: %q", want, joined)
		}
	}
	if len(r.menus) != 1 {
		t.Fatalf("/persona must send one inline menu, got %d", len(r.menus))
	}
	seen := map[string]bool{}
	for _, row := range r.menus[0].InlineKeyboard {
		for _, button := range row {
			seen[button.CallbackData] = true
		}
	}
	for _, want := range []string{"persona:assistant", "persona:coder", "persona:writer", "persona:translator", "persona:custom", "persona:reset"} {
		if !seen[want] {
			t.Errorf("persona menu misses %q", want)
		}
	}
	if err := dispatch(t, d, testEvent(100, 11, 4001, "/persona coder")); err != nil {
		t.Fatalf("/persona coder: %v", err)
	}
	prompt, name, override, err := d.effectivePersona(ctx, 100)
	if err != nil || !override || name != "coder" || !strings.Contains(prompt, "senior software engineer") {
		t.Fatalf("effective persona=%q/%q override=%v err=%v", prompt, name, override, err)
	}
	messages, err := d.BuildMessages(ctx, 100)
	if err != nil || len(messages) != 1 || messages[0].Content != prompt {
		t.Fatalf("model system prompt did not use persona: %+v err=%v", messages, err)
	}
	if err := dispatch(t, d, testEvent(101, 11, 4002, "/persona custom Ты отвечаешь коротко.")); err != nil {
		t.Fatalf("/persona custom: %v", err)
	}
	custom, name, _, err := d.effectivePersona(ctx, 101)
	if err != nil || name != "custom" || custom != "Ты отвечаешь коротко." {
		t.Fatalf("custom persona=%q/%q err=%v", custom, name, err)
	}
	if _, _, otherOverride, err := d.effectivePersona(ctx, 100); err != nil || !otherOverride {
		t.Fatalf("custom leaked between chats: override=%v err=%v", otherOverride, err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 4003, "/model reset")); err != nil {
		t.Fatalf("/model reset: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 4004, "/stream reset")); err != nil {
		t.Fatalf("/stream reset: %v", err)
	}
	if _, found, err := store.ChatPersona(ctx, db, 100); err != nil || !found {
		t.Fatalf("other resets removed persona found=%v err=%v", found, err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 4005, "/status")); err != nil {
		t.Fatalf("/status: %v", err)
	}
	if !strings.Contains(r.joined(), "Persona: coder (override этого чата)") {
		t.Fatalf("status misses persona override: %q", r.joined())
	}
	if err := dispatch(t, d, testEvent(100, 11, 4006, "/persona reset")); err != nil {
		t.Fatalf("/persona reset: %v", err)
	}
	prompt, name, override, err = d.effectivePersona(ctx, 100)
	if err != nil || override || name != "default" || prompt != d.deps.Cfg.SystemPrompt {
		t.Fatalf("reset persona=%q/%q override=%v err=%v", prompt, name, override, err)
	}
}

func TestPersonaRejectsEmptyCustomPrompt(t *testing.T) {
	r := &fakeReplier{}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, testStore(t))
	if err := dispatch(t, d, testEvent(100, 11, 4010, "/persona custom")); err != nil {
		t.Fatalf("empty custom: %v", err)
	}
	if !strings.Contains(r.joined(), "нужен текст роли") {
		t.Fatalf("empty custom did not explain the syntax: %q", r.joined())
	}
}
