// Package health — the §10 День 5 module: GET /healthz on HEALTH_PORT (§7), the target of the
// compose HEALTHCHECK (§8.1, `wget -qO- http://127.0.0.1:8080/healthz`).
//
// Two measured facts shape it:
//  1. The container's process is PID 1 of the run stage (§8.1 `USER 65534`, `ENTRYPOINT ["/bot"]`),
//     so a listening server is what keeps the process alive; `docker compose restart` then has
//     something to graceful-shut down instead of SIGKILL after the stop grace period (§12.1.4
//     MsgStuckShutdown: the bot must unwind inside 2 seconds of the signal).
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
type Server struct {
	srv  *http.Server
	port int64
}

// New — the server bound to the port of §7 (HEALTH_PORT, default 8080). The listen happens in Run
// so a port conflict names the port in the error of the start, not in an ignored field.
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

// Run — listen on the loopback interface and serve until ctx is done. The bind is loopback-only:
// §12 security keeps /healthz inside the container network namespace, and compose's HEALTHCHECK
// probes 127.0.0.1 (§8.1). It returns nil on a clean stop and an error when the port is taken.
func (s *Server) Run(ctx context.Context) error {
	address := fmt.Sprintf("127.0.0.1:%d", s.port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("health: HEALTH_PORT %d cannot be listened on (%w) — §7: the port of the §7 table is taken by another process", s.port, err)
	}
	serveDone := make(chan error, 1)
	go func() {
		// http.ErrServerClosed is the Shutdown path, not a failure (§12.1.4 MsgStopped).
		serveDone <- s.srv.Serve(listener)
	}()

	select {
	case err := <-serveDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("health: the listener on %s failed (%w)", address, err)
		}
		return nil
	case <-ctx.Done():
		// §12.1.4 MsgStuckShutdown: the unwind window is 2 seconds, the same budget main gives the
		// bot schedule, so the container exits inside the 10-second stop grace period of Docker.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("health: /healthz did not stop inside 2 seconds (%w) — §12.1.4 MsgStuckShutdown", err)
		}
		return nil
	}
}
