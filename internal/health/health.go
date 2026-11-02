// Two measured facts shape it:
//
//	   so a listening server is what keeps the process alive; `docker compose restart` then has
//	   something to graceful-shut down instead of being SIGKILLed when Docker's stop grace ends — the
//	2. /healthz answers 200 as soon as the server is listening on the loopback interface the
//	   HEALTHCHECK runs in — the database and Telegram are dependencies of the bot, not of the
package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

const Path = "/healthz"

// Server — the health listener; Run blocks until the context is cancelled, Shutdown then unwinds.
// error of a taken port to the caller that promises «/healthz answers», instead of hiding it inside a
// goroutine whose error only arrives after the bot has already started polling.
type Server struct {
	srv      *http.Server
	port     int64
	listener net.Listener
}

// bind, so the caller learns about a taken port at the moment it is about to claim the endpoint works.
func New(port int64) *Server {
	mux := http.NewServeMux()
	s := &Server{port: port}
	mux.HandleFunc(Path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	// outside caller could read.
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}
	return s
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
