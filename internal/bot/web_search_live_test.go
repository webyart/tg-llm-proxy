//go:build livecheck

package bot

import (
	"bot/internal/config"
	"bot/internal/model"
	"context"
	"encoding/json"
	"os"
	"testing"
)

// TestLiveWebSearchRealProvider answers one query against the operator's own SearxNG and reports counts
// only: the query text and the base URL of a private instance never reach the log, and the snippets of
// the answer are counted rather than printed. Run it with
// WEB_SEARCH_LIVE_BASE_URL=https://searx.internal pwsh -File scripts/test.ps1 (or go test -tags livecheck).
func TestLiveWebSearchRealProvider(t *testing.T) {
	base := os.Getenv("WEB_SEARCH_LIVE_BASE_URL")
	if base == "" {
		t.Skip("WEB_SEARCH_LIVE_BASE_URL is not set: the live check needs the operator's own SearxNG instance")
	}
	cfg, problems := config.LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":  "123:live-check-placeholder",
		"WEB_SEARCH_ENABLED":  "true",
		"WEB_SEARCH_BASE_URL": base,
	})
	if len(problems) != 0 {
		t.Fatalf("config.LoadFrom rejected the live rows: %v", problems)
	}
	arguments, err := json.Marshal(map[string]string{"query": "Go language release notes"})
	if err != nil {
		t.Fatalf("marshal arguments: %v", err)
	}
	payload, err := executeWebSearch(context.Background(), cfg, model.ToolCall{Name: "web_search", Arguments: string(arguments)})
	if err != nil {
		t.Fatalf("executeWebSearch: %v", err)
	}
	var got struct {
		Results []webSearchResult `json:"results"`
		Error   string            `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if got.Error != "" {
		t.Fatalf("the live provider reported an error: %s", got.Error)
	}
	snippetRunes := 0
	for _, result := range got.Results {
		snippetRunes += len([]rune(result.Snippet))
	}
	t.Logf("results=%d of max=%d snippetRunes=%d timeoutSeconds=%d", len(got.Results), cfg.WebSearchMaxResults, snippetRunes, cfg.WebSearchTimeoutSeconds)
	if len(got.Results) == 0 {
		t.Fatalf("the live provider returned no results")
	}
	if int64(len(got.Results)) > cfg.WebSearchMaxResults {
		t.Fatalf("results=%d exceeds WEB_SEARCH_MAX_RESULTS=%d", len(got.Results), cfg.WebSearchMaxResults)
	}
}
