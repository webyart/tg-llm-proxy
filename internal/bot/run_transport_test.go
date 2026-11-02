package bot

import (
	"net/http"
	"testing"
	"time"
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
