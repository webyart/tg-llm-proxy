package health

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// TestNewOnBindsTheRequestedHost — a container passes 0.0.0.0 so the published port and Prometheus can reach the
// listener; the probe paths must answer on it exactly like on the loopback default.
func TestNewOnBindsTheRequestedHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "0.0.0.0"} {
		t.Run(host, func(t *testing.T) {
			var server = NewOn(host, 0)
			var ctx, cancel = context.WithCancel(context.Background())
			defer cancel()
			var served = make(chan error, 1)
			go func() { served <- server.Run(ctx) }()
			var deadline = time.Now().Add(3 * time.Second)
			for server.listener == nil && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if server.listener == nil {
				t.Fatalf("the listener did not bind for host %q", host)
			}
			var client = &http.Client{Timeout: 3 * time.Second}
			var response, err = client.Get("http://127.0.0.1:" + strconv.FormatInt(server.Port(), 10) + Path)
			if err != nil {
				t.Fatalf("GET %s: %v", Path, err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != http.StatusOK || string(body) != "ok" {
				t.Fatalf("GET %s = %d %q", Path, response.StatusCode, body)
			}
			cancel()
			select {
			case <-served:
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not stop after cancellation")
			}
		})
	}
}
