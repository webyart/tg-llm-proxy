// provider_attribution.go — which provider is charged for a stored model id.
//
// A daily provider budget is an administrator-level number: it covers every chat, task and Mini App answer
// that reaches one provider. The store keeps only the tokens and the model id of an answer, so the provider
// of a stored answer has to be derived from the effective catalogue (MODEL_PROVIDERS plus any runtime
// override) rather than from the row itself.
//
// The rule is deliberately conservative:
//
//   - "provider:model" names its provider explicitly and is charged to it (also "strata:model", which is the
//     default endpoint and therefore never budgeted);
//   - a plain model id is charged to the single configured provider that lists it;
//   - a plain model that no provider lists (the STRATA catalogue) and a model that more than one provider
//     lists are charged to the default endpoint. Ambiguity is resolved toward "no provider budget" on
//     purpose: charging provider A for a model that provider B also serves would let one provider's budget
//     refuse the other's calls.
//
// The same helper serves the bot (enforcement), the admin provider card and /usage, so the number the
// administrator sees and the number the bot compares with the budget are produced by one rule.
package model

import "strings"

// ProviderAttribution maps a model id to the provider whose daily budget it consumes. The zero value is
// usable: it has no configured provider, so every model belongs to the default endpoint.
type ProviderAttribution struct {
	// explicit is the set of configured provider names: only these can be named before the colon.
	explicit map[string]bool
	// listedBy is the set of providers that list a plain model id. More than one entry is ambiguous and is
	// charged to the default endpoint.
	listedBy map[string][]string
}

// NewProviderAttribution builds the mapping from the effective provider catalogue. A disabled provider stays
// in the catalogue (it can be enabled again) and therefore keeps its models attributed to it; an explicit
// call to a disabled provider is refused by the router before a budget could matter.
func NewProviderAttribution(catalog []ProviderCatalogEntry) ProviderAttribution {
	var attribution = ProviderAttribution{
		explicit: make(map[string]bool, len(catalog)),
		listedBy: make(map[string][]string, len(catalog)),
	}
	for _, entry := range catalog {
		var name = strings.TrimSpace(entry.Provider)
		if name == "" || name == DefaultProviderName {
			continue
		}
		attribution.explicit[name] = true
		var seen = make(map[string]bool, len(entry.Models))
		for _, model := range entry.Models {
			var trimmed = strings.TrimSpace(model)
			if trimmed == "" || seen[trimmed] {
				continue
			}
			seen[trimmed] = true
			attribution.listedBy[trimmed] = append(attribution.listedBy[trimmed], name)
		}
	}
	return attribution
}

// ProviderFor returns the provider name that owns modelID. DefaultProviderName means "the default endpoint":
// it has no provider row and therefore no budget, which is exactly the unchanged chat/default path.
func (a ProviderAttribution) ProviderFor(modelID string) string {
	var trimmed = strings.TrimSpace(modelID)
	if trimmed == "" {
		return DefaultProviderName
	}
	if prefix, rest, ok := strings.Cut(trimmed, ":"); ok {
		prefix, rest = strings.TrimSpace(prefix), strings.TrimSpace(rest)
		// "strata:model" is the explicit spelling of the default endpoint; any other known prefix names
		// its provider. An unknown prefix can only be a model the router would refuse, so it is not charged
		// to a configured provider's budget.
		if prefix == "" || prefix == DefaultProviderName || rest == "" {
			return DefaultProviderName
		}
		if a.explicit[prefix] {
			return prefix
		}
		return DefaultProviderName
	}
	var names = a.listedBy[trimmed]
	if len(names) == 1 {
		return names[0]
	}
	return DefaultProviderName
}

// ProviderCosts groups per-model micros by the provider each model is charged to. The result is the spend
// that a provider budget compares with. A model with zero cost is still attributed, so a caller that asks
// for one provider's spend gets 0 rather than a missing key; the default endpoint is included under its own
// name and is never budgeted.
func (a ProviderAttribution) ProviderCosts(modelMicros map[string]int64) map[string]int64 {
	var costs = make(map[string]int64, len(modelMicros))
	for modelID, micros := range modelMicros {
		costs[a.ProviderFor(modelID)] += micros
	}
	return costs
}
