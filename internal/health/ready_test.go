package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDB struct {
	err   error
	pings atomic.Int64
}

func (f *fakeDB) PingContext(ctx context.Context) error {
	f.pings.Add(1)
	return f.err
}

type fakeModels struct {
	err   error
	calls atomic.Int64
}

func (f *fakeModels) Models(ctx context.Context) ([]string, error) {
	f.calls.Add(1)
	return nil, f.err
}

type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// startReadyServer binds the listener through Run and waits until /healthz answers.
func startReadyServer(t *testing.T, configure func(*Server)) (*Server, string, *http.Client) {
	t.Helper()
	var port = freePort(t)
	var server = New(port)
	if configure != nil {
		configure(server)
	}
	var ctx, cancel = context.WithCancel(context.Background())
	var done = make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Run(ctx)
	}()

	var client = &http.Client{Timeout: 2 * time.Second}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("the health listener did not stop")
		}
	})

	var base = fmt.Sprintf("http://127.0.0.1:%d", port)
	for attempt := 0; attempt < 40; attempt++ {
		response, err := client.Get(base + Path)
		if err == nil {
			response.Body.Close()
			return server, base, client
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the health listener never answered")
	return nil, "", nil
}

type readyReport struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func probeReady(t *testing.T, client *http.Client, base string) (int, readyReport) {
	t.Helper()
	response, err := client.Get(base + ReadyPath)
	if err != nil {
		t.Fatalf("GET %s: %v", ReadyPath, err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatalf("read %s: %v", ReadyPath, readErr)
	}
	var report readyReport
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("%s answered %q, not JSON: %v", ReadyPath, body, err)
	}
	return response.StatusCode, report
}

func TestReadyzAnswers200WithFakes(t *testing.T) {
	var db = &fakeDB{}
	var models = &fakeModels{}
	var _, base, client = startReadyServer(t, func(s *Server) { s.RegisterReadiness(db, models) })

	var code, report = probeReady(t, client, base)
	if code != http.StatusOK {
		t.Fatalf("GET %s answered %d, want 200: %+v", ReadyPath, code, report)
	}
	if report.Status != "ready" {
		t.Errorf("status = %q, want ready", report.Status)
	}
	if report.Checks["database"] != "ok" || report.Checks["model"] != "ok" {
		t.Errorf("checks = %+v, want both ok", report.Checks)
	}
	if db.pings.Load() == 0 {
		t.Error("the database was not pinged")
	}
}

func TestHealthzStillAnswers200NextToReadyz(t *testing.T) {
	var _, base, client = startReadyServer(t, func(s *Server) { s.RegisterReadiness(&fakeDB{}, &fakeModels{}) })
	var response, err = client.Get(base + Path)
	if err != nil {
		t.Fatalf("GET %s: %v", Path, err)
	}
	defer response.Body.Close()
	var body, readErr = io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("GET %s answered %d %q, liveness must stay a plain 200 ok", Path, response.StatusCode, body)
	}
}

func TestReadyzAnswers503OnDatabaseError(t *testing.T) {
	var db = &fakeDB{err: errors.New("database is locked")}
	var models = &fakeModels{}
	var _, base, client = startReadyServer(t, func(s *Server) { s.RegisterReadiness(db, models) })

	var code, report = probeReady(t, client, base)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s answered %d, want 503", ReadyPath, code)
	}
	if report.Status != "not_ready" {
		t.Errorf("status = %q, want not_ready", report.Status)
	}
	if !strings.Contains(report.Checks["database"], "locked") {
		t.Errorf("checks[database] = %q, want the database error", report.Checks["database"])
	}
	if report.Checks["model"] != "ok" {
		t.Errorf("checks[model] = %q, want ok — one failing check must not hide a passing one", report.Checks["model"])
	}
}

func TestReadyzAnswers503OnModelError(t *testing.T) {
	var models = &fakeModels{err: errors.New("connection refused")}
	var _, base, client = startReadyServer(t, func(s *Server) { s.RegisterReadiness(&fakeDB{}, models) })

	var code, report = probeReady(t, client, base)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s answered %d, want 503", ReadyPath, code)
	}
	if !strings.Contains(report.Checks["model"], "refused") {
		t.Errorf("checks[model] = %q, want the model error", report.Checks["model"])
	}
}

func TestReadyzCachesTheModelProbe(t *testing.T) {
	var clock = &fakeClock{at: time.Unix(1000, 0)}
	var models = &fakeModels{}
	var _, base, client = startReadyServer(t, func(s *Server) {
		s.now = clock.Now
		s.RegisterReadiness(&fakeDB{}, models)
	})

	probeReady(t, client, base)
	probeReady(t, client, base)
	if models.calls.Load() != 1 {
		t.Fatalf("the model was probed %d times inside the 30 s window, want 1", models.calls.Load())
	}

	clock.Advance(modelProbeTTL + time.Second)
	probeReady(t, client, base)
	if models.calls.Load() != 2 {
		t.Fatalf("the model was probed %d times after the window expired, want 2", models.calls.Load())
	}
}

func TestHandleServesAnExtraEndpointOnTheSameListener(t *testing.T) {
	var _, base, client = startReadyServer(t, func(s *Server) {
		s.Handle(MetricsPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "# HELP x_total X.\n")
		}))
	})
	var response, err = client.Get(base + MetricsPath)
	if err != nil {
		t.Fatalf("GET %s: %v", MetricsPath, err)
	}
	defer response.Body.Close()
	var body, readErr = io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "x_total") {
		t.Fatalf("GET %s answered %d %q", MetricsPath, response.StatusCode, body)
	}
}

func TestReadyzWithoutDependenciesAnswers503(t *testing.T) {
	var _, base, client = startReadyServer(t, nil)
	var code, report = probeReady(t, client, base)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s answered %d without dependencies, want 503", ReadyPath, code)
	}
	if report.Checks["database"] == "ok" || report.Checks["model"] == "ok" {
		t.Errorf("checks = %+v, want the missing dependencies named", report.Checks)
	}
}
