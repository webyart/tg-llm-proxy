package store

import (
	"context"
	"testing"
)

func TestReplacePendingUserContent(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "voice_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	tgID := int64(800)
	id, err := Append(ctx, db, Message{ChatID: 90, UserID: 11, Role: RoleUser, Content: "ожидает", TelegramMessageID: &tgID})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ReplacePendingUserContent(ctx, db, 90, tgID, "[Голосовое]\nтекст"); err != nil {
		t.Fatalf("ReplacePendingUserContent: %v", err)
	}
	rows, err := RecentByChat(ctx, db, 90, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != id || rows[0].Content != "[Голосовое]\nтекст" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if err := ReplacePendingUserContent(ctx, db, 90, 999, "чужое"); err == nil {
		t.Fatal("unknown message must not report success")
	}
}
