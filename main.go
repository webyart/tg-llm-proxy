// entry — the program entry point of the module
// (`signal.NotifyContext`); the DoD of that day is «go build проходит; при пустом токене — ясная ошибка
// и код выхода 1».

package main

import (
	"bot/internal/bot"
	"bot/internal/config"
	"bot/internal/health"
	"bot/internal/model"
	"bot/internal/store"
	"context"
	"errors"

	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// The signal set that stops the program: Ctrl-C on Windows and the SIGTERM a process supervisor sends
// without it the container is SIGKILLed when Docker's stop grace ends — 10 s by default, because
var stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

var buildCommit = "dev"

// per line: the program's own report of what it read. The token is hidden, only its length is printed
func PrintConfig(cfg config.Config) {
	fmt.Println("TELEGRAM_BOT_TOKEN", "is set, length", len(cfg.BotToken), "")
	fmt.Println("STRATA_API_KEY", "is set, length", len(cfg.StrataKey), "")
	fmt.Println("STRATA_BASE_URL", cfg.StrataBase, "")
	fmt.Println("STRATA_MODEL", cfg.StrataModel, "")
	fmt.Println("STRATA_MODELS_URL", cfg.ModelsURL, "")
	fmt.Println("STRATA_CHAT_COMPLETIONS_URL", cfg.ChatURL, "")
	fmt.Println("STRATA_TIMEOUT_SECONDS", cfg.TimeoutSeconds, "")
	fmt.Println("TG_REPLY_DEADLINE_SECONDS", cfg.ReplyDeadlineSeconds, "")
	fmt.Println("RECOVERY_MAX_AGE_HOURS", cfg.RecoveryMaxAgeHours, "")
	fmt.Println("STREAMING_ENABLED", cfg.Streaming, "")
	fmt.Println("STREAM_EDIT_INTERVAL_MS", cfg.StreamEditIntervalMS, "")
	fmt.Println("STREAM_IDLE_TIMEOUT_SECONDS", cfg.StreamIdleSeconds, "")
	fmt.Println("STREAM_TOTAL_DEADLINE_SECONDS", cfg.StreamTotalSeconds, "")
	fmt.Println("WEBHOOK_ENABLED", cfg.WebhookEnabled, "")
	if cfg.WebhookEnabled {
		fmt.Println("WEBHOOK_PUBLIC_URL", cfg.WebhookPublicURL)
		fmt.Println("WEBHOOK_LISTEN", cfg.WebhookAddress(), cfg.WebhookPath)
		fmt.Println("WEBHOOK_SECRET_TOKEN is set, length", len(cfg.WebhookSecretToken), "")
	}
	fmt.Println("DB_PATH", cfg.DBPath, "")
	fmt.Println("CONTEXT_MESSAGES", cfg.ContextMessages, "")
	fmt.Println("MAX_CONTEXT_CHARS", cfg.MaxContextChars, "")
	fmt.Println("MAX_INPUT_CHARS", cfg.MaxInputChars, "")
	fmt.Println("SYSTEM_PROMPT is set, length", len(cfg.SystemPrompt), "")
	fmt.Println("RATE_LIMIT_PER_MINUTE", cfg.RateLimitPerMinute, "")
	fmt.Println("MAX_CONCURRENT_MODEL_REQUESTS", cfg.MaxConcurrentRequests, "")
	fmt.Println("PROXY_FREE_TEXT", cfg.FreeText, "")
	fmt.Println("MAX_MESSAGE_CHUNK", cfg.MaxMessageChunk, "")
	fmt.Println("PARSE_MODE", cfg.ParseMode, "")
	fmt.Println("HEALTH_PORT", cfg.HealthPort, "")
	fmt.Println("LOG_LEVEL", cfg.LogLevel, "")
	if cfg.DenyByDefault {
		// has one name — the name a real .env carries; the old spelling is refused by name in LoadFrom).
		fmt.Println("TELEGRAM_ALLOWED_USERS is empty : the bot is closed, deny by default, only /start and /whoami answer ")
		fmt.Println("TELEGRAM_ADMIN_USERS", len(cfg.AdminIDs), "entries")
		return
	}
	fmt.Println("TELEGRAM_ALLOWED_USERS", len(cfg.UserIDs), "entries ")
	fmt.Println("TELEGRAM_ADMIN_USERS", len(cfg.AdminIDs), "entries")
}

// loader for «валидация, понятные сообщения об ошибках», and this message names the variable and the
// cannot come from a second spelling that the loader would have to know about.
func PrintMissing(raw map[string]string) {
	var names = config.Env
	var i = 0
	for i < len(names) {
		if !config.RowIsSet(raw, names[i]) {
			fmt.Println("ENV variable", names[i], "is not set ")
		}
		i += 1
	}
}

// «internal/config: ENV → Config, валидация, понятные сообщения об ошибках»).
func LoadConfig() (config.Config, []string, map[string]string) {
	var entries = os.Environ()
	var raw = map[string]string{}
	var entry = 0
	for entry < len(entries) {
		var pair = strings.Split(entries[entry], "=")
		if len(pair) > 1 {
			raw[pair[0]] = strings.Join(pair[1:], "=")
		}
		entry += 1
	}
	var cfg, problems = config.LoadFrom(raw)
	return cfg, problems, raw
}

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

var pollerWait = 3 * time.Second

func main() {
	// log/slog: the JSON log layer of the bot arrives with the bot itself; the start messages День 1 asks for
	// с graceful shutdown (`signal.NotifyContext`).
	// copies it into every connection at Connect), so it is set here, before the first sql.Open —
	store.EnableStrictPragmas()
	fmt.Println("build commit", buildCommit)
	var ctx, cancel = signal.NotifyContext(context.Background(), stopSignals...)
	var cfg, problems, raw = LoadConfig()
	PrintMissing(raw)
	if len(problems) > 0 {
		// DoD "a missing token must exit with code 1").
		var bad = 0
		for bad < len(problems) {
			fmt.Println("configuration problem:", problems[bad])
			bad += 1
		}
		if cfg.BotToken == "" {
			// mandatory row the configuration is not printed at all.
			fmt.Println("ENV TELEGRAM_BOT_TOKEN is not set: the start falls with a clear error and the configuration is not printed ")
		}
		fmt.Println("the loader resolved 1 configuration out of the table and found", len(problems), "problem(s): every broken row is named once, the loader does not stop at the first one ")
		os.Exit(1)
	}
	PrintConfig(cfg)
	// go:embed put into the binary. A failure stops the start with exit code 1 — the same rule as a
	var db, openErr = store.Open(ctx, cfg.DBPath)
	if openErr != nil {
		fmt.Println("store.Open:", openErr, "")
		os.Exit(1)
	}
	var journalMode, modeErr = store.Pragma(ctx, db, store.JournalModePragma)
	var foreignKeys, keysErr = store.Pragma(ctx, db, store.ForeignKeysPragma)
	var schemaVersion, schemaErr = store.SchemaVersion(ctx, db)
	if modeErr != nil || keysErr != nil || schemaErr != nil {
		fmt.Println("store.Pragma/SchemaVersion:", modeErr, keysErr, schemaErr, "")
		db.Close()
		os.Exit(1)
	}
	fmt.Println("store is open:", cfg.DBPath, "journal_mode", journalMode, "foreign_keys", foreignKeys,
		"migrations applied", len(store.MigrationSQL), "schema_version", schemaVersion, "")
	fmt.Println("program is running, PID", os.Getpid(), "")
	// compose HEALTHCHECK probes (wget on 127.0.0.1) and what keeps the container process alive
	var healthServer = health.New(cfg.HealthPort)
	// День 4 has already burned the timeout — and instead of a container that reports healthy while
	if listenErr := healthServer.Listen(); listenErr != nil {
		fmt.Println(listenErr, "")
		db.Close()
		os.Exit(1)
	}
	var healthErr = make(chan error, 1)
	go func() {
		healthErr <- healthServer.Run(ctx)
	}()
	fmt.Println("health is listening on 127.0.0.1:", healthServer.Port(), health.Path, "")
	var logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel(cfg.LogLevel)}))
	var client, clientErr = model.New(model.Options{
		ChatURL:       cfg.ChatURL,
		ModelsURL:     cfg.ModelsURL,
		APIKey:        cfg.StrataKey,
		Model:         cfg.StrataModel,
		Timeout:       time.Duration(cfg.TimeoutSeconds) * time.Second,
		MaxConcurrent: int(cfg.MaxConcurrentRequests),
		// client is the cadence of the chat: a thinking model may refresh the placeholder no more often than
		Stream: model.StreamSettings{
			Idle:     time.Duration(cfg.StreamIdleSeconds) * time.Second,
			Total:    time.Duration(cfg.StreamTotalSeconds) * time.Second,
			Progress: time.Duration(cfg.StreamEditIntervalMS) * time.Millisecond,
		},
	})
	if clientErr != nil {
		fmt.Println("model.New:", clientErr, "")
		db.Close()
		os.Exit(1)
	}
	var startOffset, offsetFound, offsetErr = store.LastUpdateID(ctx, db)
	if offsetErr != nil {
		fmt.Println("store.LastUpdateID:", offsetErr, "")
		db.Close()
		os.Exit(1)
	}
	var dispatcher = bot.New(bot.Deps{
		Cfg:         cfg,
		DB:          db,
		Model:       client,
		Logger:      logger,
		Token:       cfg.BotToken,
		StartOffset: startOffset,
		BuildCommit: buildCommit,
		Offset: func(writeCtx context.Context, updateID int64) error {
			return store.SetLastUpdateID(writeCtx, db, updateID)
		},
	})
	// is what answers 401 on a wrong token. A process that cannot poll must not sit next to a healthz that
	var tg, tgErr = dispatcher.NewTelegram(bot.Options{WebhookSecretToken: cfg.WebhookSecretToken})
	if tgErr != nil {
		fmt.Println("bot.New:", tgErr, "")
		db.Close()
		os.Exit(1)
	}
	var menuCtx, cancelMenu = context.WithTimeout(ctx, bot.PublishCommandsTimeout)
	if menuErr := dispatcher.PublishCommands(menuCtx, tg); menuErr != nil {
		logger.Warn("setMyCommands refused, the menu of the client stays empty", "error", bot.Sanitize(menuErr, cfg.BotToken))
	}
	cancelMenu()
	var webhookServer *bot.WebhookServer
	if cfg.WebhookEnabled {
		var err error
		webhookServer, err = bot.NewWebhookServer(tg, cfg.WebhookAddress(), cfg.WebhookPath)
		if err != nil {
			fmt.Println("webhook listener:", err, "(the local address must be free before setWebhook, exit code 1)")
			db.Close()
			os.Exit(1)
		}
		defer webhookServer.Close()
		if err := dispatcher.ConfigureWebhook(ctx, tg); err != nil {
			fmt.Println("setWebhook:", err, "")
			db.Close()
			os.Exit(1)
		}
	}
	// Start owns polling, or Run owns the webhook workers and listener. Either way an unexpected return means
	// this bot can no longer receive updates and the process must not remain a healthy-looking shell.
	var botDone = make(chan struct{}, 1)
	go func() {
		if webhookServer != nil {
			if err := webhookServer.Run(ctx); err != nil {
				logger.Error("webhook transport stopped", "error", bot.Sanitize(err, cfg.BotToken))
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
		fmt.Println("bot is accepting Telegram webhooks on", webhookServer.URL(), "public URL", cfg.WebhookPublicURL,
			"bot_state last_update_id", startOffset, "found", offsetFound)
	} else {
		fmt.Println("bot is polling Telegram, bot_state last_update_id", startOffset, "found", offsetFound, "")
	}
	// walk: the stop left the offset at 920551679 with the interrupted update numbered 920551680, and the
	// restarted poller asked Telegram for exactly that id and got empty batches for 9.5 minutes — Telegram
	// re-delivers only what never reached the client. The scan of resume.go asks the database instead, and it
	// runs next to the polling goroutine rather than before it: a model that spends its whole
	// STRATA_TIMEOUT_SECONDS on a question from the previous run must not delay the first getUpdates of this
	// update from answering it twice.
	var resumeDone = make(chan struct{})
	go func() {
		defer close(resumeDone)
		var answered, pending, resumeErr = dispatcher.ResumeInterruptedQuestions(ctx)
		if resumeErr != nil && !errors.Is(resumeErr, context.Canceled) {
			logger.Error("the startup scan of stopped", "answered", answered, "pending", pending,
				"error", bot.Sanitize(resumeErr, cfg.BotToken))
			return
		}
		if answered > 0 || pending > 0 {
			fmt.Println("the start of the process of answered", answered,
				"stored question(s) of that had no answer of, left", pending,
				"still without an answer (the next start of the process tries them again)")
		}
	}()

	// os/signal: NotifyContext is installed before anything starts, so Ctrl+C / SIGTERM can never be
	// call). The wait below is on the context, not on a fixed window — the process stays running until a stop
	// signal arrives — and a health listener that dies takes the run down with a clear error instead of a
	var stoppedBySignal = true
	select {
	case <-ctx.Done():
	case <-botDone:
		// a chance to bring it back.
		fmt.Println("the Telegram transport stopped without a stop signal, exit code 1 ")
		cancel()
		db.Close()
		os.Exit(1)
	case err := <-healthErr:
		if err != nil {
			fmt.Println("health:", err, "")
			db.Close()
			os.Exit(1)
		}
		stoppedBySignal = false
	}
	cancel()
	fmt.Println("stopped by signal", stoppedBySignal, "")
	// The line an operator looks for when a container is stopped: the process left on a signal, not because
	// is bounded by pollerWait: a stop that never finishes cannot be told apart from a hung process, and
	var pollerGone = false
	select {
	case <-botDone:
		pollerGone = true
	case <-time.After(pollerWait):
	}
	fmt.Println("poller stopped", pollerGone, "")
	// the database stays open for that goroutine too — bounded by the same pollerWait window, because a stop
	// Response), which is the state the next start of the process picks up again.
	var resumeGone = false
	select {
	case <-resumeDone:
		resumeGone = true
	case <-time.After(pollerWait):
	}
	if !resumeGone {
		fmt.Println("the startup scan of was still answering when the process left ")
	}
	db.Close()
	fmt.Println("stopped by signal ")
	os.Exit(0)
}
