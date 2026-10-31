// Package health — the §10 День 5 module: GET /healthz on HEALTH_PORT (§7), the target of the
// compose HEALTHCHECK (§8.1, `wget -qO- http://127.0.0.1:8080/healthz`).
//
// Two measured facts shape it:
//  1. The container's process is PID 1 of the run stage (§8.1 `USER 65534`, `ENTRYPOINT ["/bot"]`),
//     so a listening server is what keeps the process alive; `docker compose restart` then has
//     something to graceful-shut down instead of being SIGKILLed when Docker's stop grace ends — the
//     graceful shutdown §10 День 1 asks for, and the 2-second unwind window below is this package's own.
//  2. /healthz answers 200 as soon as the server is listening on the loopback interface the
//     HEALTHCHECK runs in — the database and Telegram are dependencies of the bot, not of the
//     health answer, and §12 reliability wants the container reported healthy while the store
//     reconnects (§8.1 DoD: /healthz = 200 while the chat history survives a restart).
package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Path — the single endpoint of §10 День 5 and of the compose HEALTHCHECK.
const Path = "/healthz"

// Server — the health listener; Run blocks until the context is cancelled, Shutdown then unwinds.
// The listener is a field, because §12 reliability wants the bind to be observable: Listen returns the
// error of a taken port to the caller that promises «/healthz answers», instead of hiding it inside a
// goroutine whose error only arrives after the bot has already started polling.
type Server struct {
	srv      *http.Server
	port     int64
	listener net.Listener
}

// New — the server for the port of §7 (HEALTH_PORT, default 8080). Nothing is bound yet: Listen does the
// bind, so the caller learns about a taken port at the moment it is about to claim the endpoint works.
func New(port int64) *Server {
	mux := http.NewServeMux()
	s := &Server{port: port}
	mux.HandleFunc(Path, func(w http.ResponseWriter, r *http.Request) {
		// §12 security: the answer carries no configuration, no token and no model text — the
		// HEALTHCHECK of §8.1 only needs a 200, and the log line of §12.2 names the probe.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	// §6: nothing else is exposed. A request to another path answers 404 rather than a body an
	// outside caller could read.
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}
	return s
}

// Listen — bind the loopback address of §12 security and keep the listener. main calls this BEFORE it
// prints «health is listening»: measured on this machine, a HEALTH_PORT that another process already
// holds (the local STRATA proxy sits on 8080, §14 item 7) made the old start print the promise and die
// only later, when the goroutine's error reached the select — after getMe and setMyCommands had burned
// the timeout of §7. §12 reliability: a claim about the running system is made only after the fact.
// Calling it twice is a no-op, so Run stays usable on its own (the tests of §11 call Run directly).
func (s *Server) Listen() error {
	if s.listener != nil {
		return nil
	}
	var address = fmt.Sprintf("127.0.0.1:%d", s.port)
	var listener, err = net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("health: HEALTH_PORT %d cannot be listened on (%w) — §7: the port of the §7 table is taken by another process", s.port, err)
	}
	s.listener = listener
	return nil
}

// Port — the port the listener actually holds. With HEALTH_PORT of §7 pinned by config, main prints this
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

// Run — serve on the bound listener until ctx is done. The bind is loopback-only: §12 security keeps
// /healthz inside the container network namespace, and compose's HEALTHCHECK probes 127.0.0.1 (§8.1).
// It returns nil on a clean stop and an error when the port is taken or the listener dies.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Listen(); err != nil {
		return err
	}
	var address = s.listener.Addr().String()
	serveDone := make(chan error, 1)
	go func() {
		// http.ErrServerClosed is the Shutdown path, not a failure (the clean stop of §10 День 1).
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
		// goroutine (§10 День 1 graceful shutdown) — so the container exits well inside Docker's default
		// 10-second stop grace (docker-compose.yml sets no `stop_grace_period` of its own).
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("health: /healthz did not stop inside 2 seconds (%w) — the 2 s budget of this package", err)
		}
		return nil
	}
}
