package main

// The mapping itself had no test before, and the row is the one an operator edits by hand in .env (it was
// debug during the live walk of 2026-10-31 and went back to info afterwards).

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestLogLevelMapsTheRowsOfTheSevenTable(t *testing.T) {
	var cases = []struct {
		in   string
		want slog.Level
	}{
		{"error", slog.LevelError},
		{"warning", slog.LevelWarn},
		{"info", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{"INFO", slog.LevelInfo},      // has no case rule, the loader keeps the value as it was written
		{"  debug ", slog.LevelDebug}, // the spaces around the value of a .env line reach this point
		{"verbose", slog.LevelInfo},   // not a row of: config.LoadFrom names it, the level falls back
		{"", slog.LevelInfo},          // the quiet default of is what an empty value must mean
	}
	for _, testCase := range cases {
		if got := logLevel(testCase.in); got != testCase.want {
			t.Errorf("logLevel(%q) = %v, want %v ", testCase.in, got, testCase.want)
		}
	}
}

// jsonLinesOf — the handler of the process (main.go: NewJSONHandler(os.Stdout, …)), only the destination is a
// buffer: the same Level value logLevel produced, the same Debug + Error pair the bot writes.
func jsonLinesOf(name string) []string {
	var buf bytes.Buffer
	var logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: logLevel(name)}))
	logger.Debug("the update arrived", "update_id", 920551556, "text_len", 7)
	logger.Error("the model call failed", "chat_id", 4242)
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}

func TestInfoLevelHidesThePerUpdateLineAndKeepsErrors(t *testing.T) {
	// only update_id and text_len anyway (run.go: the text itself stays out of the log even at debug).
	var lines = jsonLinesOf("info")
	if len(lines) != 1 {
		t.Fatalf("LOG_LEVEL=info must write the ERROR line only, it wrote %d lines:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], `"level":"ERROR"`) || !strings.Contains(lines[0], `"chat_id":4242`) {
		t.Errorf("LOG_LEVEL=info must still write an ERROR line with its chat_id , it wrote: %s", lines[0])
	}
	if strings.Contains(lines[0], "the update arrived") {
		t.Errorf("LOG_LEVEL=info must not write the per-update line , it wrote: %s", lines[0])
	}

	var atDebug = jsonLinesOf("debug")
	if len(atDebug) != 2 {
		t.Fatalf("LOG_LEVEL=debug must write both lines, it wrote %d:\n%s", len(atDebug), strings.Join(atDebug, "\n"))
	}
	for _, want := range []string{`"level":"DEBUG"`, "the update arrived", `"update_id":920551556`, `"text_len":7`} {
		if !strings.Contains(atDebug[0], want) {
			t.Errorf("LOG_LEVEL=debug must write %s, the line is: %s", want, atDebug[0])
		}
	}
}
