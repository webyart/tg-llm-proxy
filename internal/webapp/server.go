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
	sessionRateLimit      = 6
	approvalRateLimit     = 10
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
	session  rateLimitBucket
	approval rateLimitBucket
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
	if export {
		return l.allowBucket(userID, bucketExport, exportRateLimit)
	}
	return l.allowBucket(userID, bucketMutation, mutationRateLimit)
}

// allowSession bounds the cheapest way to create state: every POST /api/session adds a 15-minute entry to
// the session table, and the table is global. Without a per-user budget one allowlisted account could fill
// all maxTrackedPrincipals entries and keep every other user out of the Mini App for a quarter of an hour.
func (l *perUserRateLimiter) allowSession(userID int64) (bool, time.Duration) {
	return l.allowBucket(userID, bucketSession, sessionRateLimit)
}

// allowApproval bounds how fast one user can answer network approvals. The waiting stream is not
// affected: only the decision endpoint is counted, and a legitimate answer is a single POST.
func (l *perUserRateLimiter) allowApproval(userID int64) (bool, time.Duration) {
	return l.allowBucket(userID, bucketApproval, approvalRateLimit)
}

type rateLimitBucketKind int

const (
	bucketMutation rateLimitBucketKind = iota
	bucketExport
	bucketSession
	bucketApproval
)

func (l *perUserRateLimiter) allowBucket(userID int64, kind rateLimitBucketKind, limit int) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for id, principal := range l.principals {
		if rateLimitBucketExpired(principal.mutation, now) && rateLimitBucketExpired(principal.export, now) &&
			rateLimitBucketExpired(principal.session, now) && rateLimitBucketExpired(principal.approval, now) {
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
	var bucket *rateLimitBucket
	switch kind {
	case bucketExport:
		bucket, limit = &principal.export, exportRateLimit
	case bucketSession:
		bucket = &principal.session
	case bucketApproval:
		bucket = &principal.approval
	default:
		bucket, limit = &principal.mutation, mutationRateLimit
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

// ChatBackend is the Mini App conversation surface. Production wiring is *bot.Dispatcher (see
// New); tests inject a fake with SetChatBackend. Every method takes the chat and user from the
// authenticated session — the client never supplies a chat id.
type ChatBackend interface {
	AnswerForMiniApp(ctx context.Context, chatID, userID int64, text string, emit func(bot.MiniAppStreamEvent) error) error
	// ResolveMiniAppApproval delivers a decision for a pending network action. The token must belong
	// to the same chat and user; used/expired tokens are rejected.
	ResolveMiniAppApproval(ctx context.Context, token string, chatID, userID int64, approve bool) error
	ResetContextForMiniApp(ctx context.Context, chatID, userID int64) error
	SkipPendingForMiniApp(ctx context.Context, chatID, userID int64, skip bool) (bool, error)
	// TranscribeForMiniApp validates one recorded clip with the same audio allowlist, MEDIA_MAX_MB and
	// per-minute budget as the Telegram voice handler and returns the trimmed transcript. The audio is
	// only forwarded to the shared model.Transcriber and is never stored.
	TranscribeForMiniApp(ctx context.Context, chatID, userID int64, data []byte, filename, mime string) (string, error)
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
	chat          ChatBackend
	// embeddings is the optional hybrid-search client of the document search; nil keeps it lexical.
	embeddings bot.EmbeddingClient
	rateLimits *perUserRateLimiter
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
	// The Mini App conversation runs through a dispatcher of its own rather than a second copy of
	// the ask pipeline. main.go builds its Telegram dispatcher separately and does not pass its
	// Locks/Limit here, so the two clients keep independent per-minute budgets and per-chat locks;
	// the shared database keeps message ordering and the assistant row still appears only after a
	// successful Response. A nil model client (some standalone deployments) leaves chat disabled
	// without breaking the read-only endpoints.
	if client != nil {
		app.chat = bot.New(bot.Deps{Cfg: cfg, DB: db, Model: client, Logger: logger, Token: cfg.BotToken})
	}
	app.handler = securityHeaders(limitInFlight(recoverPanics(logger, http.HandlerFunc(app.route)), maxInFlight, documentUploadBodyLimit(cfg), transcribeBodyLimit(cfg)))
	return app
}

// SetEmbeddings attaches the optional hybrid-search client to the document search. It is called by main after
// New; a Mini App built only for a test keeps the lexical search without it.
func (a *App) SetEmbeddings(client bot.EmbeddingClient) {
	a.embeddings = client
}

// responseRecorder remembers whether anything reached the client, so the panic handler knows if a 500 can
// still be sent.
type responseRecorder struct {
	http.ResponseWriter
	wrote bool
}

// Unwrap lets http.ResponseController reach the real connection from behind this wrapper. The SSE
// handler needs it for Flush and for its per-response write deadline.
func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *responseRecorder) WriteHeader(status int) {
	r.wrote = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(body []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(body)
}

// recoverPanics — one panic in one handler used to kill the connection with no JSON envelope at all, and
// the mutation audit that had already started kept the 200 it began with. The response is now the same
// error envelope the frontend understands, and only the panic type and length are logged: a panic message
// can quote the user data that caused it.
func recoverPanics(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var recorder = &responseRecorder{ResponseWriter: w}
		defer func() {
			var recovered = recover()
			if recovered == nil {
				return
			}
			logger.Error("the Mini App handler panicked",
				"path", req.URL.Path, "method", req.Method,
				"panic_type", fmt.Sprintf("%T", recovered),
				"panic_length", len(fmt.Sprint(recovered)))
			if recorder.wrote {
				return
			}
			writeError(recorder, http.StatusInternalServerError, "internal_error", "The Mini App could not handle this request. Retry or reopen it from Telegram.")
		}()
		next.ServeHTTP(recorder, req)
	})
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

// SetChatBackend replaces the Mini App conversation backend. Production uses the dispatcher built
// in New; the tests inject a fake so the HTTP contract can be checked without a model.
func (a *App) SetChatBackend(backend ChatBackend) {
	a.chat = backend
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

// limitInFlight caps concurrent requests and replaces every body with a bounded reader. documentLimit and
// transcribeLimit are the larger caps of the two multipart routes (POST /api/documents and POST
// /api/transcribe); every other route keeps maxBodyBytes.
func limitInFlight(next http.Handler, capacity int, documentLimit, transcribeLimit int64) http.Handler {
	inFlight := make(chan struct{}, capacity)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case inFlight <- struct{}{}:
			defer func() { <-inFlight }()
		default:
			writeError(w, http.StatusServiceUnavailable, "overloaded", "The Mini App is busy. Try again shortly.")
			return
		}
		limit := int64(maxBodyBytes)
		if req.Method == http.MethodPost {
			switch req.URL.Path {
			case "/api/documents":
				if documentLimit > 0 {
					limit = documentLimit
				}
			case "/api/transcribe":
				if transcribeLimit > 0 {
					limit = transcribeLimit
				}
			}
		}
		req.Body = http.MaxBytesReader(w, req.Body, limit)
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
		// A panic unwinds through middlewares, so the audit above would log the 200 it started with.
		// Mark it as a server error here, then let recoverPanics build the response the client sees.
		defer func() {
			if recovered := recover(); recovered != nil {
				audited.status = http.StatusInternalServerError
				panic(recovered)
			}
		}()
	}
	if req.URL.Path == "/api/messages" {
		a.chatMessage(w, req, current)
		return
	}
	if req.URL.Path == "/api/transcribe" {
		a.transcribe(w, req, current)
		return
	}
	if req.URL.Path == "/api/chat/approval" {
		a.chatApproval(w, req, current)
		return
	}
	if req.URL.Path == "/api/context/reset" {
		a.resetChatContext(w, req, current)
		return
	}
	if req.URL.Path == "/api/pending/skip" {
		a.skipPendingChat(w, req, current)
		return
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
	if req.URL.Path == "/api/documents" {
		switch req.Method {
		case http.MethodPost:
			a.uploadDocument(w, req, current)
		case http.MethodGet:
			a.documents(w, req, current)
		default:
			methodAllowed(w, http.MethodGet, http.MethodPost)
		}
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
	case "/api/settings/miniapp-fallback":
		a.settingsMiniAppFallback(w, req, current)
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
	case "/api/links":
		a.links(w, req, current)
	case "/api/tasks":
		a.tasks(w, req, current)
	case "/api/usage":
		a.usage(w, req, current)
	case "/api/whoami":
		a.miniAppWhoami(w, req, current)
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
		case "/api/documents":
			return "documents.add", true
		case "/api/transcribe":
			return "transcribe", true
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
		case "/api/settings/miniapp-fallback":
			operation = "settings.miniapp_fallback."
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
	// The exchange creates a 15-minute session and the session table is global, so one allowlisted account
	// could fill it and lock every other user out of the Mini App. The signed user id is read here only to
	// apply that user's own budget; the verification that matters still happens inside exchange.
	if userID, verifyErr := verifyInitData(string(body), a.auth.botToken, a.auth.now()); verifyErr == nil {
		if allowed, retryAfter := a.rateLimits.allowSession(userID); !allowed {
			writeRateLimited(w, retryAfter)
			return
		}
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

// chatStreamWriteBudget is the write deadline of one SSE answer. It matches the reply budget the
// dispatcher uses (TG_REPLY_DEADLINE_SECONDS around the answer, plus
// STREAM_TOTAL_DEADLINE_SECONDS when a stream is possible, which the per-chat setting can turn on
// even when the global STREAMING_ENABLED is off) with a small delivery grace.
//
// The server-level WriteTimeout stays 10s: every JSON endpoint must keep failing fast, and raising
// the listener timeout for the whole Mini App would let any slow request park a connection for a
// quarter of an hour. Instead the chat handler calls http.ResponseController.SetWriteDeadline for
// its own response, so only the event stream gets the long budget and a client that stops reading
// is still bounded. Read/ReadHeader timeouts are untouched.
func chatStreamWriteBudget(cfg config.Config) time.Duration {
	seconds := cfg.ReplyDeadlineSeconds + cfg.StreamTotalSeconds
	if seconds <= 0 {
		seconds = 60
	}
	return time.Duration(seconds)*time.Second + 5*time.Second
}

// sseResponse writes one text/event-stream response. Headers are sent lazily, on the first event:
// that way an admission failure (denied, rate limited, bad text) can still use the ordinary JSON
// error envelope with a real status code.
type sseResponse struct {
	w         http.ResponseWriter
	control   *http.ResponseController
	started   bool
	errorSeen bool
}

func newSSEResponse(w http.ResponseWriter) *sseResponse {
	return &sseResponse{w: w, control: http.NewResponseController(w)}
}

func (s *sseResponse) begin() {
	if s.started {
		return
	}
	header := s.w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("X-Accel-Buffering", "no")
	header.Set("Cache-Control", "no-store")
	s.w.WriteHeader(http.StatusOK)
	s.started = true
	_ = s.flush()
}

func (s *sseResponse) flush() error {
	err := s.control.Flush()
	if err == nil || errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (s *sseResponse) send(name string, value any) error {
	s.begin()
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return err
	}
	return s.flush()
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
		ReadHeaderTimeout: 2 * time.Second,
		// A document upload from a phone can take minutes: a 8.6 MiB PDF failed at the old 5 s read timeout even
		// though the handler asked for its own deadline (the ResponseController call is not supported by every
		// wrapper), so the server-wide value has to carry the slow case. Header floods stay bounded by
		// ReadHeaderTimeout, bodies by MaxBytesReader and the per-route rate limits.
		ReadTimeout: 10 * time.Minute,
		// The write side must cover the same slow case: a 12-second upload already finished storing the document and
		// then lost the response to this timeout, which Traefik reported as 502 while the row existed.
		WriteTimeout: 15 * time.Minute, IdleTimeout: 30 * time.Second,
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
