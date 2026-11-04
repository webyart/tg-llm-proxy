package store

import (
	"context"
	"testing"
)

func TestHistoryExportSearchAndDelete(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "history_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for _, row := range []Message{
		{ChatID: 70, UserID: 11, Role: RoleUser, Content: "Привет МИР"},
		{ChatID: 70, UserID: 11, Role: RoleAssistant, Content: "ответ"},
		{ChatID: 71, UserID: 12, Role: RoleUser, Content: "другой чат"},
	} {
		if _, err := Append(ctx, db, row); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	rows, err := ExportMessages(ctx, db, 70, 10)
	if err != nil || len(rows) != 2 || rows[0].Content != "Привет МИР" {
		t.Fatalf("export=%+v err=%v", rows, err)
	}
	matches, err := SearchMessages(ctx, db, 70, "мир", 20)
	if err != nil || len(matches) != 1 || matches[0].Content != "Привет МИР" {
		t.Fatalf("search=%+v err=%v", matches, err)
	}
	if err := SetResumeSkip(ctx, db, 70, rows[0].ID); err != nil {
		t.Fatalf("SetResumeSkip: %v", err)
	}
	if err := SetState(ctx, db, ContextResetIDKey(70), "1"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	deleted, err := DeleteChatHistory(ctx, db, 70)
	if err != nil || deleted != 2 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	left, err := HistoryOfChat(ctx, db, 70, 10)
	if err != nil || len(left) != 0 {
		t.Fatalf("left=%+v err=%v", left, err)
	}
	other, err := HistoryOfChat(ctx, db, 71, 10)
	if err != nil || len(other) != 1 {
		t.Fatalf("other chat=%+v err=%v", other, err)
	}
	if _, found, err := ResumeSkip(ctx, db, 70); err != nil || found {
		t.Fatalf("resume suppression remains: found=%v err=%v", found, err)
	}
	deleted, err = DeleteChatHistory(ctx, db, 70)
	if err != nil || deleted != 0 {
		t.Fatalf("second delete=%d err=%v", deleted, err)
	}
}
