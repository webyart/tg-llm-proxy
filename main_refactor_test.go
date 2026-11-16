package main

// The entry point has two things worth a test now that it is a function: the startup summary must not leak a
// secret (or drown in "is not set" lines), and run() must return an error instead of exiting when a component
// cannot start — the exit code belongs to main() alone.

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bot/internal/config"
)

func TestStartupSummaryNeverPrintsSecretsOrUnsetRows(t *testing.T) {
	var cfg, problems = config.LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "12345:very-secret-token",
		"STRATA_API_KEY":     "sk-very-secret-key",
		"STRATA_MODEL":       "qwen-test",
	})
	if len(problems) > 0 {
		t.Fatalf("config: %v", problems)
	}
	var buffer bytes.Buffer
	var logger = slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	startupSummary(logger, cfg)
	var logged = buffer.String()

	for _, secret := range []string{"very-secret-token", "sk-very-secret-key"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("the startup summary leaked %q: %s", secret, logged)
		}
	}
	if !strings.Contains(logged, "qwen-test") || !strings.Contains(logged, "configuration loaded") {
		t.Fatalf("the summary does not name the model: %s", logged)
	}
	if strings.Contains(logged, "is not set") {
		t.Fatalf("unset optional rows are still listed: %s", logged)
	}
	if !strings.Contains(logged, "token_set=true") {
		t.Fatalf("the summary does not report the token as present: %s", logged)
	}
}

// TestStartupSummaryNamesTheMiniAppURL — the incident: a root URL behind a path-prefix gateway opens the
// gateway's fallback route, and the only symptom is Traefik's «no available server» with a healthy bot behind
// it. The summary must print the URL Telegram will use, and warn when it has no path at all.
func TestStartupSummaryNamesTheMiniAppURL(t *testing.T) {
	var rootRaw = map[string]string{}
	rootRaw["TELEGRAM_BOT_TOKEN"] = "12345:test-token"
	rootRaw["MINIAPP_ENABLED"] = "true"
	rootRaw["MINIAPP_PUBLIC_URL"] = "https://example.test"
	var rootCfg, rootProblems = config.LoadFrom(rootRaw)
	if len(rootProblems) > 0 {
		t.Fatalf("config: %v", rootProblems)
	}
	var buffer bytes.Buffer
	startupSummary(slog.New(slog.NewTextHandler(&buffer, nil)), rootCfg)
	var rootLog = buffer.String()
	if !strings.Contains(rootLog, "https://example.test") {
		t.Fatalf("the summary does not name the Mini App URL: %s", rootLog)
	}
	if !strings.Contains(rootLog, "no path") {
		t.Fatalf("a root Mini App URL produced no warning: %s", rootLog)
	}

	var prefixedRaw = map[string]string{}
	prefixedRaw["TELEGRAM_BOT_TOKEN"] = "12345:test-token"
	prefixedRaw["MINIAPP_ENABLED"] = "true"
	prefixedRaw["MINIAPP_PUBLIC_URL"] = "https://example.test/miniapp"
	var prefixedCfg, prefixedProblems = config.LoadFrom(prefixedRaw)
	if len(prefixedProblems) > 0 {
		t.Fatalf("config: %v", prefixedProblems)
	}
	buffer.Reset()
	startupSummary(slog.New(slog.NewTextHandler(&buffer, nil)), prefixedCfg)
	if strings.Contains(buffer.String(), "no path") {
		t.Fatalf("a path-prefixed Mini App URL was warned about: %s", buffer.String())
	}
}

// TestStartupSummaryWarnsWhenTheStepBudgetCannotCoverAFullySlowStep — the step deadline is the hard ceiling of
// a task step, so a step budget below 5 × TASK_MODEL_TIMEOUT_SECONDS cuts a fully slow step. The warning is
// advisory (the operator may accept it), but it must stay: it fires for 600/240 and disappears only when the
// configured value actually covers the worst case, as the shipped default 1500/240 does.
func TestStartupSummaryWarnsWhenTheStepBudgetCannotCoverAFullySlowStep(t *testing.T) {
	var summary = func(raw map[string]string) string {
		t.Helper()
		var cfg, problems = config.LoadFrom(raw)
		if len(problems) > 0 {
			t.Fatalf("config: %v", problems)
		}
		var buffer bytes.Buffer
		startupSummary(slog.New(slog.NewTextHandler(&buffer, nil)), cfg)
		return buffer.String()
	}
	var uncovered = summary(map[string]string{
		"TELEGRAM_BOT_TOKEN":         "12345:test-token",
		"TASK_MODEL_TIMEOUT_SECONDS": "240",
		"TASK_STEP_DEADLINE_SECONDS": "600",
	})
	if !strings.Contains(uncovered, "cannot cover every model call") {
		t.Fatalf("a 600 s step with 240 s calls produced no warning: %s", uncovered)
	}
	if !strings.Contains(uncovered, "recommended_task_step_deadline_seconds=1200") ||
		!strings.Contains(uncovered, "task_lease_seconds=630") {
		t.Fatalf("the warning does not name the recommended deadline and lease: %s", uncovered)
	}

	var covered = summary(map[string]string{
		"TELEGRAM_BOT_TOKEN":         "12345:test-token",
		"TASK_MODEL_TIMEOUT_SECONDS": "240",
		"TASK_STEP_DEADLINE_SECONDS": "1500",
	})
	if strings.Contains(covered, "cannot cover every model call") {
		t.Fatalf("a 1500 s step with 240 s calls still warned: %s", covered)
	}

	// The tools switch gates the warning: without tools a step is a single model call and cannot spend five.
	var noTools = summary(map[string]string{
		"TELEGRAM_BOT_TOKEN":         "12345:test-token",
		"TASK_MODEL_TIMEOUT_SECONDS": "240",
		"TASK_STEP_DEADLINE_SECONDS": "600",
		"TOOLS_ENABLED":              "false",
	})
	if strings.Contains(noTools, "cannot cover every model call") {
		t.Fatalf("the warning fired with tools disabled: %s", noTools)
	}
}

// TestRunReturnsAnErrorWhenTheHealthPortIsTaken — the old code printed the listener error and called os.Exit
// from inside the wiring. Now it returns, which is what makes a failed start testable.
func TestRunReturnsAnErrorWhenTheHealthPortIsTaken(t *testing.T) {
	var listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	var _, port, splitErr = net.SplitHostPort(listener.Addr().String())
	if splitErr != nil {
		t.Fatalf("SplitHostPort: %v", splitErr)
	}

	var cfg, problems = config.LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "12345:test-token",
		"HEALTH_PORT":        port,
		"DB_PATH":            filepath.Join(t.TempDir(), "bot.db"),
	})
	if len(problems) > 0 {
		t.Fatalf("config: %v", problems)
	}
	var logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	var runErr = run(context.Background(), cfg, "test", io.Discard, logger)
	if runErr == nil {
		t.Fatal("run accepted a taken health port")
	}
	if !strings.Contains(runErr.Error(), "listened") {
		t.Fatalf("run error = %v, want the health listener failure", runErr)
	}
}

// TestRunClosesTheDatabaseOnAFailure — the refactor replaced eleven copies of `db.Close(); os.Exit(1)` with a
// deferred close. On Windows a leaked handle keeps the file locked, so renaming the database after run()
// returned is the check: it succeeds only if the handle is gone.
func TestRunClosesTheDatabaseOnAFailure(t *testing.T) {
	var listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	var _, port, splitErr = net.SplitHostPort(listener.Addr().String())
	if splitErr != nil {
		t.Fatalf("SplitHostPort: %v", splitErr)
	}
	var path = filepath.Join(t.TempDir(), "bot.db")
	var cfg, problems = config.LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "12345:test-token",
		"HEALTH_PORT":        port,
		"DB_PATH":            path,
	})
	if len(problems) > 0 {
		t.Fatalf("config: %v", problems)
	}
	var logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	if runErr := run(context.Background(), cfg, "test", io.Discard, logger); runErr == nil {
		t.Fatal("run accepted a taken health port")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("the database was not created before the failure: %v", statErr)
	}
	if renameErr := os.Rename(path, path+".moved"); renameErr != nil {
		t.Fatalf("the database handle is still held after run() returned: %v", renameErr)
	}
}
