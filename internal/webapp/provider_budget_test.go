package webapp

// Provider daily budgets in the admin API and in /api/usage: the stored amount, today's attributed spend,
// the 80% warning, the optional PUT field, the audit row and the reset. The fixture is a real router over one
// configured provider, so the catalogue the API reports is the one production reads.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

func providerBudgetFixture(t *testing.T) (*App, *httptest.Server, string, model.ProviderOverrides) {
	t.Helper()
	app, server := newTestAppWithRaw(t, map[string]string{
		"TELEGRAM_ADMIN_USERS": strconv.FormatInt(testUserID, 10),
		"MODEL_PROVIDERS": `[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key":"sk-secret-test",` +
			`"models":["kimi-k3","kimi-k2"],"default_model":"kimi-k3"}]`,
		"MODEL_PRICES": `[{"model":"kimi-k3","input_per_mtok_usd":1,"output_per_mtok_usd":2}]`,
	})
	bearer := launchSession(t, app, server)
	def := &fakeModelClient{models: []string{"qwen"}}
	kimi := &fakeModelClient{models: []string{"kimi-k3", "kimi-k2"}}
	client := model.NewRouter(model.RouterOptions{
		Default: def, DefaultModel: "qwen",
		Providers: []model.Provider{{
			Name: "kimi", Models: []string{"kimi-k3", "kimi-k2"}, DefaultModel: "kimi-k3", Client: kimi,
		}},
	})
	overrides, ok := client.(model.ProviderOverrides)
	if !ok {
		t.Fatalf("router %T does not implement ProviderOverrides", client)
	}
	app.model = client
	app.modelCache = modelListCache{}
	return app, server, bearer, overrides
}

func providerListEntry(t *testing.T, server *httptest.Server, bearer string) adminProviderEntry {
	t.Helper()
	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/admin/providers", bearer)
	if status != http.StatusOK {
		t.Fatalf("GET providers status=%d body=%s", status, body)
	}
	items := decodeAdminProviderList(t, body)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want one provider", items)
	}
	return items[0]
}

func putAdminProvider(t *testing.T, server *httptest.Server, bearer string, body string) (int, adminProviderEntry, []byte) {
	t.Helper()
	status, raw := putSettings(t, server.URL, adminProviderTestPath("kimi"), bearer, body)
	var entry adminProviderEntry
	if status == http.StatusOK {
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("PUT provider JSON: %v body=%s", err, raw)
		}
	}
	return status, entry, raw
}

func TestAdminProviderBudgetSetPreserveSpendAndReset(t *testing.T) {
	app, server, bearer, overrides := providerBudgetFixture(t)
	ctx := context.Background()

	// The default state: no row, no budget, no spend.
	entry := providerListEntry(t, server, bearer)
	if entry.DailyBudgetUSD != 0 || entry.TodayCostUSD != 0 || entry.Warning {
		t.Fatalf("initial budget entry=%+v, want 0/0/false", entry)
	}

	// A budget-only PUT sets the amount without touching the model override.
	status, saved, raw := putAdminProvider(t, server, bearer, `{"daily_budget_usd":2}`)
	if status != http.StatusOK {
		t.Fatalf("budget-only PUT status=%d body=%s", status, raw)
	}
	if saved.DailyBudgetUSD != 2 || strings.Join(saved.Models, ",") != "kimi-k3,kimi-k2" ||
		saved.Default != "kimi-k3" || !saved.Enabled {
		t.Fatalf("budget-only response=%+v", saved)
	}
	if catalog := overrides.ProviderCatalog(); catalog[0].Source != model.ProviderSourceDB ||
		strings.Join(catalog[0].Models, ",") != "kimi-k3,kimi-k2" || catalog[0].DefaultModel != "kimi-k3" {
		t.Fatalf("a budget-only PUT must pin the unchanged models as the stored row: %+v", catalog[0])
	}
	stored, found, err := store.GetProviderSettings(ctx, app.db, "kimi")
	if err != nil || !found || stored.DailyBudgetMicros != 2_000_000 {
		t.Fatalf("stored row=%+v found=%v err=%v", stored, found, err)
	}

	// Today's spend is attributed to the provider with the price table: 1.7M input tokens at 1 USD/MTok.
	appendWebappAssistant(t, app, testChatID, testUserID, "priced answer", "kimi-k3", 1_700_000, 0)
	entry = providerListEntry(t, server, bearer)
	if math.Abs(entry.TodayCostUSD-1.7) > 1e-9 {
		t.Fatalf("today_cost_usd=%v, want 1.7", entry.TodayCostUSD)
	}
	if entry.DailyBudgetUSD != 2 || !entry.Warning {
		t.Fatalf("warning entry=%+v, want budget 2 and the 80%% flag", entry)
	}

	// The full model update omits the budget: the stored amount must survive it.
	status, saved, raw = putAdminProvider(t, server, bearer,
		`{"models":["kimi-k3","kimi-k2"],"default_model":"kimi-k2","enabled":true}`)
	if status != http.StatusOK || saved.DailyBudgetUSD != 2 || saved.Default != "kimi-k2" {
		t.Fatalf("model-only PUT status=%d entry=%+v body=%s", status, saved, raw)
	}
	if micros, err := store.GetProviderBudget(ctx, app.db, "kimi"); err != nil || micros != 2_000_000 {
		t.Fatalf("model-only PUT changed the budget to %d err=%v", micros, err)
	}

	// Six decimals are stored exactly; the provider is already over this much smaller budget.
	status, saved, raw = putAdminProvider(t, server, bearer, `{"daily_budget_usd":1.234567}`)
	if status != http.StatusOK || saved.DailyBudgetUSD != 1.234567 || !saved.Warning {
		t.Fatalf("fractional budget status=%d entry=%+v body=%s", status, saved, raw)
	}
	if stored, _, err := store.GetProviderSettings(ctx, app.db, "kimi"); err != nil || stored.DailyBudgetMicros != 1_234_567 {
		t.Fatalf("fractional budget stored=%+v err=%v", stored, err)
	}

	// The change is audited under its own operation, with the provider and the amount.
	var detail string
	if err := app.db.QueryRowContext(ctx,
		`SELECT COALESCE(detail, '') FROM events WHERE kind = 'miniapp_mutation' AND detail LIKE '%admin.provider.budget%' ORDER BY id DESC LIMIT 1`).
		Scan(&detail); err != nil {
		t.Fatalf("read the budget audit rows: %v", err)
	}
	for _, want := range []string{"operation=admin.provider.budget", "provider=kimi", "budget_micros=1234567"} {
		if !strings.Contains(detail, want) {
			t.Errorf("budget audit detail=%q misses %q", detail, want)
		}
	}

	// Reset removes the row, so the budget returns to 0 and the model list to env.
	status, raw = settingsRequest(t, server.URL, http.MethodDelete, adminProviderTestPath("kimi"), bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("DELETE status=%d body=%s", status, raw)
	}
	var reset adminProviderEntry
	if err := json.Unmarshal(raw, &reset); err != nil {
		t.Fatalf("DELETE JSON: %v body=%s", err, raw)
	}
	if reset.DailyBudgetUSD != 0 || reset.Source != model.ProviderSourceEnv {
		t.Fatalf("reset entry=%+v, want no budget and the env source", reset)
	}
	if _, found, err := store.GetProviderSettings(ctx, app.db, "kimi"); err != nil || found {
		t.Fatalf("row survived the reset: found=%v err=%v", found, err)
	}
}

func TestAdminProviderBudgetRejectsInvalidAmounts(t *testing.T) {
	_, server, bearer, _ := providerBudgetFixture(t)
	for name, test := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"negative":           {`{"daily_budget_usd":-1}`, 400, "provider_validation"},
		"above the range":    {`{"daily_budget_usd":100000.000001}`, 400, "provider_validation"},
		"wrong type":         {`{"daily_budget_usd":"10"}`, 400, "invalid_json"},
		"unknown field":      {`{"daily_budget_usd":10,"api_key":"x"}`, 400, "invalid_json"},
		"empty model body":   {`{"models":["m"]}`, 400, "provider_validation"},
		"no fields at all":   {`{}`, 400, "provider_validation"},
		"budget and models":  {`{"daily_budget_usd":10,"models":["m"],"default_model":"m"}`, 400, "provider_validation"},
		"too many decimals?": {`{"daily_budget_usd":100000}`, 200, ""},
	} {
		status, _, raw := putAdminProvider(t, server, bearer, test.body)
		if status != test.status {
			t.Errorf("%s: status=%d body=%s, want %d", name, status, raw, test.status)
			continue
		}
		if test.code != "" && !strings.Contains(string(raw), test.code) {
			t.Errorf("%s: body=%s, want code %s", name, raw, test.code)
		}
	}
}

func TestUsageReportsProviderCosts(t *testing.T) {
	app, server, bearer, _ := providerBudgetFixture(t)
	ctx := context.Background()
	appendWebappAssistant(t, app, testChatID, testUserID, "priced answer", "kimi-k3", 1_700_000, 0)
	if err := store.SetProviderBudget(ctx, app.db, "kimi", 2_000_000, store.ProviderSettings{
		Provider: "kimi", Models: []string{"kimi-k3", "kimi-k2"}, DefaultModel: "kimi-k3", Enabled: true,
	}); err != nil {
		t.Fatalf("SetProviderBudget: %v", err)
	}

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/usage", bearer)
	if status != http.StatusOK {
		t.Fatalf("GET usage status=%d body=%s", status, body)
	}
	var decoded usageResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("usage JSON: %v body=%s", err, body)
	}
	// The pre-existing fields are untouched by the new block.
	if decoded.Date == "" || len(decoded.Models) != 1 || decoded.Models[0].Model != "kimi-k3" {
		t.Fatalf("usage daily fields=%+v", decoded)
	}
	if len(decoded.ProviderCosts) != 1 {
		t.Fatalf("provider_costs=%+v, want one provider", decoded.ProviderCosts)
	}
	cost := decoded.ProviderCosts[0]
	if cost.Provider != "kimi" {
		t.Fatalf("provider=%q, want kimi", cost.Provider)
	}
	if math.Abs(cost.CostUSD-1.7) > 1e-9 || math.Abs(cost.BudgetUSD-2) > 1e-9 {
		t.Fatalf("cost=%v budget=%v, want 1.7/2", cost.CostUSD, cost.BudgetUSD)
	}
	if cost.RemainingUSD == nil || math.Abs(*cost.RemainingUSD-0.3) > 1e-9 {
		t.Fatalf("remaining=%v, want 0.3", cost.RemainingUSD)
	}
	if !cost.Warning {
		t.Fatalf("warning=false, want the 80%% flag for 1.7 of 2")
	}

	// Without a budget the provider still shows its spend, with no remaining amount and no warning.
	if _, err := store.DeleteProviderSettings(ctx, app.db, "kimi"); err != nil {
		t.Fatalf("DeleteProviderSettings: %v", err)
	}
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/usage", bearer)
	if status != http.StatusOK {
		t.Fatalf("GET usage after reset status=%d body=%s", status, body)
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("usage reset JSON: %v", err)
	}
	if len(decoded.ProviderCosts) != 1 || decoded.ProviderCosts[0].BudgetUSD != 0 ||
		decoded.ProviderCosts[0].RemainingUSD != nil || decoded.ProviderCosts[0].Warning {
		t.Fatalf("provider costs after reset=%+v", decoded.ProviderCosts)
	}
}
