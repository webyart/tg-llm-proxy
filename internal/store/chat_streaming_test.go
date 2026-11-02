package store

import (
	"context"
	"testing"
)

func TestChatStreamingIsScopedResettableAndIndependentOfModel(t *testing.T) {
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
	if _, found, err := ChatStreaming(ctx, db, 42); err != nil || found {
		t.Fatalf("fresh chat must use the global default, found=%v err=%v", found, err)
	}
	if err := SetChatStreaming(ctx, db, 42, true); err != nil {
		t.Fatalf("SetChatStreaming on: %v", err)
	}
	if err := SetChatStreaming(ctx, db, 43, false); err != nil {
		t.Fatalf("SetChatStreaming off: %v", err)
	}
	enabled, found, err := ChatStreaming(ctx, db, 42)
	if err != nil || !found || !enabled {
		t.Fatalf("ChatStreaming=%v found=%v err=%v, want on override", enabled, found, err)
	}
	enabled, found, err = ChatStreaming(ctx, db, 43)
	if err != nil || !found || enabled {
		t.Fatalf("other chat=%v found=%v err=%v, want off override", enabled, found, err)
	}
	if err := SetChatModel(ctx, db, 42, "qwen-alt"); err != nil {
		t.Fatalf("SetChatModel: %v", err)
	}
	if err := ClearChatModel(ctx, db, 42); err != nil {
		t.Fatalf("ClearChatModel: %v", err)
	}
	if _, found, err := ChatStreaming(ctx, db, 42); err != nil || !found {
		t.Fatalf("model reset removed streaming override, found=%v err=%v", found, err)
	}
	if err := ClearChatStreaming(ctx, db, 42); err != nil {
		t.Fatalf("ClearChatStreaming: %v", err)
	}
	if _, found, err := ChatStreaming(ctx, db, 42); err != nil || found {
		t.Fatalf("streaming reset must restore the default, found=%v err=%v", found, err)
	}
	if _, found, err := ChatStreaming(ctx, db, 43); err != nil || !found {
		t.Fatalf("reset leaked into another chat, found=%v err=%v", found, err)
	}
}
