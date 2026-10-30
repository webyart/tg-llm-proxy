// entry — the program entry point of the module
// The program entry: read the configuration from the process ENV (§7.1),
// echo it back (§10.2 Day 1 DoD) and stay running until a stop signal arrives
// (§10.2 Day 1 DoD: start and stop with Ctrl-C).

package main

import (
	"bot/internal/config"
	"bot/internal/health"
	"bot/internal/store"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// The signal set that stops the program (§10.2 Day 1 DoD: Ctrl-C on Windows
// and the SIGTERM of the process supervisors, §12.1.4 MsgStopped). `docker stop` sends SIGTERM
// (§8.1 restart: unless-stopped, the compose down of the §10 День 5 DoD), so it is part of the set:
// without it the container is SIGKILLed when the stop grace period ends, and db.Close never
// checkpoints the WAL of §5.
var stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// PrintConfig - the §10.2 Day 1 DoD "echo the read configuration back": print
// every §7.2 variable with the §-reference of its meaning, one variable per
// line. The token is hidden, only the length is printed (§12.1.1).
func PrintConfig(cfg config.Config) {
	// The secrets of §12.1.1 are never printed, only their length.
	fmt.Println("TELEGRAM_BOT_TOKEN", "is set, length", len(cfg.BotToken), "(§7, §12 security: the token of BotFather is never printed, only its length)")
	fmt.Println("STRATA_API_KEY", "is set, length", len(cfg.StrataKey), "(§7, §12 security: only the length is printed)")
	fmt.Println("STRATA_BASE_URL", cfg.StrataBase, "(§7.1 BaseURL(), §16.3.3: stored without the /v1 suffix)")
	fmt.Println("STRATA_MODEL", cfg.StrataModel, "(§7, §14 item 2: the model of Strata that answers)")
	fmt.Println("STRATA_MODELS_URL", cfg.ModelsURL, "(§7, §16 change 8: joined as {base}/models)")
	fmt.Println("STRATA_CHAT_COMPLETIONS_URL", cfg.ChatURL, "(§7, §16 change 8: joined as {base}/chat/completions)")
	fmt.Println("STRATA_TIMEOUT_SECONDS", cfg.TimeoutSeconds, "(§7, §10 День 3: the timeout of one attempt)")
	fmt.Println("TG_REPLY_DEADLINE_SECONDS", cfg.ReplyDeadlineSeconds, "(§7, §10 День 3: the total budget of the model retries + timeout + the answer into Telegram)")
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
		// §7: an empty ALLOWED_USER_IDS means the closed bot, deny by default.
		fmt.Println("ALLOWED_USER_IDS is empty: the bot is closed, deny by default, only /start and /whoami answer (§7, §16 change 13)")
		return
	}
	fmt.Println("ALLOWED_USER_IDS", len(cfg.UserIDs), "entries (§7, §12 security: the allowlist of the chat IDs)")
}

// PrintMissing - print the name of every §7.2 variable that is not set
// (§10.2 Day 1 DoD: a missing token gives a clear error; §12: the message
// names the variable and the §-reference).
func PrintMissing(raw map[string]string) {
	var names = config.Env
	var i = 0
	for i < len(names) {
		var value = strings.TrimSpace(raw[names[i]])
		if value == "" {
			// §10.2 Day 1 DoD: the names of the unset variables are printed.
			fmt.Println("ENV variable", names[i], "is not set (§7.2)")
		}
		i += 1
	}
}

// LoadConfig - read the process ENV (§7.2 Day 1 DoD: "every ENV variable
// below is read by the process"), hand it to the §7.1 loader and keep the
// configurations and the problems apart (§12.1: the errors are not fatal yet).
func LoadConfig() (config.Config, []string, map[string]string) {
	var entries = os.Environ()
	var raw = map[string]string{}
	var entry = 0
	for entry < len(entries) {
		var pair = strings.Split(entries[entry], "=")
		if len(pair) > 1 {
			// §7.2: the name is written exactly as in the table, so the lookup is
			// direct (§12.1.1: the ENV form of the variable name).
			raw[pair[0]] = strings.Join(pair[1:], "=")
		}
		entry += 1
	}
	var cfg, problems = config.LoadFrom(raw)
	return cfg, problems, raw
}

func main() {
	// §12.2, §15.5 log/slog: the JSON log layer of the bot arrives with the bot
	// itself; the §10.2 Day 1 DoD messages go to the standard output.
	// §9.4: the signal handler is installed before anything starts, so the stop
	// signal is never missed (§10.2 Day 1 DoD "start and stop with Ctrl-C").
	// §5: sqlite.StrictPragmas is process-wide, off by default and NOT a DSN parameter (the driver
	// copies it into every connection at Connect), so it is set here, before the first sql.Open —
	// the second line of defence behind the whitelist of store.BuildDSN (§13 «_pragma via %3B»).
	store.EnableStrictPragmas()
	var ctx, cancel = signal.NotifyContext(context.Background(), stopSignals...)
	// §7.1: the configuration is read before the server starts (§12.1.1 MsgTGNoID
	// and MsgStrataNoKey are the reasons of a refused start).
	var cfg, problems, raw = LoadConfig()
	PrintMissing(raw)
	if len(problems) > 0 {
		// §12: every problem is printed, the run stops with exit code 1 (§10 Day 1
		// DoD "a missing token must exit with code 1").
		var bad = 0
		for bad < len(problems) {
			// §16.2.5 DoD: the message shows the problem with the §-reference.
			fmt.Println("configuration problem:", problems[bad])
			bad += 1
		}
		if cfg.BotToken == "" {
			// §7.1: with an empty mandatory row the configuration is not printed at all.
			fmt.Println("ENV TELEGRAM_BOT_TOKEN is not set: the start falls with a clear error and the configuration is not printed (§7 behavior, §7.1, §10 День 1 DoD: exit code 1)")
		}
		fmt.Println("the loader resolved 1 configuration out of the §7 table and found", len(problems), "problem(s): every broken row is named once, the loader does not stop at the first one (§7.2, §12 reliability, §16 change 24; exit code 1)")
		os.Exit(1)
	}
	// §7.1: only a complete configuration starts the program (§10.2 Day 1 DoD:
	// "all ENV vars are filled, the program starts, echoes the configuration back
	// and stays running").
	// §7.1: the configuration of §7 was already read by LoadConfig.
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
	if modeErr != nil || keysErr != nil {
		fmt.Println("store.Pragma:", modeErr, keysErr, "(§5: the pragmas of the DSN must be readable back)")
		db.Close()
		os.Exit(1)
	}
	fmt.Println("store is open:", cfg.DBPath, "journal_mode", journalMode, "foreign_keys", foreignKeys,
		"migrations applied", len(store.MigrationSQL), "(§5 schema through go:embed, §10 День 2)")
	fmt.Println("program is running, PID", os.Getpid(), "(§10.2 Day 1 DoD)")
	// §10 День 5 + §8.1: internal/health answers GET /healthz on HEALTH_PORT, which is what the
	// compose HEALTHCHECK probes (wget on 127.0.0.1) and what keeps the container process alive
	// under restart: unless-stopped. The port of §7 is pinned by config.LoadFrom at ≤65535.
	var healthServer = health.New(cfg.HealthPort)
	var healthErr = make(chan error, 1)
	go func() {
		healthErr <- healthServer.Run(ctx)
	}()
	fmt.Println("health is listening on 127.0.0.1:", cfg.HealthPort, health.Path, "(§7 HEALTH_PORT, §8.1 HEALTHCHECK of compose)")
	// §9.4, §15.5 os/signal: NotifyContext is installed before anything starts, so Ctrl+C / SIGTERM
	// can never be missed; cancel() is the explicit cancellation path when the run ends.
	// §12.1.4 MsgStopped: the wait is on the context, not on a fixed window — the process stays
	// running until a stop signal arrives (the §10.2 Day 1 DoD "stays running"), and a health
	// listener that dies takes the run down with a clear error instead of a silent healthy shell.
	var stoppedBySignal = true
	select {
	case <-ctx.Done():
	case err := <-healthErr:
		if err != nil {
			fmt.Println("health:", err, "(§12 reliability: the start falls with a clear error, exit code 1)")
			db.Close()
			os.Exit(1)
		}
		stoppedBySignal = false
	}
	cancel()
	fmt.Println("stopped by signal", stoppedBySignal, "(§12.1.4 MsgStopped, §12.1.4 MsgStuckShutdown)")
	// §10.2 Day 1 DoD: the stopped program prints "stopped by signal".
	// §10 День 2 + §5: the store is closed before the process leaves, so the WAL is checkpointed and
	// no -wal/-shm sidecar survives a restart (a correct db.Close leaves only bot.db, §5 table row).
	db.Close()
	fmt.Println("stopped by signal (§10.2 Day 1 DoD)")
	os.Exit(0)
}
