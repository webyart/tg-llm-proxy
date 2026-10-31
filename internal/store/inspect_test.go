// inspect_test.go — the §11 Store level for the read-only side of §5: the reader of scripts/db_inspect.ps1
// must not be able to change the database of a RUNNING bot, must not re-mode its journal, and must not
// invent an empty history. The tests write through the runtime Open and read through OpenReadOnly over the
// same file, because that pair is exactly what the script does while the bot is up (§9).
package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fileDB — a real file database of §5 in t.TempDir() with data in it: one chat, the two rows of one answer
// (a user message with its Telegram id and the assistant row with the metadata of §5), two events of
// different kinds and the poller offset. Written through the RUNTIME Open, so the file is in WAL and the
// reader is looked at as an outsider — which is the situation this file exists for.
func fileDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	var ctx = context.Background()
	var path = filepath.Join(t.TempDir(), "bot.db")
	var db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := EnsureChat(ctx, db, 4242, 666244095, "Arti"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if _, err := Append(ctx, db, Message{ChatID: 4242, UserID: 666244095, Role: RoleUser,
		Content: "привет", TelegramMessageID: intPtr(1001)}); err != nil {
		t.Fatalf("Append user: %v", err)
	}
	if _, err := Append(ctx, db, Message{ChatID: 4242, UserID: 666244095, Role: RoleAssistant,
		Content: "здарова", TokensIn: intPtr(11), TokensOut: intPtr(22), Model: "qwen-test", LatencyMs: 133}); err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	if err := LogEvent(ctx, db, 1, "denied", "user 1 is outside the allowlist"); err != nil {
		t.Fatalf("LogEvent denied: %v", err)
	}
	if err := LogEvent(ctx, db, 666244095, "command", "/stats"); err != nil {
		t.Fatalf("LogEvent command: %v", err)
	}
	if err := SetLastUpdateID(ctx, db, 90210); err != nil {
		t.Fatalf("SetLastUpdateID: %v", err)
	}
	return path, db
}

// readOnly — OpenReadOnly over the same path, closed with the test.
func readOnly(t *testing.T, path string) *sql.DB {
	t.Helper()
	var db, err = OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenReadOnly(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestReadOnlyDSNShape — the reader's DSN is the reader's own (§5 «Maintenance-DSN — отдельный»): mode=ro
// and the busy timeout, and deliberately none of the keys that would write to the file (_journal_mode),
// take the write lock for a SELECT (_txlock) or collide with mode=ro (cache=shared, §5 case D). A memory
// path is refused outright: mode=memory under mode=ro opens a FRESH empty database, and a viewer that
// reports «the history is empty» about a running bot is the worst lie this tool can tell.
func TestReadOnlyDSNShape(t *testing.T) {
	var dsn, err = ReadOnlyDSN(`C:\Tools\data\bot.db`)
	if err != nil {
		t.Fatalf("ReadOnlyDSN: %v", err)
	}
	if !strings.HasPrefix(dsn, "file:") || !strings.Contains(dsn, "?mode=ro&_busy_timeout="+ReadOnlyBusyTimeout) {
		t.Errorf("the reader DSN is %q, want file:<path>?mode=ro&_busy_timeout=%s", dsn, ReadOnlyBusyTimeout)
	}
	for _, banned := range []string{"_txlock", "_journal_mode", "_pragma", "cache=shared", "_foreign_keys"} {
		if strings.Contains(dsn, banned) {
			t.Errorf("the reader DSN carries %s (%q) — a viewer must not write, must not re-mode the journal and must not take the write lock (§5)", banned, dsn)
		}
	}

	if _, memErr := ReadOnlyDSN(":memory:"); memErr == nil {
		t.Error("ReadOnlyDSN accepted the memory path of §5 — that would open a fresh EMPTY database, not the running one")
	} else if !strings.Contains(memErr.Error(), "memory") {
		t.Errorf("the memory refusal says %q, want it to name the memory database", memErr)
	}

	// The path rules of BuildDSN are reused, so the reader cannot be pointed at a query-bearing path (§5).
	if _, badErr := ReadOnlyDSN(`C:\data\bo?t.db`); badErr == nil {
		t.Error("ReadOnlyDSN accepted a path carrying ? (§5: the DSN whitelist rejects it, and so does the reader)")
	}
}

// TestOpenReadOnlyCannotWriteAndCannotRemode — §5 case D proved that mode=ro answers «attempt to write a
// readonly database»; what the §9 script needs is the PAIR of guarantees: the INSERT is refused AND the file
// is left as it was, journal_mode included (§5: journal_mode is a property of the file, so a tool that only
// came to look must not be the thing that changes it).
func TestOpenReadOnlyCannotWriteAndCannotRemode(t *testing.T) {
	var path, runtime = fileDB(t)
	var ctx = context.Background()

	var before, pragmaErr = Pragma(ctx, runtime, "journal_mode")
	if pragmaErr != nil {
		t.Fatalf("Pragma journal_mode: %v", pragmaErr)
	}
	if before != "wal" {
		t.Fatalf("the runtime file is in journal_mode=%s, want wal before testing the reader (§10 День 2)", before)
	}

	var reader = readOnly(t, path)
	if _, writeErr := reader.ExecContext(ctx,
		`INSERT INTO messages (chat_id, role, content) VALUES (4242, 'user', 'written by the viewer')`); writeErr == nil {
		t.Fatal("the read-only handle accepted an INSERT — a viewer could rewrite the history of §5")
	} else if !strings.Contains(strings.ToLower(writeErr.Error()), "readonly") {
		t.Errorf("the refusal says %q, want the readonly refusal of §5 case D", writeErr)
	}
	if _, ddlErr := reader.ExecContext(ctx, `CREATE TABLE viewer_left_a_table (x INTEGER)`); ddlErr == nil {
		t.Error("the read-only handle created a table — DDL is a write too (§5)")
	}

	var after, afterErr = Pragma(ctx, runtime, "journal_mode")
	if afterErr != nil {
		t.Fatalf("Pragma journal_mode after: %v", afterErr)
	}
	if after != before {
		t.Errorf("journal_mode went %s → %s while a viewer only read (§5: journal_mode belongs to the file)", before, after)
	}

	var count int64
	if countErr := runtime.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&count); countErr != nil {
		t.Fatalf("count after the refused write: %v", countErr)
	}
	if count != 2 {
		t.Errorf("the runtime file holds %d messages after the refused write, want the 2 that were written (§12 reliability: the refusal must be a real refusal)", count)
	}
}

// TestOpenReadOnlyMissingFile — a missing database is reported as a missing database. mode=ro never creates
// a file (§5 case D), so the driver would answer SQLITE_CANTOPEN; the operator needs to hear which case it
// is, and the tool must not leave a zero-byte file behind the way a runtime open would.
func TestOpenReadOnlyMissingFile(t *testing.T) {
	var path = filepath.Join(t.TempDir(), "nobody-started-here.db")
	var _, err = OpenReadOnly(context.Background(), path)
	if err == nil {
		t.Fatal("OpenReadOnly opened a database that does not exist — the reader would print an empty history as truth")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("the refusal says %q, want it to name the missing file (§9 db_inspect: DB_PATH is the thing to fix)", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("the refused reader left a file behind — a viewer must not touch the directory (§5 case D)")
	}
}

// TestWALSideFileIsThereWhileTheWriterIsOpenAndGoneAfterAClose — the decision rule of OpenReadOnlySnapshot in
// one test: the -wal of §5 exists while a writer holds the file, and a clean Close checkpoints and deletes it.
// Measured in the container of §8.1 on 2026-10-31 (`docker compose stop` left bot.db alone in the
// named volume), and it is what makes an immutable read legitimate: with no -wal file the main file already
// holds every committed page, so the reader is not looking at an old state.
func TestWALSideFileIsThereWhileTheWriterIsOpenAndGoneAfterAClose(t *testing.T) {
	var ctx = context.Background()
	var path = filepath.Join(t.TempDir(), "wal-side-file.db")
	var db, openErr = Open(ctx, path)
	if openErr != nil {
		t.Fatalf("Open(%s): %v", path, openErr)
	}
	if writeErr := SetLastUpdateID(ctx, db, 920551556); writeErr != nil {
		t.Fatalf("SetLastUpdateID: %v", writeErr)
	}
	if !WALSideFilePresent(path) {
		t.Fatalf("no %s next to a database a writer is holding open — the WAL of §5 is not what this layer thinks it is", WALSideFile(path))
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	if WALSideFilePresent(path) {
		t.Errorf("%s is still next to a database whose writer closed cleanly — the checkpoint of §5 did not delete it, and the immutable reader of §9 would then be reading a state that is not the whole state", WALSideFile(path))
	}
}

// TestImmutableReaderSeesTheSameNumbersAndCallsJournalModeDelete — the substance of the fallback: the same
// numbers as the plain reader, and the one value that differs. The journal_mode answer is asserted rather than
// described: it is the reason cmd/dbinspect prints the open mode next to every number (§12 reliability), and
// the assertion pins what an immutable handle reports even for a WAL-mode file (measured in the container of
// §8.1 on 2026-10-31).
func TestImmutableReaderSeesTheSameNumbersAndCallsJournalModeDelete(t *testing.T) {
	var ctx = context.Background()
	var path, runtime = fileDB(t)
	var plainOffset, plainFound, plainErr = LastUpdateID(ctx, runtime)
	if plainErr != nil {
		t.Fatalf("LastUpdateID through the runtime handle: %v", plainErr)
	}
	if !WALSideFilePresent(path) {
		t.Fatalf("the writer is still open, so %s must exist before testing the immutable handle", WALSideFile(path))
	}
	if closeErr := runtime.Close(); closeErr != nil {
		t.Fatalf("Close: %v (§5: the clean close is what checkpoints the WAL)", closeErr)
	}

	var snapshot, openErr = openReadOnlyImmutable(ctx, path)
	if openErr != nil {
		t.Fatalf("openReadOnlyImmutable(%s): %v", path, openErr)
	}
	defer snapshot.Close()

	var mode, pragmaErr = Pragma(ctx, snapshot, "journal_mode")
	if pragmaErr != nil {
		t.Fatalf("Pragma journal_mode through the immutable handle: %v", pragmaErr)
	}
	if mode != "delete" {
		t.Errorf("the immutable handle answers journal_mode=%q, want \"delete\" — the caveat cmd/dbinspect prints rests on this being the handle's answer and not the file's (§9, §12 reliability)", mode)
	}

	var offset, found, offsetErr = LastUpdateID(ctx, snapshot)
	if offsetErr != nil {
		t.Fatalf("LastUpdateID through the immutable handle: %v", offsetErr)
	}
	if offset != plainOffset || found != plainFound {
		t.Errorf("the immutable reader sees offset %d (found %t) where the writer left %d (found %t) — with no -wal file the main file IS the state, so a difference here is a broken open of §5", offset, found, plainOffset, plainFound)
	}
	var counts, countsErr = TableCounts(ctx, snapshot)
	if countsErr != nil {
		t.Fatalf("TableCounts through the immutable handle: %v", countsErr)
	}
	if len(counts) == 0 {
		t.Error("the immutable reader found no tables in a database the writer created — the migrations of §5 live in the main file")
	}
}

// TestOpenReadOnlySnapshotUsesThePlainOpenFirst — on a path a writable process can reach, the fallback is not
// used at all: the caller gets the accurate handle that attaches the WAL of §5.
func TestOpenReadOnlySnapshotUsesThePlainOpenFirst(t *testing.T) {
	var path, _ = fileDB(t)
	var db, mode, openErr = OpenReadOnlySnapshot(context.Background(), path)
	if openErr != nil {
		t.Fatalf("OpenReadOnlySnapshot(%s): %v", path, openErr)
	}
	defer db.Close()
	if mode != ReadOnlyModePlain {
		t.Errorf("the snapshot open chose %s over %s on a writable path — the reader would lose the pages of §5 that are still in the WAL", mode, ReadOnlyModePlain)
	}
	if _, _, offsetErr := LastUpdateID(context.Background(), db); offsetErr != nil {
		t.Errorf("the snapshot handle cannot read bot_state: %v", offsetErr)
	}
}

// TestOpenReadOnlySnapshotNeverCreatesAFile — the fallback must not turn «there is no database here» into a
// zero-byte database: both opens are mode=ro, and a missing path is reported as a missing path, without dragging
// the immutable story into a message about a file that was never created.
func TestOpenReadOnlySnapshotNeverCreatesAFile(t *testing.T) {
	var path = filepath.Join(t.TempDir(), "never-created.db")
	var _, mode, openErr = OpenReadOnlySnapshot(context.Background(), path)
	if openErr == nil {
		t.Fatal("OpenReadOnlySnapshot opened a database that does not exist — the viewer would print an empty history as truth (§5 case D)")
	}
	if mode != "" {
		t.Errorf("a refused open must not report a mode, it reported %q", mode)
	}
	if !strings.Contains(openErr.Error(), "does not exist") {
		t.Errorf("the refusal says %q, want it to name the missing file", openErr)
	}
	if strings.Contains(openErr.Error(), string(ReadOnlyModeImmutable)) {
		t.Errorf("the refusal talks about the immutable fallback for a missing file — the -wal check of §9 never ran here, so the operator would chase the wrong thing: %v", openErr)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("the refused snapshot open left a file behind (§5 case D: a viewer must not touch the directory)")
	}
}

// TestReadOnlyImmutableDSNIsTheReaderDSNPlusOneParameter — the second DSN differs from the first by exactly the
// URI parameter that makes the open possible on a read-only mount, and keeps every rule that keeps the viewer
// honest: no _txlock (a reader must not take the write lock of a live bot), no _journal_mode (journal_mode
// belongs to the file, §5), no cache=shared (illegal with mode=ro, §5 case D), no memory path, no query mark.
func TestReadOnlyImmutableDSNIsTheReaderDSNPlusOneParameter(t *testing.T) {
	var dsn, err = ReadOnlyImmutableDSN(`C:\Tools\data\bot.db`)
	if err != nil {
		t.Fatalf("ReadOnlyImmutableDSN: %v", err)
	}
	if !strings.Contains(dsn, "mode=ro&immutable=1&_busy_timeout="+ReadOnlyBusyTimeout) {
		t.Errorf("the snapshot DSN is %q, want mode=ro&immutable=1&_busy_timeout=%s — the underscore spelling is the driver namespace and does not mean this (measured 2026-10-31 in the container)", dsn, ReadOnlyBusyTimeout)
	}
	for _, banned := range []string{"_txlock", "_journal_mode", "_pragma", "cache=shared", "_foreign_keys", "_immutable"} {
		if strings.Contains(dsn, banned) {
			t.Errorf("the snapshot DSN carries %s (%q) — the rules of the plain reader plus one parameter, nothing else", banned, dsn)
		}
	}
	if _, memErr := ReadOnlyImmutableDSN(":memory:"); memErr == nil {
		t.Error("the snapshot DSN accepted the memory path of §5 — that opens a fresh EMPTY database and calls it the history")
	}
	if _, badErr := ReadOnlyImmutableDSN(`C:\data\bo?t.db`); badErr == nil {
		t.Error("the snapshot DSN accepted a path carrying ? (§5: the DSN whitelist rejects it, and so does the reader)")
	}
}

// TestReadOnlyReadsEverythingTheScriptPrints — the whole read side of §9 db_inspect over the file of a
// running bot: chats with their message count, the newest messages with the §10 День 7 metadata, events and
// their per-kind counts, the bot_state offset, the per-table counts and integrity_check. Every value here is
// a value the script prints, so a wrong column order or a wrong COALESCE fails a test instead of confusing a
// human at two in the morning (§12 reliability).
func TestReadOnlyReadsEverythingTheScriptPrints(t *testing.T) {
	var path, _ = fileDB(t)
	var ctx = context.Background()
	var reader = readOnly(t, path)

	var chats, chatErr = ListChats(ctx, reader, 0, 10)
	if chatErr != nil {
		t.Fatalf("ListChats: %v", chatErr)
	}
	if len(chats) != 1 {
		t.Fatalf("ListChats returned %d rows, want the one chat of §5", len(chats))
	}
	if chats[0].ChatID != 4242 || chats[0].UserID != 666244095 || chats[0].Title != "Arti" {
		t.Errorf("the chat row is %+v, want chat 4242 / user 666244095 / title Arti", chats[0])
	}
	if chats[0].Messages != 2 {
		t.Errorf("the chat reports %d messages, want 2 (the FULL count of §5, not the context window)", chats[0].Messages)
	}
	if chats[0].CreatedAt == "" || chats[0].LastActiveAt == "" {
		t.Errorf("the chat row has no timestamps: %+v (§5 datetime('now'), UTC — README «Часовой пояс»)", chats[0])
	}
	if chats[0].ContextResetAt != "" {
		t.Errorf("ContextResetAt is %q without a /reset (§5 NULL must read as empty)", chats[0].ContextResetAt)
	}

	var messages, msgErr = RecentMessages(ctx, reader, 4242, 10)
	if msgErr != nil {
		t.Fatalf("RecentMessages: %v", msgErr)
	}
	if len(messages) != 2 {
		t.Fatalf("RecentMessages returned %d rows, want 2", len(messages))
	}
	// id DESC: the assistant answer is the newest row, and the order must not depend on the second-resolution
	// created_at of §5, which ties both rows.
	if messages[0].Role != RoleAssistant || messages[1].Role != RoleUser {
		t.Errorf("the newest-first order is %s then %s (§5 id DESC)", messages[0].Role, messages[1].Role)
	}
	if messages[0].Model != "qwen-test" || messages[0].LatencyMS != 133 || messages[0].TokensIn != 11 || messages[0].TokensOut != 22 {
		t.Errorf("the answer row lost its §10 День 7 metadata: %+v", messages[0])
	}
	if messages[0].TelegramMessageID != -1 {
		t.Errorf("the model answer shows telegram_message_id=%d, want -1 for the NULL of §5", messages[0].TelegramMessageID)
	}
	if messages[1].TelegramMessageID != 1001 {
		t.Errorf("the user row shows telegram_message_id=%d, want 1001", messages[1].TelegramMessageID)
	}
	// The two nullable shapes of §5 read differently, and the reader must not invent either of them:
	// model is NULL for a user row (Append passes nullable(m.Model)) and reads back empty, while
	// latency_ms is written as the plain int64 of Message and therefore IS 0 in the file — a viewer that
	// showed -1 there would hide a number the writer really stored (§12 reliability).
	if messages[1].Model != "" {
		t.Errorf("the user row shows model %q the model never set (§5 NULL must read as empty)", messages[1].Model)
	}
	if messages[1].TokensIn != -1 || messages[1].TokensOut != -1 {
		t.Errorf("the user row reports tokens %d/%d, want -1 for the NULL of §5 («nobody counted», not «zero»)",
			messages[1].TokensIn, messages[1].TokensOut)
	}
	if messages[1].LatencyMS != 0 {
		t.Errorf("the user row reports latency %d, want the 0 that Append stores (§5 latency_ms is not nullable in practice)", messages[1].LatencyMS)
	}

	var events, eventErr = RecentEvents(ctx, reader, 10)
	if eventErr != nil {
		t.Fatalf("RecentEvents: %v", eventErr)
	}
	if len(events) != 2 {
		t.Fatalf("RecentEvents returned %d rows, want 2", len(events))
	}
	if events[0].Kind != "command" || events[0].Detail != "/stats" {
		t.Errorf("the newest event is %+v, want the /stats row (id DESC)", events[0])
	}

	var kinds, kindErr = EventCounts(ctx, reader)
	if kindErr != nil {
		t.Fatalf("EventCounts: %v", kindErr)
	}
	if len(kinds) != 2 || kinds[0].Count != 1 || kinds[1].Count != 1 {
		t.Fatalf("EventCounts returned %+v, want one row per kind with count 1", kinds)
	}

	var state, stateErr = StateEntries(ctx, reader)
	if stateErr != nil {
		t.Fatalf("StateEntries: %v", stateErr)
	}
	if len(state) != 1 || state[0].Key != StateLastUpdateID || state[0].Value != "90210" {
		t.Errorf("bot_state reads %+v, want %s=90210 (§5: the offset survives a restart)", state, StateLastUpdateID)
	}

	var tables, tableErr = TableCounts(ctx, reader)
	if tableErr != nil {
		t.Fatalf("TableCounts: %v", tableErr)
	}
	var byName = map[string]int64{}
	for _, tc := range tables {
		byName[tc.Name] = tc.Rows
	}
	for name, want := range map[string]int64{"chats": 1, "messages": 2, "events": 2, "bot_state": 1} {
		got, ok := byName[name]
		if !ok {
			t.Errorf("TableCounts has no %s — the tables of migrations/001_init.sql (§9 lists them)", name)
		} else if got != want {
			t.Errorf("table %s holds %d rows, want %d", name, got, want)
		}
	}
	if _, noisy := byName["sqlite_sequence"]; noisy {
		t.Error("TableCounts counted sqlite_sequence — SQLite's own bookkeeping is not a table of §5 (§5 messages AUTOINCREMENT)")
	}

	var verdict, integrityErr = IntegrityCheck(ctx, reader)
	if integrityErr != nil {
		t.Fatalf("IntegrityCheck: %v", integrityErr)
	}
	if verdict != "ok" {
		t.Errorf("integrity_check answered %q, want ok on a file the bot just wrote (§15)", verdict)
	}
}

// TestReadOnlyRefusesBadLimits — the limit reaches SQLite as a parameter, but a viewer that accepted
// -limit 0 would print an empty section and look like an empty database. The refusal is the point (§9).
func TestReadOnlyRefusesBadLimits(t *testing.T) {
	var path, _ = fileDB(t)
	var reader = readOnly(t, path)

	if _, err := ListChats(context.Background(), reader, 0, 0); err == nil {
		t.Error("ListChats accepted limit 0 (§9 db_inspect -limit must be positive)")
	}
	if _, err := RecentMessages(context.Background(), reader, 4242, -1); err == nil {
		t.Error("RecentMessages accepted a negative limit")
	}
	if _, err := RecentEvents(context.Background(), reader, 0); err == nil {
		t.Error("RecentEvents accepted limit 0")
	}
}

// TestListChatsFilterAndResetMark — the -Chat flag of §9 db_inspect is a SQL filter, not a Go filter: with two
// chats in the file and a limit of 1, asking for the chat that is NOT the newest must still return that chat.
// A filter applied after LIMIT would answer «empty» about a chat that exists, which is the same class of lie
// as the missing-file case above. The /reset mark of §5 comes back too, because the script prints it as a note:
// «the history is in the file, the context just starts after context_reset_at».
func TestListChatsFilterAndResetMark(t *testing.T) {
	var path, runtime = fileDB(t)
	var ctx = context.Background()
	if err := EnsureChat(ctx, runtime, 4343, 777, "Группа"); err != nil {
		t.Fatalf("EnsureChat 4343: %v", err)
	}
	if _, err := Append(ctx, runtime, Message{ChatID: 4343, UserID: 777, Role: RoleUser, Content: "сообщение второго чата"}); err != nil {
		t.Fatalf("Append 4343: %v", err)
	}
	if _, resetErr := ClearChat(ctx, runtime, 4242); resetErr != nil {
		t.Fatalf("ClearChat (/reset of §6): %v", resetErr)
	}
	var reader = readOnly(t, path)

	// Which chat is the newest depends on the SECOND-resolution created_at of §5 (both writes can land in the
	// same second), so the test reads the order first and then argues about it instead of hard-coding it:
	// the point is that a filtered read of the chat which is NOT the newest still finds it at limit 1.
	var all, allErr = ListChats(ctx, reader, 0, 10)
	if allErr != nil {
		t.Fatalf("ListChats of both chats: %v", allErr)
	}
	if len(all) != 2 {
		t.Fatalf("ListChats returned %d chats, want 2", len(all))
	}
	var first, firstErr = ListChats(ctx, reader, 0, 1)
	if firstErr != nil {
		t.Fatalf("ListChats without a filter: %v", firstErr)
	}
	if len(first) != 1 || first[0].ChatID != all[0].ChatID {
		t.Fatalf("the unfiltered limit-1 read returned %+v, want the newest chat %d of the full read", first, all[0].ChatID)
	}
	// …while the same limit with the filter must return the other chat, which limit 1 without a filter never
	// shows (§9 -Chat).
	var older = all[1].ChatID
	var one, oneErr = ListChats(ctx, reader, older, 1)
	if oneErr != nil {
		t.Fatalf("ListChats with chatID %d: %v", older, oneErr)
	}
	if len(one) != 1 || one[0].ChatID != older {
		t.Fatalf("the filtered read returned %+v, want chat %d — the filter belongs in SQL, not after LIMIT", one, older)
	}

	var reset = chatsByID(all, 4242)
	if reset.ContextResetAt == "" {
		t.Errorf("chat 4242 carries no context_reset_at after /reset (§5 stores it, the script prints it)")
	}
	if reset.Messages != 2 {
		t.Errorf("chat 4242 counts %d messages after /reset, want 2 — §5 keeps the history, only the context moves", reset.Messages)
	}
	var untouched = chatsByID(all, 4343)
	if untouched.ContextResetAt != "" {
		t.Errorf("chat 4343 has context_reset_at %q without a /reset (§5 NULL must read as empty)", untouched.ContextResetAt)
	}
	if untouched.Messages != 1 {
		t.Errorf("chat 4343 counts %d messages, want 1 (§5 subquery over messages)", untouched.Messages)
	}
	// An unknown chat is an empty answer, not an error: the operator asked a legal question (§6 /start is what
	// creates the row), and the script says so in its own words.
	var none, noneErr = ListChats(ctx, reader, 999999, 5)
	if noneErr != nil {
		t.Fatalf("ListChats for an unknown chat: %v (§9: no rows is not an error)", noneErr)
	}
	if len(none) != 0 {
		t.Errorf("ListChats for chat 999999 returned %+v, want no rows", none)
	}
}

// chatsByID — one row of a ListChats result by chat_id, or the zero row when the list does not have it (the
// test then fails on the empty fields with a message that names the chat).
func chatsByID(rows []ChatRow, chatID int64) ChatRow {
	for _, row := range rows {
		if row.ChatID == chatID {
			return row
		}
	}
	return ChatRow{}
}

// TestIsTableName — TableCounts quotes the identifier it read from sqlite_master, and an identifier cannot
// be a bound parameter, so the whitelist is the only thing between a strange table name and two SQL
// statements (§12 security, the §5 line about multi-statement DSN values).
func TestIsTableName(t *testing.T) {
	for _, good := range []string{"chats", "messages", "bot_state", "events", "migrations_001", "_private", "a1"} {
		if !isTableName(good) {
			t.Errorf("isTableName(%q) is false, want true (the §5 tables of migrations/001_init.sql)", good)
		}
	}
	for _, bad := range []string{"", "1st", `x"); DROP TABLE chats; --`, "has space", `quote"`, "semi;colon", "bracket]"} {
		if isTableName(bad) {
			t.Errorf("isTableName(%q) is true — that name would be quoted straight into COUNT(*) (§12 security)", bad)
		}
	}
}
