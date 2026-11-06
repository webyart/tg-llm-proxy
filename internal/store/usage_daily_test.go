package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestUserDailyUsageAggregatesUTCModelsAndToolsPerUser(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "daily_usage_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	appendAssistantAt(t, ctx, db, 100, 11, "model-a", 10, 20, "2026-03-02 00:00:00")
	appendAssistantAt(t, ctx, db, 101, 11, "model-a", 20, 40, "2026-03-02 23:59:59")
	appendAssistantAt(t, ctx, db, 100, 11, "model-b", 5, 7, "2026-03-02 12:00:00")
	appendAssistantAt(t, ctx, db, 200, 12, "model-a", 1000, 2000, "2026-03-02 12:00:00")
	appendAssistantAt(t, ctx, db, 100, 11, "model-a", 1000, 2000, "2026-03-01 23:59:59")
	appendAssistantAt(t, ctx, db, 100, 11, "model-a", 1000, 2000, "2026-03-03 00:00:00")
	if _, err := Append(ctx, db, Message{
		ChatID: 100, UserID: 11, Role: RoleUser, Content: "private query text", Model: "model-a",
		TokensIn: int64Pointer(1000), TokensOut: int64Pointer(2000), TelegramMessageID: int64Pointer(900),
	}); err != nil {
		t.Fatalf("Append user message: %v", err)
	}

	for _, event := range []struct {
		userID    int64
		kind      string
		detail    string
		createdAt string
	}{
		{11, EventUsage, "read_url", "2026-03-02 00:00:00"},
		{11, EventUsage, "read_url", "2026-03-02 23:59:59"},
		{11, EventUsage, "web_search", "2026-03-02 12:00:00"},
		{11, EventUsage, "calculator", "2026-03-02 12:00:00"},
		{11, EventUsage, "rag", "2026-03-02 12:00:00"},
		{11, EventUsage, "private query text", "2026-03-02 12:00:00"},
		{11, EventCommand, "read_url", "2026-03-02 12:00:00"},
		{11, EventUsage, "read_url", "2026-03-01 23:59:59"},
		{11, EventUsage, "rag", "2026-03-03 00:00:00"},
		{12, EventUsage, "read_url", "2026-03-02 12:00:00"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO events (user_id, kind, detail, created_at) VALUES (?, ?, ?, ?)`,
			event.userID, event.kind, event.detail, event.createdAt); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}

	now := time.Date(2026, 3, 2, 18, 0, 0, 0, time.FixedZone("test", 5*60*60))
	usage, err := UserDailyUsage(ctx, db, 11, now)
	if err != nil {
		t.Fatalf("UserDailyUsage: %v", err)
	}
	if len(usage.Models) != 2 {
		t.Fatalf("models=%+v, want model-a and model-b", usage.Models)
	}
	if got := usage.Models[0]; got.Model != "model-a" || got.Calls != 2 || got.TokensIn != 30 || got.TokensOut != 60 {
		t.Errorf("model-a=%+v, want 2 calls and 30/60 tokens", got)
	}
	if got := usage.Models[1]; got.Model != "model-b" || got.Calls != 1 || got.TokensIn != 5 || got.TokensOut != 7 {
		t.Errorf("model-b=%+v, want 1 call and 5/7 tokens", got)
	}
	if usage.Tools.ReadURL != 2 || usage.Tools.WebSearch != 1 || usage.Tools.Calculator != 1 || usage.Tools.RAG != 1 {
		t.Errorf("tools=%+v, want 2/1/1/1", usage.Tools)
	}
	if usage.ModelCalls() != 3 || usage.NetworkToolCalls() != 3 {
		t.Errorf("totals=%d/%d, want 3/3", usage.ModelCalls(), usage.NetworkToolCalls())
	}
	modelCalls, err := DailyModelCalls(ctx, db, 11, now)
	if err != nil || modelCalls != 3 {
		t.Fatalf("DailyModelCalls=%d err=%v, want 3", modelCalls, err)
	}
	networkCalls, err := DailyNetworkToolCalls(ctx, db, 11, now)
	if err != nil || networkCalls != 3 {
		t.Fatalf("DailyNetworkToolCalls=%d err=%v, want 3", networkCalls, err)
	}
	otherUser, err := UserDailyUsage(ctx, db, 12, now)
	if err != nil || otherUser.ModelCalls() != 1 || otherUser.NetworkToolCalls() != 1 {
		t.Fatalf("other user usage=%+v err=%v, want isolated totals", otherUser, err)
	}
}

func appendAssistantAt(t *testing.T, ctx context.Context, db *sql.DB, chatID int64, userID int64, model string, tokensIn int64, tokensOut int64, createdAt string) {
	t.Helper()
	id, err := Append(ctx, db, Message{
		ChatID: chatID, UserID: userID, Role: RoleAssistant, Content: "aggregate-only answer", Model: model,
		TokensIn: &tokensIn, TokensOut: &tokensOut,
	})
	if err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE messages SET created_at = ? WHERE id = ?`, createdAt, id); err != nil {
		t.Fatalf("set assistant timestamp: %v", err)
	}
}

func int64Pointer(value int64) *int64 { return &value }

func TestUserDailyUsageUsesActualModelCallEventsWithMessageTokens(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "daily_model_events_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	appendAssistantAt(t, ctx, db, 100, 11, "model-a", 10, 20, "2026-03-02 12:00:00")
	for _, event := range []struct {
		userID    int64
		model     string
		createdAt string
	}{
		{11, "model-a", "2026-03-02 00:00:00"},
		{11, "model-a", "2026-03-02 12:00:00"},
		{11, "model-a", "2026-03-02 23:59:59"},
		{11, "model-b", "2026-03-02 12:00:00"},
		{11, "model-a", "2026-03-01 23:59:59"},
		{12, "model-a", "2026-03-02 12:00:00"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO events (user_id, kind, detail, created_at) VALUES (?, ?, ?, ?)`,
			event.userID, EventModelCall, event.model, event.createdAt); err != nil {
			t.Fatalf("insert model call: %v", err)
		}
	}
	usage, err := UserDailyUsage(ctx, db, 11, time.Date(2026, 3, 2, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("UserDailyUsage: %v", err)
	}
	if len(usage.Models) != 2 || usage.ModelCalls() != 4 {
		t.Fatalf("usage=%+v, want 4 calls across two models", usage)
	}
	if got := usage.Models[0]; got.Model != "model-a" || got.Calls != 3 || got.TokensIn != 10 || got.TokensOut != 20 {
		t.Errorf("model-a=%+v, want 3 calls and 10/20 tokens", got)
	}
	if got := usage.Models[1]; got.Model != "model-b" || got.Calls != 1 || got.TokensIn != 0 || got.TokensOut != 0 {
		t.Errorf("model-b=%+v, want one event-only call", got)
	}
}
