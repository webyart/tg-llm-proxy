package bot

// Provider daily budgets. The administrator sets one USD budget per named provider (Kimi, DeepSeek, ...) in
// the Mini App; provider_settings stores it in millionths of a dollar. Before every model call the bot
// prices today's answers, attributes them to providers and refuses a call whose provider already spent its
// budget. An 80% crossing is recorded once per provider per UTC day, so the administrator notices a budget
// before it bites but is not flooded with warnings.
//
// The check is deliberately cheap on the unchanged path: no budget row (or no MODEL_PRICES) means no extra
// database read at all, and the default endpoint "strata" has no row by construction. Tasks, the Mini App
// and a plain chat answer all go through callModel, so they share this one gate.

import (
	"context"
	"fmt"

	"bot/internal/i18n"
	"bot/internal/model"
	"bot/internal/store"
)

// providerCatalog is the effective provider catalogue: the router's current state when the model client is
// a router, otherwise the environment list (the same base the admin API falls back to when a deployment has
// no usable named provider). It is only used to attribute a model id to a provider.
func (d *Dispatcher) providerCatalog() []model.ProviderCatalogEntry {
	if overrides, ok := d.deps.Model.(model.ProviderOverrides); ok {
		return overrides.ProviderCatalog()
	}
	var entries = make([]model.ProviderCatalogEntry, 0, len(d.deps.Cfg.ModelProviders))
	for _, provider := range d.deps.Cfg.ModelProviders {
		var models = append([]string{}, provider.Models...)
		var defaultModel = provider.DefaultModel
		if defaultModel == "" && len(models) > 0 {
			defaultModel = models[0]
		}
		entries = append(entries, model.ProviderCatalogEntry{
			Provider: provider.Name, Models: models, DefaultModel: defaultModel,
			Enabled: true, Source: model.ProviderSourceEnv,
		})
	}
	return entries
}

// checkProviderBudget refuses a model call when the provider that owns modelID has already spent its daily
// budget. It runs BEFORE the per-user reservation in callModel: a call the provider cannot serve must not
// spend the user's daily quota either. A provider without a row, a budget of 0 (unlimited), the default
// endpoint and a deployment without MODEL_PRICES all pass without a database read of the spend.
func (d *Dispatcher) checkProviderBudget(ctx context.Context, chatID int64, userID int64, modelID string) error {
	if d.deps.DB == nil {
		return nil
	}
	budgets, err := store.ProviderDailyBudgets(ctx, d.deps.DB)
	if err != nil {
		return fmt.Errorf("read provider budgets: %w", err)
	}
	if len(budgets) == 0 {
		return nil
	}
	// Without prices every cost is zero and a budget could never be reached; skipping the grouped spend
	// query keeps an unpriced deployment on exactly the old call path.
	if !d.deps.Cfg.ModelPrices.HasPrices() {
		return nil
	}
	var attribution = model.NewProviderAttribution(d.providerCatalog())
	var provider = attribution.ProviderFor(modelID)
	if provider == "" || provider == model.DefaultProviderName {
		return nil
	}
	var budget = budgets[provider]
	if budget <= 0 {
		return nil
	}
	modelCosts, err := store.ModelDailyCostMicros(ctx, d.deps.DB, d.now(), d.deps.Cfg.ModelPrices)
	if err != nil {
		return fmt.Errorf("read provider daily cost: %w", err)
	}
	var spent = attribution.ProviderCosts(modelCosts)[provider]
	var warningAt = (budget*store.ProviderDailyCostWarningPercent + 99) / 100
	if spent >= warningAt {
		warned, warnErr := store.EventKindDetailToday(ctx, d.deps.DB, store.EventProviderBudgetWarning, provider, d.now())
		if warnErr != nil {
			return fmt.Errorf("read provider budget warning: %w", warnErr)
		}
		if !warned {
			d.logChatEvent(ctx, chatID, userID, store.EventProviderBudgetWarning, provider)
			if d.deps.Logger != nil {
				d.deps.Logger.Warn("provider daily budget is nearly exhausted",
					"provider", provider, "spent_micros", spent, "budget_micros", budget)
			}
		}
	}
	if spent < budget {
		return nil
	}
	d.logChatEvent(ctx, chatID, userID, store.EventLimit,
		fmt.Sprintf("provider_budget %s %d/%d", provider, spent, budget))
	return &chatLimitRefusal{
		cause: ErrProviderBudgetExceeded,
		text:  i18n.T(d.chatLanguage(ctx, chatID), "ask.provider_budget_limited"),
	}
}
