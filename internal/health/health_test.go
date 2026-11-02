package health

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

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
			t.Fatalf("GET %s answered %d, HEALTHCHECK needs 200", Path, response.StatusCode)
		}
		if string(body) != "ok" {
			t.Fatalf("the body is %q, security: the probe answer carries no configuration", body)
		}
		return
	}
	t.Fatalf("GET %s never answered : %v", Path, last)
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
			t.Fatalf("an unknown path answered %d,: only %s is exposed", response.StatusCode, Path)
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
			t.Fatalf("Run returned %v, the clean stop of: a cancelled context is a clean stop", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("the listener did not stop inside 2 seconds ")
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
		t.Fatalf("Run bound a port another listener holds ")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", port)) {
		t.Errorf("the error does not name the port of HEALTH_PORT, an operator has to guess: %v", err)
	}
}

// item 7 holds 127.0.0.1:8080, and the old main printed «health is listening on 127.0.0.1: 8080» because
func TestListenNamesTheTakenPortAtOnce(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var port = int64(listener.Addr().(*net.TCPAddr).Port)

	var server = New(port)
	if listenErr := server.Listen(); listenErr == nil {
		t.Fatalf("Listen took a port another listener holds ")
	} else if !strings.Contains(listenErr.Error(), fmt.Sprintf("%d", port)) || !strings.Contains(listenErr.Error(), "HEALTH_PORT") {
		t.Errorf("Listen must name HEALTH_PORT and the number, got: %v", listenErr)
	}
	if server.Port() != port {
		t.Errorf("Port reports %d while nothing was bound on %d — the start line would describe a port the process does not own", server.Port(), port)
	}
}

// TestListenThenRunServes — Listen does not consume the listener: Run afterwards still answers 200 on the
// the printed «health is listening» line has to describe the fact, and main calls both functions).
func TestListenThenRunServes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var port = freePort(t)
	var server = New(port)
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	if again := server.Listen(); again != nil {
		t.Errorf("the second Listen re-bound and failed: %v ", again)
	}
	if server.Port() != port {
		t.Errorf("Port is %d, the listener holds %d ", server.Port(), port)
	}
	go func() { _ = server.Run(ctx) }()

	var client = &http.Client{Timeout: 2 * time.Second}
	var last error
	for attempt := 0; attempt < 40; attempt++ {
		response, getErr := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", server.Port(), Path))
		if getErr != nil {
			last = getErr
			time.Sleep(25 * time.Millisecond)
			continue
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s answered %d after an explicit Listen ", Path, response.StatusCode)
		}
		return
	}
	t.Fatalf("GET %s never answered after Listen+Run: %v", Path, last)
}
