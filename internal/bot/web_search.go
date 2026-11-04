package bot

import (
	"bot/internal/config"
	"bot/internal/model"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	webSearchMaxQueryRunes     = 512
	webSearchMaxBodyBytes      = 1 << 20
	webSearchMaxResultRunes    = 4000
	webSearchMaxTitleRunes     = 200
	webSearchMaxSnippetRunes   = 500
	webSearchMaxResultURLRunes = 2048
	webSearchProviderPath      = "/search"
	webSearchProviderFormat    = "json"
	webSearchToolDescription   = "Search the web through the operator's own SearxNG and return up to WEB_SEARCH_MAX_RESULTS results as JSON with title, url and snippet. Result URLs are text only: nothing is fetched from them."
)

// errWebSearchRedirect is returned by the client's CheckRedirect. It is a named value so the provider
// call can tell "the instance answered with a redirect" apart from a socket that never connected.
var errWebSearchRedirect = errors.New("redirects are not followed")

// webSearchHooks — the seam a test uses to point the provider client at an httptest server without
// opening a socket. The production path passes an empty value and gets the transport built below.
type webSearchHooks struct {
	transport http.RoundTripper
}

func webSearchTools(enabled bool) []model.ToolDefinition {
	if !enabled {
		return nil
	}
	return []model.ToolDefinition{{
		Name:        "web_search",
		Description: webSearchToolDescription,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search query"},
			},
			"required": []string{"query"},
		},
	}}
}

func executeWebSearch(ctx context.Context, cfg config.Config, call model.ToolCall) (string, error) {
	return executeWebSearchWithClient(ctx, cfg, call, newWebSearchClient(cfg.WebSearchTimeoutSeconds, webSearchHooks{}))
}

func executeWebSearchWithClient(ctx context.Context, cfg config.Config, call model.ToolCall, client *http.Client) (string, error) {
	if call.Name != "web_search" {
		return "", errors.New("unknown tool")
	}
	decoder := json.NewDecoder(strings.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	var input struct {
		Query string `json:"query"`
	}
	if err := decoder.Decode(&input); err != nil {
		return "", err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return "", err
	}
	query := strings.TrimSpace(input.Query)
	if query == "" || utf8.RuneCountInString(query) > webSearchMaxQueryRunes {
		return "", errors.New("web_search query is empty or too long")
	}
	results, err := searchWeb(ctx, cfg, client, query)
	if err != nil {
		payload, marshalErr := json.Marshal(map[string]string{"error": err.Error()})
		return string(payload), marshalErr
	}
	payload, err := json.Marshal(struct {
		Results []webSearchResult `json:"results"`
	}{Results: results})
	return string(payload), err
}

func newWebSearchClient(timeoutSeconds int64, hooks webSearchHooks) *http.Client {
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(config.DefaultWebSearchTimeoutSeconds) * time.Second
	}
	transport := hooks.transport
	if transport == nil {
		dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
		transport = &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: time.Second,
			IdleConnTimeout:       30 * time.Second,
		}
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return errWebSearchRedirect },
	}
}

// webSearchResult is one row of the payload the model reads. The provider answers its summary of a hit
// in "content", which becomes "snippet" here so the field of the tool schema says what it holds.
type webSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// webSearchResponse is the shape SearxNG answers with format=json. An answer without "results" is a
// normal empty outcome rather than an error: the model is told there was nothing to read.
type webSearchResponse struct {
	Results []webSearchProviderResult `json:"results"`
}

type webSearchProviderResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// searchWeb asks the provider for JSON and turns its answer into the bounded payload the model sees.
// The URLs of the results are never fetched: they are handed over as text, so a hit may point at an
// internal address without this process ever dialling it.
func searchWeb(ctx context.Context, cfg config.Config, client *http.Client, query string) ([]webSearchResult, error) {
	endpoint, err := webSearchEndpoint(cfg.WebSearchBaseURL, query)
	if err != nil {
		return nil, err
	}
	body, status, err := webSearchBody(ctx, client, http.MethodGet, endpoint, "")
	if err != nil {
		return nil, err
	}
	// A SearxNG behind some gateways refuses GET /search and only accepts its own form POST, so the
	// same question goes out once more as a form before the answer counts as unavailable.
	if status == http.StatusMethodNotAllowed {
		form := url.Values{"q": []string{query}, "format": []string{webSearchProviderFormat}}
		body, status, err = webSearchBody(ctx, client, http.MethodPost, endpoint, form.Encode())
		if err != nil {
			return nil, err
		}
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("unexpected HTTP status %d", status)
	}
	var provider webSearchResponse
	if err := json.Unmarshal(body, &provider); err != nil {
		return nil, errors.New("web search provider returned invalid JSON")
	}
	return boundedWebSearchResults(provider.Results, cfg.WebSearchMaxResults), nil
}

// webSearchBody performs one request and returns the status and, for a 2xx answer, the body it accepts
// to read. A non-2xx status is returned without an error so the caller can decide whether a different
// method of the same provider fits.
func webSearchBody(ctx context.Context, client *http.Client, method, endpoint, form string) ([]byte, int, error) {
	var payload io.Reader
	if form != "" {
		payload = strings.NewReader(form)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return nil, 0, errors.New("invalid web search endpoint")
	}
	req.Header.Set("Accept", "application/json")
	if form != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errWebSearchRedirect) {
			return nil, 0, errWebSearchRedirect
		}
		return nil, 0, errors.New("web search provider is unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, resp.StatusCode, nil
	}
	if err := webSearchIsJSON(resp.Header.Get("Content-Type")); err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.ContentLength > webSearchMaxBodyBytes {
		return nil, resp.StatusCode, errors.New("response body exceeds 1 MiB")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, webSearchMaxBodyBytes+1))
	if err != nil {
		return nil, resp.StatusCode, errors.New("failed to read response body")
	}
	if len(body) > webSearchMaxBodyBytes {
		return nil, resp.StatusCode, errors.New("response body exceeds 1 MiB")
	}
	return body, resp.StatusCode, nil
}

// webSearchEndpoint is <base>/search?q=<query>&format=json: the base URL comes only from the ENV, the
// query is url-encoded by url.Values, and anything the operator wrote after the host stays in front of
// the provider path, because an instance behind a proxy is reached through its prefix.
func webSearchEndpoint(base string, query string) (string, error) {
	if base == "" {
		return "", errors.New("web search is not configured")
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", errors.New("invalid web search endpoint")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("invalid web search endpoint")
	}
	if parsed.User != nil {
		return "", errors.New("invalid web search endpoint")
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	if !strings.HasSuffix(path, webSearchProviderPath) {
		path += webSearchProviderPath
	}
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	values := url.Values{}
	values.Set("q", query)
	values.Set("format", webSearchProviderFormat)
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func webSearchIsJSON(header string) error {
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return errors.New("unsupported content type")
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
		return errors.New("unsupported content type")
	}
	return nil
}

// boundedWebSearchResults caps the payload at WEB_SEARCH_MAX_RESULTS rows and at webSearchMaxResultRunes
// of JSON. A row that would cross the budget is dropped whole, so what the model reads stays parseable;
// the budget counts the JSON as it is written, escapes included, because that is what the model pays for.
func boundedWebSearchResults(provider []webSearchProviderResult, maxResults int64) []webSearchResult {
	out := []webSearchResult{}
	limit := int(maxResults)
	if limit < 1 {
		limit = int(config.DefaultWebSearchMaxResults)
	}
	for _, item := range provider {
		if len(out) >= limit {
			break
		}
		candidate := webSearchResult{
			Title:   cutWebSearchRunes(cleanProviderText(item.Title), webSearchMaxTitleRunes),
			URL:     cleanProviderText(item.URL),
			Snippet: cutWebSearchRunes(cleanProviderText(item.Content), webSearchMaxSnippetRunes),
		}
		if !webSearchIsHTTPURL(candidate.URL) || utf8.RuneCountInString(candidate.URL) > webSearchMaxResultURLRunes {
			continue
		}
		trial := append(append([]webSearchResult(nil), out...), candidate)
		if webSearchJSONRunes(trial) > webSearchMaxResultRunes {
			break
		}
		out = append(out, candidate)
	}
	return out
}

func webSearchJSONRunes(results []webSearchResult) int {
	payload, err := json.Marshal(results)
	if err != nil {
		return webSearchMaxResultRunes + 1
	}
	return utf8.RuneCountInString(string(payload))
}

func webSearchIsHTTPURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// cleanProviderText turns the markup a search engine puts in the title and the summary of a hit into one
// line of text; the same cleaner the URL reader uses on a page.
func cleanProviderText(input string) string {
	return cleanURLReaderText(htmlURLReaderText(input))
}

func cutWebSearchRunes(input string, limit int) string {
	if utf8.RuneCountInString(input) <= limit {
		return input
	}
	return string([]rune(input)[:limit])
}
