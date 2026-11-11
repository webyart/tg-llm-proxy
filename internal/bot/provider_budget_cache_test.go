package bot

// Provider budget cache and metrics: the gate must answer the same questions as before while reading
// provider_settings and the grouped spend far less often. These tests use a counting providerBudgetSource so
// a cache hit is proven by a counter, not by a timing assumption, and they still go through callModel where
// the behavior under test is the real one (refusal, warning event, quota order).

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"bot/internal/metrics"
	"bot/internal/model"
	"bot/internal/store"
)

// countingProviderBudgetSource is the two reads of the gate with counters and scripted values. The delay lets
// the concurrency test overlap goroutines long enough for a broken cache to show duplicate queries.
type countingProviderBudgetSource struct {
	mu          sync.Mutex
	budgetCalls int
	costCalls   int
	budgets     map[string]int64
	modelCosts  map[string]int64
	delay       time.Duration
}

func (s *countingProviderBudgetSource) Budgets(context.Context) (map[string]int64, error) {
	s.mu.Lock()
	s.budgetCalls++
	var budgets = make(map[string]int64, len(s.budgets))
	for provider, micros := range s.budgets {
		budgets[provider] = micros
	}
	var delay = s.delay
	s.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	return budgets, nil
}

func (s *countingProviderBudgetSource) ModelCosts(context.Context, time.Time) (map[string]int64, error) {
	s.mu.Lock()
	s.costCalls++
	var costs = make(map[string]int64, len(s.modelCosts))
	for modelID, micros := range s.modelCosts {
		costs[modelID] = micros
	}
	var delay = s.delay
	s.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	return costs, nil
}

func (s *countingProviderBudgetSource) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budgetCalls, s.costCalls
}

func (s *countingProviderBudgetSource) setModelCosts(costs map[string]int64) {
	s.mu.Lock()
	s.modelCosts = costs
	s.mu.Unlock()
}

// newProviderBudgetCacheDispatcher builds the production call path (callModel, real store, real prices)
// with only the two budget reads replaced by the fake.
func newProviderBudgetCacheDispatcher(t *testing.T, source *countingProviderBudgetSource,
	modelClient model.Client) (*Dispatcher, *sql.DB) {
	t.Helper()
	if modelClient == nil {
		modelClient = &fakeModel{response: model.Response{Content: "answer"}}
	}
	var db = testStore(t)
	var d = newTestDispatcher(t, providerBudgetConfig(t), modelClient, &fakeReplier{}, db)
	d.budgetSource = source
	return d, db
}

// fixedClock pins the dispatcher clock so a test can advance past the TTL without sleeping.
func fixedClock(d *Dispatcher, at time.Time) {
	d.now = func() time.Time { return at }
}

// TestProviderBudgetCacheHitSkipsStoreReads: a second call inside the TTL must not read provider_settings or
// the spend table. The fake model reports no tokens, so nothing invalidates the spend snapshot.
func TestProviderBudgetCacheHitSkipsStoreReads(t *testing.T) {
	var source = &countingProviderBudgetSource{
		budgets:    map[string]int64{"kimi": 2_000_000},
		modelCosts: map[string]int64{"kimi-k3": 1_000_000},
	}
	var d, _ = newProviderBudgetCacheDispatcher(t, source, nil)
	var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	fixedClock(d, now)

	for round := 0; round < 3; round++ {
		if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); err != nil {
			t.Fatalf("round %d callModel: %v", round, err)
		}
	}
	if budgetCalls, costCalls := source.counts(); budgetCalls != 1 || costCalls != 1 {
		t.Fatalf("store reads=%d/%d, want 1 budget and 1 spend read for three calls", budgetCalls, costCalls)
	}
}

// TestProviderBudgetCacheTTLExpiry: after 30 seconds both snapshots are re-read, before that they are not.
func TestProviderBudgetCacheTTLExpiry(t *testing.T) {
	var source = &countingProviderBudgetSource{
		budgets:    map[string]int64{"kimi": 2_000_000},
		modelCosts: map[string]int64{"kimi-k3": 1_000_000},
	}
	var d, _ = newProviderBudgetCacheDispatcher(t, source, nil)
	var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	fixedClock(d, now)

	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); err != nil {
		t.Fatalf("first callModel: %v", err)
	}
	fixedClock(d, now.Add(providerBudgetCacheTTL-time.Second))
	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); err != nil {
		t.Fatalf("callModel inside the TTL: %v", err)
	}
	if budgetCalls, costCalls := source.counts(); budgetCalls != 1 || costCalls != 1 {
		t.Fatalf("store reads=%d/%d inside the TTL, want 1/1", budgetCalls, costCalls)
	}
	fixedClock(d, now.Add(providerBudgetCacheTTL+time.Second))
	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); err != nil {
		t.Fatalf("callModel after the TTL: %v", err)
	}
	if budgetCalls, costCalls := source.counts(); budgetCalls != 2 || costCalls != 2 {
		t.Fatalf("store reads=%d/%d after the TTL, want 2/2", budgetCalls, costCalls)
	}
}

// TestProviderBudgetCacheInvalidatedBySetProviderBudget: a committed provider_settings write in this process
// bumps store.ProviderSettingsVersion and the next check re-reads the budget immediately, without waiting for
// the TTL. This is the path a Mini App budget edit takes.
func TestProviderBudgetCacheInvalidatedBySetProviderBudget(t *testing.T) {
	var source = &countingProviderBudgetSource{
		budgets:    map[string]int64{"kimi": 2_000_000},
		modelCosts: map[string]int64{"kimi-k3": 1_000_000},
	}
	var d, db = newProviderBudgetCacheDispatcher(t, source, nil)
	var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	fixedClock(d, now)

	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); err != nil {
		t.Fatalf("first callModel: %v", err)
	}
	if err := store.SetProviderBudget(context.Background(), db, "kimi", 4_000_000, store.ProviderSettings{
		Provider: "kimi", Models: []string{"kimi-k3"}, DefaultModel: "kimi-k3", Enabled: true,
	}); err != nil {
		t.Fatalf("SetProviderBudget: %v", err)
	}
	source.mu.Lock()
	source.budgets = map[string]int64{"kimi": 4_000_000}
	source.mu.Unlock()
	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); err != nil {
		t.Fatalf("callModel after the write: %v", err)
	}
	if budgetCalls, costCalls := source.counts(); budgetCalls != 2 || costCalls != 2 {
		t.Fatalf("store reads=%d/%d after the write, want an immediate re-read", budgetCalls, costCalls)
	}
}

// TestProviderBudgetSpendInvalidatedByCompletedCall: an answer that reported tokens drops the spend snapshot,
// so the next check sees the answer instead of a pre-call value, while the unchanged budget snapshot survives.
func TestProviderBudgetSpendInvalidatedByCompletedCall(t *testing.T) {
	var source = &countingProviderBudgetSource{
		budgets:    map[string]int64{"kimi": 10_000_000},
		modelCosts: map[string]int64{"kimi-k3": 1_000_000},
	}
	var reply = &fakeModel{response: model.Response{
		Content: "answer", TokensIn: intPtr(10), TokensOut: intPtr(5),
	}}
	var d, _ = newProviderBudgetCacheDispatcher(t, source, reply)
	var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	fixedClock(d, now)

	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); err != nil {
		t.Fatalf("first callModel: %v", err)
	}
	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); err != nil {
		t.Fatalf("second callModel: %v", err)
	}
	if budgetCalls, costCalls := source.counts(); budgetCalls != 1 || costCalls != 2 {
		t.Fatalf("store reads=%d/%d, want the budget cached and the spend refreshed", budgetCalls, costCalls)
	}
}

// TestProviderBudgetRefusalWithCachedSpend: a refused call never reaches the model, and a second refused call
// inside the TTL is answered from the cache without another store read.
func TestProviderBudgetRefusalWithCachedSpend(t *testing.T) {
	var source = &countingProviderBudgetSource{
		budgets:    map[string]int64{"kimi": 2_000_000},
		modelCosts: map[string]int64{"kimi-k3": 2_500_000},
	}
	var reply = &fakeModel{response: model.Response{Content: "should not be called"}}
	var d, _ = newProviderBudgetCacheDispatcher(t, source, reply)
	var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	fixedClock(d, now)

	for round := 0; round < 2; round++ {
		if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); !errors.Is(err, ErrProviderBudgetExceeded) {
			t.Fatalf("round %d error=%v, want ErrProviderBudgetExceeded", round, err)
		}
	}
	if reply.callCount() != 0 {
		t.Fatalf("the model was called %d times, want 0", reply.callCount())
	}
	if budgetCalls, costCalls := source.counts(); budgetCalls != 1 || costCalls != 1 {
		t.Fatalf("store reads=%d/%d, want the cached refusal", budgetCalls, costCalls)
	}
}

// TestProviderBudgetCacheHasNoCrossProviderLeak: one exhausted provider does not borrow the spend of another,
// and the cached grouped read is shared by both providers instead of being queried per provider.
func TestProviderBudgetCacheHasNoCrossProviderLeak(t *testing.T) {
	var source = &countingProviderBudgetSource{
		budgets: map[string]int64{"kimi": 2_000_000, "deepseek": 1_000_000},
		modelCosts: map[string]int64{
			"kimi-k3":        2_000_000, // exactly kimi's budget
			"deepseek-flash": 100_000,   // 10% of deepseek's budget
		},
	}
	var reply = &fakeModel{response: model.Response{Content: "answer"}}
	var d, _ = newProviderBudgetCacheDispatcher(t, source, reply)
	var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	fixedClock(d, now)

	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "kimi:kimi-k3"}, nil, nil); !errors.Is(err, ErrProviderBudgetExceeded) {
		t.Fatalf("kimi error=%v, want ErrProviderBudgetExceeded", err)
	}
	if _, err := d.callModel(context.Background(), 11, model.Request{Model: "deepseek:deepseek-flash"}, nil, nil); err != nil {
		t.Fatalf("deepseek error=%v, want nil (its own spend is far below its budget)", err)
	}
	if reply.callCount() != 1 {
		t.Fatalf("model calls=%d, want exactly the deepseek call", reply.callCount())
	}
	if budgetCalls, costCalls := source.counts(); budgetCalls != 1 || costCalls != 1 {
		t.Fatalf("store reads=%d/%d, want one shared grouped read", budgetCalls, costCalls)
	}
}

// TestProviderBudgetCacheIsConcurrencySafe: concurrent checks serialize on the cache and issue one read, and
// no check observes another provider's budget. The fake sleeps inside the read to widen the window.
func TestProviderBudgetCacheIsConcurrencySafe(t *testing.T) {
	var source = &countingProviderBudgetSource{
		budgets:    map[string]int64{"kimi": 2_000_000, "deepseek": 1_000_000},
		modelCosts: map[string]int64{"kimi-k3": 500_000, "deepseek-flash": 100_000},
		delay:      5 * time.Millisecond,
	}
	var d, _ = newProviderBudgetCacheDispatcher(t, source, nil)
	fixedClock(d, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))

	var wg sync.WaitGroup
	var errs = make(chan error, 16)
	for i := 0; i < 16; i++ {
		var modelID = "kimi:kimi-k3"
		if i%2 == 1 {
			modelID = "deepseek:deepseek-flash"
		}
		wg.Add(1)
		go func(modelID string) {
			defer wg.Done()
			errs <- d.checkProviderBudget(context.Background(), 0, 11, modelID)
		}(modelID)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent checkProviderBudget: %v", err)
		}
	}
	if budgetCalls, costCalls := source.counts(); budgetCalls != 1 || costCalls != 1 {
		t.Fatalf("store reads=%d/%d, want one budget and one spend read for 16 concurrent checks", budgetCalls, costCalls)
	}
}

// TestProviderBudgetMetrics: the gauge is spend/budget (0 for an unlimited provider) and the counter counts
// refusals, both labelled only by provider.
func TestProviderBudgetMetrics(t *testing.T) {
	var source = &countingProviderBudgetSource{
		budgets:    map[string]int64{"kimi": 2_000_000},
		modelCosts: map[string]int64{"kimi-k3": 1_000_000},
	}
	var d, db = newProviderBudgetCacheDispatcher(t, source, &fakeModel{response: model.Response{Content: "answer"}})
	var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	fixedClock(d, now)
	var reg = metrics.NewRegistry()
	d.SetMetrics(reg)

	if err := d.checkProviderBudget(context.Background(), 0, 11, "kimi:kimi-k3"); err != nil {
		t.Fatalf("kimi check: %v", err)
	}
	if err := d.checkProviderBudget(context.Background(), 0, 11, "deepseek:deepseek-flash"); err != nil {
		t.Fatalf("deepseek check: %v", err)
	}
	var body = metricsBody(t, reg)
	for _, want := range []string{
		"# TYPE bot_provider_budget_ratio gauge",
		"# TYPE bot_provider_budget_exceeded_total counter",
		`bot_provider_budget_ratio{provider="kimi"} 0.5`,
		// No deepseek budget: the checked provider is unlimited, not absent.
		`bot_provider_budget_ratio{provider="deepseek"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics miss %q:\n%s", want, body)
		}
	}
	// Raise kimi's spend and invalidate through the store version, then refuse once.
	source.setModelCosts(map[string]int64{"kimi-k3": 2_500_000})
	if err := store.SetProviderBudget(context.Background(), db, "kimi", 2_000_000, store.ProviderSettings{
		Provider: "kimi", Models: []string{"kimi-k3"}, DefaultModel: "kimi-k3", Enabled: true,
	}); err != nil {
		t.Fatalf("SetProviderBudget: %v", err)
	}
	if err := d.checkProviderBudget(context.Background(), 0, 11, "kimi:kimi-k3"); !errors.Is(err, ErrProviderBudgetExceeded) {
		t.Fatalf("exhausted check error=%v, want ErrProviderBudgetExceeded", err)
	}
	body = metricsBody(t, reg)
	for _, want := range []string{
		`bot_provider_budget_ratio{provider="kimi"} 1.25`,
		`bot_provider_budget_exceeded_total{provider="kimi"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics after refusal miss %q:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{"chat_id", "user_id", `model="kimi-k3"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("provider budget metrics expose %q:\n%s", forbidden, body)
		}
	}
}

// TestProviderCatalogKeepsRuntimeOverridesAfterMetrics: SetMetrics wraps the model client and the wrapper does
// not forward ProviderOverrides. The dispatcher must still attribute with the router's edited catalogue, or a
// model moved between providers would be charged to the wrong budget.
func TestProviderCatalogKeepsRuntimeOverridesAfterMetrics(t *testing.T) {
	var client, _, _, _ = providerBudgetModelRouter()
	var overrides, ok = client.(model.ProviderOverrides)
	if !ok {
		t.Fatal("the test router does not implement ProviderOverrides")
	}
	if err := overrides.SetProviderModels("kimi", []string{"kimi-new"}, "kimi-new", true); err != nil {
		t.Fatalf("SetProviderModels: %v", err)
	}
	var d = newTestDispatcher(t, providerBudgetConfig(t), client, &fakeReplier{}, testStore(t))
	d.SetMetrics(metrics.NewRegistry())

	for _, entry := range d.providerCatalog() {
		if entry.Provider != "kimi" {
			continue
		}
		if len(entry.Models) != 1 || entry.Models[0] != "kimi-new" {
			t.Fatalf("catalog kimi models=%v, want the runtime override", entry.Models)
		}
		return
	}
	t.Fatal("the provider catalogue lost kimi")
}
