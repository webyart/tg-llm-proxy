package bot

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// telegramFallbackTransport retries only read-only Telegram bootstrap/polling calls through the direct route
// when the configured proxy fails. Delivery and edit calls never use this path: a late network error there does
// not prove that Telegram has not already accepted the message.
type telegramFallbackTransport struct {
	proxy    http.RoundTripper
	direct   http.RoundTripper
	proxyFor func(*http.Request) (*url.URL, error)
}

func newTelegramFallbackTransport(proxy *http.Transport) *telegramFallbackTransport {
	direct := proxy.Clone()
	direct.Proxy = nil
	return &telegramFallbackTransport{proxy: proxy, direct: direct, proxyFor: http.ProxyFromEnvironment}
}

func (t *telegramFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	proxyURL, proxyErr := t.proxyFor(req)
	if proxyErr != nil || proxyURL == nil {
		return t.proxy.RoundTrip(req)
	}
	resp, err := t.proxy.RoundTrip(req)
	if err == nil || req.Context().Err() != nil || !telegramReadOnlyCall(req) {
		return resp, err
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}

	retry := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil, fmt.Errorf("proxy Telegram request failed and the polling body cannot be replayed: %w", err)
		}
		body, bodyErr := req.GetBody()
		if bodyErr != nil {
			return nil, fmt.Errorf("proxy Telegram request failed and the polling body cannot be reopened: %w", err)
		}
		retry.Body = body
	}
	directResp, directErr := t.direct.RoundTrip(retry)
	if directErr != nil {
		return nil, fmt.Errorf("proxy Telegram request failed: %v; direct fallback failed: %w", err, directErr)
	}
	return directResp, nil
}

func telegramReadOnlyCall(req *http.Request) bool {
	return strings.HasSuffix(req.URL.Path, "/getMe") || strings.HasSuffix(req.URL.Path, "/getUpdates")
}
