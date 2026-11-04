package config

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	DefaultWebSearchTimeoutSeconds = int64(10)
	DefaultWebSearchMaxResults     = int64(5)
	webSearchBaseURLMaxBytes       = 2048
)

// loadWebSearch fills the four WEB_SEARCH_* rows. A disabled provider keeps only its defaults and
// reports nothing: an unused row of a stale .env must not stop the bot, and its value never reaches the
// runtime of a provider that is not offered to the model. Once enabled, the base URL of the self-hosted
// SearxNG becomes required, because there is no public instance this project is willing to send a
// user's query to. The problem strings name the ENV and never echo the value — an internal URL is a
// locator of the deployment, not something to print at info level.
func loadWebSearch(raw map[string]string, cfg *Config) []string {
	cfg.WebSearchTimeoutSeconds = DefaultWebSearchTimeoutSeconds
	cfg.WebSearchMaxResults = DefaultWebSearchMaxResults
	if !cfg.WebSearchEnabled {
		return nil
	}
	cfg.WebSearchBaseURL = strings.TrimSpace(raw["WEB_SEARCH_BASE_URL"])

	var problems []string
	if !cfg.ToolsEnabled {
		problems = Push(problems, "ENV WEB_SEARCH_ENABLED=true requires TOOLS_ENABLED=true: the web search tool is offered to the model through the tool loop that TOOLS_ENABLED switches on")
	}
	if cfg.WebSearchBaseURL == "" {
		problems = Push(problems, "ENV WEB_SEARCH_BASE_URL must name the base URL of your own SearxNG instance when WEB_SEARCH_ENABLED=true")
	} else if len(cfg.WebSearchBaseURL) > webSearchBaseURLMaxBytes {
		problems = Push(problems, fmt.Sprintf("ENV WEB_SEARCH_BASE_URL is longer than %d characters", webSearchBaseURLMaxBytes))
	} else {
		parsed, err := url.Parse(cfg.WebSearchBaseURL)
		if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
			problems = Push(problems, "ENV WEB_SEARCH_BASE_URL must use http or https")
		} else if parsed.Host == "" {
			problems = Push(problems, "ENV WEB_SEARCH_BASE_URL must contain a host")
		} else if parsed.User != nil {
			problems = Push(problems, "ENV WEB_SEARCH_BASE_URL must not contain userinfo or credentials")
		} else if parsed.RawQuery != "" || parsed.Fragment != "" {
			problems = Push(problems, "ENV WEB_SEARCH_BASE_URL must not contain a query or a fragment")
		}
	}

	timeout, timeoutProblem := Num(raw, "WEB_SEARCH_TIMEOUT_SECONDS", DefaultWebSearchTimeoutSeconds, 1, 30)
	cfg.WebSearchTimeoutSeconds = timeout
	problems = Push(problems, timeoutProblem)

	results, resultsProblem := Num(raw, "WEB_SEARCH_MAX_RESULTS", DefaultWebSearchMaxResults, 1, 10)
	cfg.WebSearchMaxResults = results
	problems = Push(problems, resultsProblem)
	return problems
}
