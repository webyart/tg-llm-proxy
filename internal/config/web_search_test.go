package config

import (
	"strings"
	"testing"
)

func validWebSearchRaw() map[string]string {
	return map[string]string{
		"TELEGRAM_BOT_TOKEN":  "123:fake",
		"WEB_SEARCH_ENABLED":  "true",
		"WEB_SEARCH_BASE_URL": "https://searx.internal.example",
	}
}

func TestWebSearchIsOptIn(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake"})
	if len(problems) != 0 {
		t.Fatalf("default config problems: %v", problems)
	}
	if cfg.WebSearchEnabled || cfg.WebSearchBaseURL != "" || cfg.WebSearchTimeoutSeconds != DefaultWebSearchTimeoutSeconds || cfg.WebSearchMaxResults != DefaultWebSearchMaxResults {
		t.Fatalf("web search defaults changed: %+v", cfg)
	}
	// A stale row of a disabled provider costs nothing but its name.
	cfg, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":         "123:fake",
		"WEB_SEARCH_ENABLED":         "false",
		"WEB_SEARCH_BASE_URL":        "not a URL",
		"WEB_SEARCH_TIMEOUT_SECONDS": "0",
	})
	if len(problems) != 0 {
		t.Fatalf("disabled web search reported problems: %v", problems)
	}
	if cfg.WebSearchTimeoutSeconds != DefaultWebSearchTimeoutSeconds || cfg.WebSearchMaxResults != DefaultWebSearchMaxResults {
		t.Fatalf("disabled web search applied unused rows: %+v", cfg)
	}
}

func TestWebSearchAcceptsOwnInstanceOverHTTPS(t *testing.T) {
	cfg, problems := LoadFrom(validWebSearchRaw())
	if len(problems) != 0 {
		t.Fatalf("valid web search problems: %v", problems)
	}
	if !cfg.WebSearchEnabled || cfg.WebSearchBaseURL != "https://searx.internal.example" {
		t.Fatalf("valid web search was not applied: %+v", cfg)
	}
}

// A self-hosted instance on the same docker network answers plain http, so the row must accept it.
func TestWebSearchAcceptsOwnInstanceOverHTTP(t *testing.T) {
	raw := validWebSearchRaw()
	raw["WEB_SEARCH_BASE_URL"] = "http://searx:8080/search"
	cfg, problems := LoadFrom(raw)
	if len(problems) != 0 {
		t.Fatalf("local http web search problems: %v", problems)
	}
	if cfg.WebSearchBaseURL != "http://searx:8080/search" {
		t.Fatalf("base URL=%q", cfg.WebSearchBaseURL)
	}
}

func TestWebSearchRequiresBaseURL(t *testing.T) {
	raw := validWebSearchRaw()
	delete(raw, "WEB_SEARCH_BASE_URL")
	_, problems := LoadFrom(raw)
	if !hasProblemNamed(problems, "WEB_SEARCH_BASE_URL") {
		t.Fatalf("missing base URL was accepted: %v", problems)
	}
}

func TestWebSearchRejectsUnsafeBaseURL(t *testing.T) {
	for _, base := range []string{
		"https://user:password@searx.example",
		"https://searx.example?format=json",
		"https://searx.example/#results",
		"https://user@searx.example",
		"ftp://searx.example",
		"searx.example",
		"https://",
	} {
		raw := validWebSearchRaw()
		raw["WEB_SEARCH_BASE_URL"] = base
		_, problems := LoadFrom(raw)
		joined := strings.Join(problems, "\n")
		if !strings.Contains(joined, "WEB_SEARCH_BASE_URL") {
			t.Errorf("unsafe base URL %q was accepted: %v", base, problems)
			continue
		}
		if strings.Contains(joined, base) {
			t.Errorf("the problem echoes the value of %q: %v", base, problems)
		}
	}
}

// The provider is one tool of the tool loop, so enabling it alone cannot mean anything.
func TestWebSearchRequiresToolsEnabled(t *testing.T) {
	raw := validWebSearchRaw()
	raw["TOOLS_ENABLED"] = "false"
	_, problems := LoadFrom(raw)
	if !hasProblemNamed(problems, "WEB_SEARCH_ENABLED=true requires TOOLS_ENABLED=true") {
		t.Fatalf("web search without tools was accepted: %v", problems)
	}
}

func TestWebSearchBoundsOfTheNumericRows(t *testing.T) {
	for _, tc := range []struct {
		env   string
		value string
	}{
		{"WEB_SEARCH_TIMEOUT_SECONDS", "0"},
		{"WEB_SEARCH_TIMEOUT_SECONDS", "31"},
		{"WEB_SEARCH_TIMEOUT_SECONDS", "not-a-number"},
		{"WEB_SEARCH_MAX_RESULTS", "0"},
		{"WEB_SEARCH_MAX_RESULTS", "11"},
		{"WEB_SEARCH_MAX_RESULTS", "not-a-number"},
	} {
		raw := validWebSearchRaw()
		raw[tc.env] = tc.value
		cfg, problems := LoadFrom(raw)
		if !hasProblemNamed(problems, tc.env) {
			t.Errorf("%s=%s reported no problem naming it: %v", tc.env, tc.value, problems)
		}
		if cfg.WebSearchTimeoutSeconds != DefaultWebSearchTimeoutSeconds || cfg.WebSearchMaxResults != DefaultWebSearchMaxResults {
			t.Errorf("%s=%s was applied: %d/%d", tc.env, tc.value, cfg.WebSearchTimeoutSeconds, cfg.WebSearchMaxResults)
		}
	}

	raw := validWebSearchRaw()
	raw["WEB_SEARCH_TIMEOUT_SECONDS"] = "3"
	raw["WEB_SEARCH_MAX_RESULTS"] = "2"
	cfg, problems := LoadFrom(raw)
	if len(problems) != 0 {
		t.Fatalf("in-range rows reported problems: %v", problems)
	}
	if cfg.WebSearchTimeoutSeconds != 3 || cfg.WebSearchMaxResults != 2 {
		t.Fatalf("in-range rows were not applied: %d/%d", cfg.WebSearchTimeoutSeconds, cfg.WebSearchMaxResults)
	}
}
