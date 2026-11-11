package model

import "testing"

func attributionCatalog() []ProviderCatalogEntry {
	return []ProviderCatalogEntry{
		{
			Provider: "kimi", Models: []string{"kimi-k3", "kimi-k2"},
			DefaultModel: "kimi-k3", Enabled: true, Source: ProviderSourceEnv,
		},
		{
			Provider: "deepseek", Models: []string{"deepseek-flash", "kimi-k2"},
			DefaultModel: "deepseek-flash", Enabled: true, Source: ProviderSourceEnv,
		},
		{
			Provider: "local", Models: []string{"qwen"},
			DefaultModel: "qwen", Enabled: false, Source: ProviderSourceDB,
		},
	}
}

// TestProviderAttributionPrefersTheExplicitPrefix: a "provider:model" id is charged to the named provider
// even when the plain model would be ambiguous, and "strata:" is the default endpoint.
func TestProviderAttributionPrefersTheExplicitPrefix(t *testing.T) {
	var attribution = NewProviderAttribution(attributionCatalog())
	for _, test := range []struct {
		model string
		want  string
	}{
		{"kimi:kimi-k3", "kimi"},
		{"deepseek:kimi-k2", "deepseek"},
		{"kimi:kimi-k2", "kimi"},
		{"strata:kimi-k3", DefaultProviderName},
		{"strata:", DefaultProviderName},
		{"unknown-model", DefaultProviderName},
		{"openai:gpt-4", DefaultProviderName},
		{"", DefaultProviderName},
		{"  kimi:kimi-k3  ", "kimi"},
	} {
		if got := attribution.ProviderFor(test.model); got != test.want {
			t.Errorf("ProviderFor(%q)=%q, want %q", test.model, got, test.want)
		}
	}
}

// TestProviderAttributionPlainModels: a plain id is charged to the single provider that lists it, and an id
// listed by two providers (kimi-k2 here) is ambiguous and charged to the default endpoint.
func TestProviderAttributionPlainModels(t *testing.T) {
	var attribution = NewProviderAttribution(attributionCatalog())
	for _, test := range []struct {
		model string
		want  string
	}{
		{"kimi-k3", "kimi"},
		{"deepseek-flash", "deepseek"},
		{"kimi-k2", DefaultProviderName},
		{"qwen", "local"},
		{"qwen2.5-7b-instruct", DefaultProviderName},
	} {
		if got := attribution.ProviderFor(test.model); got != test.want {
			t.Errorf("ProviderFor(%q)=%q, want %q", test.model, got, test.want)
		}
	}
}

// TestProviderAttributionEmptyCatalogKeepsTheDefaultPath: a deployment without named providers charges every
// model to the default endpoint, so the chat/default path is unchanged by this feature.
func TestProviderAttributionEmptyCatalogKeepsTheDefaultPath(t *testing.T) {
	var attribution = NewProviderAttribution(nil)
	for _, model := range []string{"kimi-k3", "kimi:kimi-k3", ""} {
		if got := attribution.ProviderFor(model); got != DefaultProviderName {
			t.Errorf("ProviderFor(%q)=%q, want %q", model, got, DefaultProviderName)
		}
	}
}

// TestProviderAttributionCostsGroupByProvider: per-model micros are summed per provider, including the
// default endpoint, and a model with zero cost keeps its provider key rather than vanishing.
func TestProviderAttributionCostsGroupByProvider(t *testing.T) {
	var attribution = NewProviderAttribution(attributionCatalog())
	var costs = attribution.ProviderCosts(map[string]int64{
		"kimi-k3":        1_000,
		"deepseek-flash": 250,
		"kimi-k2":        0, // ambiguous -> default endpoint
		"strata-model":   7,
	})
	if costs["kimi"] != 1_000 {
		t.Errorf("kimi cost=%d, want 1000", costs["kimi"])
	}
	if costs["deepseek"] != 250 {
		t.Errorf("deepseek cost=%d, want 250", costs["deepseek"])
	}
	if costs[DefaultProviderName] != 7 {
		t.Errorf("default cost=%d, want 7 (the ambiguous model adds 0)", costs[DefaultProviderName])
	}
	if costs["local"] != 0 {
		t.Errorf("local cost=%d, want 0", costs["local"])
	}
	if got := attribution.ProviderCosts(nil); len(got) != 0 {
		t.Errorf("empty costs=%v, want empty", got)
	}
}
