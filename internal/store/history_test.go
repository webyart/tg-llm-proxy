package store

import (
	"context"
	"errors"
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

func TestStreamExportMessagesStopsImmediatelyAndPreservesWindowOrder(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "history_stream_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for index := 0; index < 25; index++ {
		if _, err := Append(ctx, db, Message{ChatID: 72, UserID: 11, Role: RoleUser, Content: string(rune('a' + index))}); err != nil {
			t.Fatalf("Append %d: %v", index, err)
		}
	}
	stop := errors.New("stop export")
	calls := 0
	window, truncated, err := StreamExportMessages(ctx, db, 72, 10, func(window MessageExportWindow, truncated bool) error {
		if window.Count != 10 || window.OldestID != 16 || window.NewestID != 25 || !truncated {
			t.Fatalf("window=%+v truncated=%v", window, truncated)
		}
		return nil
	}, func(row Message) error {
		calls++
		if row.ID != 16 {
			t.Fatalf("first streamed row=%d, want 16", row.ID)
		}
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("stream calls=%d err=%v", calls, err)
	}
	if window.Count != 10 || !truncated {
		t.Fatalf("early-exit metadata=%+v truncated=%v", window, truncated)
	}

	ids := []int64{}
	_, _, err = StreamExportMessages(ctx, db, 72, 10, func(MessageExportWindow, bool) error { return nil }, func(row Message) error {
		ids = append(ids, row.ID)
		return nil
	})
	if err != nil || len(ids) != 10 {
		t.Fatalf("full stream ids=%v err=%v", ids, err)
	}
	for index, id := range ids {
		if want := int64(16 + index); id != want {
			t.Fatalf("stream ids=%v", ids)
		}
	}
}
