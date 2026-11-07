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
	"sync"
	"time"

	"bot/internal/bot"
	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
)

const (
	maxBodyBytes          = 16 << 10
	maxHeaderBytes        = 16 << 10
	maxInFlight           = 8
	shutdownTimeout       = 2 * time.Second
	contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors https://web.telegram.org"
	mutationRateLimit     = 30
	exportRateLimit       = 5
	rateLimitWindow       = time.Minute
	maxRateLimitUsers     = 4096
)

//go:embed static/index.html static/app.js static/style.css
var staticFiles embed.FS

type rateLimitBucket struct {
	startedAt time.Time
	count     int
}

type rateLimitPrincipal struct {
	mutation rateLimitBucket
	export   rateLimitBucket
}

type perUserRateLimiter struct {
	mu         sync.Mutex
	now        func() time.Time
	principals map[int64]rateLimitPrincipal
}

func newPerUserRateLimiter() *perUserRateLimiter {
	return &perUserRateLimiter{now: time.Now, principals: map[int64]rateLimitPrincipal{}}
}

func rateLimitBucketExpired(bucket rateLimitBucket, now time.Time) bool {
	return bucket.startedAt.IsZero() || !now.Before(bucket.startedAt.Add(rateLimitWindow))
}

func (l *perUserRateLimiter) allow(userID int64, export bool) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for id, principal := range l.principals {
		if rateLimitBucketExpired(principal.mutation, now) && rateLimitBucketExpired(principal.export, now) {
			delete(l.principals, id)
		}
	}
	principal, found := l.principals[userID]
	if !found {
		if len(l.principals) >= maxRateLimitUsers {
			return false, rateLimitWindow
		}
		principal = rateLimitPrincipal{}
	}
	bucket := &principal.mutation
	limit := mutationRateLimit
	if export {
		bucket = &principal.export
		limit = exportRateLimit
	}
	if rateLimitBucketExpired(*bucket, now) {
		*bucket = rateLimitBucket{startedAt: now}
	}
	if bucket.count >= limit {
		return false, bucket.startedAt.Add(rateLimitWindow).Sub(now)
	}
	bucket.count++
	l.principals[userID] = principal
	return true, 0
}

var errLifecycleUnavailable = errors.New("lifecycle unavailable")

type TaskLifecycle interface {
	CreateTask(ctx context.Context, chatID int64, userID int64, goal string) (store.Task, error)
	CancelTask(ctx context.Context, chatID int64) (store.Task, bool, error)
	ResumeTask(ctx context.Context, chatID int64, taskID int64) (store.Task, bool, error)
	ClearTasks(ctx context.Context, chatID int64) (int64, error)
}

type LinkLifecycle interface {
	CreateLink(ctx context.Context, chatID int64, userID int64, rawURL string) (store.SavedLink, bool, error)
}

type storeTaskLifecycle struct {
	db *sql.DB
}

func (l storeTaskLifecycle) CreateTask(context.Context, int64, int64, string) (store.Task, error) {
	return store.Task{}, errLifecycleUnavailable
}

type unavailableLinkLifecycle struct{}

func (unavailableLinkLifecycle) CreateLink(context.Context, int64, int64, string) (store.SavedLink, bool, error) {
	return store.SavedLink{}, false, errLifecycleUnavailable
}

func (l storeTaskLifecycle) CancelTask(ctx context.Context, chatID int64) (store.Task, bool, error) {
	return store.CancelTask(ctx, l.db, chatID, time.Now().UTC())
}

func (l storeTaskLifecycle) ResumeTask(ctx context.Context, chatID int64, taskID int64) (store.Task, bool, error) {
	return store.ResumeTask(ctx, l.db, chatID, taskID, time.Now().UTC(), bot.TaskLifetime)
}

func (l storeTaskLifecycle) ClearTasks(ctx context.Context, chatID int64) (int64, error) {
	return store.ClearTasks(ctx, l.db, chatID)
}

type App struct {
	cfg           config.Config
	db            *sql.DB
	model         model.Client
	modelCache    modelListCache
	logger        *slog.Logger
	auth          *authenticator
	handler       http.Handler
	taskLifecycle TaskLifecycle
	linkLifecycle LinkLifecycle
	rateLimits    *perUserRateLimiter
}

func New(cfg config.Config, db *sql.DB, client model.Client, logger *slog.Logger) *App {
	if logger == nil {
		logger = slog.Default()
	}
	app := &App{cfg: cfg, db: db, model: client, logger: logger}
	app.taskLifecycle = storeTaskLifecycle{db: db}
	app.linkLifecycle = unavailableLinkLifecycle{}
	app.auth = newAuthenticator(cfg.BotToken, cfg.Allows)
	app.rateLimits = newPerUserRateLimiter()
	app.handler = securityHeaders(limitInFlight(http.HandlerFunc(app.route), maxInFlight))
	return app
}

func (a *App) SetTaskLifecycle(lifecycle TaskLifecycle) {
	if lifecycle == nil {
		lifecycle = storeTaskLifecycle{db: a.db}
	}
	a.taskLifecycle = lifecycle
}

func (a *App) SetLinkLifecycle(lifecycle LinkLifecycle) {
	if lifecycle == nil {
		lifecycle = unavailableLinkLifecycle{}
	}
	a.linkLifecycle = lifecycle
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
	isExport := req.URL.Path == "/api/history/export" && req.Method == http.MethodGet
	if isExport || req.Method != http.MethodGet && req.Method != http.MethodHead {
		allowed, retryAfter := a.rateLimits.allow(current.UserID, isExport)
		if !allowed {
			writeRateLimited(w, retryAfter)
			return
		}
	}
	if operation, ok := miniAppMutationOperation(req); ok {
		audited := &auditResponseWriter{ResponseWriter: w, status: http.StatusOK}
		w = audited
		defer func() {
			a.logMiniAppMutation(req, current, operation, audited.status, audited.body)
		}()
	}
	if req.URL.Path == "/api/documents/search" {
		a.searchDocuments(w, req, current)
		return
	}
	if strings.HasPrefix(req.URL.Path, "/api/history/") && req.URL.Path != "/api/history/search" &&
		req.URL.Path != "/api/history/item" && req.URL.Path != "/api/history/export" {
		a.historyControl(w, req, current)
		return
	}
	if req.URL.Path == "/api/reminders" {
		switch req.Method {
		case http.MethodGet:
			a.reminders(w, req, current)
		case http.MethodPost:
			a.createReminder(w, req, current)
		default:
			methodAllowed(w, http.MethodGet, http.MethodPost)
		}
		return
	}
	if strings.HasPrefix(req.URL.Path, "/api/reminders/") {
		a.reminderControl(w, req, current)
		return
	}
	if strings.HasPrefix(req.URL.Path, "/api/documents/") {
		a.documentControl(w, req, current)
		return
	}
	if req.URL.Path == "/api/links/search" {
		a.searchLinks(w, req, current)
		return
	}
	if req.URL.Path == "/api/links/item" {
		if req.Method != http.MethodGet {
			methodAllowed(w, http.MethodGet)
			return
		}
		a.linkByID(w, req, current)
		return
	}
	if strings.HasPrefix(req.URL.Path, "/api/links/") {
		a.linkControl(w, req, current)
		return
	}
	if req.URL.Path == "/api/tasks/cancel" || strings.HasPrefix(req.URL.Path, "/api/tasks/") {
		a.taskControl(w, req, current)
		return
	}
	if req.URL.Path == "/api/links" || req.URL.Path == "/api/tasks" {
		if req.Method == http.MethodPost {
			if req.URL.Path == "/api/links" {
				a.createLink(w, req, current)
			} else {
				a.createTask(w, req, current)
			}
			return
		}
		if req.Method != http.MethodGet {
			methodAllowed(w, http.MethodGet, http.MethodPost)
			return
		}
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
	case "/api/settings/tts-speed":
		a.settingsTTSSpeed(w, req, current)
		return
	}
	if req.Method != http.MethodGet {
		methodAllowed(w, http.MethodGet)
		return
	}
	switch req.URL.Path {
	case "/api/status":
		a.status(w, req, current)
	case "/api/history":
		a.history(w, req, current)
	case "/api/history/search":
		a.searchHistory(w, req, current)
	case "/api/history/item":
		a.historyByID(w, req, current)
	case "/api/history/export":
		a.exportHistory(w, req, current)
	case "/api/documents":
		a.documents(w, req, current)
	case "/api/links":
		a.links(w, req, current)
	case "/api/tasks":
		a.tasks(w, req, current)
	case "/api/usage":
		a.usage(w, req, current)
	default:
		writeError(w, http.StatusNotFound, "not_found", "API endpoint not found.")
	}
}

func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests. Try again later.")
}

type auditResponseWriter struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (w *auditResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(content []byte) (int, error) {
	w.body = append(w.body, content...)
	return w.ResponseWriter.Write(content)
}

func miniAppMutationOperation(req *http.Request) (string, bool) {
	path := req.URL.Path
	if req.Method == http.MethodPost {
		switch path {
		case "/api/documents/clear":
			return "documents.clear", true
		case "/api/history/clear":
			return "history.clear", true
		case "/api/links":
			return "links.add", true
		case "/api/links/clear":
			return "links.clear", true
		case "/api/reminders":
			return "reminders.create", true
		case "/api/tasks":
			return "tasks.create", true
		case "/api/tasks/cancel":
			return "tasks.cancel", true
		case "/api/tasks/clear":
			return "tasks.clear", true
		}
		if _, ok := deleteHistoryID(path); ok {
			return "history.delete", true
		}
		if _, ok := deleteDocumentID(path); ok {
			return "documents.delete", true
		}
		if _, ok := deleteLinkID(path); ok {
			return "links.delete", true
		}
		if _, ok := resumeTaskID(path); ok {
			return "tasks.resume", true
		}
		if _, ok := deleteTaskID(path); ok {
			return "tasks.delete", true
		}
		if _, action, ok := reminderActionID(path); ok {
			return "reminders." + action, true
		}
	}
	if req.Method == http.MethodPut || req.Method == http.MethodDelete {
		operation := ""
		switch path {
		case "/api/settings/model":
			operation = "settings.model."
		case "/api/settings/persona":
			operation = "settings.persona."
		case "/api/settings/stream":
			operation = "settings.stream."
		case "/api/settings/tts-speed":
			operation = "settings.tts_speed."
		}
		if operation != "" {
			if req.Method == http.MethodPut {
				return operation + "set", true
			}
			return operation + "reset", true
		}
	}
	return "", false
}

func (a *App) logMiniAppMutation(req *http.Request, current scope, operation string, status int, body []byte) {
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return
	}
	var result struct {
		ID      int64  `json:"id"`
		TaskID  int64  `json:"task_id"`
		Deleted *int64 `json:"deleted"`
	}
	_ = json.Unmarshal(body, &result)
	id := result.ID
	if id == 0 {
		id = result.TaskID
	}
	var detail strings.Builder
	fmt.Fprintf(&detail, "operation=%s chat_id=%d", operation, current.ChatID)
	if id > 0 {
		fmt.Fprintf(&detail, " id=%d", id)
	}
	if result.Deleted != nil {
		fmt.Fprintf(&detail, " count=%d", *result.Deleted)
	}
	if err := store.LogEvent(req.Context(), a.db, current.UserID, "miniapp_mutation", detail.String()); err != nil {
		a.logger.Error("Mini App mutation audit failed", "operation", operation, "error", err)
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
	current, err := a.auth.authorizeBearer(bearer)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "session_expired", "Session expired. Reopen the Mini App from Telegram.")
		return
	}
	if allowed, retryAfter := a.rateLimits.allow(current.UserID, false); !allowed {
		writeRateLimited(w, retryAfter)
		return
	}
	a.auth.revoke(bearer)
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

func forwardedPrefix(req *http.Request) string {
	prefix := strings.TrimRight(req.Header.Get("X-Forwarded-Prefix"), "/")
	if prefix == "" {
		return ""
	}
	if !strings.HasPrefix(prefix, "/") {
		return ""
	}
	for _, segment := range strings.Split(prefix[1:], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ""
		}
		for _, char := range segment {
			if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
				char == '-' || char == '.' || char == '_' || char == '~' {
				continue
			}
			return ""
		}
	}
	return prefix
}

func indexWithPrefix(content []byte, prefix string) []byte {
	if prefix == "" {
		return content
	}
	page := string(content)
	page = strings.Replace(page, "<title>", `<meta name="miniapp-base" content="`+prefix+`">`+"\n  <title>", 1)
	page = strings.Replace(page, `href="/style.css"`, `href="`+prefix+`/style.css"`, 1)
	page = strings.Replace(page, `src="/app.js"`, `src="`+prefix+`/app.js"`, 1)
	return []byte(page)
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
	if req.URL.Path == "/" {
		content = indexWithPrefix(content, forwardedPrefix(req))
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
