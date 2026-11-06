package bot

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"bot/internal/model"
)

func TestDirectNetworkIntentWebSearchRequiresLiteralRequestedQuery(t *testing.T) {
	const request = "Найди в интернете проект tg-llm-proxy"
	for _, tc := range []struct {
		name      string
		request   string
		arguments string
		want      bool
	}{
		{name: "literal query", request: request, arguments: `{"query":"tg-llm-proxy"}`, want: true},
		{name: "case insensitive", request: request, arguments: `{"query":"TG-LLM-PROXY"}`, want: true},
		{name: "trimmed query", request: request, arguments: `{"query":" \t tg-llm-proxy \n "}`, want: true},
		{name: "unicode case and whitespace", request: "Найди в интернете проект ПроектЁж", arguments: `{"query":"  проектёж  "}`, want: true},
		{name: "english case insensitive", request: "Search the web for GoLang", arguments: `{"query":"  golang  "}`, want: true},
		{name: "expanded query", request: request, arguments: `{"query":"tg-llm-proxy security audit"}`},
		{name: "private label", request: request, arguments: `{"query":"tg-llm-proxy private-canary"}`},
		{name: "paraphrased query", request: request, arguments: `{"query":"репозиторий tg llm proxy"}`},
		{name: "internal whitespace is literal", request: "Найди в интернете распределённые системы", arguments: `{"query":"распределённые   системы"}`},
		{name: "no explicit search intent", request: "Расскажи про tg-llm-proxy", arguments: `{"query":"tg-llm-proxy"}`},
		{name: "malformed JSON", request: request, arguments: `{"query":`},
		{name: "empty query", request: request, arguments: `{"query":""}`},
		{name: "whitespace query", request: request, arguments: `{"query":" \t\n "}`},
		{name: "missing query", request: request, arguments: `{}`},
		{name: "non-string query", request: request, arguments: `{"query":42}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := model.ToolCall{Name: "web_search", Arguments: tc.arguments}
			if got := directNetworkIntent(tc.request, call); got != tc.want {
				t.Fatalf("directNetworkIntent()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestDirectNetworkIntentReadURLBehaviorUnchanged(t *testing.T) {
	const request = "Прочитай https://example.com/docs"
	read, err := normalizeNetworkCall(model.ToolCall{Name: "read_url", Arguments: `{"url":"https://example.com/docs"}`})
	if err != nil || !directNetworkIntent(request, read) {
		t.Fatalf("requested URL was not authorized: call=%+v err=%v", read, err)
	}
	other, err := normalizeNetworkCall(model.ToolCall{Name: "read_url", Arguments: `{"url":"https://attacker.example/"}`})
	if err != nil || directNetworkIntent(request, other) {
		t.Fatalf("unrequested URL was authorized: call=%+v err=%v", other, err)
	}
	if directNetworkIntent(request, model.ToolCall{Name: "read_url", Arguments: `{"url":`}) {
		t.Fatal("malformed read_url arguments were authorized")
	}
}

func TestWebSearchExpandedQueryCanaryRequiresApproval(t *testing.T) {
	for _, tc := range []struct {
		name              string
		responses         []model.Response
		wantProviderCalls int64
		wantModelRequests int
		wantNetworkWork   int
	}{
		{
			name: "first round",
			responses: []model.Response{
				{ToolCalls: []model.ToolCall{{ID: "first-canary", Name: "web_search", Arguments: `{"query":"tg-llm-proxy private-canary"}`}}},
				{Content: "must wait for approval"},
			},
			wantModelRequests: 1,
		},
		{
			name: "subsequent round",
			responses: []model.Response{
				{ToolCalls: []model.ToolCall{{ID: "literal", Name: "web_search", Arguments: `{"query":"tg-llm-proxy"}`}}},
				{ToolCalls: []model.ToolCall{{ID: "subsequent-canary", Name: "web_search", Arguments: `{"query":"tg-llm-proxy private-canary"}`}}},
				{Content: "must wait for approval"},
			},
			wantProviderCalls: 1,
			wantModelRequests: 2,
			wantNetworkWork:   1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var providerCalls atomic.Int64
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				providerCalls.Add(1)
				if got := req.URL.Query().Get("q"); got != "tg-llm-proxy" {
					t.Errorf("unapproved provider query=%q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"results":[]}`))
			}))
			t.Cleanup(provider.Close)

			m := &scriptedToolModel{responses: tc.responses}
			r := &networkMenuReplier{}
			cfg := testConfig(t, map[string]string{
				"TELEGRAM_ALLOWED_USERS": "11",
				"WEB_SEARCH_ENABLED":     "true",
				"WEB_SEARCH_BASE_URL":    provider.URL,
			})
			d := newTestDispatcher(t, cfg, m, r, testStore(t))
			if err := dispatch(t, d, testEvent(100, 11, 9100, "Найди в интернете проект tg-llm-proxy")); err != nil {
				t.Fatal(err)
			}
			if providerCalls.Load() != tc.wantProviderCalls || len(m.requests) != tc.wantModelRequests || d.networkWork.Count(11) != tc.wantNetworkWork {
				t.Fatalf("canary side effects: provider=%d model=%d network=%d", providerCalls.Load(), len(m.requests), d.networkWork.Count(11))
			}
			if len(r.menus) != 1 {
				t.Fatalf("approval menus=%d, want 1", len(r.menus))
			}
			if got := networkUsageCount(t, d, "web_search"); got != tc.wantNetworkWork {
				t.Fatalf("web_search usage=%d, want %d", got, tc.wantNetworkWork)
			}
			latestNetworkCallbacks(t, r)
		})
	}
}

func TestWebSearchLiteralQueryRunsWithoutApproval(t *testing.T) {
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		providerCalls.Add(1)
		if got := req.URL.Query().Get("q"); got != "TG-LLM-PROXY" {
			t.Errorf("provider query=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(provider.Close)

	m := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{{ID: "literal", Name: "web_search", Arguments: `{"query":"  TG-LLM-PROXY  "}`}}},
		{Content: "Готово"},
	}}
	r := &networkMenuReplier{}
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11",
		"WEB_SEARCH_ENABLED":     "true",
		"WEB_SEARCH_BASE_URL":    provider.URL,
	})
	d := newTestDispatcher(t, cfg, m, r, testStore(t))
	if err := dispatch(t, d, testEvent(100, 11, 9101, "Найди в интернете проект tg-llm-proxy")); err != nil {
		t.Fatal(err)
	}
	if providerCalls.Load() != 1 || len(m.requests) != 2 || len(r.menus) != 0 {
		t.Fatalf("literal query was not automatic: provider=%d model=%d menus=%d", providerCalls.Load(), len(m.requests), len(r.menus))
	}
}
