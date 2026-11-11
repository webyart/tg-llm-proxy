package webapp

// Provider spend of /api/usage and of the admin provider card.
//
// A provider budget is administrator-level: it covers every chat, task and Mini App answer that reaches one
// configured provider, so the spend the usage screen reports is the same global number the bot compares with
// the budget before a call. Prices are MODEL_PRICES (an environment row); the tokens are today's assistant
// answers, priced on demand exactly like the per-chat cost. The attribution rule is model.ProviderAttribution,
// the same helper the bot enforces with, so the screen and the refusal cannot disagree.

import (
	"context"
	"sort"
	"time"

	"bot/internal/model"
	"bot/internal/store"
)

// usageProviderCost is one provider row of /api/usage. BudgetUSD 0 means unlimited, and RemainingUSD is null
// in that case (the same convention as the daily limits). Warning is the 80% flag of an active budget.
type usageProviderCost struct {
	Provider     string   `json:"provider"`
	CostUSD      float64  `json:"cost_usd"`
	BudgetUSD    float64  `json:"budget_usd"`
	RemainingUSD *float64 `json:"remaining_usd"`
	Warning      bool     `json:"warning"`
}

// usageProviderCosts builds the provider_costs list: one row per configured provider that either has a budget
// or already spent something today. A provider with neither is omitted, so a deployment that never set a
// budget keeps an empty list and the usage tab stays exactly as it was.
func (a *App) usageProviderCosts(ctx context.Context, now time.Time) ([]usageProviderCost, error) {
	budgets, err := store.ProviderDailyBudgets(ctx, a.db)
	if err != nil {
		return nil, err
	}
	var modelCosts map[string]int64
	if a.cfg.ModelPrices.HasPrices() {
		modelCosts, err = store.ModelDailyCostMicros(ctx, a.db, now, a.cfg.ModelPrices)
		if err != nil {
			return nil, err
		}
	}
	var catalog = a.providerCatalog()
	var costs = model.NewProviderAttribution(catalog).ProviderCosts(modelCosts)
	var names = make([]string, 0, len(catalog))
	var seen = make(map[string]bool, len(catalog))
	for _, entry := range catalog {
		if entry.Provider == "" || seen[entry.Provider] {
			continue
		}
		seen[entry.Provider] = true
		names = append(names, entry.Provider)
	}
	sort.Strings(names)
	var result = make([]usageProviderCost, 0, len(names))
	for _, name := range names {
		var cost = costs[name]
		var budget = budgets[name]
		if budget <= 0 && cost == 0 {
			continue
		}
		var item = usageProviderCost{
			Provider:  name,
			CostUSD:   float64(cost) / 1_000_000,
			BudgetUSD: store.ProviderBudgetUSD(budget),
		}
		if budget > 0 {
			var remaining = budget - cost
			if remaining < 0 {
				remaining = 0
			}
			var remainingUSD = float64(remaining) / 1_000_000
			item.RemainingUSD = &remainingUSD
			var warningAt = (budget*store.ProviderDailyCostWarningPercent + 99) / 100
			item.Warning = cost >= warningAt
		}
		result = append(result, item)
	}
	return result, nil
}
