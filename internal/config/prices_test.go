package config

import (
	"strings"
	"testing"
)

func priceEnv(prices string) map[string]string {
	return map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:secret",
		"MODEL_PROVIDERS":    `[{"name":"deepseek","base_url":"https://api.deepseek.com/v1","models":["deepseek-flash","deepseek-chat"]},{"name":"kimi","base_url":"https://api.moonshot.ai/v1","models":["kimi-k3"]}]`,
		"MODEL_PRICES":       prices,
	}
}

func TestLoadFromModelPricesPlainAndProviderPrefixed(t *testing.T) {
	cfg, problems := LoadFrom(priceEnv(`[
		{"model":"deepseek-flash","input_per_mtok_usd":0.27,"output_per_mtok_usd":1.10},
		{"model":"kimi:kimi-k3","input_per_mtok_usd":2,"output_per_mtok_usd":8},
		{"model":"deepseek-chat","provider":"deepseek","input_per_mtok_usd":0.5,"output_per_mtok_usd":1.5}
	]`))
	if len(problems) != 0 {
		t.Fatalf("LoadFrom problems: %v", problems)
	}
	if len(cfg.ModelPrices) != 3 {
		t.Fatalf("ModelPrices=%+v, want 3", cfg.ModelPrices)
	}
	if got, ok := cfg.ModelPrices.Lookup("deepseek-flash"); !ok || got.InputPerMTokUSD != 0.27 || got.OutputPerMTokUSD != 1.10 {
		t.Fatalf("plain lookup=%+v ok=%v", got, ok)
	}
	// A plain row applies to a provider-prefixed id of any provider with that model.
	if got, ok := cfg.ModelPrices.Lookup("kimi:deepseek-flash"); !ok || got.InputPerMTokUSD != 0.27 {
		t.Fatalf("plain row did not apply to kimi:deepseek-flash: %+v ok=%v", got, ok)
	}
	if got, ok := cfg.ModelPrices.Lookup("kimi:kimi-k3"); !ok || got.Provider != "kimi" || got.Model != "kimi-k3" || got.InputPerMTokUSD != 2 {
		t.Fatalf("provider-prefixed lookup=%+v ok=%v", got, ok)
	}
	// The separate provider field is the same key as the prefixed spelling.
	if got, ok := cfg.ModelPrices.Lookup("deepseek:deepseek-chat"); !ok || got.InputPerMTokUSD != 0.5 {
		t.Fatalf("provider field lookup=%+v ok=%v", got, ok)
	}
}

func TestPriceTableScopeAndCostMath(t *testing.T) {
	prices := PriceTable{{Model: "prefixed", Provider: "deepseek", InputPerMTokUSD: 1, OutputPerMTokUSD: 2}}
	if _, ok := prices.Lookup("prefixed"); ok {
		t.Fatal("a provider-prefixed row matched a plain model id without a provider")
	}
	if _, ok := prices.Lookup("kimi:prefixed"); ok {
		t.Fatal("a provider-prefixed row matched another provider")
	}
	if _, ok := prices.Lookup("deepseek:prefixed"); !ok {
		t.Fatal("the exact provider-prefixed row did not match")
	}

	both := PriceTable{
		{Model: "m", InputPerMTokUSD: 1},
		{Model: "m", Provider: "deepseek", InputPerMTokUSD: 2},
	}
	if got, ok := both.Lookup("deepseek:m"); !ok || got.InputPerMTokUSD != 2 {
		t.Fatalf("the exact provider row did not win: %+v ok=%v", got, ok)
	}
	if got, ok := both.Lookup("kimi:m"); !ok || got.InputPerMTokUSD != 1 {
		t.Fatalf("the plain row did not cover another provider: %+v ok=%v", got, ok)
	}

	plain := PriceTable{{Model: "m", InputPerMTokUSD: 0.27, OutputPerMTokUSD: 1.10}}
	if got := plain.CostMicros("m", 1_000_000, 0); got != 270_000 {
		t.Fatalf("input cost micros=%d, want 270000", got)
	}
	if got := plain.CostMicros("m", 0, 1_000_000); got != 1_100_000 {
		t.Fatalf("output cost micros=%d, want 1100000", got)
	}
	if got := plain.CostUSD("m", 1_000_000, 0); got != 0.27 {
		t.Fatalf("input cost USD=%v, want 0.27", got)
	}
	if got := plain.CostUSD("m", 1, 1); got != 0.000001 {
		t.Fatalf("two-token cost=%v, want 0.000001 (rounded to micro-dollars)", got)
	}
	if got := plain.CostUSD("unpriced", 1_000_000, 1_000_000); got != 0 {
		t.Fatalf("unpriced model cost=%v, want 0", got)
	}
	if (PriceTable{}).HasPrices() {
		t.Fatal("an empty table reported prices")
	}
	if !plain.HasPrices() {
		t.Fatal("a positive table reported no prices")
	}
	if (PriceTable{{Model: "zero"}}).HasPrices() {
		t.Fatal("an all-zero table reported prices")
	}
}

func TestLoadFromModelPricesEmptyMeansNoPrices(t *testing.T) {
	for name, row := range map[string]string{"missing": "", "empty": "[]"} {
		raw := priceEnv(row)
		if name == "missing" {
			delete(raw, "MODEL_PRICES")
		}
		cfg, problems := LoadFrom(raw)
		if len(problems) != 0 {
			t.Errorf("%s: problems=%v", name, problems)
		}
		if len(cfg.ModelPrices) != 0 || cfg.ModelPrices.HasPrices() {
			t.Errorf("%s: ModelPrices=%+v, want none", name, cfg.ModelPrices)
		}
		if got := cfg.ModelPrices.CostUSD("deepseek-flash", 1_000_000, 1_000_000); got != 0 {
			t.Errorf("%s: cost=%v, want 0", name, got)
		}
	}
}

func TestLoadFromModelPricesRejectsBadRows(t *testing.T) {
	hundredOne := make([]string, MaxModelPrices+1)
	for index := range hundredOne {
		hundredOne[index] = `{"model":"m` + string(rune('a'+index%26)) + `","input_per_mtok_usd":1,"output_per_mtok_usd":1}`
	}
	tests := map[string]string{
		"not_json":         `{`,
		"empty_model":      `[{"model":"  ","input_per_mtok_usd":1,"output_per_mtok_usd":1}]`,
		"negative_input":   `[{"model":"m","input_per_mtok_usd":-1,"output_per_mtok_usd":1}]`,
		"negative_output":  `[{"model":"m","input_per_mtok_usd":1,"output_per_mtok_usd":-1}]`,
		"too_expensive":    `[{"model":"m","input_per_mtok_usd":10000.01,"output_per_mtok_usd":1}]`,
		"unknown_provider": `[{"model":"m","provider":"nope","input_per_mtok_usd":1,"output_per_mtok_usd":1}]`,
		"unknown_prefix":   `[{"model":"nope:m","input_per_mtok_usd":1,"output_per_mtok_usd":1}]`,
		"duplicate":        `[{"model":"m","input_per_mtok_usd":1,"output_per_mtok_usd":1},{"model":"m","input_per_mtok_usd":2,"output_per_mtok_usd":2}]`,
		"duplicate_key":    `[{"model":"m","provider":"deepseek","input_per_mtok_usd":1,"output_per_mtok_usd":1},{"model":"deepseek:m","input_per_mtok_usd":2,"output_per_mtok_usd":2}]`,
		"too_many":         `[` + strings.Join(hundredOne, ",") + `]`,
	}
	for name, row := range tests {
		cfg, problems := LoadFrom(priceEnv(row))
		joined := strings.Join(problems, "\n")
		if !strings.Contains(joined, "MODEL_PRICES") {
			t.Errorf("%s: problems do not name MODEL_PRICES: %v", name, problems)
		}
		if len(cfg.ModelPrices) != 0 {
			t.Errorf("%s: a broken table must stay inactive: %+v", name, cfg.ModelPrices)
		}
	}
}

func TestPriceKeyNormalizesTheTwoSpellings(t *testing.T) {
	provider, model := priceKey(ModelPrice{Model: " deepseek : deepseek-flash ", Provider: " deepseek "})
	if provider != "deepseek" || model != "deepseek-flash" {
		t.Fatalf("priceKey=%q/%q", provider, model)
	}
	if got := priceKeyText("", "m"); got != "m" {
		t.Fatalf("priceKeyText plain=%q", got)
	}
	if got := priceKeyText("deepseek", "m"); got != "deepseek:m" {
		t.Fatalf("priceKeyText provider=%q", got)
	}
}
