// health_test.go — §11 Store/health levels: /healthz answers 200 on the loopback port of §7, the
// compose HEALTHCHECK target of §8.1 (`wget -qO- http://127.0.0.1:8080/healthz`), and a cancelled
// context stops the listener inside the 2-second window of §12.1.4 MsgStuckShutdown.
package health

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// freePort — an OS-assigned loopback port, so a test never collides with HEALTH_PORT 8080 of §7.
func freePort(t *testing.T) int64 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return int64(listener.Addr().(*net.TCPAddr).Port)
}

func TestHealthzAnswers200(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := New(freePort(t))
	go func() { _ = server.Run(ctx) }()

	client := &http.Client{Timeout: 2 * time.Second}
	var last error
	for attempt := 0; attempt < 40; attempt++ {
		response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", server.port, Path))
		if err != nil {
			last = err
			time.Sleep(25 * time.Millisecond)
			continue
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s answered %d, §8.1 HEALTHCHECK needs 200", Path, response.StatusCode)
		}
		if string(body) != "ok" {
			t.Fatalf("the body is %q, §12 security: the probe answer carries no configuration", body)
		}
		return
	}
	t.Fatalf("GET %s never answered (§8.1 HEALTHCHECK): %v", Path, last)
}

func TestOtherPathIs404(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := New(freePort(t))
	go func() { _ = server.Run(ctx) }()

	client := &http.Client{Timeout: 2 * time.Second}
	for attempt := 0; attempt < 40; attempt++ {
		response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthzz", server.port))
		if err != nil {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("an unknown path answered %d, §6: only %s is exposed", response.StatusCode, Path)
		}
		return
	}
	t.Fatalf("the listener never answered")
}

func TestCancelStopsInsideTwoSeconds(t *testing.T) {
	server := New(freePort(t))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	// Give the listener a moment to bind, then stop it the way a signal stops the program.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, §12.1.4 MsgStopped: a cancelled context is a clean stop", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("the listener did not stop inside 2 seconds (§12.1.4 MsgStuckShutdown, §8.1 stop grace)")
	}
}

func TestTakenPortFailsWithThePortNamed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := int64(listener.Addr().(*net.TCPAddr).Port)

	err = New(port).Run(context.Background())
	if err == nil {
		t.Fatalf("Run bound a port another listener holds (§12 reliability: the start must fail)")
	}
}
