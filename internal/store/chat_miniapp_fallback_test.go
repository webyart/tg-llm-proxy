package store

import (
	"context"
	"testing"
)

func TestChatMiniAppFallbackSetReadResetIsScopedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for _, chatID := range []int64{42, 43} {
		if err := EnsureChat(ctx, db, chatID, 7, ""); err != nil {
			t.Fatalf("EnsureChat: %v", err)
		}
	}
	if _, found, err := ChatMiniAppFallback(ctx, db, 42); err != nil || found {
		t.Fatalf("fresh chat must use the global default, found=%v err=%v", found, err)
	}
	if err := SetChatMiniAppFallback(ctx, db, 42, true); err != nil {
		t.Fatalf("SetChatMiniAppFallback on: %v", err)
	}
	if err := SetChatMiniAppFallback(ctx, db, 42, true); err != nil {
		t.Fatalf("repeated SetChatMiniAppFallback on: %v", err)
	}
	if err := SetChatMiniAppFallback(ctx, db, 43, false); err != nil {
		t.Fatalf("SetChatMiniAppFallback off: %v", err)
	}
	enabled, found, err := ChatMiniAppFallback(ctx, db, 42)
	if err != nil || !found || !enabled {
		t.Fatalf("ChatMiniAppFallback=%v found=%v err=%v, want on override", enabled, found, err)
	}
	enabled, found, err = ChatMiniAppFallback(ctx, db, 43)
	if err != nil || !found || enabled {
		t.Fatalf("other chat=%v found=%v err=%v, want off override", enabled, found, err)
	}
	if err := SetChatMiniAppFallback(ctx, db, 42, false); err != nil {
		t.Fatalf("SetChatMiniAppFallback flip to off: %v", err)
	}
	enabled, found, err = ChatMiniAppFallback(ctx, db, 42)
	if err != nil || !found || enabled {
		t.Fatalf("flipped chat=%v found=%v err=%v, want off override", enabled, found, err)
	}
	if err := ResetChatMiniAppFallback(ctx, db, 42); err != nil {
		t.Fatalf("ResetChatMiniAppFallback: %v", err)
	}
	if err := ResetChatMiniAppFallback(ctx, db, 42); err != nil {
		t.Fatalf("repeated ResetChatMiniAppFallback: %v", err)
	}
	if _, found, err := ChatMiniAppFallback(ctx, db, 42); err != nil || found {
		t.Fatalf("reset must restore the default, found=%v err=%v", found, err)
	}
	if _, found, err := ChatMiniAppFallback(ctx, db, 43); err != nil || !found {
		t.Fatalf("reset leaked into another chat, found=%v err=%v", found, err)
	}
	if _, found, err := ChatMiniAppFallback(ctx, db, 44); err != nil || found {
		t.Fatalf("unknown chat=%v err=%v, want no override and no error", found, err)
	}
	if err := ResetChatMiniAppFallback(ctx, db, 44); err != nil {
		t.Fatalf("reset of an unknown chat: %v", err)
	}
}
