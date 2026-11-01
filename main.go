// entry — the program entry point of the module
// The program entry: read the configuration from the process ENV (§7), print it back, open the store,
// and stay running until a stop signal arrives — §10 День 1 asks for `main.go` с graceful shutdown
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
// (§10 День 1: `main.go` с graceful shutdown (`signal.NotifyContext`)). `docker stop` sends SIGTERM
// (§8.1 restart: unless-stopped, the compose down of the §10 День 5 DoD), so it is part of the set:
// without it the container is SIGKILLed when Docker's stop grace ends — 10 s by default, because
// docker-compose.yml sets no `stop_grace_period` — and db.Close never checkpoints the WAL of §5.
var stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

var buildCommit = "dev"

// PrintConfig - print every variable of the §7 table with the §-reference of its meaning, one variable
// per line: the program's own report of what it read. The token is hidden, only its length is printed
// (§12 безопасность).
func PrintConfig(cfg config.Config) {
	// The secrets of §12 безопасность are never printed, only their length.
	fmt.Println("TELEGRAM_BOT_TOKEN", "is set, length", len(cfg.BotToken), "(§7, §12 security: the token of BotFather is never printed, only its length)")
	fmt.Println("STRATA_API_KEY", "is set, length", len(cfg.StrataKey), "(§7, §12 security: only the length is printed)")
	fmt.Println("STRATA_BASE_URL", cfg.StrataBase, "(§7, §16 change 26: normalized to exactly one /v1)")
	fmt.Println("STRATA_MODEL", cfg.StrataModel, "(§7, §14 item 2: the model of Strata that answers)")
	fmt.Println("STRATA_MODELS_URL", cfg.ModelsURL, "(§7, §16 change 8: joined as {base}/models over NormalizeBase)")
	fmt.Println("STRATA_CHAT_COMPLETIONS_URL", cfg.ChatURL, "(§7, §16 change 8: joined as {base}/chat/completions over NormalizeBase)")
	fmt.Println("STRATA_TIMEOUT_SECONDS", cfg.TimeoutSeconds, "(§7, §10 День 3: the timeout of one attempt)")
	fmt.Println("TG_REPLY_DEADLINE_SECONDS", cfg.ReplyDeadlineSeconds, "(§7, §10 День 3: the total budget of the model retries + timeout + the answer into Telegram)")
	fmt.Println("STREAMING_ENABLED", cfg.Streaming, "(§7, §16 change 34: §15 «стриминг ответов» — one message rewritten as the model writes; off by default because it changes the UX of every answer)")
	fmt.Println("STREAM_EDIT_INTERVAL_MS", cfg.StreamEditIntervalMS, "(§7, §15: the cadence of the rewrites — the default is five seconds after the live 429 measurement, §10 День 4 flood)")
	fmt.Println("STREAM_IDLE_TIMEOUT_SECONDS", cfg.StreamIdleSeconds, "(§7, §15: the pause between two SSE events past which the stream is dead; measured gap of a healthy stream is ~20 ms)")
	fmt.Println("STREAM_TOTAL_DEADLINE_SECONDS", cfg.StreamTotalSeconds, "(§7, §15: the ceiling of one streamed answer, thinking included — the GPU of §12 is bounded)")
	fmt.Println("WEBHOOK_ENABLED", cfg.WebhookEnabled, "(§15: long polling remains the default; HTTPS terminates at the reverse proxy/tunnel)")
	if cfg.WebhookEnabled {
		fmt.Println("WEBHOOK_PUBLIC_URL", cfg.WebhookPublicURL)
		fmt.Println("WEBHOOK_LISTEN", cfg.WebhookAddress(), cfg.WebhookPath)
		fmt.Println("WEBHOOK_SECRET_TOKEN is set, length", len(cfg.WebhookSecretToken), "(§12 security: only the length is printed)")
	}
	fmt.Println("DB_PATH", cfg.DBPath, "(§7, §16 change 9: the default is a local Windows path, compose overrides /data/bot.db)")
	fmt.Println("CONTEXT_MESSAGES", cfg.ContextMessages, "(§7, §16 change 10: the glued history is now bounded by the count and by MAX_CONTEXT_CHARS)")
	fmt.Println("MAX_CONTEXT_CHARS", cfg.MaxContextChars, "(§7, §12 UX: the budget of the glued history)")
	fmt.Println("MAX_INPUT_CHARS", cfg.MaxInputChars, "(§7, §12 reliability, §16 change 10: one message longer than it is cut and the answer says so)")
	fmt.Println("SYSTEM_PROMPT", cfg.SystemPrompt, "(§7: the system message sent with every chat request)")
	fmt.Println("RATE_LIMIT_PER_MINUTE", cfg.RateLimitPerMinute, "(§7, §12 reliability: the answers per minute of one user; 0 = no limit)")
	fmt.Println("MAX_CONCURRENT_MODEL_REQUESTS", cfg.MaxConcurrentRequests, "(§7, §12 производительность: the parallel load of the GPU)")
	fmt.Println("PROXY_FREE_TEXT", cfg.FreeText, "(§7: the free texts of the allowed users instead of the commands)")
	fmt.Println("MAX_MESSAGE_CHUNK", cfg.MaxMessageChunk, "(§7, §12 UX: the limit in SYMBOLS of one Telegram message, §16 change 7: the cut goes by runes)")
	fmt.Println("PARSE_MODE", cfg.ParseMode, "(§7: MarkdownV2 | HTML | plain; the escaping marks change the length, so the cut goes after the escaping)")
	fmt.Println("HEALTH_PORT", cfg.HealthPort, "(§7, §16 change 11: the module internal/health with GET /healthz, the HEALTHCHECK of compose)")
	fmt.Println("LOG_LEVEL", cfg.LogLevel, "(§7, §12 security: the token and the full texts of the model stay out of the logs)")
	if cfg.DenyByDefault {
		// §7: an empty TELEGRAM_ALLOWED_USERS means the closed bot, deny by default (§16 change 29: the row
		// has one name — the name a real .env carries; the old spelling is refused by name in LoadFrom).
		fmt.Println("TELEGRAM_ALLOWED_USERS is empty (§7): the bot is closed, deny by default, only /start and /whoami answer (§16 change 13)")
		return
	}
	fmt.Println("TELEGRAM_ALLOWED_USERS", len(cfg.UserIDs), "entries (§7, §12 security: the allowlist of the chat IDs)")
}

// PrintMissing - print the name of every variable of the §7 table that is not set: §10 День 1 asks the
// loader for «валидация, понятные сообщения об ошибках», and this message names the variable and the
// §-reference. RowIsSet checks the one name of the row
// of §16 change 29, so a wrong "not set" line — which is worse than no line at all —
// cannot come from a second spelling that the loader would have to know about.
func PrintMissing(raw map[string]string) {
	var names = config.Env
	var i = 0
	for i < len(names) {
		if !config.RowIsSet(raw, names[i]) {
			// §10 День 1 DoD: the names of the unset variables are printed.
			fmt.Println("ENV variable", names[i], "is not set (§7)")
		}
		i += 1
	}
}

// LoadConfig - read the process ENV, hand it to the loader of §7 and keep the configuration and the
// problems apart: one pass names every broken row instead of stopping at the first one (§10 День 1:
// «internal/config: ENV → Config, валидация, понятные сообщения об ошибках»).
func LoadConfig() (config.Config, []string, map[string]string) {
	var entries = os.Environ()
	var raw = map[string]string{}
	var entry = 0
	for entry < len(entries) {
		var pair = strings.Split(entries[entry], "=")
		if len(pair) > 1 {
			// §7: the name is written exactly as in the table, so the lookup is
			// direct (§12 безопасность: the ENV form of the variable name).
			raw[pair[0]] = strings.Join(pair[1:], "=")
		}
		entry += 1
	}
	var cfg, problems = config.LoadFrom(raw)
	return cfg, problems, raw
}

// logLevel — the §7 LOG_LEVEL into a slog.Level (§12: the depth of the JSON log). An unknown value is
// already reported by config.LoadFrom and replaced by the default of §7, so this maps the four rows of
// §16 change 15 and falls back to info.
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

// pollerWait — how long the stop path waits for the polling goroutine of §5 before db.Close. The reason
// is §5: SetLastUpdateID of a processed update must land before Close checkpoints the WAL, otherwise the
// restart reads an offset it has already answered (§13 риск «повторные ответы после рестарта»). The wait
// is bounded on purpose: the graceful shutdown §10 День 1 asks for must not turn Ctrl-C into a hung
// process, and Docker would SIGKILL the container at the end of its stop grace in any case (§8.1).
var pollerWait = 3 * time.Second

func main() {
	// log/slog: the JSON log layer of the bot arrives with the bot itself; the start messages День 1 asks for
	// go to the standard output, and §12 безопасность decides what these logs may contain. The signal handler
	// is installed before anything starts, so the stop signal is never missed — §10 День 1 asks for `main.go`
	// с graceful shutdown (`signal.NotifyContext`).
	// §5: sqlite.StrictPragmas is process-wide, off by default and NOT a DSN parameter (the driver
	// copies it into every connection at Connect), so it is set here, before the first sql.Open —
	// the second line of defence behind the whitelist of store.BuildDSN (§13 «_pragma via %3B»).
	store.EnableStrictPragmas()
	fmt.Println("build commit", buildCommit)
	var ctx, cancel = signal.NotifyContext(context.Background(), stopSignals...)
	// The configuration is read before anything starts: MsgTGNoID and MsgStrataNoKey of the §7 loader name the
	// reasons of a refused start, and the DoD of §10 День 1 asks for a clear error and exit code 1.
	var cfg, problems, raw = LoadConfig()
	PrintMissing(raw)
	if len(problems) > 0 {
		// §12: every problem is printed, the run stops with exit code 1 (§10 Day 1
		// DoD "a missing token must exit with code 1").
		var bad = 0
		for bad < len(problems) {
			// §10 День 1 DoD: the message shows the problem with the §-reference.
			fmt.Println("configuration problem:", problems[bad])
			bad += 1
		}
		if cfg.BotToken == "" {
			// The DoD of §10 День 1: «при пустом токене — ясная ошибка и код выхода 1». With an empty
			// mandatory row the configuration is not printed at all.
			fmt.Println("ENV TELEGRAM_BOT_TOKEN is not set: the start falls with a clear error and the configuration is not printed (§7, the DoD of §10 День 1: exit code 1)")
		}
		fmt.Println("the loader resolved 1 configuration out of the §7 table and found", len(problems), "problem(s): every broken row is named once, the loader does not stop at the first one (§7, §12 reliability, §16 change 24; exit code 1)")
		os.Exit(1)
	}
	// Only a complete configuration of §7 gets the program running: a broken row has already exited above
	// with the DoD of §10 День 1 («ясная ошибка и код выхода 1»). PrintConfig only reports what was read.
	PrintConfig(cfg)
	// §10 День 2: store.Open builds the DSN through the whitelist of §5, Pings (the only place a bad
	// DSN surfaces, §5 spike 11), verifies journal_mode/foreign_keys and applies the schema of §5 that
	// go:embed put into the binary. A failure stops the start with exit code 1 — the same rule as a
	// broken configuration row (§12 reliability: a half-configured database must not run).
	var db, openErr = store.Open(ctx, cfg.DBPath)
	if openErr != nil {
		fmt.Println("store.Open:", openErr, "(§10 День 2: the start falls on the database, exit code 1)")
		os.Exit(1)
	}
	var journalMode, modeErr = store.Pragma(ctx, db, store.JournalModePragma)
	var foreignKeys, keysErr = store.Pragma(ctx, db, store.ForeignKeysPragma)
	var schemaVersion, schemaErr = store.SchemaVersion(ctx, db)
	if modeErr != nil || keysErr != nil || schemaErr != nil {
		fmt.Println("store.Pragma/SchemaVersion:", modeErr, keysErr, schemaErr, "(§5: pragmas and migration state must be readable)")
		db.Close()
		os.Exit(1)
	}
	fmt.Println("store is open:", cfg.DBPath, "journal_mode", journalMode, "foreign_keys", foreignKeys,
		"migrations applied", len(store.MigrationSQL), "schema_version", schemaVersion, "(§5 schema through go:embed, §10 День 2)")
	fmt.Println("program is running, PID", os.Getpid(), "(§10 День 1: `signal.NotifyContext` keeps the process alive until a stop signal)")
	// §10 День 5 + §8.1: internal/health answers GET /healthz on HEALTH_PORT, which is what the
	// compose HEALTHCHECK probes (wget on 127.0.0.1) and what keeps the container process alive
	// under restart: unless-stopped. The port of §7 is pinned by config.LoadFrom at ≤65535.
	var healthServer = health.New(cfg.HealthPort)
	// §12 reliability: the bind happens HERE, before the line below claims the endpoint answers. A
	// HEALTH_PORT taken by another process (on this machine the local STRATA proxy holds 8080, §14 item 7)
	// therefore stops the start immediately instead of surfacing 30 seconds later, after getMe of §10
	// День 4 has already burned the timeout — and instead of a container that reports healthy while
	// /healthz answers nothing (§10 День 5 DoD).
	if listenErr := healthServer.Listen(); listenErr != nil {
		fmt.Println(listenErr, "(§10 День 5 DoD: /healthz must answer — free the port of §7 HEALTH_PORT or point it elsewhere, exit code 1)")
		db.Close()
		os.Exit(1)
	}
	var healthErr = make(chan error, 1)
	go func() {
		healthErr <- healthServer.Run(ctx)
	}()
	fmt.Println("health is listening on 127.0.0.1:", healthServer.Port(), health.Path, "(§7 HEALTH_PORT, §8.1 HEALTHCHECK of compose)")
	// §10 День 4: the bot itself. The JSON log of §12 is built here because run.go writes through it, and
	// Sanitize of §12 keeps TELEGRAM_BOT_TOKEN out of every line — including the lines the Telegram
	// library writes into its own logger (§10 День 4 hands that logger to bot.WithLogger).
	var logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel(cfg.LogLevel)}))
	// §10 День 3: the client of §4 over the OpenAI-compatible API of §16 change 17, built from the §7 rows
	// it needs; the attempt count and the backoff stay the defaults of §10 День 3.
	var client, clientErr = model.New(model.Options{
		ChatURL:       cfg.ChatURL,
		ModelsURL:     cfg.ModelsURL,
		APIKey:        cfg.StrataKey,
		Model:         cfg.StrataModel,
		Timeout:       time.Duration(cfg.TimeoutSeconds) * time.Second,
		MaxConcurrent: int(cfg.MaxConcurrentRequests),
		// §15 (§16 change 34): the budgets of one streamed answer come from §7 too. The progress cadence of the
		// client is the cadence of the chat: a thinking model may refresh the placeholder no more often than
		// Telegram tolerates being edited (§10 День 4 flood).
		Stream: model.StreamSettings{
			Idle:     time.Duration(cfg.StreamIdleSeconds) * time.Second,
			Total:    time.Duration(cfg.StreamTotalSeconds) * time.Second,
			Progress: time.Duration(cfg.StreamEditIntervalMS) * time.Millisecond,
		},
	})
	if clientErr != nil {
		fmt.Println("model.New:", clientErr, "(§10 День 3: the client is built from §7, exit code 1)")
		db.Close()
		os.Exit(1)
	}
	// §5 «bot_state last_update_id»: a restart continues after the last PROCESSED update and does not
	// answer it a second time (§13 риск «повторные ответы после рестарта», §11 TestOffsetSurvivesRestart).
	var startOffset, offsetFound, offsetErr = store.LastUpdateID(ctx, db)
	if offsetErr != nil {
		fmt.Println("store.LastUpdateID:", offsetErr, "(§5 bot_state: the restart must know the offset, exit code 1)")
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
		// §5 «bot_state last_update_id»: every processed update moves the offset through the writer of the
		// store, so the restart of §8.1 continues after it instead of answering the same user twice.
		Offset: func(writeCtx context.Context, updateID int64) error {
			return store.SetLastUpdateID(writeCtx, db, updateID)
		},
	})
	// §10 День 4 «bot.New(c.BotToken, opts) с WithServerURL для тестов, WithInitialOffset из bot_state,
	// WithHTTPClient(pollTimeout) и workers»: NewTelegram calls getMe on its own (§10 День 4), and getMe
	// is what answers 401 on a wrong token. A process that cannot poll must not sit next to a healthz that
	// still says 200 (§12 reliability), so this is exit code 1 — with Sanitize already applied (§12 security).
	var tg, tgErr = dispatcher.NewTelegram(bot.Options{WebhookSecretToken: cfg.WebhookSecretToken})
	if tgErr != nil {
		fmt.Println("bot.New:", tgErr, "(§10 День 4: getMe did not answer — a wrong TELEGRAM_BOT_TOKEN or no route to api.telegram.org stops the start, exit code 1)")
		db.Close()
		os.Exit(1)
	}
	// §10 День 4 «меню команд через setMyCommands». A refusal of Telegram is not fatal: the commands of §6
	// still work, only the menu of the client stays empty (§12 reliability), and the error is sanitized.
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
			fmt.Println("setWebhook:", err, "(§15: the public HTTPS URL or secret was refused, exit code 1)")
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
		fmt.Println("bot is polling Telegram, bot_state last_update_id", startOffset, "found", offsetFound, "(§5, §10 День 4)")
	}
	// §15 «догонять вопрос, оборванный внутри обработчика» (`internal/bot/resume.go`): a question that §5 kept
	// without an answer of §6 is not brought back by the offset of §5. Measured 2026-10-31 in the §11 container
	// walk: the stop left the offset at 920551679 with the interrupted update numbered 920551680, and the
	// restarted poller asked Telegram for exactly that id and got empty batches for 9.5 minutes — Telegram
	// re-delivers only what never reached the client. The scan of resume.go asks the database instead, and it
	// runs next to the polling goroutine rather than before it: a model that spends its whole
	// STRATA_TIMEOUT_SECONDS on a question from the previous run must not delay the first getUpdates of this
	// one (§12 UX), and the per-chat lock of §12 is what keeps the replay and a real redelivery of the same
	// update from answering it twice.
	var resumeDone = make(chan struct{})
	go func() {
		defer close(resumeDone)
		var answered, pending, resumeErr = dispatcher.ResumeInterruptedQuestions(ctx)
		if resumeErr != nil && !errors.Is(resumeErr, context.Canceled) {
			logger.Error("the startup scan of §15 stopped", "answered", answered, "pending", pending,
				"error", bot.Sanitize(resumeErr, cfg.BotToken))
			return
		}
		if answered > 0 || pending > 0 {
			// §12 UX: the two numbers are different promises. «answered» is a row of §6 the user actually
			// received, «pending» is a question §5 still holds with no answer and that the next start will
			// try again. Measured 2026-10-31 15:56 in the container of §8.1: this line printed the first
			// number for a question that had earned the second one, because the ask handler of §6 answers a
			// model failure itself (the events row of §5 and the notice to the user) and returns nil.
			fmt.Println("the start of the process of §15 answered", answered,
				"stored question(s) of §5 that had no answer of §6, left", pending,
				"still without an answer (the next start of the process tries them again)")
		}
	}()

	// os/signal: NotifyContext is installed before anything starts, so Ctrl+C / SIGTERM can never be
	// missed; cancel() is the explicit cancellation path when the run ends (§10 День 1 asks for exactly this
	// call). The wait below is on the context, not on a fixed window — the process stays running until a stop
	// signal arrives — and a health listener that dies takes the run down with a clear error instead of a
	// silent healthy shell (§12 надёжность).
	var stoppedBySignal = true
	select {
	case <-ctx.Done():
	case <-botDone:
		// §12 reliability: the Telegram transport left without a stop signal. The bot answers nothing from now
		// on, and the health endpoint of §8.1 would still say 200 — that is exactly the silent failure the
		// §12 rules forbid, so the process exits with code 1 and the container restart policy of §8.1 gets
		// a chance to bring it back.
		fmt.Println("the Telegram transport stopped without a stop signal, exit code 1 (§12 reliability)")
		cancel()
		db.Close()
		os.Exit(1)
	case err := <-healthErr:
		if err != nil {
			fmt.Println("health:", err, "(§12 reliability: the start falls with a clear error, exit code 1)")
			db.Close()
			os.Exit(1)
		}
		stoppedBySignal = false
	}
	cancel()
	fmt.Println("stopped by signal", stoppedBySignal, "(the graceful shutdown of §10 День 1: a signal, then a bounded unwind)")
	// The line an operator looks for when a container is stopped: the process left on a signal, not because
	// something broke. §10 День 1 requires the graceful shutdown; it does not name this line, so the line is
	// this program's own report (§12 UX: the reason belongs next to the fact, not in a manual).
	// §10 День 2 + §5: the store is closed before the process leaves, so the WAL is checkpointed and
	// no -wal/-shm sidecar survives a restart (a correct db.Close leaves only bot.db, §5 table row).
	// §10 День 4: Close has to happen after the polling goroutine is gone — an in-flight update writes
	// SetLastUpdateID through the same handle, and a write after Close would fail (§5 bot_state). The wait
	// is bounded by pollerWait: a stop that never finishes cannot be told apart from a hung process, and
	// Docker SIGKILLs the container at the end of its stop grace anyway (§8.1).
	var pollerGone = false
	select {
	case <-botDone:
		pollerGone = true
	case <-time.After(pollerWait):
	}
	fmt.Println("poller stopped", pollerGone, "(§5: bot_state is written before db.Close checkpoints the WAL)")
	// §15 (resume.go): the replay of a stored question writes its row of the answer through the same handle, so
	// the database stays open for that goroutine too — bounded by the same pollerWait window, because a stop
	// that never finishes cannot be told apart from a hung process (§10 День 1). A replay still with the model
	// when the window closes leaves no assistant row at all (§10 День 3 writes exactly one, after the final
	// Response), which is the state the next start of the process picks up again.
	var resumeGone = false
	select {
	case <-resumeDone:
		resumeGone = true
	case <-time.After(pollerWait):
	}
	if !resumeGone {
		fmt.Println("the startup scan of §15 was still answering when the process left (§5: no answer row is written for an answer that never happened, the next start tries it again)")
	}
	db.Close()
	fmt.Println("stopped by signal (the graceful shutdown of §10 День 1)")
	os.Exit(0)
}
