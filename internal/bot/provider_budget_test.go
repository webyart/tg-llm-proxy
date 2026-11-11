package bot

// Provider daily budgets: the bot refuses a model call of a provider whose today's attributed spend already
// reached its budget. These tests exercise the real callModel path with a real router and a real store, so
// the attribution, the price table, the event rows and the localized refusal are the production ones.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
)

// providerBudgetConfig is the environment of these tests: two named providers and a price table whose
// numbers are whole micro-dollars per token, so a test expresses a spend directly in input tokens.
func providerBudgetConfig(t *testing.T) config.Config {
	t.Helper()
	return testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11",
		"MODEL_PROVIDERS": `[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key":"k",` +
			`"models":["kimi-k3"],"default_model":"kimi-k3"},` +
			`{"name":"deepseek","base_url":"https://api.deepseek.com/v1","api_key":"k",` +
			`"models":["deepseek-flash"],"default_model":"deepseek-flash"}]`,
		"MODEL_PRICES": `[{"model":"kimi-k3","input_per_mtok_usd":1,"output_per_mtok_usd":2},` +
			`{"model":"deepseek-flash","input_per_mtok_usd":1,"output_per_mtok_usd":1}]`,
	})
}

// providerBudgetModelRouter builds the router the dispatcher sees: a default endpoint (strata) plus the two
// configured providers, each with its own scripted fake so a test can prove which client was called.
func providerBudgetModelRouter() (model.Client, *fakeModel, *fakeModel, *fakeModel) {
	var def = &fakeModel{response: model.Response{Content: "default answer"}}
	var kimi = &fakeModel{response: model.Response{Content: "kimi answer"}}
	var deepseek = &fakeModel{response: model.Response{Content: "deepseek answer"}}
	var client = model.NewRouter(model.RouterOptions{
		Default: def, DefaultModel: "qwen",
		Providers: []model.Provider{
			{Name: "kimi", Models: []string{"kimi-k3"}, DefaultModel: "kimi-k3", Client: kimi},
			{Name: "deepseek", Models: []string{"deepseek-flash"}, DefaultModel: "deepseek-flash", Client: deepseek},
		},
	})
	return client, def, kimi, deepseek
}

// seedProviderSpend stores one assistant answer of today with the given input tokens, which is exactly what
// the spend query prices.
func seedProviderSpend(t *testing.T, db *sql.DB, chatID int64, modelName string, tokensIn int64) {
	t.Helper()
	ctx := context.Background()
	if err := store.EnsureChat(ctx, db, chatID, 11, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if _, err := store.Append(ctx, db, store.Message{
		ChatID: chatID, UserID: 11, Role: store.RoleAssistant, Content: "stored answer",
		Model: modelName, TokensIn: &tokensIn,
	}); err != nil {
		t.Fatalf("seed assistant answer: %v", err)
	}
}

func setTestProviderBudget(t *testing.T, db *sql.DB, provider string, micros int64, modelName string) {
	t.Helper()
	if err := store.SetProviderBudget(context.Background(), db, provider, micros, store.ProviderSettings{
		Provider: provider, Models: []string{modelName}, DefaultModel: modelName, Enabled: true,
	}); err != nil {
		t.Fatalf("SetProviderBudget(%s): %v", provider, err)
	}
}

func eventCount(t *testing.T, db *sql.DB, kind string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = ?`, kind).Scan(&count); err != nil {
		t.Fatalf("count events %s: %v", kind, err)
	}
	return count
}

func TestProviderBudgetEnforcementUnderAtOverAndUnlimited(t *testing.T) {
	for _, test := range []struct {
		name         string
		spendTokens  int64 // 1 USD per million input tokens
		budgetMicros int64
		refused      bool
	}{
		{name: "under", spendTokens: 1_000_000, budgetMicros: 2_000_000},
		{name: "at the budget", spendTokens: 2_000_000, budgetMicros: 2_000_000, refused: true},
		{name: "over the budget", spendTokens: 3_000_000, budgetMicros: 2_000_000, refused: true},
		{name: "unlimited", spendTokens: 3_000_000, budgetMicros: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, _, kimi, _ := providerBudgetModelRouter()
			db := testStore(t)
			d := newTestDispatcher(t, providerBudgetConfig(t), client, &fakeReplier{}, db)
			seedProviderSpend(t, db, 100, "kimi-k3", test.spendTokens)
			setTestProviderBudget(t, db, "kimi", test.budgetMicros, "kimi-k3")

			_, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil)
			if test.refused {
				if !errors.Is(err, ErrProviderBudgetExceeded) {
					t.Fatalf("call error=%v, want ErrProviderBudgetExceeded", err)
				}
				if kimi.callCount() != 0 {
					t.Fatalf("the refused provider was called %d times, want 0", kimi.callCount())
				}
				if text := chatLimitRefusalText(err); !strings.Contains(text, "Дневной бюджет провайдера") {
					t.Fatalf("RU refusal text=%q", text)
				}
				var detail string
				if err := db.QueryRow(`SELECT COALESCE(detail, '') FROM events WHERE kind = ? ORDER BY id DESC LIMIT 1`,
					store.EventLimit).Scan(&detail); err != nil {
					t.Fatalf("read limit event: %v", err)
				}
				if !strings.HasPrefix(detail, "provider_budget kimi ") {
					t.Fatalf("limit event detail=%q, want a provider_budget detail", detail)
				}
				return
			}
			if err != nil {
				t.Fatalf("call error=%v, want nil", err)
			}
			if kimi.callCount() != 1 {
				t.Fatalf("provider calls=%d, want 1", kimi.callCount())
			}
			if refused := eventCount(t, db, store.EventLimit); refused != 0 {
				t.Fatalf("limit events=%d, want none", refused)
			}
		})
	}
}

// TestProviderBudgetIsolation: one exhausted provider does not block another provider, and it does not block
// the default endpoint (a plain model id), which is exactly the unchanged chat path.
func TestProviderBudgetIsolation(t *testing.T) {
	client, def, kimi, deepseek := providerBudgetModelRouter()
	db := testStore(t)
	d := newTestDispatcher(t, providerBudgetConfig(t), client, &fakeReplier{}, db)
	seedProviderSpend(t, db, 100, "kimi-k3", 2_000_000)
	setTestProviderBudget(t, db, "kimi", 2_000_000, "kimi-k3")
	setTestProviderBudget(t, db, "deepseek", 1_000_000, "deepseek-flash")

	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); !errors.Is(err, ErrProviderBudgetExceeded) {
		t.Fatalf("kimi call error=%v, want ErrProviderBudgetExceeded", err)
	}
	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "deepseek:deepseek-flash"}, nil, nil); err != nil {
		t.Fatalf("deepseek call error=%v, want nil (its own budget is untouched)", err)
	}
	if deepseek.callCount() != 1 {
		t.Fatalf("deepseek calls=%d, want 1", deepseek.callCount())
	}
	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "qwen"}, nil, nil); err != nil {
		t.Fatalf("default endpoint call error=%v, want nil (strata is never budgeted)", err)
	}
	if def.callCount() != 1 || kimi.callCount() != 0 {
		t.Fatalf("default calls=%d kimi calls=%d, want 1/0", def.callCount(), kimi.callCount())
	}
}

// TestProviderBudgetWarningOncePerDay: crossing 80% writes one provider_budget_warning per UTC day, and a row
// written yesterday does not suppress today's warning.
func TestProviderBudgetWarningOncePerDay(t *testing.T) {
	client, _, kimi, _ := providerBudgetModelRouter()
	db := testStore(t)
	d := newTestDispatcher(t, providerBudgetConfig(t), client, &fakeReplier{}, db)
	seedProviderSpend(t, db, 100, "kimi-k3", 1_600_000) // 80% of the 2 USD budget
	setTestProviderBudget(t, db, "kimi", 2_000_000, "kimi-k3")

	for round := 1; round <= 2; round++ {
		if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); err != nil {
			t.Fatalf("round %d call error=%v, want nil", round, err)
		}
		if got := eventCount(t, db, store.EventProviderBudgetWarning); got != 1 {
			t.Fatalf("round %d warnings=%d, want exactly one per day", round, got)
		}
	}
	var detail string
	if err := db.QueryRow(`SELECT COALESCE(detail, '') FROM events WHERE kind = ?`, store.EventProviderBudgetWarning).Scan(&detail); err != nil {
		t.Fatalf("read warning event: %v", err)
	}
	if detail != "kimi" {
		t.Fatalf("warning detail=%q, want the provider name", detail)
	}
	// The row of "yesterday" no longer satisfies "warned today", so the next call warns again.
	if _, err := db.Exec(`UPDATE events SET created_at = '2026-01-01 00:00:00' WHERE kind = ?`, store.EventProviderBudgetWarning); err != nil {
		t.Fatalf("age the warning: %v", err)
	}
	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); err != nil {
		t.Fatalf("next-day call error=%v, want nil", err)
	}
	if got := eventCount(t, db, store.EventProviderBudgetWarning); got != 2 {
		t.Fatalf("warnings after the next day=%d, want 2", got)
	}
	if kimi.callCount() != 3 {
		t.Fatalf("allowed calls=%d, want 3", kimi.callCount())
	}
}

// TestTaskUsesTheProviderBudget: the task scheduler reaches callModel through the same gate, so a task whose
// selected model belongs to an exhausted provider fails without a model call.
func TestTaskUsesTheProviderBudget(t *testing.T) {
	client, _, kimi, _ := providerBudgetModelRouter()
	db := testStore(t)
	d := newTestDispatcher(t, providerBudgetConfig(t), client, &fakeReplier{}, db)
	ctx := context.Background()
	now := time.Now().UTC()
	seedProviderSpend(t, db, 200, "kimi-k3", 2_000_000)
	setTestProviderBudget(t, db, "kimi", 2_000_000, "kimi-k3")
	if err := store.EnsureChat(ctx, db, 101, 11, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if _, err := store.CreateTask(ctx, db, store.Task{
		ChatID: 101, UserID: 11, Goal: "task goal", Model: "kimi:kimi-k3",
		PlanJSON: validTaskPlanOne, DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := d.ProcessDueTasks(ctx, now, 10); err != nil {
		t.Fatalf("ProcessDueTasks: %v", err)
	}
	if kimi.callCount() != 0 {
		t.Fatalf("the task called the exhausted provider %d times, want 0", kimi.callCount())
	}
	task, found, err := store.CurrentTask(ctx, db, 101)
	if err != nil || !found {
		t.Fatalf("CurrentTask found=%v err=%v", found, err)
	}
	if task.Status != store.TaskStatusFailed {
		t.Fatalf("task status=%q, want failed", task.Status)
	}
	if !strings.Contains(task.Error, "Дневной бюджет провайдера") {
		t.Fatalf("task error=%q, want the localized provider budget sentence", task.Error)
	}
	if taskStepRetryable(ctx, &chatLimitRefusal{cause: ErrProviderBudgetExceeded}) {
		t.Fatal("a provider budget refusal must not be retried by a task step")
	}
}
