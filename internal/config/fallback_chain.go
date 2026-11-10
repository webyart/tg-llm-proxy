package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FallbackProvider is one reserve of the ordered chain. Model is required; BaseURL empty means the primary
// endpoint; APIKey empty means "no key" for a distinct endpoint and the primary key for the primary endpoint.
type FallbackProvider struct {
	Model          string `json:"model"`
	BaseURL        string `json:"base_url,omitempty"`
	APIKey         string `json:"api_key,omitempty"`
	TimeoutSeconds int64  `json:"timeout_seconds,omitempty"`

	ChatURL   string `json:"-"`
	ModelsURL string `json:"-"`
}

// parseFallbackChain validates STRATA_FALLBACKS: a JSON array of at most eight reserves. It is a separate
// function so the Config loader stays readable and every error names the entry index.
func parseFallbackChain(raw string, cfg Config, target *[]FallbackProvider) []string {
	var providers []FallbackProvider
	if err := json.Unmarshal([]byte(raw), &providers); err != nil {
		return []string{"ENV STRATA_FALLBACKS must be a JSON array of {model, base_url?, api_key?, timeout_seconds?}: " + err.Error()}
	}
	if len(providers) == 0 {
		return []string{"ENV STRATA_FALLBACKS is an empty array: remove the row or list at least one reserve"}
	}
	if len(providers) > 8 {
		return []string{fmt.Sprintf("ENV STRATA_FALLBACKS lists %d reserves, at most 8 are allowed", len(providers))}
	}
	var problems []string
	for index := range providers {
		var provider = &providers[index]
		provider.Model = strings.TrimSpace(provider.Model)
		provider.BaseURL = strings.TrimSpace(provider.BaseURL)
		provider.APIKey = strings.TrimSpace(provider.APIKey)
		if provider.Model == "" {
			problems = append(problems, fmt.Sprintf("ENV STRATA_FALLBACKS entry %d has no model", index))
			continue
		}
		if provider.TimeoutSeconds == 0 {
			provider.TimeoutSeconds = DefaultFallbackTimeoutSeconds
		}
		if provider.TimeoutSeconds < 5 || provider.TimeoutSeconds > 600 {
			problems = append(problems, fmt.Sprintf("ENV STRATA_FALLBACKS entry %d timeout_seconds must be between 5 and 600", index))
		}
		if provider.BaseURL == "" {
			provider.ChatURL = cfg.ChatURL
			provider.ModelsURL = cfg.ModelsURL
			continue
		}
		if problem := validateFallbackBase(provider.BaseURL); problem != "" {
			problems = append(problems, fmt.Sprintf("ENV STRATA_FALLBACKS entry %d: %s", index, strings.TrimSpace(problem)))
			continue
		}
		var clean = baseWithoutQueryOrFragment(provider.BaseURL)
		provider.ChatURL = JoinURL(clean, "/chat/completions")
		provider.ModelsURL = JoinURL(clean, "/models")
	}
	if len(problems) > 0 {
		return problems
	}
	*target = providers
	return nil
}
