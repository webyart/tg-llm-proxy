package store

import (
	"context"
	"testing"
)

func TestAdminStatsAreAggregates(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if err := EnsureChat(ctx, db, 42, 7, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	for _, message := range []Message{
		{ChatID: 42, UserID: 7, Role: RoleUser, Content: "question"},
		{ChatID: 42, UserID: 7, Role: RoleAssistant, Content: "answer"},
	} {
		if _, err := Append(ctx, db, message); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	for _, event := range []struct {
		kind   string
		detail string
	}{
		{EventCommand, "ask"},
		{EventModelError, "upstream 502"},
		{EventModelError, "empty_answer"},
		{EventTimeout, "model deadline"},
		{EventUsage, "rag"},
		{EventUsage, "calculator"},
		{EventUsage, "read_url"},
		{EventUsage, "web_search"},
		{EventDenied, "allowlist"},
	} {
		if err := LogEvent(ctx, db, 7, event.kind, event.detail); err != nil {
			t.Fatalf("LogEvent(%s, %s): %v", event.kind, event.detail, err)
		}
	}
	stats, err := AdminStats(ctx, db)
	if err != nil {
		t.Fatalf("AdminStats: %v", err)
	}
	lastActive := stats.LastActive
	stats.LastActive = ""
	want := AdminSummary{
		Chats:          1,
		Messages:       2,
		Events:         9,
		Commands:       1,
		ModelErrors:    2,
		Timeouts:       1,
		RAGUses:        1,
		CalculatorUses: 1,
		URLReaderUses:  1,
		WebSearchUses:  1,
	}
	if stats != want || lastActive == "" {
		t.Fatalf("unexpected admin stats: %+v, last active %q", stats, lastActive)
	}
}
