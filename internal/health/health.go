// Two measured facts shape it:
//
//	   so a listening server is what keeps the process alive; `docker compose restart` then has
//	   something to graceful-shut down instead of being SIGKILLed when Docker's stop grace ends — the
//	2. /healthz answers 200 as soon as the server is listening on the loopback interface the
//	   HEALTHCHECK runs in — the database and Telegram are dependencies of the bot, not of the
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	// Path is the liveness probe: 200 as soon as the listener is bound.
	Path = "/healthz"
	// ReadyPath is the readiness probe: 200 only when the database answers and the model is reachable.
	ReadyPath = "/readyz"
	// MetricsPath is served from the same loopback listener as the probes.
	MetricsPath = "/metrics"
)

const (
	readyTimeout  = time.Second
	modelProbeTTL = 30 * time.Second
)

// DBPinger is the part of *sql.DB that /readyz needs.
type DBPinger interface {
	PingContext(ctx context.Context) error
}

// ModelLister is the part of model.Client that /readyz needs.
type ModelLister interface {
	Models(ctx context.Context) ([]string, error)
}

// Server — the health listener; Run blocks until the context is cancelled, Shutdown then unwinds.
// error of a taken port to the caller that promises «/healthz answers», instead of hiding it inside a
// goroutine whose error only arrives after the bot has already started polling.
type Server struct {
	srv      *http.Server
	port     int64
	listener net.Listener
	mux      *http.ServeMux

	mu           sync.Mutex
	db           DBPinger
	models       ModelLister
	modelChecked time.Time
	modelErr     error
	now          func() time.Time
}

// bind, so the caller learns about a taken port at the moment it is about to claim the endpoint works.
func New(port int64) *Server {
	mux := http.NewServeMux()
	s := &Server{port: port, mux: mux, now: time.Now}
	mux.HandleFunc(Path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc(ReadyPath, s.handleReady)
	// outside caller could read.
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}
	return s
}

// Handle registers one more endpoint on the loopback listener (main uses it for /metrics). Call it
// before Run; the probe paths themselves cannot be replaced.
func (s *Server) Handle(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
}

// RegisterReadiness sets the dependencies /readyz probes. It may be called before Run; a missing
// dependency makes the probe answer 503 instead of pretending to be ready.
func (s *Server) RegisterReadiness(db DBPinger, models ModelLister) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db = db
	s.models = models
	s.modelChecked = time.Time{}
	s.modelErr = nil
}

// handleReady answers 200 with per-check "ok" strings when every dependency passes, 503 otherwise.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var checks, ready = s.readiness(r.Context())
	var report = struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}{Status: "ready", Checks: checks}
	if !ready {
		report.Status = "not_ready"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if ready {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(report)
}

func (s *Server) readiness(ctx context.Context) (map[string]string, bool) {
	var checks = map[string]string{}
	var ready = true

	s.mu.Lock()
	var db, models = s.db, s.models
	s.mu.Unlock()

	if db == nil {
		checks["database"] = "not configured"
		ready = false
	} else if err := pingDatabase(ctx, db); err != nil {
		checks["database"] = checkText(err)
		ready = false
	} else {
		checks["database"] = "ok"
	}

	if err := s.modelError(ctx, models); err != nil {
		checks["model"] = checkText(err)
		ready = false
	} else {
		checks["model"] = "ok"
	}
	return checks, ready
}

func pingDatabase(ctx context.Context, db DBPinger) error {
	var pingCtx, cancel = context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	return db.PingContext(pingCtx)
}

// modelError probes the model at most once per modelProbeTTL: /readyz is called every few seconds by a
// supervisor, and a probe that reaches the upstream on every call would be a load of its own.
func (s *Server) modelError(ctx context.Context, models ModelLister) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if models == nil {
		return errors.New("the model client is not configured")
	}
	if !s.modelChecked.IsZero() && s.now().Sub(s.modelChecked) < modelProbeTTL {
		return s.modelErr
	}
	var probeCtx, cancel = context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	_, err := models.Models(probeCtx)
	s.modelChecked = s.now()
	s.modelErr = err
	return err
}

// checkText keeps a probe answer readable and short. Error texts of this program never carry the bot
// token or the API key; the cap only protects the response body from a provider's html page.
func checkText(err error) string {
	var text = err.Error()
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

// prints «health is listening»: measured on this machine, a HEALTH_PORT that another process already
// only later, when the goroutine's error reached the select — after getMe and setMyCommands had burned
func (s *Server) Listen() error {
	if s.listener != nil {
		return nil
	}
	var address = fmt.Sprintf("127.0.0.1:%d", s.port)
	var listener, err = net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("health: HEALTH_PORT %d cannot be listened on (%w) —: the port of the table is taken by another process", s.port, err)
	}
	s.listener = listener
	return nil
}

// rather than its own number, so the line cannot describe a port the process does not own.
func (s *Server) Port() int64 {
	if s.listener == nil {
		return s.port
	}
	var addr, ok = s.listener.Addr().(*net.TCPAddr)
	if !ok {
		return s.port
	}
	return int64(addr.Port)
}

// It returns nil on a clean stop and an error when the port is taken or the listener dies.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Listen(); err != nil {
		return err
	}
	var address = s.listener.Addr().String()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- s.srv.Serve(s.listener)
	}()

	select {
	case err := <-serveDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("health: the listener on %s failed (%w)", address, err)
		}
		return nil
	case <-ctx.Done():
		// The unwind window is 2 seconds — this package's own budget, the same one main gives the polling
		// 10-second stop grace (docker-compose.yml sets no `stop_grace_period` of its own).
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("health: /healthz did not stop inside 2 seconds (%w) — the 2 s budget of this package", err)
		}
		return nil
	}
}
