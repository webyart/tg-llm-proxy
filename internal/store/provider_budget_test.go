package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"bot/internal/config"
)

// budgetPrices is the price table the provider-budget tests share: 1 USD per million input tokens and 2 USD
// per million output tokens for kimi-k3, 0.5 USD per million for deepseek-flash. The numbers are whole
// micros per token, so a test can express a spend in tokens without floating-point arithmetic.
func budgetPrices() config.PriceTable {
	return config.PriceTable{
		{Model: "kimi-k3", InputPerMTokUSD: 1, OutputPerMTokUSD: 2},
		{Model: "deepseek-flash", InputPerMTokUSD: 0.5, OutputPerMTokUSD: 0.5},
	}
}

// TestMigration028AddsBudgetToAnExistingSchema27 upgrades a database that already has a provider_settings row:
// the row keeps its model override and reaches the new column with the unlimited default 0.
func TestMigration028AddsBudgetToAnExistingSchema27(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 27)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO provider_settings (provider, models_json, default_model, enabled, updated_at)
		VALUES ('kimi', '["kimi-k3","kimi-k2"]', 'kimi-k3', 1, '2026-01-01 00:00:00')`); err != nil {
		t.Fatalf("seed schema-27 provider row: %v", err)
	}
	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("upgrade schema 27 to 28: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) || version == 27 {
		t.Fatalf("SchemaVersion=%d err=%v, want %d (schema 28 applied)", version, err, len(MigrationSQL))
	}
	var present int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('provider_settings') WHERE name = 'daily_budget_micros'`).Scan(&present); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if present != 1 {
		t.Fatal("column daily_budget_micros is missing after migration 028")
	}
	stored, found, err := GetProviderSettings(ctx, db, "kimi")
	if err != nil || !found {
		t.Fatalf("stored row=%+v found=%v err=%v", stored, found, err)
	}
	if stored.DailyBudgetMicros != 0 {
		t.Fatalf("new column default=%d, want 0 (unlimited)", stored.DailyBudgetMicros)
	}
	if stored.DefaultModel != "kimi-k3" || !stored.Enabled || len(stored.Models) != 2 {
		t.Fatalf("old provider row was not preserved: %+v", stored)
	}
}

// TestMigration028ToleratesAnExistingBudgetColumn reproduces a database whose provider_settings already has
// daily_budget_micros but whose 028 row is missing (an interrupted release, or an operator's repair). The
// guarded runner must skip the ALTER instead of failing with "duplicate column".
func TestMigration028ToleratesAnExistingBudgetColumn(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 27)
	if _, err := db.ExecContext(ctx, `ALTER TABLE provider_settings ADD COLUMN daily_budget_micros INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("pre-add the column: %v", err)
	}
	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema over an existing budget column: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) {
		t.Fatalf("SchemaVersion=%d err=%v, want %d", version, err, len(MigrationSQL))
	}
	if _, err := db.ExecContext(ctx, `SELECT daily_budget_micros FROM provider_settings LIMIT 1`); err != nil {
		t.Fatalf("the guarded column is unusable: %v", err)
	}
}

func TestProviderBudgetMicrosConversion(t *testing.T) {
	for _, test := range []struct {
		usd    float64
		micros int64
	}{
		{0, 0},
		{1, 1_000_000},
		{0.000001, 1},
		{12.345678, 12_345_678},
		{100000, MaxProviderDailyBudgetMicros},
	} {
		micros, err := ProviderBudgetMicros(test.usd)
		if err != nil || micros != test.micros {
			t.Errorf("ProviderBudgetMicros(%v)=%d err=%v, want %d", test.usd, micros, err, test.micros)
		}
		if got := ProviderBudgetUSD(test.micros); got != test.usd {
			t.Errorf("ProviderBudgetUSD(%d)=%v, want %v", test.micros, got, test.usd)
		}
	}
	// A value with more than six decimals is stored at the precision the API reports.
	if micros, err := ProviderBudgetMicros(0.0000014); err != nil || micros != 1 {
		t.Errorf("rounding: micros=%d err=%v, want 1", micros, err)
	}
	for _, bad := range []float64{-1, -0.000001, 100000.000001} {
		if _, err := ProviderBudgetMicros(bad); !errors.Is(err, ErrInvalidProviderSettings) {
			t.Errorf("ProviderBudgetMicros(%v) err=%v, want ErrInvalidProviderSettings", bad, err)
		}
	}
}

func TestProviderBudgetStoreGetSetAndReset(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "provider-budget-crud")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	// No row: the budget is 0 and the map is empty, never an error.
	if micros, err := GetProviderBudget(ctx, db, "kimi"); err != nil || micros != 0 {
		t.Fatalf("budget of a provider without a row = %d err=%v, want 0/nil", micros, err)
	}
	if budgets, err := ProviderDailyBudgets(ctx, db); err != nil || len(budgets) != 0 {
		t.Fatalf("budgets of an empty table = %v err=%v, want empty", budgets, err)
	}

	// A budget without an existing row is created from the seed's model list.
	if err := SetProviderBudget(ctx, db, "kimi", 2_500_000, ProviderSettings{
		Provider: "kimi", Models: []string{"kimi-k3"}, DefaultModel: "kimi-k3", Enabled: true,
	}); err != nil {
		t.Fatalf("SetProviderBudget on a new provider: %v", err)
	}
	stored, found, err := GetProviderSettings(ctx, db, "kimi")
	if err != nil || !found || stored.DailyBudgetMicros != 2_500_000 || stored.DefaultModel != "kimi-k3" {
		t.Fatalf("created row=%+v found=%v err=%v", stored, found, err)
	}
	if budgets, err := ProviderDailyBudgets(ctx, db); err != nil || budgets["kimi"] != 2_500_000 {
		t.Fatalf("budgets=%v err=%v", budgets, err)
	}

	// A later budget write must not touch the model override.
	if err := UpsertProviderSettings(ctx, db, ProviderSettings{
		Provider: "kimi", Models: []string{"new-a", "new-b"}, DefaultModel: "new-b", Enabled: false,
		DailyBudgetMicros: 3_000_000,
	}); err != nil {
		t.Fatalf("UpsertProviderSettings: %v", err)
	}
	if err := SetProviderBudget(ctx, db, "kimi", 1_000_000, ProviderSettings{Provider: "kimi"}); err != nil {
		t.Fatalf("SetProviderBudget over an existing row: %v", err)
	}
	stored, found, err = GetProviderSettings(ctx, db, "kimi")
	if err != nil || !found || stored.DailyBudgetMicros != 1_000_000 ||
		len(stored.Models) != 2 || stored.DefaultModel != "new-b" || stored.Enabled {
		t.Fatalf("budget-only write changed the model override: %+v found=%v err=%v", stored, found, err)
	}

	// Invalid budgets are refused by both writers.
	if err := SetProviderBudget(ctx, db, "kimi", -1, ProviderSettings{Provider: "kimi"}); !errors.Is(err, ErrInvalidProviderSettings) {
		t.Fatalf("negative SetProviderBudget err=%v, want ErrInvalidProviderSettings", err)
	}
	if err := UpsertProviderSettings(ctx, db, ProviderSettings{
		Provider: "kimi", Models: []string{"m"}, DefaultModel: "m", DailyBudgetMicros: MaxProviderDailyBudgetMicros + 1,
	}); !errors.Is(err, ErrInvalidProviderSettings) {
		t.Fatalf("oversized budget err=%v, want ErrInvalidProviderSettings", err)
	}

	// Reset is the DELETE of the row: the budget (and the model override) return to their defaults.
	if deleted, err := DeleteProviderSettings(ctx, db, "kimi"); err != nil || !deleted {
		t.Fatalf("DeleteProviderSettings deleted=%v err=%v", deleted, err)
	}
	if micros, err := GetProviderBudget(ctx, db, "kimi"); err != nil || micros != 0 {
		t.Fatalf("budget after reset = %d err=%v, want 0/nil", micros, err)
	}
	if budgets, err := ProviderDailyBudgets(ctx, db); err != nil || len(budgets) != 0 {
		t.Fatalf("budgets after reset = %v err=%v, want empty", budgets, err)
	}
}

func TestModelDailyCostMicrosGroupsByModelAndDay(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "model-daily-cost")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	for _, chat := range []struct{ id, user int64 }{{1, 11}, {2, 12}, {3, 11}, {4, 11}} {
		if err := EnsureChat(ctx, db, chat.id, chat.user, ""); err != nil {
			t.Fatalf("EnsureChat(%d): %v", chat.id, err)
		}
	}
	insert := func(chatID, userID int64, role, model string, tokensIn, tokensOut int64, createdAt string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO messages
			(chat_id, user_id, role, content, tokens_in, tokens_out, model, created_at)
			VALUES (?, ?, ?, 'answer', ?, ?, ?, ?)`,
			chatID, userID, role, tokensIn, tokensOut, model, createdAt); err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}
	// Two chats of two users: a provider budget is global, so both answers count.
	insert(1, 11, RoleAssistant, "kimi-k3", 1_000_000, 0, "2026-05-04 00:00:00")
	insert(2, 12, RoleAssistant, "kimi-k3", 500_000, 100_000, "2026-05-04 23:59:59")
	insert(3, 11, RoleAssistant, "deepseek-flash", 1_000_000, 0, "2026-05-04 10:00:00")
	// Not today, not an answer, not priced: none of these may be charged.
	insert(1, 11, RoleAssistant, "kimi-k3", 1_000_000, 0, "2026-05-03 23:59:59")
	insert(1, 11, RoleUser, "kimi-k3", 1_000_000, 0, "2026-05-04 10:00:00")
	insert(4, 11, RoleAssistant, "unpriced-model", 1_000_000, 0, "2026-05-04 10:00:00")

	costs, err := ModelDailyCostMicros(ctx, db, now, budgetPrices())
	if err != nil {
		t.Fatalf("ModelDailyCostMicros: %v", err)
	}
	// kimi-k3: 1.5M input tokens at 1 USD/MTok = 1.5 USD, 0.1M output at 2 USD/MTok = 0.2 USD.
	if got, want := costs["kimi-k3"], int64(1_700_000); got != want {
		t.Errorf("kimi-k3 cost=%d, want %d", got, want)
	}
	if got, want := costs["deepseek-flash"], int64(500_000); got != want {
		t.Errorf("deepseek-flash cost=%d, want %d", got, want)
	}
	if got := costs["unpriced-model"]; got != 0 {
		t.Errorf("unpriced model cost=%d, want 0", got)
	}

	// An empty price table skips the query entirely and reports no spend.
	if costs, err := ModelDailyCostMicros(ctx, db, now, nil); err != nil || len(costs) != 0 {
		t.Fatalf("priceless costs=%v err=%v, want empty", costs, err)
	}
}

func TestEventKindDetailTodayIsPerDetailAndDay(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "event-kind-detail")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	insert := func(kind, detail, createdAt string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO events (user_id, kind, detail, created_at) VALUES (11, ?, ?, ?)`,
			kind, detail, createdAt); err != nil {
			t.Fatalf("seed event: %v", err)
		}
	}
	insert(EventProviderBudgetWarning, "kimi", "2026-05-03 23:59:59")
	insert(EventProviderBudgetWarning, "deepseek", "2026-05-04 08:00:00")

	if warned, err := EventKindDetailToday(ctx, db, EventProviderBudgetWarning, "kimi", now); err != nil || warned {
		t.Fatalf("kimi warned today = %v err=%v, want false (yesterday's row)", warned, err)
	}
	if warned, err := EventKindDetailToday(ctx, db, EventProviderBudgetWarning, "deepseek", now); err != nil || !warned {
		t.Fatalf("deepseek warned today = %v err=%v, want true", warned, err)
	}
	if warned, err := EventKindDetailToday(ctx, db, EventProviderBudgetWarning, "unknown", now); err != nil || warned {
		t.Fatalf("unknown provider warned today = %v err=%v, want false", warned, err)
	}
}
