package store

import (
	"context"
	"testing"
	"time"
)

func TestPrunePreviewAndApplyKeepIndependentRetentionClasses(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for _, message := range []Message{
		{ChatID: 42, UserID: 7, Role: RoleUser, Content: "old question", TelegramMessageID: intPtr(700)},
		{ChatID: 42, UserID: 7, Role: RoleAssistant, Content: "old answer"},
		{ChatID: 42, UserID: 7, Role: RoleUser, Content: "new question", TelegramMessageID: intPtr(701)},
		{ChatID: 42, UserID: 7, Role: RoleAssistant, Content: "new answer"},
	} {
		if _, err := Append(ctx, db, message); err != nil {
			t.Fatalf("Append %s: %v", message.Content, err)
		}
	}
	if err := LogEvent(ctx, db, 7, EventCommand, "old event"); err != nil {
		t.Fatalf("LogEvent old: %v", err)
	}
	if err := LogEvent(ctx, db, 7, EventCommand, "new event"); err != nil {
		t.Fatalf("LogEvent new: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE messages SET created_at = '2000-01-01 00:00:00' WHERE content LIKE 'old %'`); err != nil {
		t.Fatalf("age messages: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE events SET created_at = '2000-01-01 00:00:00' WHERE detail = 'old event'`); err != nil {
		t.Fatalf("age event: %v", err)
	}

	cutoff := time.Now().AddDate(0, 0, -90)
	preview, err := PrunePreview(ctx, db, PruneOptions{EventsBefore: cutoff, MessagesBefore: cutoff})
	if err != nil || preview.Events != 1 || preview.Messages != 2 {
		t.Fatalf("preview=%+v err=%v, want one event and two messages", preview, err)
	}
	var count int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("preview changed events, count=%d err=%v", count, err)
	}

	deletedEvents, err := Prune(ctx, db, PruneOptions{EventsBefore: cutoff})
	if err != nil || deletedEvents.Events != 1 || deletedEvents.Messages != 0 {
		t.Fatalf("event prune=%+v err=%v", deletedEvents, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("one current event must remain, count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("event-only prune changed messages, count=%d err=%v", count, err)
	}

	deletedMessages, err := Prune(ctx, db, PruneOptions{MessagesBefore: cutoff})
	if err != nil || deletedMessages.Messages != 2 || deletedMessages.Events != 0 {
		t.Fatalf("message prune=%+v err=%v", deletedMessages, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("two current messages must remain, count=%d err=%v", count, err)
	}
	if _, err := PrunePreview(ctx, db, PruneOptions{}); err == nil {
		t.Fatal("an empty retention selection must be refused")
	}
}
