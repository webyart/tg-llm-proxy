package store

import (
	"context"
	"testing"
)

func TestChatModelIsScopedAndResettable(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if err := EnsureChat(ctx, db, 42, 7, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if err := EnsureChat(ctx, db, 43, 8, ""); err != nil {
		t.Fatalf("EnsureChat other chat: %v", err)
	}
	if _, found, err := ChatModel(ctx, db, 42); err != nil || found {
		t.Fatalf("fresh chat must use the default, found=%v err=%v", found, err)
	}
	if err := SetChatModel(ctx, db, 42, "qwen-alt"); err != nil {
		t.Fatalf("SetChatModel: %v", err)
	}
	model, found, err := ChatModel(ctx, db, 42)
	if err != nil || !found || model != "qwen-alt" {
		t.Fatalf("ChatModel=%q found=%v err=%v", model, found, err)
	}
	if _, found, err := ChatModel(ctx, db, 43); err != nil || found {
		t.Fatalf("another chat must not inherit the override, found=%v err=%v", found, err)
	}
	if err := SetChatModel(ctx, db, 42, "  qwen-new  "); err != nil {
		t.Fatalf("SetChatModel trimmed: %v", err)
	}
	if err := ClearChatModel(ctx, db, 42); err != nil {
		t.Fatalf("ClearChatModel: %v", err)
	}
	if _, found, err := ChatModel(ctx, db, 42); err != nil || found {
		t.Fatalf("reset must remove the override, found=%v err=%v", found, err)
	}
	if err := SetChatModel(ctx, db, 42, " "); err == nil {
		t.Fatal("empty model name must be refused")
	}
}
