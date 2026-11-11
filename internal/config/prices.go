package config

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Limits of MODEL_PRICES. A hundred rows cover a small catalogue; ten thousand dollars per million
// tokens is already far above any real API and keeps a typo from turning into an absurd budget.
const (
	MaxModelPrices    = 100
	MaxPricePerMTok   = 10000.0
	priceMicrosPerUSD = 1_000_000.0
)

// ModelPrice is one row of MODEL_PRICES: the price of one model in USD per million tokens. Provider is
// optional. An empty provider means the row applies to that model on any provider; a set provider (or a
// "provider:model" spelling of Model) narrows it to that provider. The wire form is the same JSON object
// the operator writes in the environment.
type ModelPrice struct {
	Model            string  `json:"model"`
	Provider         string  `json:"provider,omitempty"`
	InputPerMTokUSD  float64 `json:"input_per_mtok_usd"`
	OutputPerMTokUSD float64 `json:"output_per_mtok_usd"`
}

// PriceTable is the validated MODEL_PRICES list. The zero value and an empty list both mean "no prices":
// every cost is zero and the per-chat cost budget cannot be enforced.
type PriceTable []ModelPrice

// HasPrices reports whether at least one row carries a positive price. A table of explicit zeroes is the
// same as an absent one for enforcement: with every token free a budget could never be reached.
func (t PriceTable) HasPrices() bool {
	for _, price := range t {
		if price.InputPerMTokUSD > 0 || price.OutputPerMTokUSD > 0 {
			return true
		}
	}
	return false
}

// priceKey normalizes one row to the internal "provider:model" or "model" key. A model written with a
// provider prefix is the same key as a separate provider field: both spellings describe one price.
func priceKey(price ModelPrice) (provider, model string) {
	provider = strings.TrimSpace(price.Provider)
	model = strings.TrimSpace(price.Model)
	if prefix, rest, ok := strings.Cut(model, ":"); ok && strings.TrimSpace(prefix) != "" && strings.TrimSpace(rest) != "" {
		if provider == "" {
			provider = strings.TrimSpace(prefix)
		}
		model = strings.TrimSpace(rest)
	}
	return provider, model
}

func priceKeyText(provider, model string) string {
	if provider == "" {
		return model
	}
	return provider + ":" + model
}

// Lookup finds the price of a model. The argument may be plain ("deepseek-flash") or provider-prefixed
// ("deepseek:deepseek-flash"). An exact provider row wins; a plain row applies to any provider with that
// model. A plain argument can only match a plain row, because the provider is not known.
func (t PriceTable) Lookup(name string) (ModelPrice, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ModelPrice{}, false
	}
	provider, model, prefixed := "", name, false
	if prefix, rest, ok := strings.Cut(name, ":"); ok && prefix != "" && rest != "" {
		provider, model, prefixed = prefix, rest, true
	}
	if prefixed {
		// An exact provider row always wins over the provider-agnostic row for the same model, no matter
		// which one the operator wrote first.
		for _, price := range t {
			rowProvider, rowModel := priceKey(price)
			if rowModel == model && rowProvider == provider {
				return normalizePrice(price), true
			}
		}
		for _, price := range t {
			rowProvider, rowModel := priceKey(price)
			if rowModel == model && rowProvider == "" {
				return normalizePrice(price), true
			}
		}
		return ModelPrice{}, false
	}
	for _, price := range t {
		rowProvider, rowModel := priceKey(price)
		if rowModel == model && rowProvider == "" {
			return normalizePrice(price), true
		}
	}
	return ModelPrice{}, false
}

func normalizePrice(price ModelPrice) ModelPrice {
	provider, model := priceKey(price)
	price.Provider = provider
	price.Model = model
	return price
}

// CostMicros is the cost of one answer in millionths of a dollar, the unit the chat budget is stored in.
// One USD per million tokens equals one micro-dollar per token, so tokens × price is already micros:
// rounding it once gives exactly the six decimals the API reports and the budget compares.
func (t PriceTable) CostMicros(model string, tokensIn, tokensOut int64) int64 {
	price, ok := t.Lookup(model)
	if !ok {
		return 0
	}
	if tokensIn < 0 {
		tokensIn = 0
	}
	if tokensOut < 0 {
		tokensOut = 0
	}
	var micros = float64(tokensIn)*price.InputPerMTokUSD + float64(tokensOut)*price.OutputPerMTokUSD
	if math.IsNaN(micros) || math.IsInf(micros, 0) {
		return 0
	}
	return int64(math.Round(micros))
}

// CostUSD is CostMicros in dollars with the six decimals of the usage API.
func (t PriceTable) CostUSD(model string, tokensIn, tokensOut int64) float64 {
	return float64(t.CostMicros(model, tokensIn, tokensOut)) / priceMicrosPerUSD
}

// parseModelPrices validates MODEL_PRICES. The row is all or nothing, exactly like MODEL_PROVIDERS: one
// broken entry rejects the whole list, so a typo cannot silently make the budget cheaper than intended.
func parseModelPrices(raw string, providers []ProviderConfig) (PriceTable, []string) {
	var prices []ModelPrice
	if err := json.Unmarshal([]byte(raw), &prices); err != nil {
		return nil, []string{"ENV MODEL_PRICES must be a JSON array of {model, input_per_mtok_usd, output_per_mtok_usd, provider?}: " + err.Error()}
	}
	if len(prices) == 0 {
		return nil, nil
	}
	if len(prices) > MaxModelPrices {
		return nil, []string{fmt.Sprintf("ENV MODEL_PRICES lists %d entries, at most %d are allowed", len(prices), MaxModelPrices)}
	}
	var known = make(map[string]bool, len(providers))
	for _, provider := range providers {
		known[provider.Name] = true
	}

	var problems []string
	var seen = map[string]bool{}
	for index := range prices {
		price := &prices[index]
		price.Model = strings.TrimSpace(price.Model)
		price.Provider = strings.TrimSpace(price.Provider)
		// A row may spell the provider either in the model ("deepseek:m") or in its own field. Both
		// spellings are the same key, but naming two different providers is a typo, not a scope.
		if prefix, _, ok := strings.Cut(price.Model, ":"); ok && price.Provider != "" &&
			strings.TrimSpace(prefix) != "" && strings.TrimSpace(prefix) != price.Provider {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PRICES entry %d names provider %q in the model but %q in the provider field", index, strings.TrimSpace(prefix), price.Provider))
			continue
		}
		provider, model := priceKey(*price)
		label := fmt.Sprintf("entry %d", index)
		if model != "" {
			label = fmt.Sprintf("entry %d (%s)", index, priceKeyText(provider, model))
		}
		if model == "" {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PRICES %s has no model", label))
			continue
		}
		if provider != "" && !known[provider] {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PRICES %s names provider %q, which is not configured in MODEL_PROVIDERS", label, provider))
			continue
		}
		if price.InputPerMTokUSD < 0 || price.InputPerMTokUSD > MaxPricePerMTok {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PRICES %s input_per_mtok_usd must be between 0 and %g", label, MaxPricePerMTok))
			continue
		}
		if price.OutputPerMTokUSD < 0 || price.OutputPerMTokUSD > MaxPricePerMTok {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PRICES %s output_per_mtok_usd must be between 0 and %g", label, MaxPricePerMTok))
			continue
		}
		if math.IsNaN(price.InputPerMTokUSD) || math.IsInf(price.InputPerMTokUSD, 0) ||
			math.IsNaN(price.OutputPerMTokUSD) || math.IsInf(price.OutputPerMTokUSD, 0) {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PRICES %s has a non-finite price", label))
			continue
		}
		key := priceKeyText(provider, model)
		if seen[key] {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PRICES lists %q twice: keys must be unique", key))
			continue
		}
		seen[key] = true
		price.Provider = provider
		price.Model = model
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return PriceTable(prices), nil
}
