package bot

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestTelegramPollingFallsBackDirectAndReplaysBody(t *testing.T) {
	proxyCalls := 0
	directCalls := 0
	transport := &telegramFallbackTransport{
		proxy: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			proxyCalls++
			return nil, errors.New("proxy TLS timeout")
		}),
		direct: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			directCalls++
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			if string(body) != "offset=41" {
				t.Fatalf("replayed body = %q, want offset=41", body)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
		}),
		proxyFor: func(*http.Request) (*url.URL, error) { return &url.URL{Scheme: "http", Host: "proxy.local"}, nil },
	}
	req := newRequest(t, "/getUpdates", "offset=41")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if proxyCalls != 1 || directCalls != 1 {
		t.Fatalf("proxy/direct calls = %d/%d, want 1/1", proxyCalls, directCalls)
	}
}

func TestTelegramDeliveryDoesNotFallBack(t *testing.T) {
	directCalls := 0
	wantErr := errors.New("proxy connection failed after delivery")
	transport := &telegramFallbackTransport{
		proxy: roundTripFunc(func(req *http.Request) (*http.Response, error) { return nil, wantErr }),
		direct: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			directCalls++
			return nil, nil
		}),
		proxyFor: func(*http.Request) (*url.URL, error) { return &url.URL{Scheme: "http", Host: "proxy.local"}, nil },
	}
	_, err := transport.RoundTrip(newRequest(t, "/sendMessage", "text=once"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want the original delivery error", err)
	}
	if directCalls != 0 {
		t.Fatalf("sendMessage used the direct fallback %d times; a duplicate response would be unsafe", directCalls)
	}
}

func newRequest(t *testing.T, path, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://api.telegram.org/botfake"+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return req
}
