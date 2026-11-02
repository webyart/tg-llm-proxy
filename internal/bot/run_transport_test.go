package bot

import (
	"net/http"
	"testing"
	"time"
)

func TestDefaultTelegramHTTPClientUsesExtendedTLSHandshakeBudget(t *testing.T) {
	client := defaultTelegramHTTPClient(DefaultPollTimeout)
	if client.Timeout != DefaultPollTimeout+5*time.Second {
		t.Fatalf("client.Timeout = %s, want %s", client.Timeout, DefaultPollTimeout+5*time.Second)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.TLSHandshakeTimeout != 30*time.Second {
		t.Fatalf("TLSHandshakeTimeout = %s, want 30s", transport.TLSHandshakeTimeout)
	}
	if transport.Proxy == nil || transport.DialContext == nil {
		t.Fatal("cloned transport must retain proxy and dial behavior of http.DefaultTransport")
	}
}

func TestTelegramInitTimeoutCoversTLSHandshake(t *testing.T) {
	if InitTimeout < TLSHandshakeTimeout {
		t.Fatalf("InitTimeout = %s, TLSHandshakeTimeout = %s; startup must not expire first", InitTimeout, TLSHandshakeTimeout)
	}
}
