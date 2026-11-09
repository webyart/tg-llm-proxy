package config

import (
	"strings"
	"testing"
)

func TestFallbackRowsAreOptionalAndOffByDefault(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake"})
	if len(problems) != 0 {
		t.Fatalf("default config problems: %v", problems)
	}
	if cfg.StrataFallbackModel != "" || cfg.FallbackBase != "" || cfg.FallbackChatURL != "" || cfg.FallbackModelsURL != "" {
		t.Fatalf("fallback defaults are not empty: %+v", cfg)
	}
}

func TestFallbackModelDefaultsToThePrimaryEndpoint(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":       "123:fake",
		"STRATA_MODEL":             "primary-model",
		"STRATA_FALLBACK_MODEL":    "reserve-model",
		"STRATA_FALLBACK_BASE_URL": "",
	})
	if len(problems) != 0 {
		t.Fatalf("a distinct fallback on the primary endpoint reported problems: %v", problems)
	}
	if cfg.StrataFallbackModel != "reserve-model" {
		t.Fatalf("StrataFallbackModel = %q", cfg.StrataFallbackModel)
	}
	if cfg.FallbackBase != "" {
		t.Fatalf("FallbackBase = %q, want empty (same endpoint)", cfg.FallbackBase)
	}
	if cfg.FallbackChatURL != cfg.ChatURL || cfg.FallbackModelsURL != cfg.ModelsURL {
		t.Fatalf("fallback URLs = %q/%q, want the primary %q/%q",
			cfg.FallbackChatURL, cfg.FallbackModelsURL, cfg.ChatURL, cfg.ModelsURL)
	}
}

// A fallback that is the same model on the same endpoint cannot answer a failure of that model.
func TestFallbackSameModelOnTheSameEndpointIsRefused(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":    "123:fake",
		"STRATA_MODEL":          "one-model",
		"STRATA_FALLBACK_MODEL": "one-model",
	})
	if !hasProblemNamed(problems, "STRATA_FALLBACK_MODEL") {
		t.Fatalf("the same model on the same endpoint was accepted: %v", problems)
	}
	if !hasProblemNamed(problems, "STRATA_FALLBACK_BASE_URL") {
		t.Fatalf("the problem does not name the row that would make the fallback distinct: %v", problems)
	}
	if cfg.FallbackBase != "" {
		t.Fatalf("FallbackBase = %q, want empty", cfg.FallbackBase)
	}
}

// The same model id on a DIFFERENT endpoint is a real second copy of the model and must be allowed.
func TestFallbackSameModelOnAnotherEndpointIsAllowed(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":       "123:fake",
		"STRATA_MODEL":             "one-model",
		"STRATA_FALLBACK_MODEL":    "one-model",
		"STRATA_FALLBACK_BASE_URL": "https://backup.example.com/llm/v1",
	})
	if len(problems) != 0 {
		t.Fatalf("the same model on a separate endpoint reported problems: %v", problems)
	}
	if cfg.FallbackBase != "https://backup.example.com/llm/v1" {
		t.Fatalf("FallbackBase = %q", cfg.FallbackBase)
	}
	if cfg.FallbackChatURL != "https://backup.example.com/llm/v1/chat/completions" ||
		cfg.FallbackModelsURL != "https://backup.example.com/llm/v1/models" {
		t.Fatalf("fallback URLs = %q/%q", cfg.FallbackChatURL, cfg.FallbackModelsURL)
	}
}

func TestFallbackBaseURLValidation(t *testing.T) {
	for _, base := range []string{
		"https://user:password@backup.example.com",
		"https://user@backup.example.com",
		"ftp://backup.example.com",
		"backup.example.com",
		"https://",
		"",
	} {
		raw := map[string]string{
			"TELEGRAM_BOT_TOKEN":       "123:fake",
			"STRATA_FALLBACK_MODEL":    "reserve-model",
			"STRATA_FALLBACK_BASE_URL": base,
		}
		cfg, problems := LoadFrom(raw)
		if base == "" {
			if len(problems) != 0 {
				t.Errorf("an empty base must mean the primary endpoint, got %v", problems)
			}
			if cfg.FallbackChatURL != cfg.ChatURL {
				t.Errorf("empty base gave fallback URL %q, want the primary %q", cfg.FallbackChatURL, cfg.ChatURL)
			}
			continue
		}
		if !hasProblemNamed(problems, "STRATA_FALLBACK_BASE_URL") {
			t.Errorf("unsafe base URL %q was accepted: %v", base, problems)
		}
		if strings.Contains(strings.Join(problems, "\n"), base) {
			t.Errorf("the problem echoes the value of %q: %v", base, problems)
		}
	}
}

// The base URL of a disabled fallback is not a configuration of the running bot.
func TestFallbackBaseIsIgnoredWhenTheFallbackIsOff(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":       "123:fake",
		"STRATA_FALLBACK_BASE_URL": "not a URL",
	})
	if len(problems) != 0 {
		t.Fatalf("a base URL without a fallback model reported problems: %v", problems)
	}
	if cfg.FallbackBase != "" || cfg.FallbackChatURL != "" {
		t.Fatalf("a disabled fallback applied its base: %+v", cfg)
	}
}
