package store

import (
	"context"
	"testing"
	"time"

	"bot/internal/config"
)

func TestChatDailyCostMicrosUsesPricesAndUTCDay(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for _, chatID := range []int64{220, 221} {
		if err := EnsureChat(ctx, db, chatID, 11, ""); err != nil {
			t.Fatalf("EnsureChat(%d): %v", chatID, err)
		}
	}
	prices := config.PriceTable{
		{Model: "deepseek-flash", InputPerMTokUSD: 0.27, OutputPerMTokUSD: 1.10},
		{Model: "kimi-k3", Provider: "kimi", InputPerMTokUSD: 2, OutputPerMTokUSD: 4},
	}
	now := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)
	append := func(chatID int64, model string, tokensIn, tokensOut int64, createdAt string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO messages (chat_id, user_id, role, content, tokens_in, tokens_out, model, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			chatID, 11, RoleAssistant, "answer", tokensIn, tokensOut, model, createdAt); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}
	// 1000 input at 0.27 + 500 output at 1.10 = 270 + 550 = 820 micro-dollars.
	append(220, "deepseek-flash", 1000, 500, "2026-04-05 10:00:00")
	append(220, "deepseek-flash", 0, 0, "2026-04-05 11:00:00")
	// A provider-prefixed model id matches its exact row.
	append(220, "kimi:kimi-k3", 100, 50, "2026-04-05 11:30:00")
	// Yesterday is outside the UTC day.
	append(220, "deepseek-flash", 1_000_000, 0, "2026-04-04 23:59:59")
	// Another chat is independent.
	append(221, "deepseek-flash", 1000, 500, "2026-04-05 10:00:00")

	cost, err := ChatDailyCostMicros(ctx, db, 220, now, prices)
	if err != nil {
		t.Fatalf("ChatDailyCostMicros: %v", err)
	}
	// 820 + (100*2 + 50*4 = 400) = 1220.
	if cost != 1220 {
		t.Fatalf("chat 220 cost=%d micros, want 1220", cost)
	}
	other, err := ChatDailyCostMicros(ctx, db, 221, now, prices)
	if err != nil || other != 820 {
		t.Fatalf("chat 221 cost=%d err=%v, want 820", other, err)
	}
	// Without prices the spend is zero and the budget cannot enforce anything.
	empty, err := ChatDailyCostMicros(ctx, db, 220, now, config.PriceTable{})
	if err != nil || empty != 0 {
		t.Fatalf("cost without prices=%d err=%v, want 0", empty, err)
	}
}

func TestListActiveChatsWithCostShowsSpendAndEightyPercentWarning(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	now := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)
	prices := config.PriceTable{{Model: "m", InputPerMTokUSD: 1, OutputPerMTokUSD: 1}}
	for _, chatID := range []int64{220, 221} {
		if err := EnsureChat(ctx, db, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
	}
	// 800 micro-dollars of a 1000 micro-dollar budget is exactly 80%.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO messages (chat_id, user_id, role, content, tokens_in, tokens_out, model, created_at)
		VALUES (220, 11, ?, 'a', 600, 200, 'm', '2026-04-05 10:00:00')`, RoleAssistant); err != nil {
		t.Fatal(err)
	}
	if err := SetChatLimits(ctx, db, ChatLimits{ChatID: 220, DailyCostLimitMicros: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := SetChatLimits(ctx, db, ChatLimits{ChatID: 221, DailyCostLimitMicros: 1000}); err != nil {
		t.Fatal(err)
	}
	chats, err := ListActiveChatsWithCost(ctx, db, now, 50, prices)
	if err != nil {
		t.Fatalf("ListActiveChatsWithCost: %v", err)
	}
	var warned, clean bool
	for _, chat := range chats {
		switch chat.ID {
		case 220:
			warned = chat.Today.CostWarning && chat.Today.CostUSD == 0.0008
		case 221:
			clean = !chat.Today.CostWarning && chat.Today.CostUSD == 0
		}
	}
	if !warned {
		t.Fatalf("chat 220 did not reach the 80%% warning: %+v", chats)
	}
	if !clean {
		t.Fatalf("chat 221 without spend got a warning: %+v", chats)
	}
}

func TestSetChatLimitsStoresAndValidatesCostBudget(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if err := EnsureChat(ctx, db, 220, 11, ""); err != nil {
		t.Fatal(err)
	}
	if err := SetChatLimits(ctx, db, ChatLimits{ChatID: 220, DailyCostLimitMicros: 1_250_000}); err != nil {
		t.Fatalf("SetChatLimits: %v", err)
	}
	limits, found, err := GetChatLimits(ctx, db, 220)
	if err != nil || !found || limits.DailyCostLimitMicros != 1_250_000 {
		t.Fatalf("limits=%+v found=%v err=%v", limits, found, err)
	}
	if limits.IsUnlimited() {
		t.Fatal("a cost budget did not make the row limited")
	}
	if err := SetChatLimits(ctx, db, ChatLimits{ChatID: 220, DailyCostLimitMicros: ChatCostLimitMaxMicros + 1}); err == nil {
		t.Fatal("a cost budget above the maximum was accepted")
	}
	if err := SetChatLimits(ctx, db, ChatLimits{ChatID: 220, DailyCostLimitMicros: -1}); err == nil {
		t.Fatal("a negative cost budget was accepted")
	}
	if _, err := ResetChatLimits(ctx, db, 220); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := GetChatLimits(ctx, db, 220); found {
		t.Fatal("reset left the cost budget row behind")
	}
}
