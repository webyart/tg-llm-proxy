package health

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"bot/internal/testutil"
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
			var client = &http.Client{Timeout: 3 * time.Second}
			var probe = func() (int, string, error) {
				var response, err = client.Get("http://127.0.0.1:" + strconv.FormatInt(server.Port(), 10) + Path)
				if err != nil {
					return 0, "", err
				}
				defer response.Body.Close()
				var body, _ = io.ReadAll(response.Body)
				return response.StatusCode, string(body), nil
			}
			// The listener is bound asynchronously; poll for the answer instead of guessing how long it takes.
			testutil.Eventually(t, 3*time.Second, func() bool {
				if server.listener == nil {
					return false
				}
				var status, body, err = probe()
				return err == nil && status == http.StatusOK && body == "ok"
			}, func() string {
				var status, body, err = probe()
				return fmt.Sprintf("GET %s for host %q = %d %q, err=%v", Path, host, status, body, err)
			})
			cancel()
			select {
			case <-served:
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not stop after cancellation")
			}
		})
	}
}
