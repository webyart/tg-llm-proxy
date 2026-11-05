package main

// `key=value`, one per number, keys only from [a-z0-9_]. A snapshot that stops parsing is worse than no

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"bot/internal/store"
)

// with one chat, one pair of messages, one poll offset and two events. The second event carries a kind the
// kind with spaces and dots without a migration and without a key the reader cannot grep.
func fileDBWith(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	var ctx = context.Background()
	var path = filepath.Join(t.TempDir(), "bot.db")
	var db, openErr = store.Open(ctx, path)
	if openErr != nil {
		t.Fatalf("store.Open(%s): %v", path, openErr)
	}
	t.Cleanup(func() { db.Close() })

	var tgID = int64(111)
	var seed = []func() error{
		func() error { return store.SetLastUpdateID(ctx, db, 920551547) },
		func() error { return store.EnsureChat(ctx, db, 4242, 77, "chat title") },
		func() error {
			_, err := store.Append(ctx, db, store.Message{ChatID: 4242, UserID: 77, Role: "user", Content: "2+2", TelegramMessageID: &tgID})
			return err
		},
		func() error {
			_, err := store.Append(ctx, db, store.Message{ChatID: 4242, UserID: 77, Role: "assistant", Content: "4", Model: "qwen-test"})
			return err
		},
		func() error { return store.LogEvent(ctx, db, 77, store.EventCommand, "ask") },
		func() error {
			return store.LogEvent(ctx, db, 77, "bad kind.x", "a kind the CHECK of does not forbid")
		},
	}
	for step, write := range seed {
		if err := write(); err != nil {
			t.Fatalf("seed step %d: %v", step, err)
		}
	}
	return ctx, db
}

func TestCountsReportIsOneKeyValuePairPerLine(t *testing.T) {
	var ctx, db = fileDBWith(t)

	var buf bytes.Buffer
	if err := countsReport(&buf, ctx, db, `C:\data\bot.db`, "wal", store.ReadOnlyModePlain); err != nil {
		t.Fatalf("countsReport: %v", err)
	}
	var out = buf.String()

	var pair = regexp.MustCompile(`^[a-z0-9_]+=[^ ]*$`)
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !pair.MatchString(line) {
			t.Errorf("the line %q is not a `key=value` pair a script can parse ", line)
		}
	}

	var want = []string{
		"open_mode=mode_ro",
		"journal_mode=wal",
		"integrity=ok",
		"schema_version=8",
		"last_update_id=920551547",
		"bot_state_offset_found=true",
		"table_bot_state=1",
		"table_chats=1",
		"table_events=2",
		"table_messages=2",
		"events_command=1",
		"events_bad_kind_x=1",
	}
	for _, line := range want {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("the block misses %s, it printed:\n%s", line, out)
		}
	}
	// '\'), and the script compares snapshots of the SAME file — so it must be present, quoted as it is.
	if !strings.HasPrefix(out, "path=C:\\data\\bot.db\n") {
		t.Errorf("the block must name the file it read, it printed:\n%s", out)
	}
	if !strings.Contains(out, "wal_side_file=absent\n") {
		t.Errorf("the block must say whether the -wal of sits next to the file, it printed:\n%s", out)
	}

}

func TestCountsReportNamesAMissingOffsetRow(t *testing.T) {
	// `last_update_id=0` that is indistinguishable from a bot that stopped at update 0, and the restart check
	var ctx = context.Background()
	var path = filepath.Join(t.TempDir(), "fresh.db")
	var db, openErr = store.Open(ctx, path)
	if openErr != nil {
		t.Fatalf("store.Open(%s): %v", path, openErr)
	}
	defer db.Close()

	var buf bytes.Buffer
	if err := countsReport(&buf, ctx, db, path, "wal", store.ReadOnlyModePlain); err != nil {
		t.Fatalf("countsReport: %v", err)
	}
	var out = buf.String()
	if !strings.Contains(out, "bot_state_offset_found=false\n") {
		t.Errorf("a fresh database must say the offset row is absent, it printed:\n%s", out)
	}
	if !strings.Contains(out, "last_update_id=0\n") {
		t.Errorf("a fresh database prints offset 0, it printed:\n%s", out)
	}
	// holds the file open, so the -wal exists. The key is not decoration — it is what tells the restart check of
	if !strings.Contains(out, "open_mode=mode_ro\n") {
		t.Errorf("the block must name the handle the numbers came through, it printed:\n%s", out)
	}
	if !strings.Contains(out, "wal_side_file=present\n") {
		t.Errorf("a database open for writing has a -wal of next to it, the block printed:\n%s", out)
	}
}

func TestCountKeyKeepsTheKeysGreppable(t *testing.T) {
	var cases = []struct{ in, want string }{
		{"model_error", "model_error"},
		{"bad kind.x", "bad_kind_x"},
		{"HTTP 500: gateway", "HTTP_500__gateway"},
		{"не прочитан", "___________"}, // the Russian note of the human report cannot become a key
		{"", ""},
	}
	for _, testCase := range cases {
		if got := countKey(testCase.in); got != testCase.want {
			t.Errorf("countKey(%q) = %q, want %q", testCase.in, got, testCase.want)
		}
	}
}

// TestModeNoteNamesTheLimitsOfTheImmutableOpen — the human report prints a number and the handle it came
// shape that reaches it is a checkpointed WAL file on a read-only mount, measured 2026-10-31 on the named
func TestModeNoteNamesTheLimitsOfTheImmutableOpen(t *testing.T) {
	var plain = modeNote(store.ReadOnlyModePlain)
	if !strings.Contains(plain, "mode=ro") {
		t.Errorf("the plain note must name mode=ro, it says: %s", plain)
	}
	if journalCaveat(store.ReadOnlyModePlain) != "" {
		t.Errorf("the plain open attaches the WAL of, so it needs no journal_mode caveat, it says: %q", journalCaveat(store.ReadOnlyModePlain))
	}

	var snapshot = modeNote(store.ReadOnlyModeImmutable)
	for _, want := range []string{"immutable", "-wal", "clean stop"} {
		if !strings.Contains(snapshot, want) {
			t.Errorf("the snapshot note must name %s (why this open happened and what it means), it says: %s", want, snapshot)
		}
	}

	var caveat = journalCaveat(store.ReadOnlyModeImmutable)
	if !strings.Contains(caveat, "delete") || !strings.Contains(caveat, "nothing about the journal mode of the file") {
		t.Errorf("the snapshot caveat must say that journal_mode through this handle describes the handle, it says: %s", caveat)
	}
}
