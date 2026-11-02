package store

import (
	"context"
	"strings"
	"testing"
)

func TestChatPersonaIsScopedResettableAndValidated(t *testing.T) {
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
	if _, found, err := ChatPersona(ctx, db, 42); err != nil || found {
		t.Fatalf("fresh chat has override found=%v err=%v", found, err)
	}
	if err := SetChatPersona(ctx, db, 42, "  You are a coder.  "); err != nil {
		t.Fatalf("SetChatPersona: %v", err)
	}
	prompt, found, err := ChatPersona(ctx, db, 42)
	if err != nil || !found || prompt != "You are a coder." {
		t.Fatalf("prompt=%q found=%v err=%v", prompt, found, err)
	}
	if _, found, err := ChatPersona(ctx, db, 43); err != nil || found {
		t.Fatalf("other chat inherited persona found=%v err=%v", found, err)
	}
	if err := SetChatModel(ctx, db, 42, "qwen-alt"); err != nil {
		t.Fatalf("SetChatModel: %v", err)
	}
	if err := ClearChatModel(ctx, db, 42); err != nil {
		t.Fatalf("ClearChatModel: %v", err)
	}
	if err := SetChatStreaming(ctx, db, 42, true); err != nil {
		t.Fatalf("SetChatStreaming: %v", err)
	}
	if err := ClearChatStreaming(ctx, db, 42); err != nil {
		t.Fatalf("ClearChatStreaming: %v", err)
	}
	if _, found, err := ChatPersona(ctx, db, 42); err != nil || !found {
		t.Fatalf("other settings removed persona found=%v err=%v", found, err)
	}
	if err := SetChatPersona(ctx, db, 42, " "); err == nil {
		t.Fatal("empty persona must be refused")
	}
	if err := SetChatPersona(ctx, db, 42, strings.Repeat("x", MaxPersonaPromptRunes+1)); err == nil {
		t.Fatal("overlong persona must be refused")
	}
	if err := ClearChatPersona(ctx, db, 42); err != nil {
		t.Fatalf("ClearChatPersona: %v", err)
	}
	if _, found, err := ChatPersona(ctx, db, 42); err != nil || found {
		t.Fatalf("reset left persona found=%v err=%v", found, err)
	}
}
