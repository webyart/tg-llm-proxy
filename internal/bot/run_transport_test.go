package bot

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
)

func TestDefaultTelegramHTTPClientUsesExtendedTLSHandshakeBudget(t *testing.T) {
	client := defaultTelegramHTTPClient(DefaultPollTimeout)
	wantTimeout := DefaultPollTimeout + 2*TLSHandshakeTimeout + 5*time.Second
	if client.Timeout != wantTimeout {
		t.Fatalf("client.Timeout = %s, want %s", client.Timeout, wantTimeout)
	}
	fallback, ok := client.Transport.(*telegramFallbackTransport)
	if !ok {
		t.Fatalf("transport type = %T, want *telegramFallbackTransport", client.Transport)
	}
	transport, ok := fallback.proxy.(*http.Transport)
	if !ok {
		t.Fatalf("proxy transport type = %T, want *http.Transport", fallback.proxy)
	}
	if transport.TLSHandshakeTimeout != 30*time.Second {
		t.Fatalf("TLSHandshakeTimeout = %s, want 30s", transport.TLSHandshakeTimeout)
	}
	if transport.Proxy == nil || transport.DialContext == nil {
		t.Fatal("cloned transport must retain proxy and dial behavior of http.DefaultTransport")
	}
	direct := fallback.direct.(*http.Transport)
	if direct.Proxy != nil {
		t.Fatal("direct fallback must bypass the configured proxy")
	}
}

func TestTelegramInitTimeoutCoversTLSHandshake(t *testing.T) {
	if InitTimeout < TLSHandshakeTimeout {
		t.Fatalf("InitTimeout = %s, TLSHandshakeTimeout = %s; startup must not expire first", InitTimeout, TLSHandshakeTimeout)
	}
}

const testWebhookUpdate = `{"update_id":160,"message":{"message_id":160,"date":1700000000,"chat":{"id":4242,"type":"private"},"from":{"id":1717,"is_bot":false,"first_name":"Tester"},"text":"/ping"}}`

type webhookCountingBody struct {
	reader    io.Reader
	reads     int
	bytesRead int64
}

func (b *webhookCountingBody) Read(p []byte) (int, error) {
	b.reads++
	n, err := b.reader.Read(p)
	b.bytesRead += int64(n)
	return n, err
}

func (b *webhookCountingBody) Close() error { return nil }

type webhookBlockingBody struct {
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (b *webhookBlockingBody) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return 0, io.EOF
}

func (b *webhookBlockingBody) Close() error { return nil }

func newWebhookTestServer(t *testing.T, secret string, libraryErrors *atomic.Int64) *WebhookServer {
	t.Helper()
	tg, err := tgbot.New("123:test-token",
		tgbot.WithSkipGetMe(),
		tgbot.WithWebhookSecretToken(secret),
		tgbot.WithErrorsHandler(func(error) { libraryErrors.Add(1) }),
	)
	if err != nil {
		t.Fatalf("tgbot.New: %v", err)
	}
	server, err := NewWebhookServer(tg, "127.0.0.1:0", "/webhook", secret)
	if err != nil {
		t.Fatalf("NewWebhookServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func newWebhookTestRequest(secret string, body io.ReadCloser) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/webhook", nil)
	req.Body = body
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set(webhookSecretHeader, secret)
	}
	return req
}

func TestWebhookRejectsInvalidSecretBeforeReadingBody(t *testing.T) {
	const secret = "1234567890abcdef"
	var libraryErrors atomic.Int64
	server := newWebhookTestServer(t, secret, &libraryErrors)
	for _, test := range []struct {
		name   string
		secret string
	}{
		{name: "wrong", secret: "wrong-secret"},
		{name: "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &webhookCountingBody{reader: strings.NewReader(testWebhookUpdate)}
			req := newWebhookTestRequest(test.secret, body)
			req.Header.Set("X-Forwarded-For", "203.0.113.10")
			response := httptest.NewRecorder()
			server.srv.Handler.ServeHTTP(response, req)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", response.Code)
			}
			if body.reads != 0 || body.bytesRead != 0 {
				t.Fatalf("rejected body was read: reads=%d bytes=%d", body.reads, body.bytesRead)
			}
			if got := libraryErrors.Load(); got != 0 {
				t.Fatalf("library error handler called %d times, want no rejection log spam", got)
			}
		})
	}
}

func TestWebhookValidUpdateStillReachesLibrary(t *testing.T) {
	const secret = "1234567890abcdef"
	var libraryErrors atomic.Int64
	server := newWebhookTestServer(t, secret, &libraryErrors)
	body := &webhookCountingBody{reader: strings.NewReader(testWebhookUpdate)}
	response := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(response, newWebhookTestRequest(secret, body))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", response.Code, response.Body.String())
	}
	if body.bytesRead != int64(len(testWebhookUpdate)) {
		t.Fatalf("body bytes read = %d, want %d", body.bytesRead, len(testWebhookUpdate))
	}
	if got := libraryErrors.Load(); got != 0 {
		t.Fatalf("library error handler called %d times for a valid update", got)
	}
}

func TestWebhookOversizedBodyRemainsCapped(t *testing.T) {
	const secret = "1234567890abcdef"
	var libraryErrors atomic.Int64
	server := newWebhookTestServer(t, secret, &libraryErrors)
	body := &webhookCountingBody{reader: strings.NewReader(strings.Repeat(" ", webhookBodyLimit+1))}
	response := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(response, newWebhookTestRequest(secret, body))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
	if body.bytesRead != webhookBodyLimit+1 {
		t.Fatalf("body bytes read = %d, want exactly %d", body.bytesRead, webhookBodyLimit+1)
	}
	if got := libraryErrors.Load(); got != 0 {
		t.Fatalf("oversized body reached the library error handler %d times", got)
	}
}

func TestWebhookOverloadIsImmediateAndBounded(t *testing.T) {
	const secret = "1234567890abcdef"
	var libraryErrors atomic.Int64
	server := newWebhookTestServer(t, secret, &libraryErrors)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)

	started := make([]chan struct{}, webhookMaxInFlight)
	results := make(chan int, webhookMaxInFlight)
	for i := 0; i < webhookMaxInFlight; i++ {
		started[i] = make(chan struct{})
		body := &webhookBlockingBody{started: started[i], release: release}
		req := newWebhookTestRequest(secret, body)
		go func() {
			response := httptest.NewRecorder()
			server.srv.Handler.ServeHTTP(response, req)
			results <- response.Code
		}()
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for i := 0; i < webhookMaxInFlight; i++ {
		select {
		case <-started[i]:
		case <-timer.C:
			t.Fatalf("only %d of %d requests entered the handler", i, webhookMaxInFlight)
		}
	}

	overloadedBody := &webhookCountingBody{reader: strings.NewReader(testWebhookUpdate)}
	overloadedResponse := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(overloadedResponse, newWebhookTestRequest(secret, overloadedBody))
	if overloadedResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("overload status = %d, want 503", overloadedResponse.Code)
	}
	if overloadedBody.reads != 0 || overloadedBody.bytesRead != 0 {
		t.Fatalf("overloaded body was read: reads=%d bytes=%d", overloadedBody.reads, overloadedBody.bytesRead)
	}

	releaseAll()
	for i := 0; i < webhookMaxInFlight; i++ {
		select {
		case status := <-results:
			if status != http.StatusBadRequest {
				t.Fatalf("released request status = %d, want 400 for its empty body", status)
			}
		case <-timer.C:
			t.Fatalf("only %d released requests completed", i)
		}
	}
	if got := libraryErrors.Load(); got != 0 {
		t.Fatalf("admission failures reached the library error handler %d times", got)
	}
}

func TestWebhookHTTPServerLimitsHeadersAndKeepsTimeouts(t *testing.T) {
	var libraryErrors atomic.Int64
	server := newWebhookTestServer(t, "1234567890abcdef", &libraryErrors)
	if server.srv.MaxHeaderBytes != 16<<10 {
		t.Fatalf("MaxHeaderBytes = %d, want %d", server.srv.MaxHeaderBytes, 16<<10)
	}
	if server.srv.ReadHeaderTimeout != 2*time.Second || server.srv.ReadTimeout != 5*time.Second ||
		server.srv.WriteTimeout != 5*time.Second || server.srv.IdleTimeout != 30*time.Second {
		t.Fatalf("HTTP timeouts changed: %+v", server.srv)
	}
}
