package bot

// Provider daily budgets. The administrator sets one USD budget per named provider (Kimi, DeepSeek, ...) in
// the Mini App; provider_settings stores it in millionths of a dollar. Before every model call the bot
// prices today's answers, attributes them to providers and refuses a call whose provider already spent its
// budget. An 80% crossing is recorded once per provider per UTC day, so the administrator notices a budget
// before it bites but is not flooded with warnings.
//
// The check is cached because it sits on the model-call hot path: every call used to read provider_settings
// (all budgets) and, for a budgeted provider, the grouped spend table. A 30-second TTL cache now holds both
// snapshots, and a successful provider_settings write in this process bumps store.ProviderSettingsVersion,
// which invalidates the cache immediately (a Mini App save therefore takes effect on the next call, while a
// write made by another instance is visible within 30 seconds). The cache is bounded by the number of
// provider_settings rows: budgets are one map, and spend entries are pruned to the providers that currently
// have a positive budget.
//
// The spend is also invalidated after every completed model call that reported tokens: the next check re-reads
// the grouped spend instead of trusting a snapshot taken before that answer existed. A call that is still in
// flight is therefore the only thing the cache can hide, which is exactly the "one in-flight answer" overrun
// the uncached gate already had. The warning event itself is still checked against the database, so the
// once-per-UTC-day rule stays global across instances.
//
// The check is deliberately cheap on the unchanged path: no budget row (or no MODEL_PRICES) means no spend
// read, a cached empty budget snapshot means no provider_settings read at all, and the default endpoint
// "strata" has no row by construction. Tasks, the Mini App and a plain chat answer all go through callModel,
// so they share this one gate.

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"bot/internal/config"
	"bot/internal/i18n"
	"bot/internal/model"
	"bot/internal/store"
)

const (
	// providerBudgetCacheTTL bounds how long a budget or spend snapshot may be served. It is the documented
	// bound for a provider_settings write made by ANOTHER process; a write in this process invalidates
	// immediately through store.ProviderSettingsVersion.
	providerBudgetCacheTTL = 30 * time.Second

	providerBudgetRatioMetric    = "bot_provider_budget_ratio"
	providerBudgetRatioHelp      = "Today's provider spend divided by its daily budget; 0 when the provider has no budget."
	providerBudgetExceededMetric = "bot_provider_budget_exceeded_total"
	providerBudgetExceededHelp   = "Model calls refused because the provider's daily budget was already spent."
)

// providerBudgetSource is the two reads of the budget gate. It exists so a test can count (and script) the
// store calls without a database shim; production wires storeProviderBudgetSource to the real store.
type providerBudgetSource interface {
	Budgets(ctx context.Context) (map[string]int64, error)
	ModelCosts(ctx context.Context, now time.Time) (map[string]int64, error)
}

// storeProviderBudgetSource is the production reader: two queries of the store package with the prices the
// process was configured with.
type storeProviderBudgetSource struct {
	db     *sql.DB
	prices config.PriceTable
}

func (s storeProviderBudgetSource) Budgets(ctx context.Context) (map[string]int64, error) {
	return store.ProviderDailyBudgets(ctx, s.db)
}

func (s storeProviderBudgetSource) ModelCosts(ctx context.Context, now time.Time) (map[string]int64, error) {
	return store.ModelDailyCostMicros(ctx, s.db, now, s.prices)
}

// providerBudgetCache is the TTL cache of the gate. The zero value is usable and guards both snapshots with
// one mutex, so a refresh is a single-flight: concurrent checks wait for the one query instead of issuing
// their own. The mutex is held across the store call on purpose; the query is one indexed read and deduping it
// is the point of the cache.
type providerBudgetCache struct {
	mu        sync.Mutex
	version   uint64
	fetchedAt time.Time
	budgets   map[string]int64
	spends    map[string]providerBudgetSpend
}

// providerBudgetSpend is today's attributed spend of one budgeted provider and the instant it was read.
type providerBudgetSpend struct {
	micros      int64
	refreshedAt time.Time
	day         time.Time
}

// providerCatalog is the effective provider catalogue: the router's current state when the model client is
// a router, otherwise the environment list (the same base the admin API falls back to when a deployment has
// no usable named provider). It is only used to attribute a model id to a provider.
//
// overrides is captured before SetMetrics wraps the client, because the metrics wrapper does not forward the
// optional ProviderOverrides interface. Without it a production dispatcher would attribute models with the
// environment list and ignore a runtime model edit even though the router itself honoured it.
func (d *Dispatcher) providerCatalog() []model.ProviderCatalogEntry {
	if d.overrides != nil {
		return d.overrides.ProviderCatalog()
	}
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
// endpoint and a deployment without MODEL_PRICES all pass; only a budgeted provider reads the spend, and that
// read is cached for at most 30 seconds.
func (d *Dispatcher) checkProviderBudget(ctx context.Context, chatID int64, userID int64, modelID string) error {
	if d.deps.DB == nil {
		return nil
	}
	// Without prices every cost is zero and a budget could never be reached; skipping both reads keeps an
	// unpriced deployment on exactly the old call path.
	if !d.deps.Cfg.ModelPrices.HasPrices() {
		return nil
	}
	var now = d.now()
	budgets, err := d.providerBudgetSnapshot(ctx, now)
	if err != nil {
		return fmt.Errorf("read provider budgets: %w", err)
	}
	if len(budgets) == 0 {
		return nil
	}
	var attribution = model.NewProviderAttribution(d.providerCatalog())
	var provider = attribution.ProviderFor(modelID)
	if provider == "" || provider == model.DefaultProviderName {
		return nil
	}
	var budget = budgets[provider]
	if budget <= 0 {
		// A provider without a budget is unlimited, not missing: the series stays present at 0 so a
		// dashboard does not mistake "no budget" for "no data".
		d.setProviderBudgetRatio(provider, 0)
		return nil
	}
	spent, err := d.providerSpendSnapshot(ctx, now, provider, budgets, attribution)
	if err != nil {
		return fmt.Errorf("read provider daily cost: %w", err)
	}
	d.setProviderBudgetRatio(provider, float64(spent)/float64(budget))
	var warningAt = (budget*store.ProviderDailyCostWarningPercent + 99) / 100
	if spent >= warningAt {
		warned, warnErr := store.EventKindDetailToday(ctx, d.deps.DB, store.EventProviderBudgetWarning, provider, now)
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
	d.countProviderBudgetRefusal(provider)
	d.logChatEvent(ctx, chatID, userID, store.EventLimit,
		fmt.Sprintf("provider_budget %s %d/%d", provider, spent, budget))
	return &chatLimitRefusal{
		cause: ErrProviderBudgetExceeded,
		text:  i18n.T(d.chatLanguage(ctx, chatID), "ask.provider_budget_limited"),
	}
}

// providerBudgetSource returns the configured reader, falling back to the real store for a Dispatcher built
// as a literal (the production path always goes through New).
func (d *Dispatcher) providerBudgetSource() providerBudgetSource {
	if d.budgetSource != nil {
		return d.budgetSource
	}
	return storeProviderBudgetSource{db: d.deps.DB, prices: d.deps.Cfg.ModelPrices}
}

// providerBudgetSnapshot returns the positive daily budgets, reusing the cached map while it is fresh. A
// version change (a provider_settings write in this process) drops the whole cache: a budget, model list or
// enabled flag edit can change both the budget and the model-to-provider attribution.
func (d *Dispatcher) providerBudgetSnapshot(ctx context.Context, now time.Time) (map[string]int64, error) {
	var cache = &d.providerBudget
	cache.mu.Lock()
	defer cache.mu.Unlock()
	var version = store.ProviderSettingsVersion()
	if cache.version != version {
		cache.version = version
		cache.budgets = nil
		cache.spends = nil
		cache.fetchedAt = time.Time{}
	}
	if cache.budgets != nil && cacheFresh(cache.fetchedAt, now) {
		return cache.budgets, nil
	}
	budgets, err := d.providerBudgetSource().Budgets(ctx)
	if err != nil {
		return nil, err
	}
	if budgets == nil {
		budgets = map[string]int64{}
	}
	cache.budgets = budgets
	cache.fetchedAt = now
	// Spend entries follow the budget set: a removed budget leaves no entry behind, so the cache cannot grow
	// past the provider_settings rows that created it.
	for provider := range cache.spends {
		if _, exists := budgets[provider]; !exists {
			delete(cache.spends, provider)
		}
	}
	return cache.budgets, nil
}

// providerSpendSnapshot returns today's attributed spend of one provider. One grouped store query yields the
// spend of every budgeted provider, so the cache is filled for all of them at once and a second budgeted
// provider checked in the same window is a hit. The entry is scoped to the UTC day as well as the TTL, so
// midnight starts from zero without waiting for the old snapshot to expire.
func (d *Dispatcher) providerSpendSnapshot(ctx context.Context, now time.Time, provider string,
	budgets map[string]int64, attribution model.ProviderAttribution) (int64, error) {
	var cache = &d.providerBudget
	var day = utcDay(now)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if entry, found := cache.spends[provider]; found && entry.day.Equal(day) && cacheFresh(entry.refreshedAt, now) {
		return entry.micros, nil
	}
	modelCosts, err := d.providerBudgetSource().ModelCosts(ctx, now)
	if err != nil {
		return 0, err
	}
	var costs = attribution.ProviderCosts(modelCosts)
	if cache.spends == nil {
		cache.spends = make(map[string]providerBudgetSpend, len(budgets))
	}
	for name := range budgets {
		cache.spends[name] = providerBudgetSpend{micros: costs[name], refreshedAt: now, day: day}
	}
	return costs[provider], nil
}

// invalidateProviderBudgetSpend drops every spend snapshot but keeps the budget snapshot: the answer that just
// finished may already be stored and priced, while provider_settings itself did not change. The next check
// re-reads the grouped spend, so a completed call is never hidden from enforcement for a full TTL.
func (d *Dispatcher) invalidateProviderBudgetSpend() {
	var cache = &d.providerBudget
	cache.mu.Lock()
	cache.spends = nil
	cache.mu.Unlock()
}

// setProviderBudgetRatio publishes spend/budget of one provider. A budget of 0 (unlimited) is published as 0,
// never as a division by zero; the gauge carries only the configured provider name.
func (d *Dispatcher) setProviderBudgetRatio(provider string, ratio float64) {
	var reg = d.metrics.Load()
	if reg == nil || provider == "" {
		return
	}
	reg.Gauge(providerBudgetRatioMetric, providerBudgetRatioHelp).
		Set(map[string]string{"provider": provider}, ratio)
}

// countProviderBudgetRefusal records one model call the provider budget refused. Refusals that hit the cache
// still count, so the alert sees an exhausted budget even when no store query happens.
func (d *Dispatcher) countProviderBudgetRefusal(provider string) {
	var reg = d.metrics.Load()
	if reg == nil || provider == "" {
		return
	}
	reg.Counter(providerBudgetExceededMetric, providerBudgetExceededHelp).
		Add(map[string]string{"provider": provider}, 1)
}

// cacheFresh reports whether a snapshot taken at at may be served at now. A backwards clock is treated as
// stale instead of fresh-forever.
func cacheFresh(at, now time.Time) bool {
	if at.IsZero() {
		return false
	}
	var age = now.Sub(at)
	return age >= 0 && age < providerBudgetCacheTTL
}

// utcDay is the UTC midnight that starts the day of value; it is the same boundary the store groups by.
func utcDay(value time.Time) time.Time {
	year, month, day := value.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
