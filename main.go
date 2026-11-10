// entry — the program entry point of the module.
//
// The file is deliberately thin: main() reads the configuration, builds one logger and calls run(), which
// owns the whole lifecycle (database, health listener, model client, dispatcher, transports, schedulers).
// run() returns an error instead of calling os.Exit from the middle, so every resource it opened is closed by
// a deferred call on every path, and a failed start can be tested instead of only observed in production.
package main

import (
	"bot/internal/bot"
	"bot/internal/config"
	"bot/internal/embed"
	"bot/internal/health"
	"bot/internal/metrics"
	"bot/internal/model"
	"bot/internal/store"
	"bot/internal/webapp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The signal set that stops the program: Ctrl-C on Windows and the SIGTERM a process supervisor sends —
// without it the container is SIGKILLed when Docker's stop grace ends (10 s by default).
var stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

var buildCommit = "dev"

// pollerWait bounds how long shutdown waits for one goroutine to notice the cancelled context. A stop that
// never finishes cannot be told apart from a hung process, so the wait is a window, not a promise.
var pollerWait = 3 * time.Second

// logLevel maps the LOG_LEVEL row to a slog level. An unknown value is the loader's problem (it names the row
// and falls back to info), and this function follows the same fallback silently.
func logLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "error":
		return slog.LevelError
	case "warning":
		return slog.LevelWarn
	case "debug":
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

// LoadConfig reads the process ENV once and hands the raw map to the loader. The map is returned as well
// because the loader reports problems by row name and the caller prints them together.
func LoadConfig() (config.Config, []string, map[string]string) {
	var entries = os.Environ()
	var raw = map[string]string{}
	for _, entry := range entries {
		var pair = strings.Split(entry, "=")
		if len(pair) > 1 {
			raw[pair[0]] = strings.Join(pair[1:], "=")
		}
	}
	var cfg, problems = config.LoadFrom(raw)
	return cfg, problems, raw
}

// configuredReserves names the ordered reserve chain for the startup summary: STRATA_FALLBACKS entries keep their
// order, and the legacy single reserve appears as a one-element list.
func configuredReserves(cfg config.Config) []string {
	if len(cfg.FallbackProviders) > 0 {
		var names = make([]string, len(cfg.FallbackProviders))
		for index, provider := range cfg.FallbackProviders {
			names[index] = provider.Model
		}
		return names
	}
	if cfg.StrataFallbackModel != "" {
		return []string{cfg.StrataFallbackModel}
	}
	return nil
}

// startupSummary logs the configuration an operator asks about first. Secrets are never printed — only their
// presence and length — and rows that are simply unset are not listed: the previous version printed one
// "is not set" line per optional row on every start, which buried the six lines that mattered.
func startupSummary(logger *slog.Logger, cfg config.Config) {
	var summary = []any{
		"token_set", cfg.BotToken != "",
		"token_length", len(cfg.BotToken),
		"strata_base", cfg.StrataBase,
		"model", cfg.StrataModel,
		"default_language", cfg.DefaultLanguage,
		"groups_enabled", cfg.GroupsEnabled,
		"fallback_model", cfg.StrataFallbackModel,
		"fallback_models", configuredReserves(cfg),
		"fallback_timeout_seconds", cfg.StrataFallbackTimeoutSeconds,
		"transcription_model", cfg.TranscriptionModel,
		"timeout_seconds", cfg.TimeoutSeconds,
		"reply_deadline_seconds", cfg.ReplyDeadlineSeconds,
		"streaming", cfg.Streaming,
		"tools_enabled", cfg.ToolsEnabled,
		"url_reader", cfg.URLReaderEnabled,
		"web_search", cfg.WebSearchEnabled,
		"rag_embeddings", cfg.RAGEmbeddingsEnabled,
		"rag_embeddings_base", cfg.RAGEmbeddingsBase,
		"rag_embeddings_model", cfg.RAGEmbeddingsModel,
		"tts", cfg.TTSEnabled,
		"miniapp", cfg.MiniAppEnabled,
		"miniapp_telegram_fallback", cfg.MiniAppTelegramFallback,
		"miniapp_public_url", cfg.MiniAppPublicURL,
		"webhook", cfg.WebhookEnabled,
		"db_path", cfg.DBPath,
		"health_port", cfg.HealthPort,
		"health_listen_host", cfg.HealthListenHost,
		"allowed_users", len(cfg.UserIDs),
		"admin_users", len(cfg.AdminIDs),
		"deny_by_default", cfg.DenyByDefault,
		"rate_limit_per_minute", cfg.RateLimitPerMinute,
		"max_concurrent_requests", cfg.MaxConcurrentRequests,
	}
	// fallback_base is printed only when a SECOND endpoint is configured: an empty value means "same as
	// strata_base", and repeating the primary URL would look like a different endpoint in the summary.
	if cfg.FallbackBase != "" {
		summary = append(summary, "fallback_base", cfg.FallbackBase)
	}
	logger.Info("configuration loaded", summary...)
	// The URL above is what Telegram stores as the menu button and pastes into every WebApp button. A root URL
	// behind a path-prefix gateway opens the gateway's fallback route instead of the Mini App — the symptom is
	// Traefik's «no available server» with a perfectly healthy bot behind it. This warning names the two
	// values an operator has to compare.
	if cfg.MiniAppEnabled {
		var parsed, parseErr = url.Parse(cfg.MiniAppPublicURL)
		if parseErr != nil || parsed.Host == "" {
			logger.Warn("MINIAPP_PUBLIC_URL is not an absolute URL; the Mini App button will not open", "url", cfg.MiniAppPublicURL)
		} else if parsed.Path == "" || parsed.Path == "/" {
			logger.Warn("MINIAPP_PUBLIC_URL has no path: if a gateway routes the Mini App under a prefix, Telegram will open that prefix's fallback route instead — use e.g. https://host/miniapp",
				"url", cfg.MiniAppPublicURL, "example_suffix", "/miniapp")
		}
	}
}

func main() {
	// The strict-pragma switch is process-wide state that the store copies into every connection at Connect,
	// so it is set before the first sql.Open.
	store.EnableStrictPragmas()
	fmt.Println("build commit", buildCommit)

	var ctx, cancel = signal.NotifyContext(context.Background(), stopSignals...)
	defer cancel()
	var cfg, problems, _ = LoadConfig()
	if len(problems) > 0 {
		// The loader names every broken row exactly once and does not stop at the first: the start exits with
		// code 1 and prints the whole list, so a wrong .env is fixed in one pass.
		for _, problem := range problems {
			fmt.Println("configuration problem:", problem)
		}
		if cfg.BotToken == "" {
			fmt.Println("ENV TELEGRAM_BOT_TOKEN is not set: the start falls with a clear error and the configuration is not printed")
		}
		fmt.Println("the loader resolved the configuration and found", len(problems), "problem(s)")
		os.Exit(1)
	}

	var logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel(cfg.LogLevel)}))
	startupSummary(logger, cfg)
	if err := run(ctx, cfg, buildCommit, os.Stdout, logger); err != nil {
		logger.Error("the bot stopped with an error", "error", bot.Sanitize(err, cfg.BotToken))
		fmt.Fprintln(os.Stderr, "bot:", bot.Sanitize(err, cfg.BotToken))
		os.Exit(1)
	}
}

// run wires the process and blocks until the context is cancelled or a component that the bot cannot live
// without stops. Everything it opens is closed on return; the caller owns the exit code.
func run(ctx context.Context, cfg config.Config, commit string, out io.Writer, logger *slog.Logger) error {
	var registry = metrics.NewRegistry()
	var db, err = store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("store.Open: %w", err)
	}
	defer db.Close()

	var journalMode, modeErr = store.Pragma(ctx, db, store.JournalModePragma)
	var foreignKeys, keysErr = store.Pragma(ctx, db, store.ForeignKeysPragma)
	var schemaVersion, schemaErr = store.SchemaVersion(ctx, db)
	if modeErr != nil || keysErr != nil || schemaErr != nil {
		return fmt.Errorf("store.Pragma/SchemaVersion: journal=%v keys=%v schema=%v", modeErr, keysErr, schemaErr)
	}
	fmt.Fprintln(out, "store is open:", cfg.DBPath, "journal_mode", journalMode, "foreign_keys", foreignKeys,
		"migrations applied", len(store.MigrationSQL), "schema_version", schemaVersion)
	store.RegisterMetrics(registry, cfg.DBPath, schemaVersion)
	fmt.Fprintln(out, "program is running, PID", os.Getpid())

	var modelMetrics = model.NewMetrics(registry)
	var modelOptions = model.Options{
		ChatURL:             cfg.ChatURL,
		ModelsURL:           cfg.ModelsURL,
		APIKey:              cfg.StrataKey,
		Model:               cfg.StrataModel,
		TranscriptionModel:  cfg.TranscriptionModel,
		TranscriptionURL:    cfg.TranscriptionURL,
		TranscriptionNoAuth: cfg.TranscriptionBase != cfg.StrataBase,
		Timeout:             time.Duration(cfg.TimeoutSeconds) * time.Second,
		MaxConcurrent:       int(cfg.MaxConcurrentRequests),
		Metrics:             modelMetrics,
		Stream: model.StreamSettings{
			Idle:     time.Duration(cfg.StreamIdleSeconds) * time.Second,
			Total:    time.Duration(cfg.StreamTotalSeconds) * time.Second,
			Progress: time.Duration(cfg.StreamEditIntervalMS) * time.Millisecond,
		},
	}
	var primary, clientErr = model.New(modelOptions)
	if clientErr != nil {
		return fmt.Errorf("model.New: %w", clientErr)
	}

	// STRATA is a single-model server: it may keep the loaded model while answering requests that name another
	// one, or reject a name it no longer offers. When the endpoint lists exactly one model, adopt it for the
	// primary path instead of letting every answer take the fallback. A failed listing leaves the config alone.
	if discovered, changed := model.DiscoverModelName(ctx, primary, cfg.StrataModel, 3*time.Second); changed {
		logger.Warn("the endpoint offers a single model; using it instead of STRATA_MODEL",
			"configured", cfg.StrataModel, "loaded", discovered)
		primary.UseModel(discovered)
		cfg.StrataModel = discovered
	}

	// The chat client is the primary unless a reserve chain is configured. The wrapper deliberately has no
	// Transcribe method, so voice keeps using the primary: the composite reattaches that optional interface to
	// the wrapper the dispatcher sees. Legacy STRATA_FALLBACK_* is a one-entry chain; STRATA_FALLBACKS is the
	// ordered list.
	var client model.Client = primary
	var reserves []model.Reserve
	var buildReserve = func(name, chatURL, modelsURL, apiKey string, timeoutSeconds int64, distinct bool) error {
		var options = modelOptions
		if distinct {
			options.ChatURL = chatURL
			options.ModelsURL = modelsURL
		}
		options.Model = name
		// A reserve on someone else's endpoint must not receive the primary key: an explicit key wins, and a
		// distinct endpoint without one is called unauthenticated (LM Studio-style local reserves) instead of
		// leaking STRATA_API_KEY to a third party.
		if apiKey != "" {
			options.APIKey = apiKey
		} else if distinct {
			options.APIKey = ""
		}
		// A reserve model may load on demand: its own timeout must not be the primary per-attempt one.
		options.Timeout = time.Duration(timeoutSeconds) * time.Second
		options.TranscriptionModel = ""
		options.TranscriptionURL = ""
		options.TranscriptionNoAuth = false
		var reserveClient, reserveErr = model.New(options)
		if reserveErr != nil {
			return fmt.Errorf("model.New (reserve %q): %w", name, reserveErr)
		}
		reserves = append(reserves, model.Reserve{Model: name, Client: reserveClient})
		return nil
	}
	if len(cfg.FallbackProviders) > 0 {
		for _, provider := range cfg.FallbackProviders {
			var distinct = strings.TrimSpace(provider.BaseURL) != ""
			if err := buildReserve(provider.Model, provider.ChatURL, provider.ModelsURL, provider.APIKey, provider.TimeoutSeconds, distinct); err != nil {
				return err
			}
		}
	} else if cfg.StrataFallbackModel != "" {
		// FallbackBase is empty when the reserve shares the primary endpoint; then the primary key stays valid.
		var distinct = cfg.FallbackBase != ""
		if err := buildReserve(cfg.StrataFallbackModel, cfg.FallbackChatURL, cfg.FallbackModelsURL, cfg.StrataFallbackAPIKey,
			cfg.StrataFallbackTimeoutSeconds, distinct); err != nil {
			return err
		}
	}
	if len(reserves) > 0 {
		var wrapped = model.NewFallbackChain(primary, reserves, model.ChainOptions{
			PrimaryModel: cfg.StrataModel,
			Metrics:      modelMetrics,
			Logger:       logger,
		})
		client = withPrimaryTranscription(wrapped, primary)
	}

	// The hybrid RAG layer is optional and off by default. The metric families are registered either way, so a
	// dashboard sees them before the feature is switched on; the client exists only when the row is on, and the
	// dispatcher and the Mini App know the feature by a nil interface: nothing embeds, nothing changes.
	var embeddingMetrics = embed.NewMetrics(registry)
	var embeddingClient bot.EmbeddingClient
	if cfg.RAGEmbeddingsEnabled {
		var embedClient = embed.New(cfg.RAGEmbeddingsBase, cfg.RAGEmbeddingsModel, "",
			time.Duration(cfg.RAGEmbeddingsTimeoutSeconds)*time.Second)
		embedClient.SetMetrics(embeddingMetrics)
		embeddingClient = embedClient
	}

	// The health listener is bound before the bot starts: the compose HEALTHCHECK probes it and a taken
	// port must fail the start, not sit next to a healthy-looking /healthz.
	var healthServer = health.NewOn(cfg.HealthListenHost, cfg.HealthPort)
	healthServer.RegisterReadiness(db, client)
	healthServer.Handle(health.MetricsPath, metrics.Handler(registry))
	if listenErr := healthServer.Listen(); listenErr != nil {
		return listenErr
	}
	var healthErr = make(chan error, 1)
	go func() { healthErr <- healthServer.Run(ctx) }()
	fmt.Fprintln(out, "health is listening on", net.JoinHostPort(cfg.HealthListenHost, strconv.FormatInt(healthServer.Port(), 10)),
		health.Path, "readiness", health.ReadyPath, "metrics", health.MetricsPath)

	var miniApp *webapp.App
	if cfg.MiniAppEnabled {
		miniApp = webapp.New(cfg, db, client, logger)
		miniApp.SetEmbeddings(embeddingClient)
	}
	var synthesizer model.SpeechSynthesizer
	if cfg.TTSEnabled {
		var ttsClient, ttsErr = model.NewTTS(model.TTSOptions{
			URL: cfg.TTSURL, Model: cfg.TTSModel, Voice: cfg.TTSVoice,
			Timeout: time.Duration(cfg.TTSTimeoutSeconds) * time.Second, MaxChars: int(cfg.TTSMaxChars),
		})
		if ttsErr != nil {
			return fmt.Errorf("model.NewTTS: %w", ttsErr)
		}
		synthesizer = ttsClient
	}
	var startOffset, offsetFound, offsetErr = store.LastUpdateID(ctx, db)
	if offsetErr != nil {
		return fmt.Errorf("store.LastUpdateID: %w", offsetErr)
	}
	var dispatcher = bot.New(bot.Deps{
		Cfg:            cfg,
		DB:             db,
		Model:          client,
		TTS:            synthesizer,
		Embeddings:     embeddingClient,
		Logger:         logger,
		Token:          cfg.BotToken,
		StartOffset:    startOffset,
		BuildCommit:    commit,
		MiniAppTickets: miniApp,
		Offset: func(writeCtx context.Context, updateID int64) error {
			return store.SetLastUpdateID(writeCtx, db, updateID)
		},
	})
	var miniAppErr chan error
	if miniApp != nil {
		miniApp.SetTaskLifecycle(dispatcher)
		miniApp.SetLinkLifecycle(dispatcher)
		// The Mini App answers through the SAME dispatcher the Telegram poller uses: the per-chat lock, the
		// per-minute budget and the daily quota must be one set, or a Mini App message could run beside a
		// Telegram question in the same chat and the user's limits would silently double.
		miniApp.SetChatBackend(dispatcher)
		var miniAppServer, listenErr = webapp.Listen(miniApp, cfg.MiniAppAddress())
		if listenErr != nil {
			return fmt.Errorf("Mini App listener: %w", listenErr)
		}
		defer miniAppServer.Close()
		miniAppErr = make(chan error, 1)
		go func() { miniAppErr <- miniAppServer.Run(ctx) }()
		fmt.Fprintln(out, "Mini App is listening on", miniAppServer.URL(), "public URL", cfg.MiniAppPublicURL)
	}

	// NewTelegram replaces the replier and the attachment downloader with the transport implementations, so it
	// has to run after bot.New and before any update can arrive.
	var tg, tgErr = dispatcher.NewTelegram(bot.Options{WebhookSecretToken: cfg.WebhookSecretToken})
	if tgErr != nil {
		return fmt.Errorf("bot.New: %w", tgErr)
	}
	dispatcher.SetMetrics(registry)
	var menuCtx, cancelMenu = context.WithTimeout(ctx, bot.PublishCommandsTimeout)
	if menuErr := dispatcher.PublishCommands(menuCtx, tg); menuErr != nil {
		logger.Warn("setMyCommands refused, the menu of the client stays empty", "error", bot.Sanitize(menuErr, cfg.BotToken))
	}
	if menuErr := dispatcher.PublishMenuButton(menuCtx, tg); menuErr != nil {
		logger.Warn("setChatMenuButton refused, the default Telegram menu button stays active", "error", bot.Sanitize(menuErr, cfg.BotToken))
	}
	cancelMenu()

	var webhookServer *bot.WebhookServer
	if cfg.WebhookEnabled {
		var err error
		webhookServer, err = bot.NewWebhookServer(tg, cfg.WebhookAddress(), cfg.WebhookPath, cfg.WebhookSecretToken)
		if err != nil {
			return fmt.Errorf("webhook listener: %w", err)
		}
		defer webhookServer.Close()
		if err := dispatcher.ConfigureWebhook(ctx, tg); err != nil {
			return fmt.Errorf("setWebhook: %w", err)
		}
	}

	var reminderDone = make(chan struct{})
	go func() {
		defer close(reminderDone)
		dispatcher.RunReminderScheduler(ctx)
	}()
	var taskDone = make(chan struct{})
	go func() {
		defer close(taskDone)
		dispatcher.RunTaskScheduler(ctx)
	}()

	// Start owns polling, or Run owns the webhook workers and listener. Either way an unexpected return means
	// this bot can no longer receive updates and the process must not remain a healthy-looking shell.
	var botDone = make(chan struct{}, 1)
	go func() {
		if webhookServer != nil {
			if webhookErr := webhookServer.Run(ctx); webhookErr != nil {
				logger.Error("webhook transport stopped", "error", bot.Sanitize(webhookErr, cfg.BotToken))
			}
		} else {
			tg.Start(ctx)
		}
		select {
		case botDone <- struct{}{}:
		default:
		}
	}()
	if webhookServer != nil {
		fmt.Fprintln(out, "bot is accepting Telegram webhooks on", webhookServer.URL(), "public URL", cfg.WebhookPublicURL,
			"bot_state last_update_id", startOffset, "found", offsetFound)
	} else {
		fmt.Fprintln(out, "bot is polling Telegram, bot_state last_update_id", startOffset, "found", offsetFound)
	}

	// The startup scan answers questions a previous process left unanswered. It runs next to the poller rather
	// than before it: a model that spends its whole timeout on an old question must not delay the first
	// getUpdates of this run.
	var resumeDone = make(chan struct{})
	go func() {
		defer close(resumeDone)
		var answered, pending, resumeErr = dispatcher.ResumeInterruptedQuestions(ctx)
		if resumeErr != nil && !errors.Is(resumeErr, context.Canceled) {
			logger.Error("the startup scan of unanswered questions failed", "answered", answered, "pending", pending,
				"error", bot.Sanitize(resumeErr, cfg.BotToken))
			return
		}
		if answered > 0 || pending > 0 {
			fmt.Fprintln(out, "the startup scan answered", answered, "stored question(s), left", pending,
				"still without an answer (the next start of the process tries them again)")
		}
	}()

	var stoppedBySignal = true
	select {
	case <-ctx.Done():
	case <-botDone:
		fmt.Fprintln(out, "the Telegram transport stopped without a stop signal, exit code 1")
		return errors.New("the Telegram transport stopped without a stop signal")
	case err := <-healthErr:
		if err != nil {
			return fmt.Errorf("health: %w", err)
		}
		stoppedBySignal = false
	case err := <-miniAppErr:
		if err != nil {
			return fmt.Errorf("Mini App: %w", err)
		}
		stoppedBySignal = false
	}
	fmt.Fprintln(out, "stopped by signal", stoppedBySignal)
	// The line an operator looks for when a container is stopped: the process left on a signal, not because a
	// listener died. The waits below are bounded by pollerWait for the reason above.
	fmt.Fprintln(out, "poller stopped", waitClosed(botDone))
	if !waitClosed(reminderDone) {
		fmt.Fprintln(out, "reminder scheduler was still stopping when the process left")
	}
	if !waitClosed(taskDone) {
		fmt.Fprintln(out, "task scheduler was still stopping when the process left")
	}
	if !waitClosed(resumeDone) {
		fmt.Fprintln(out, "the startup scan was still answering when the process left")
	}
	return nil
}

// withPrimaryTranscription reattaches the optional Transcriber of the primary client to the fallback
// wrapper. NewFallback never handles audio — a reserve chat model is not a reserve Whisper — so the
// dispatcher's voice path keeps the client an operator configured, not the fallback.
func withPrimaryTranscription(chat, primary model.Client) model.Client {
	var transcriber, transcribes = primary.(model.Transcriber)
	if !transcribes {
		return chat
	}
	var transcribed = &transcribedChat{Client: chat, transcriber: transcriber}
	var streamer, streams = chat.(model.Streamer)
	if !streams {
		return transcribed
	}
	return &streamingTranscribedChat{transcribedChat: transcribed, streamer: streamer}
}

type transcribedChat struct {
	model.Client
	transcriber model.Transcriber
}

func (c *transcribedChat) Transcribe(ctx context.Context, req model.TranscriptionRequest) (string, error) {
	return c.transcriber.Transcribe(ctx, req)
}

type streamingTranscribedChat struct {
	*transcribedChat
	streamer model.Streamer
}

func (c *streamingTranscribedChat) Stream(ctx context.Context, req model.Request, h model.StreamHandler) (model.Response, error) {
	return c.streamer.Stream(ctx, req, h)
}

// waitClosed reports whether ch was closed (or delivered its one value) inside the shutdown window.
func waitClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(pollerWait):
		return false
	}
}
