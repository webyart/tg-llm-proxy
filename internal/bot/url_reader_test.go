package bot

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"bot/internal/model"
)

type urlReaderResolverStub struct {
	answers [][]net.IP
	calls   int
}

func (r *urlReaderResolverStub) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	if r.calls >= len(r.answers) {
		return nil, errors.New("unexpected DNS lookup")
	}
	answer := r.answers[r.calls]
	r.calls++
	return append([]net.IP(nil), answer...), nil
}

type urlReaderTestServer struct {
	server        *httptest.Server
	client        *http.Client
	url           string
	resolver      *urlReaderResolverStub
	dialAddresses []string
}

func newURLReaderTestServer(t *testing.T, handler http.Handler, answers ...[]net.IP) *urlReaderTestServer {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("server address: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	fixture := &urlReaderTestServer{
		server:   server,
		url:      "https://example.com:" + port,
		resolver: &urlReaderResolverStub{answers: answers},
	}
	fixture.client = newURLReaderClient(urlReaderHooks{
		resolver:   fixture.resolver,
		tlsRootCAs: roots,
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			fixture.dialAddresses = append(fixture.dialAddresses, address)
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	})
	return fixture
}

func executeTestURLReader(ctx context.Context, fixture *urlReaderTestServer, rawURL string) (map[string]string, error) {
	arguments, _ := json.Marshal(map[string]string{"url": rawURL})
	result, err := executeURLReaderWithClient(ctx, model.ToolCall{Name: "read_url", Arguments: string(arguments)}, fixture.client)
	if err != nil {
		return nil, err
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func TestURLReaderToolContract(t *testing.T) {
	if tools := urlReaderTools(false); tools != nil {
		t.Fatalf("disabled tools=%v", tools)
	}
	tools := urlReaderTools(true)
	if len(tools) != 1 || tools[0].Name != "read_url" {
		t.Fatalf("tools=%v", tools)
	}
	if _, err := executeURLReader(context.Background(), model.ToolCall{Name: "shell", Arguments: `{}`}); err == nil {
		t.Fatal("unknown tool must be rejected")
	}
	for _, arguments := range []string{`{"url":`, `{"url":"https://example.com","extra":true}`, `{"url":"https://example.com"} {}`} {
		if _, err := executeURLReader(context.Background(), model.ToolCall{Name: "read_url", Arguments: arguments}); err == nil {
			t.Errorf("malformed call %q must be rejected", arguments)
		}
	}
}

func TestURLReaderRejectsUnsafeURLs(t *testing.T) {
	fixture := &urlReaderTestServer{client: &http.Client{}}
	urls := []string{
		"http://example.com",
		"https://user:password@example.com",
		"https://localhost",
		"https://sub.localhost",
		"https://127.0.0.1",
		"https://10.0.0.1",
		"https://[::1]",
		"https://169.254.169.254",
		"https://224.0.0.1",
		"https://0.0.0.0",
		"https://192.0.2.1",
		"https://100.64.0.1",
		"https://198.18.0.1",
		"https://240.0.0.1",
		"https://[2001:db8::1]",
		"https://[64:ff9b::7f00:1]",
		"https://[64:ff9b::a00:1]",
		"https://[64:ff9b::a9fe:a9fe]",
		"https://example.com/" + strings.Repeat("a", urlReaderMaxURLBytes),
	}
	for _, rawURL := range urls {
		payload, err := executeTestURLReader(context.Background(), fixture, rawURL)
		if err != nil {
			t.Errorf("URL validation returned top-level error: %v", err)
			continue
		}
		if payload["error"] == "" || payload["result"] != "" {
			t.Errorf("unsafe URL was not rejected: %v", payload)
		}
	}
}

func TestURLReaderRejectsNAT64PrivateDNSAnswer(t *testing.T) {
	fixture := newURLReaderTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("private"))
	}), []net.IP{net.ParseIP("64:ff9b::a9fe:a9fe")})
	payload, err := executeTestURLReader(context.Background(), fixture, fixture.url)
	if err != nil || payload["error"] == "" {
		t.Fatalf("NAT64 private DNS answer payload=%v err=%v", payload, err)
	}
	if len(fixture.dialAddresses) != 0 {
		t.Fatalf("NAT64 private address was dialed: %v", fixture.dialAddresses)
	}
}

func TestURLReaderRejectsRedirectToPrivateIP(t *testing.T) {
	fixture := newURLReaderTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Location", "https://127.0.0.1/private")
		w.WriteHeader(http.StatusFound)
	}), []net.IP{net.ParseIP("93.184.216.34")})
	payload, err := executeTestURLReader(context.Background(), fixture, fixture.url)
	if err != nil || payload["error"] == "" {
		t.Fatalf("redirect payload=%v err=%v", payload, err)
	}
	if len(fixture.dialAddresses) != 1 {
		t.Fatalf("private redirect was dialed: %v", fixture.dialAddresses)
	}
}

func TestURLReaderRejectsOversizedBody(t *testing.T) {
	fixture := newURLReaderTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("a", urlReaderMaxBodyBytes+1)))
	}), []net.IP{net.ParseIP("93.184.216.34")})
	payload, err := executeTestURLReader(context.Background(), fixture, fixture.url)
	if err != nil || payload["error"] != "response body exceeds 1 MiB" {
		t.Fatalf("oversized body payload=%v err=%v", payload, err)
	}
}

func TestURLReaderRejectsUnsupportedContentType(t *testing.T) {
	fixture := newURLReaderTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("binary"))
	}), []net.IP{net.ParseIP("93.184.216.34")})
	payload, err := executeTestURLReader(context.Background(), fixture, fixture.url)
	if err != nil || payload["error"] != "unsupported content type" {
		t.Fatalf("content type payload=%v err=%v", payload, err)
	}
}

func TestURLReaderHTTPSCleanupAndRuneLimit(t *testing.T) {
	fixture := newURLReaderTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/text":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(strings.Repeat("a", urlReaderMaxRunes-1) + "🙂" + strings.Repeat("b", 20)))
		case "/html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head><style>hidden</style></head><body><h1>Hello&nbsp;</h1><script>secret</script><p>world &amp; all</p></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}), []net.IP{net.ParseIP("93.184.216.34")}, []net.IP{net.ParseIP("93.184.216.34")})
	payload, err := executeTestURLReader(context.Background(), fixture, fixture.url+"/text")
	if err != nil || payload["error"] != "" {
		t.Fatalf("text payload error=%v err=%v", payload, err)
	}
	if got := payload["result"]; utf8.RuneCountInString(got) != urlReaderMaxRunes || !strings.HasSuffix(got, "🙂") || strings.Contains(got, "b") {
		t.Fatalf("text was not truncated at a rune boundary: length=%d suffix=%q", utf8.RuneCountInString(got), got[len(got)-4:])
	}
	payload, err = executeTestURLReader(context.Background(), fixture, fixture.url+"/html")
	if err != nil || payload["result"] != "Hello world & all" {
		t.Fatalf("HTML payload=%v err=%v", payload, err)
	}
}

func TestURLReaderDNSRebindingAndDirectDial(t *testing.T) {
	fixture := newURLReaderTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}), []net.IP{net.ParseIP("93.184.216.34")}, []net.IP{net.ParseIP("127.0.0.1")})
	payload, err := executeTestURLReader(context.Background(), fixture, fixture.url)
	if err != nil || payload["result"] != "ok" {
		t.Fatalf("first request payload=%v err=%v", payload, err)
	}
	_, port, err := net.SplitHostPort(fixture.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	wantAddress := net.JoinHostPort("93.184.216.34", port)
	if len(fixture.dialAddresses) != 1 || fixture.dialAddresses[0] != wantAddress {
		t.Fatalf("dialed addresses=%v want=%s", fixture.dialAddresses, wantAddress)
	}
	payload, err = executeTestURLReader(context.Background(), fixture, fixture.url)
	if err != nil || payload["error"] == "" {
		t.Fatalf("rebound request payload=%v err=%v", payload, err)
	}
	if fixture.resolver.calls != 2 || len(fixture.dialAddresses) != 1 {
		t.Fatalf("DNS calls=%d dials=%v", fixture.resolver.calls, fixture.dialAddresses)
	}
}
