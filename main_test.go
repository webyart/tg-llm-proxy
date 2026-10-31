package main

// Tests of the §7 row LOG_LEVEL as it is used by main.go: the value decides the level of the JSON handler of
// §12, and the line it decides is «the update arrived» of internal/bot/run.go — the per-update DEBUG line that
// is noise in production and the only way to see an arriving update while walking the §11 manual check-list.
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
		{"INFO", slog.LevelInfo},      // §7 has no case rule, the loader keeps the value as it was written
		{"  debug ", slog.LevelDebug}, // the spaces around the value of a .env line reach this point
		{"verbose", slog.LevelInfo},   // not a row of §16 change 15: config.LoadFrom names it, the level falls back
		{"", slog.LevelInfo},          // the quiet default of §7 is what an empty value must mean
	}
	for _, testCase := range cases {
		if got := logLevel(testCase.in); got != testCase.want {
			t.Errorf("logLevel(%q) = %v, want %v (§7 LOG_LEVEL, §12)", testCase.in, got, testCase.want)
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
	// The security half of §12: at info the per-update line is not written at all, and the Debug line carries
	// only update_id and text_len anyway (run.go: the text itself stays out of the log even at debug).
	var lines = jsonLinesOf("info")
	if len(lines) != 1 {
		t.Fatalf("LOG_LEVEL=info must write the ERROR line only, it wrote %d lines:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], `"level":"ERROR"`) || !strings.Contains(lines[0], `"chat_id":4242`) {
		t.Errorf("LOG_LEVEL=info must still write an ERROR line with its chat_id (§12), it wrote: %s", lines[0])
	}
	if strings.Contains(lines[0], "the update arrived") {
		t.Errorf("LOG_LEVEL=info must not write the per-update line (§7 default of §12), it wrote: %s", lines[0])
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
