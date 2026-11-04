package bot

import (
	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

type webSearchFixture struct {
	cfg        config.Config
	server     *httptest.Server
	requests   []string
	queries    []string
	formats    []string
	methods    []string
	redirectTo string
}

func newWebSearchFixture(t *testing.T, overrides map[string]string, handler http.HandlerFunc) *webSearchFixture {
	t.Helper()
	raw := map[string]string{"WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": "https://searx.invalid"}
	for name, value := range overrides {
		raw[name] = value
	}
	cfg := testConfig(t, raw)
	fixture := &webSearchFixture{cfg: cfg}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.requests = append(fixture.requests, r.URL.Path)
		fixture.methods = append(fixture.methods, r.Method)
		fixture.queries = append(fixture.queries, r.URL.Query().Get("q"))
		fixture.formats = append(fixture.formats, r.URL.Query().Get("format"))
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	cfg.WebSearchBaseURL = server.URL
	fixture.server = server
	fixture.cfg = cfg
	return fixture
}

// client — the dedicated provider client with the injectable transport hook, i.e. the seam
// urlReaderHooks is for the reader: the same client the production path builds, pointed at the test server.
func (f *webSearchFixture) client() *http.Client {
	return newWebSearchClient(f.cfg.WebSearchTimeoutSeconds, webSearchHooks{transport: f.server.Client().Transport})
}

func (f *webSearchFixture) searchThroughHooks(t *testing.T, query string) map[string]any {
	t.Helper()
	arguments, _ := json.Marshal(map[string]string{"query": query})
	payload, err := executeWebSearchWithClient(context.Background(), f.cfg, model.ToolCall{Name: "web_search", Arguments: string(arguments)}, f.client())
	if err != nil {
		t.Fatalf("executeWebSearchWithClient: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("payload is not JSON: %s (%v)", payload, err)
	}
	return got
}

// search — the provider call through the injectable hook.
func (f *webSearchFixture) search(t *testing.T, query string) map[string]any {
	t.Helper()
	return f.searchThroughHooks(t, query)
}

func webSearchResultsOf(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	if payload["error"] != nil {
		t.Fatalf("web_search returned an error payload: %v", payload)
	}
	raw, ok := payload["results"].([]any)
	if !ok {
		t.Fatalf("payload has no results array: %v", payload)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		result, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("result is not an object: %v", item)
		}
		out = append(out, result)
	}
	return out
}

func writeWebSearchJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

func TestWebSearchToolSchema(t *testing.T) {
	if tools := webSearchTools(false); tools != nil {
		t.Fatalf("disabled tools=%v", tools)
	}
	tools := webSearchTools(true)
	if len(tools) != 1 || tools[0].Name != "web_search" || tools[0].Description == "" {
		t.Fatalf("tools=%v", tools)
	}
	properties, ok := tools[0].Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters=%+v", tools[0].Parameters)
	}
	query, ok := properties["query"].(map[string]any)
	if !ok || query["type"] != "string" {
		t.Fatalf("query parameter=%+v", properties["query"])
	}
	required, ok := tools[0].Parameters["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "query" {
		t.Fatalf("required=%+v", tools[0].Parameters["required"])
	}
}

func TestWebSearchRejectsMalformedCalls(t *testing.T) {
	fixture := newWebSearchFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		t.Error("provider must not be called for a malformed call")
	})
	if _, err := executeWebSearch(context.Background(), fixture.cfg, model.ToolCall{Name: "shell", Arguments: `{}`}); err == nil {
		t.Error("unknown tool must be rejected")
	}
	for _, arguments := range []string{`{"query":`, `{"query":"x","extra":true}`, `{"query":"x"} {}`, `{}`} {
		if _, err := executeWebSearch(context.Background(), fixture.cfg, model.ToolCall{Name: "web_search", Arguments: arguments}); err == nil {
			t.Errorf("malformed call %q must be rejected", arguments)
		}
	}
}

func TestWebSearchQueryBounds(t *testing.T) {
	fixture := newWebSearchFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		t.Error("provider must not be called for an out-of-bounds query")
	})
	for _, query := range []string{"", "   ", strings.Repeat("字", webSearchMaxQueryRunes+1)} {
		arguments, _ := json.Marshal(map[string]string{"query": query})
		if _, err := executeWebSearch(context.Background(), fixture.cfg, model.ToolCall{Name: "web_search", Arguments: string(arguments)}); err == nil {
			t.Errorf("query of %d runes must be rejected", utf8.RuneCountInString(query))
		}
	}
}

func TestWebSearchHappyPathAsksTheProviderForJSON(t *testing.T) {
	fixture := newWebSearchFixture(t, map[string]string{"WEB_SEARCH_MAX_RESULTS": "2"}, func(w http.ResponseWriter, r *http.Request) {
		writeWebSearchJSON(w, `{"results":[
			{"title":"Первая &amp; вторая","url":"https://one.example/a","content":"краткое содержание первой","timestamp":"1700000000"},
			{"title":"Вторая","url":"http://two.example/b","content":"вторая"},
			{"title":"Третья","url":"https://three.example/c","content":"третья"}
		],"answers":[],"suggestions":[]}`)
	})
	results := webSearchResultsOf(t, fixture.search(t, "столица Сириуса"))
	if len(fixture.requests) != 1 || fixture.requests[0] != "/search" {
		t.Fatalf("provider requests=%v", fixture.requests)
	}
	if len(fixture.methods) != 1 || fixture.methods[0] != http.MethodGet {
		t.Fatalf("provider methods=%v", fixture.methods)
	}
	if fixture.queries[0] != "столица Сириуса" {
		t.Errorf("query the provider received=%q", fixture.queries[0])
	}
	if fixture.formats[0] != "json" {
		t.Errorf("format the provider received=%q", fixture.formats[0])
	}
	if len(results) != 2 {
		t.Fatalf("results=%d, want WEB_SEARCH_MAX_RESULTS=2", len(results))
	}
	first := results[0]
	if first["title"] != "Первая & вторая" || first["url"] != "https://one.example/a" || first["snippet"] != "краткое содержание первой" {
		t.Errorf("first result=%v", first)
	}
	if _, ok := first["timestamp"]; ok {
		t.Errorf("a provider field the tool does not promise reached the model: %v", first)
	}
}

func TestWebSearchCapsTitleAndSnippet(t *testing.T) {
	long := strings.Repeat("ы", webSearchMaxTitleRunes+200)
	huge := strings.Repeat("a", webSearchMaxSnippetRunes+300)
	fixture := newWebSearchFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		writeWebSearchJSON(w, `{"results":[{"title":"`+long+`","url":"https://one.example/","content":"`+huge+`"}]}`)
	})
	result := webSearchResultsOf(t, fixture.search(t, "вопрос"))[0]
	if got := utf8.RuneCountInString(result["title"].(string)); got != webSearchMaxTitleRunes {
		t.Errorf("title runes=%d, want the cap %d", got, webSearchMaxTitleRunes)
	}
	if got := utf8.RuneCountInString(result["snippet"].(string)); got != webSearchMaxSnippetRunes {
		t.Errorf("snippet runes=%d, want the cap %d", got, webSearchMaxSnippetRunes)
	}
}

func TestWebSearchStaysInsideTheResultRuneBudget(t *testing.T) {
	var builder strings.Builder
	builder.WriteString(`{"results":[`)
	for i := 0; i < 10; i++ {
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"title":"` + strings.Repeat("m", webSearchMaxTitleRunes) +
			`","url":"https://example.test/` + strings.Repeat("x", 60) +
			`","content":"` + strings.Repeat("d", webSearchMaxSnippetRunes) + `"}`)
	}
	builder.WriteString(`]}`)
	fixture := newWebSearchFixture(t, map[string]string{"WEB_SEARCH_MAX_RESULTS": "10"}, func(w http.ResponseWriter, r *http.Request) {
		writeWebSearchJSON(w, builder.String())
	})
	payload := fixture.search(t, "что-нибудь")
	results := webSearchResultsOf(t, payload)
	if len(results) == 0 || len(results) >= 10 {
		t.Fatalf("the rune budget kept %d of 10 results", len(results))
	}
	if got := utf8.RuneCountInString(mustJSON(t, payload["results"])); got > webSearchMaxResultRunes {
		t.Fatalf("results array runes=%d, want at most %d", got, webSearchMaxResultRunes)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(payload)
}

func TestWebSearchDropsNonHTTPResultURLs(t *testing.T) {
	fixture := newWebSearchFixture(t, map[string]string{"WEB_SEARCH_MAX_RESULTS": "10"}, func(w http.ResponseWriter, r *http.Request) {
		writeWebSearchJSON(w, `{"results":[
			{"title":"file","url":"file:///etc/passwd","content":"x"},
			{"title":"js","url":"javascript:alert(1)","content":"x"},
			{"title":"empty","url":"","content":"x"},
			{"title":"relative","url":"/just/a/path","content":"x"},
			{"title":"no host","url":"https://","content":"x"},
			{"title":"gopher","url":"gopher://gopher.example","content":"x"},
			{"title":"good","url":"https://good.example/","content":"kept"}
		]}`)
	})
	results := webSearchResultsOf(t, fixture.search(t, "что-нибудь"))
	if len(results) != 1 || results[0]["url"] != "https://good.example/" {
		t.Fatalf("results=%v, want only the http(s) hit", results)
	}
}

// The URLs of the results are text for the model, never a target this process dials: a hit that points
// at the loopback address the test server listens on must come back unchanged and unfetched.
func TestWebSearchNeverFetchesResultURLs(t *testing.T) {
	var hits int
	var fixture *webSearchFixture
	fixture = newWebSearchFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		hits++
		writeWebSearchJSON(w, `{"results":[{"title":"provider","url":"`+fixture.server.URL+`/result","content":"текст"}]}`)
	})
	results := webSearchResultsOf(t, fixture.search(t, "вопрос"))
	if len(results) != 1 || results[0]["url"] != fixture.server.URL+"/result" {
		t.Fatalf("results=%v", results)
	}
	if hits != 1 {
		t.Fatalf("provider requests=%d, want exactly 1: a result URL was fetched", hits)
	}
}

func TestWebSearchRejectsNonJSONContentType(t *testing.T) {
	fixture := newWebSearchFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>search</body></html>`))
	})
	payload := fixture.search(t, "вопрос")
	if payload["results"] != nil || payload["error"] != "unsupported content type" {
		t.Fatalf("content type payload=%v", payload)
	}
}

func TestWebSearchRejectsOversizedBody(t *testing.T) {
	fixture := newWebSearchFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		writeWebSearchJSON(w, `{"results":[{"title":"`+strings.Repeat("a", (1<<20)+1)+`"}]}`)
	})
	payload := fixture.search(t, "вопрос")
	if payload["results"] != nil || payload["error"] != "response body exceeds 1 MiB" {
		t.Fatalf("oversized body payload=%v", payload)
	}
}

func TestWebSearchRefusesRedirects(t *testing.T) {
	fixture := newWebSearchFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://127.0.0.1:1/other")
		w.WriteHeader(http.StatusFound)
	})
	payload := fixture.search(t, "вопрос")
	if payload["results"] != nil {
		t.Fatalf("a redirected provider answered results: %v", payload)
	}
	if payload["error"] != errWebSearchRedirect.Error() && payload["error"] != "web search provider is unreachable" {
		t.Fatalf("redirect payload=%v", payload)
	}
	if len(fixture.requests) != 1 {
		t.Fatalf("provider requests=%v, want one: the redirect was followed", fixture.requests)
	}
}

func TestWebSearchReportsProviderStatus(t *testing.T) {
	fixture := newWebSearchFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		writeWebSearchJSON(w, `{"results":[]}`)
	})
	payload := fixture.search(t, "вопрос")
	if payload["results"] != nil || payload["error"] != "unexpected HTTP status 500" {
		t.Fatalf("payload=%v, want the status of the provider named", payload)
	}
	if len(fixture.methods) != 1 || fixture.methods[0] != http.MethodGet {
		t.Fatalf("provider methods=%v, want no form retry after a 500", fixture.methods)
	}
}

// A provider that returns no "results" key answered an empty answer, not a broken one: the model has to
// be able to tell "nothing was found" from a tool that failed.
func TestWebSearchToleratesMissingResults(t *testing.T) {
	fixture := newWebSearchFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		writeWebSearchJSON(w, `{"answers":[],"suggestions":["что-то ещё"]}`)
	})
	payload := fixture.search(t, "вопрос")
	if payload["error"] != nil {
		t.Fatalf("missing results reported an error: %v", payload)
	}
	results, ok := payload["results"].([]any)
	if !ok || len(results) != 0 {
		t.Fatalf("payload=%v, want an empty results array", payload)
	}
}

func TestWebSearchRetriesOnceAsFormPost(t *testing.T) {
	fixture := newWebSearchFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeWebSearchJSON(w, `{"results":[{"title":"POST","url":"https://one.example/","content":"через форму"}]}`)
	})
	results := webSearchResultsOf(t, fixture.search(t, "вопрос"))
	if len(fixture.methods) != 2 || fixture.methods[0] != http.MethodGet || fixture.methods[1] != http.MethodPost {
		t.Fatalf("provider methods=%v", fixture.methods)
	}
	if len(results) != 1 || results[0]["title"] != "POST" {
		t.Fatalf("results=%v", results)
	}
}

func TestWebSearchRejectsUnparsableJSON(t *testing.T) {
	fixture := newWebSearchFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		writeWebSearchJSON(w, `{"results":`)
	})
	payload := fixture.search(t, "вопрос")
	if payload["results"] != nil || payload["error"] != "web search provider returned invalid JSON" {
		t.Fatalf("payload=%v", payload)
	}
}

func TestWebSearchWithoutConfiguredBaseURL(t *testing.T) {
	fixture := newWebSearchFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		t.Error("provider must not be called without a base URL")
	})
	fixture.cfg.WebSearchBaseURL = ""
	payload := fixture.search(t, "вопрос")
	if payload["results"] != nil || payload["error"] != "web search is not configured" {
		t.Fatalf("payload=%v", payload)
	}
}

func TestWebSearchEndpointKeepsTheProxyPrefix(t *testing.T) {
	// url.Values.Encode sorts the keys, so format= precedes q= in the URL that goes out.
	for _, tc := range []struct{ base, want string }{
		{"https://searx.example", "https://searx.example/search?format=json&q=%D0%B7%D0%B0%D0%BF%D1%80%D0%BE%D1%81"},
		{"https://searx.example/", "https://searx.example/search?format=json&q=%D0%B7%D0%B0%D0%BF%D1%80%D0%BE%D1%81"},
		{"http://searx:8080/searx", "http://searx:8080/searx/search?format=json&q=%D0%B7%D0%B0%D0%BF%D1%80%D0%BE%D1%81"},
		{"https://searx.example/search", "https://searx.example/search?format=json&q=%D0%B7%D0%B0%D0%BF%D1%80%D0%BE%D1%81"},
	} {
		got, err := webSearchEndpoint(tc.base, "запрос")
		if err != nil {
			t.Errorf("base %q rejected: %v", tc.base, err)
			continue
		}
		if got != tc.want {
			t.Errorf("endpoint of %q = %q, want %q", tc.base, got, tc.want)
		}
	}
	for _, base := range []string{"not a url", "ftp://searx.example", "https://user:pass@searx.example", "https://", "/search"} {
		if got, err := webSearchEndpoint(base, "q"); err == nil {
			t.Errorf("unsafe base %q accepted: %q", base, got)
		}
	}
}

// The dispatcher must offer web_search only when the flag is on, and the count of tools of every other
// combination has to stay what it was.
func TestWebSearchToolsFollowTheFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  map[string]string
		want []string
	}{
		{"default", nil, []string{"calculator"}},
		{"url reader only", map[string]string{"URL_READER_ENABLED": "true"}, []string{"calculator", "read_url"}},
		{"web search only", map[string]string{"WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": "https://searx.internal.example"}, []string{"calculator", "web_search"}},
		{"both", map[string]string{"URL_READER_ENABLED": "true", "WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": "https://searx.internal.example"}, []string{"calculator", "read_url", "web_search"}},
		{"tools off", map[string]string{"TOOLS_ENABLED": "false"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, tc.raw)
			m := &scriptedToolModel{responses: []model.Response{{Content: "ok"}}}
			d := newTestDispatcher(t, cfg, m, nil, nil)
			if _, err := d.completeWithTools(context.Background(), 11, model.Request{}, nil, nil); err != nil {
				t.Fatalf("completeWithTools: %v", err)
			}
			var names []string
			for _, tool := range m.requests[0].Tools {
				names = append(names, tool.Name)
			}
			if strings.Join(names, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("tools=%v, want %v", names, tc.want)
			}
		})
	}
}

// executeWebSearch is what the dispatcher calls, so an unreachable instance has to come back as a JSON
// error the model can read rather than as a failed tool round.
func TestWebSearchUnreachableProviderIsAnErrorPayload(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"WEB_SEARCH_ENABLED":         "true",
		"WEB_SEARCH_BASE_URL":        "http://127.0.0.1:1",
		"WEB_SEARCH_TIMEOUT_SECONDS": "1",
	})
	payload, err := executeWebSearch(context.Background(), cfg, model.ToolCall{Name: "web_search", Arguments: `{"query":"вопрос"}`})
	if err != nil {
		t.Fatalf("executeWebSearch: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("payload=%s err=%v", payload, err)
	}
	if got["results"] != nil || got["error"] != "web search provider is unreachable" {
		t.Fatalf("payload=%s", payload)
	}
}

// The switch of the tool loop is what makes the tool reachable at all, and the usage counter of
// web_search is fed by the same round, so both are asserted here.
func TestWebSearchToolLoopDispatch(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS":     "11",
		"WEB_SEARCH_ENABLED":         "true",
		"WEB_SEARCH_BASE_URL":        "http://127.0.0.1:1",
		"WEB_SEARCH_TIMEOUT_SECONDS": "1",
	})
	call := model.ToolCall{ID: "call-search", Name: "web_search", Arguments: `{"query":"погодa"}`}
	m := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{call}, Attempts: 1},
		{Content: "Поиск недоступен", Attempts: 1},
	}}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, db)
	if err := dispatch(t, d, testEvent(222, 11, 1702, "Найди в сети")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	last := m.requests[1].Messages[len(m.requests[1].Messages)-1]
	if last.Role != model.RoleTool || last.ToolName != "web_search" || last.ToolCallID != "call-search" || !json.Valid([]byte(last.Content)) {
		t.Fatalf("tool result=%+v", last)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(last.Content), &payload); err != nil || payload["error"] == nil {
		t.Fatalf("search error payload=%s err=%v", last.Content, err)
	}
	events, err := store.RecentEvents(context.Background(), db, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	uses := 0
	for _, event := range events {
		if event.Kind != store.EventUsage {
			continue
		}
		if event.Detail != "web_search" {
			t.Fatalf("usage detail=%q, want web_search", event.Detail)
		}
		uses++
	}
	if uses != 1 {
		t.Fatalf("web_search usage events=%d, want 1", uses)
	}
}
