package store

import (
	"context"
	"testing"
)

func TestChatLanguageSetReadReset(t *testing.T) {
	var ctx = context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if err := EnsureChat(ctx, db, 42, 7, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if _, found, err := ChatLanguage(ctx, db, 42); err != nil || found {
		t.Fatalf("empty ChatLanguage = found %v err %v", found, err)
	}
	if err := SetChatLanguage(ctx, db, 42, "en"); err != nil {
		t.Fatalf("SetChatLanguage: %v", err)
	}
	if language, found, err := ChatLanguage(ctx, db, 42); err != nil || !found || language != "en" {
		t.Fatalf("ChatLanguage = %q found %v err %v", language, found, err)
	}
	if err := SetChatLanguage(ctx, db, 42, "ru"); err != nil {
		t.Fatalf("SetChatLanguage update: %v", err)
	}
	if language, _, _ := ChatLanguage(ctx, db, 42); language != "ru" {
		t.Fatalf("language after update = %q", language)
	}
	if err := SetChatLanguage(ctx, db, 42, "de"); err == nil {
		t.Fatal("SetChatLanguage accepted an unknown language")
	}
	if err := ResetChatLanguage(ctx, db, 42); err != nil {
		t.Fatalf("ResetChatLanguage: %v", err)
	}
	if _, found, _ := ChatLanguage(ctx, db, 42); found {
		t.Fatal("ResetChatLanguage left the row")
	}
}
