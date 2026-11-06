package webapp

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bot/internal/config"
	"bot/internal/model"
)

const (
	maxBodyBytes          = 16 << 10
	maxHeaderBytes        = 16 << 10
	maxInFlight           = 8
	shutdownTimeout       = 2 * time.Second
	contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors https://web.telegram.org"
)

//go:embed static/index.html static/app.js static/style.css
var staticFiles embed.FS

type App struct {
	cfg     config.Config
	db      *sql.DB
	model   model.Client
	logger  *slog.Logger
	auth    *authenticator
	handler http.Handler
}

func New(cfg config.Config, db *sql.DB, client model.Client, logger *slog.Logger) *App {
	if logger == nil {
		logger = slog.Default()
	}
	app := &App{cfg: cfg, db: db, model: client, logger: logger}
	app.auth = newAuthenticator(cfg.BotToken, cfg.Allows)
	app.handler = securityHeaders(limitInFlight(http.HandlerFunc(app.route), maxInFlight))
	return app
}

func (a *App) Handler() http.Handler {
	return a.handler
}

func (a *App) NewLaunchTicket(chatID int64, userID int64) (string, error) {
	if !a.cfg.MiniAppEnabled {
		return "", errors.New("Mini App is disabled")
	}
	return a.auth.NewLaunchTicket(chatID, userID)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, req)
	})
}

func limitInFlight(next http.Handler, capacity int) http.Handler {
	inFlight := make(chan struct{}, capacity)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case inFlight <- struct{}{}:
			defer func() { <-inFlight }()
		default:
			writeError(w, http.StatusServiceUnavailable, "overloaded", "The Mini App is busy. Try again shortly.")
			return
		}
		req.Body = http.MaxBytesReader(w, req.Body, maxBodyBytes)
		next.ServeHTTP(w, req)
	})
}

func (a *App) route(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Path
	if path == "/healthz" {
		if req.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if strings.HasPrefix(path, "/api/") {
		a.routeAPI(w, req)
		return
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
		return
	}
	a.serveStatic(w, req)
}

func (a *App) routeAPI(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/api/session" {
		switch req.Method {
		case http.MethodPost:
			a.exchangeSession(w, req)
		case http.MethodDelete:
			a.revokeSession(w, req)
		default:
			w.Header().Set("Allow", http.MethodPost+", "+http.MethodDelete)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
		}
		return
	}
	current, err := a.currentScope(req)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "session_expired", "Session expired. Reopen the Mini App from Telegram.")
		return
	}
	switch req.URL.Path {
	case "/api/settings":
		a.settings(w, req, current)
		return
	case "/api/settings/model":
		a.settingsModel(w, req, current)
		return
	case "/api/settings/persona":
		a.settingsPersona(w, req, current)
		return
	case "/api/settings/stream":
		a.settingsStream(w, req, current)
		return
	}
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
		return
	}
	switch req.URL.Path {
	case "/api/status":
		a.status(w, req, current)
	case "/api/history":
		a.history(w, req, current)
	case "/api/documents":
		a.documents(w, req, current)
	case "/api/links":
		a.links(w, req, current)
	case "/api/tasks":
		a.tasks(w, req, current)
	default:
		writeError(w, http.StatusNotFound, "not_found", "API endpoint not found.")
	}
}

func (a *App) exchangeSession(w http.ResponseWriter, req *http.Request) {
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/plain" {
		writeError(w, http.StatusUnsupportedMediaType, "content_type", "Send raw Telegram initData as text/plain.")
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "Request body is too large.")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_body", "Request body could not be read.")
		return
	}
	bearer, expiresAt, err := a.auth.exchange(string(body), req.Header.Get("X-Launch-Ticket"))
	if err != nil {
		status := http.StatusUnauthorized
		code := "invalid_launch"
		if errors.Is(err, ErrNotAllowed) || errors.Is(err, ErrInvalidTicket) {
			status = http.StatusForbidden
		}
		writeError(w, status, code, "Launch verification failed. Reopen the Mini App from Telegram.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": bearer, "expires_at": expiresAt.UTC().Format(time.RFC3339)})
}

func bearerToken(req *http.Request) string {
	parts := strings.Fields(req.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func (a *App) currentScope(req *http.Request) (scope, error) {
	return a.auth.authorizeBearer(bearerToken(req))
}

func (a *App) revokeSession(w http.ResponseWriter, req *http.Request) {
	bearer := bearerToken(req)
	if _, err := a.auth.authorizeBearer(bearer); err != nil {
		writeError(w, http.StatusUnauthorized, "session_expired", "Session expired. Reopen the Mini App from Telegram.")
		return
	}
	a.auth.revoke(bearer)
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

func (a *App) serveStatic(w http.ResponseWriter, req *http.Request) {
	name := ""
	contentType := ""
	switch req.URL.Path {
	case "/":
		name, contentType = "static/index.html", "text/html; charset=utf-8"
	case "/app.js":
		name, contentType = "static/app.js", "text/javascript; charset=utf-8"
	case "/style.css":
		name, contentType = "static/style.css", "text/css; charset=utf-8"
	default:
		writeError(w, http.StatusNotFound, "not_found", "Page not found.")
		return
	}
	content, err := staticFiles.ReadFile(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "static_error", "Frontend is unavailable.")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	if req.Method == http.MethodGet {
		_, _ = w.Write(content)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(true)
	_ = encoder.Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

type Server struct {
	app      *App
	srv      *http.Server
	listener net.Listener
}

func Listen(app *App, address string) (*Server, error) {
	if app == nil || app.db == nil {
		return nil, errors.New("Mini App needs an application and database")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("Mini App listener address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() && !ip.IsUnspecified() {
		return nil, errors.New("Mini App listener must use a loopback IP address or 0.0.0.0 for a container bridge")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("Mini App listener %s: %w", address, err)
	}
	return &Server{app: app, listener: listener, srv: &http.Server{
		Handler: app.Handler(), MaxHeaderBytes: maxHeaderBytes,
		ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}}, nil
}

func (s *Server) URL() string {
	return "http://" + s.listener.Addr().String()
}

func (s *Server) Close() error {
	return s.listener.Close()
}

func (s *Server) Run(ctx context.Context) error {
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.srv.Serve(s.listener) }()
	var result error
	select {
	case err := <-serveDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("Mini App HTTP listener: %w", err)
		} else if ctx.Err() == nil {
			result = errors.New("Mini App HTTP listener stopped without cancellation")
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := s.srv.Shutdown(shutdownCtx); err != nil && result == nil {
		result = fmt.Errorf("Mini App shutdown: %w", err)
	}
	return result
}
