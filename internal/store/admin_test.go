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
	if err := LogEvent(ctx, db, 7, EventCommand, "ask"); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}
	stats, err := AdminStats(ctx, db)
	if err != nil {
		t.Fatalf("AdminStats: %v", err)
	}
	if stats.Chats != 1 || stats.Messages != 2 || stats.Events != 1 || stats.Commands != 1 || stats.Pending != 0 || stats.LastActive == "" {
		t.Fatalf("unexpected admin stats: %+v", stats)
	}
}
