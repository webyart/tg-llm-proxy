package bot

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
)

const (
	webhookBodyLimit    = 1 << 20
	webhookHeaderLimit  = 16 << 10
	webhookMaxInFlight  = 16
	webhookStopTimeout  = 2 * time.Second
	webhookSecretHeader = "X-Telegram-Bot-Api-Secret-Token"
)

// WebhookServer owns the local HTTP listener. It is bound before SetWebhook is called, so Telegram is never
// pointed at a URL that has no listener yet. TLS belongs to the reverse proxy or tunnel in front of it.
type WebhookServer struct {
	tg       *tgbot.Bot
	srv      *http.Server
	listener net.Listener
	path     string
}

func NewWebhookServer(tg *tgbot.Bot, address string, path string, secretToken ...string) (*WebhookServer, error) {
	if tg == nil {
		return nil, errors.New("webhook needs the Telegram bot")
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("webhook path %q must be absolute", path)
	}
	if len(secretToken) > 1 {
		return nil, errors.New("webhook accepts at most one secret token")
	}
	secret := ""
	if len(secretToken) == 1 {
		secret = secretToken[0]
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("webhook listener %s: %w", address, err)
	}

	libraryHandler := tg.WebhookHandler()
	inFlight := make(chan struct{}, webhookMaxInFlight)
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if secret != "" && subtle.ConstantTimeCompare([]byte(req.Header.Get(webhookSecretHeader)), []byte(secret)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		select {
		case inFlight <- struct{}{}:
			defer func() { <-inFlight }()
		default:
			http.Error(w, "webhook server is overloaded", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, webhookBodyLimit))
		if err != nil {
			http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
			return
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(body, &object); err != nil {
			http.Error(w, "invalid JSON update", http.StatusBadRequest)
			return
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		libraryHandler(w, req)
	})
	srv := &http.Server{
		Handler:           mux,
		MaxHeaderBytes:    webhookHeaderLimit,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return &WebhookServer{tg: tg, srv: srv, listener: listener, path: path}, nil
}

// URL is the actual local URL, including an OS-assigned port when the listener used port 0 in a test.
func (s *WebhookServer) URL() string {
	return "http://" + s.listener.Addr().String() + s.path
}

func (s *WebhookServer) Close() error {
	return s.listener.Close()
}

// Run starts the update workers and HTTP server and stops both on cancellation or an unexpected listener failure.
func (s *WebhookServer) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	workersDone := make(chan struct{})
	go func() {
		s.tg.StartWebhook(runCtx)
		close(workersDone)
	}()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- s.srv.Serve(s.listener)
	}()

	var result error
	select {
	case err := <-serveDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("webhook HTTP listener: %w", err)
		} else if runCtx.Err() == nil {
			result = errors.New("webhook HTTP listener stopped without cancellation")
		}
	case <-workersDone:
		if runCtx.Err() == nil {
			result = errors.New("webhook workers stopped without cancellation")
		}
	case <-ctx.Done():
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), webhookStopTimeout)
	defer shutdownCancel()
	if err := s.srv.Shutdown(shutdownCtx); err != nil && result == nil {
		result = fmt.Errorf("webhook shutdown: %w", err)
	}
	select {
	case <-workersDone:
	case <-time.After(webhookStopTimeout):
		if result == nil {
			result = errors.New("webhook workers did not stop within 2 seconds")
		}
	}
	return result
}

// ConfigureWebhook registers the public HTTPS endpoint without dropping pending updates.
func (d *Dispatcher) ConfigureWebhook(ctx context.Context, tg *tgbot.Bot) error {
	cfg := d.deps.Cfg
	if !cfg.WebhookEnabled {
		return errors.New("ConfigureWebhook called while WEBHOOK_ENABLED=false")
	}
	if tg == nil {
		return errors.New("ConfigureWebhook needs the Telegram bot")
	}
	ok, err := tg.SetWebhook(ctx, &tgbot.SetWebhookParams{
		URL:            cfg.WebhookPublicURL,
		SecretToken:    cfg.WebhookSecretToken,
		AllowedUpdates: []string{"message", "callback_query"},
	})
	if err != nil {
		return fmt.Errorf("setWebhook: %w", errors.New(Sanitize(errors.New(Sanitize(err, cfg.BotToken)), cfg.WebhookSecretToken)))
	}
	if !ok {
		return errors.New("setWebhook answered false")
	}
	return nil
}
