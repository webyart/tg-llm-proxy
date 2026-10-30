// store_test.go — the §10 День 2 DoD of the store: Open + Ping + PRAGMA journal_mode/foreign_keys,
// WAL created in the first run, the schema of §5 through go:embed, an idempotent second start, the
// §11 memory pool, and the rejection of a bad DSN / a typo in a pragma (§11 Store levels).
package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
)

// TestOpenFileWAL — §10 День 2 DoD: Open + Ping + PRAGMA journal_mode/foreign_keys, WAL created in
// the first run. Every pragma of the reference DSN is read back, including the two pragms that only
// exist through `_pragma=` (§5 spikes 25/28).
func TestOpenFileWAL(t *testing.T) {
	db, path := openTemp(t)
	defer db.Close()

	ctx := context.Background()
	want := map[string]string{
		JournalModePragma: "wal",
		ForeignKeysPragma: RuntimeForeignKeys,
		BusyTimeoutPragma: RuntimeBusyTimeout,
		// §5: the DSN promises NORMAL; SQLite reports `PRAGMA synchronous` as the INTEGER 1, which is
		// NORMAL (the driver enum accepts 0/OFF, 1/NORMAL, 2/FULL, 3/EXTRA). Measured on this host.
		SynchronousPragma:       "1",
		JournalSizeLimitPragma:  RuntimeJournalSizeLimit,
		WalAutoCheckpointPragma: RuntimeWalAutoCheckpoint,
	}
	for name, expected := range want {
		got, err := Pragma(ctx, db, name)
		if err != nil {
			t.Fatalf("PRAGMA %s: %v", name, err)
		}
		if !strings.EqualFold(got, expected) {
			t.Fatalf("PRAGMA %s = %q, the reference DSN of §5 promises %q", name, got, expected)
		}
	}

	// §10 День 2 DoD: WAL created in the first run — the sidecar exists while a connection is live.
	if _, err := db.ExecContext(ctx, "INSERT INTO chats (chat_id, user_id) VALUES (1, 7)"); err != nil {
		t.Fatalf("the first write failed: %v", err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err != nil {
			t.Fatalf("%s is absent, §5 WAL needs it while the database is open: %v", sidecar, err)
		}
	}
}

// TestOpenAppliesSchema — §10 День 2: the repositories of §5 are applied on every start, and the
// guard uq_messages_chat_tg_id of §5 is what stops a duplicate answer after a restart.
func TestOpenAppliesSchema(t *testing.T) {
	db, _ := openTemp(t)
	defer db.Close()
	ctx := context.Background()

	for _, table := range []string{"chats", "messages", "bot_state", "events"} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("the table %s of §5 is missing after Open: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s holds %d rows on a fresh database", table, count)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO chats (chat_id, user_id) VALUES (1, 7)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO messages (chat_id, telegram_message_id, role, content) VALUES (1, 7, 'user', 'hi')"); err != nil {
		t.Fatal(err)
	}
	// §5: the guard uq_messages_chat_tg_id is the partial UNIQUE index that stops a repeated answer
	// after a container restart (§2 point 7, §13).
	if _, err := db.ExecContext(ctx, "INSERT INTO messages (chat_id, telegram_message_id, role, content) VALUES (1, 7, 'assistant', 'again')"); err == nil {
		t.Fatalf("uq_messages_chat_tg_id of §5 accepted a second row with telegram_message_id=7")
	}
	// The answer of the model carries no telegram_message_id, so the partial index leaves it alone.
	if _, err := db.ExecContext(ctx, "INSERT INTO messages (chat_id, role, content, model) VALUES (1, 'assistant', 'reply', 'qwen')"); err != nil {
		t.Fatalf("the answer row was refused, §5 stores answers without telegram_message_id: %v", err)
	}
	// §5: foreign_keys=1 makes REFERENCES enforced on this connection.
	if _, err := db.ExecContext(ctx, "INSERT INTO messages (chat_id, role, content) VALUES (999, 'user', 'x')"); err == nil {
		t.Fatalf("the REFERENCES of §5 are not enforced: a message for an absent chat was accepted")
	}
	// §5 CHECK on role: a role outside user/assistant/system is refused by the schema itself.
	if _, err := db.ExecContext(ctx, "INSERT INTO messages (chat_id, role, content) VALUES (1, 'robot', 'x')"); err == nil {
		t.Fatalf("the CHECK (role IN ('user','assistant','system')) of §5 accepted role=robot")
	}
	// §5 bot_state: the getUpdates offset survives a restart (§5 comment on bot_state).
	if _, err := db.ExecContext(ctx, "INSERT INTO bot_state (key, value) VALUES ('last_update_id', '42')"); err != nil {
		t.Fatalf("bot_state refused the offset row (§5): %v", err)
	}
}

// TestApplySchemaIsIdempotent — §5: one migration set, every clause with IF NOT EXISTS, applied on
// every start; the driver refuses a ';' inside one query, so Statements splits the text (§5
// «invalid semicolon separator in query»).
func TestApplySchemaIsIdempotent(t *testing.T) {
	db, _ := openTemp(t)
	defer db.Close()
	ctx := context.Background()

	statements, err := Statements(InitSQL)
	if err != nil {
		t.Fatalf("Statements(InitSQL): %v", err)
	}
	if len(statements) != 6 {
		t.Fatalf("001_init.sql split into %d statements, §5 ships 4 tables and 2 indexes: %v", len(statements), statements)
	}
	for _, statement := range statements {
		// The ';' of a comment or of a string literal is not a separator (§5: the driver refuses a
		// semicolon inside one query), so the check runs on the code of the statement.
		if strings.Contains(codeText(statement), ";") {
			t.Fatalf("statement %q carries a ';' in its code — the driver refuses it (§5)", firstLine(statement))
		}
	}
	// A second and third apply of the same text must be a no-op (§10 День 2: on every start).
	for round := 1; round <= 3; round++ {
		if err := ApplySchema(ctx, db); err != nil {
			t.Fatalf("ApplySchema round %d failed (the migration of §5 is idempotent): %v", round, err)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO chats (chat_id, user_id) VALUES (5, 7)"); err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema after a write failed: %v", err)
	}
	var kept int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chats WHERE chat_id = 5").Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("the repeated migration changed the rows: %d kept, §5 keeps 1", kept)
	}
	// The comment text of 001_init.sql contains ';' — a naive split would cut a comment in half.
	if _, err := Statements("-- a comment with ; inside\nCREATE TABLE IF NOT EXISTS t (a);"); err != nil {
		t.Fatalf("Statements of a comment with a ';' failed: %v", err)
	}
	if got, err := Statements("CREATE TABLE t (a TEXT DEFAULT ';');"); err != nil || len(got) != 1 || !strings.Contains(got[0], "';'") {
		t.Fatalf("Statements cut a ';' inside a string literal: %q, %v", got, err)
	}
	if _, err := Statements("CREATE TABLE t (a TEXT); -- trailing comment after the last ';'"); err != nil {
		t.Fatalf("Statements refused a trailing comment: %v", err)
	}
	if _, err := Statements("CREATE TABLE t (a TEXT DEFAULT 'unterminated);"); err == nil {
		t.Fatalf("Statements accepted an unterminated quoted token (§5: one malformed statement)")
	}
}

// TestOpenRestartKeepsWAL — §5 + §10 День 2: the journal_mode lives in the FILE, so the second start
// finds wal without being told twice; a clean db.Close checkpoints and leaves only bot.db.
func TestOpenRestartKeepsWAL(t *testing.T) {
	db, path := openTemp(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "INSERT INTO chats (chat_id, user_id) VALUES (42, 7)"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("the second start failed (%v), §5 applies the idempotent migration on every start", err)
	}
	mode, err := Pragma(ctx, again, JournalModePragma)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("after a restart journal_mode = %q, §5 stores wal in the file itself", mode)
	}
	var kept int
	if err := again.QueryRowContext(ctx, "SELECT COUNT(*) FROM chats WHERE chat_id = 42").Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("the restart kept %d rows, §5 keeps them all", kept)
	}
	// §5 table row: a correct Close runs the checkpoint, so no sidecar survives.
	again.Close()
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			t.Fatalf("%s survived db.Close (§5: the checkpoint runs on close)", sidecar)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the database file itself disappeared: %v", err)
	}
}

// TestMain — §5: sqlite.StrictPragmas is process-wide, off by default and NOT a DSN parameter (the
// driver copies it into every connection at Connect), so it is turned on before the first sql.Open.
// main.go does the same through store.EnableStrictPragmas; the test binary mirrors that wiring.
func TestMain(m *testing.M) {
	EnableStrictPragmas()
	os.Exit(m.Run())
}

// TestOpenRejectsBadDSN — §11 DoD: a bad DSN falls on the first Ping, not on the first query.
func TestOpenRejectsBadDSN(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// A file:-path is refused by the whitelist before sql.Open (§5 rule (c)).
	if _, err := Open(ctx, "file:/data/bot.db?_journal_mode=666"); err == nil {
		t.Fatalf("Open accepted a file: DSN the operator owns (§5 rule (c))")
	}

	// §5 spike 11: sql.Open itself stays silent about an invalid value, so the Ping is the first
	// place the problem can surface — and Open quotes the DSN in its error.
	dsn := "file:" + EscapePath(dir+"/bot.db") + "?_journal_mode=666"
	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatalf("sql.Open reported the invalid value itself (§5 spike 11 expects silence here): %v", err)
	}
	pingErr := db.PingContext(ctx)
	db.Close()
	if pingErr == nil {
		t.Fatalf("the Ping accepted _journal_mode=666, §5 spike 11 rejects it in connect")
	}
	if _, err := Open(ctx, dir+"/bot.db?_journal_mode=666"); err == nil {
		t.Fatalf("Open accepted a path carrying a query (§5 rule (b): ? hijacks the query part)")
	}

	// §5 spike 5: SQLite does not create the location of the database.
	if bad := unwritablePath(t); bad != "" {
		if _, err := Open(ctx, bad); err == nil {
			t.Fatalf("Open of %q succeeded, §5 expects an error that quotes the DSN", bad)
		}
	}
}

// TestOpenRejectsPragmaTypo — §11 DoD «typo in a pragma»: an unknown name is not an error, it is
// silence (§5 table row `_bogus_key`), so the whitelist refuses it before the driver.
func TestOpenRejectsPragmaTypo(t *testing.T) {
	ctx := context.Background()
	db, _ := openTemp(t)
	defer db.Close()

	for _, name := range []string{"journal_mod", "foriegn_keys", "sync", "_journal_size_limit", "_pragma", "dqs", "txlock", "pragma", "", "ATTACH 'x.db' AS x"} {
		if _, err := Pragma(ctx, db, name); err == nil {
			t.Fatalf("Pragma(%q) reported nothing; §5 keeps this name outside the whitelist, so a typo would read as an empty value", name)
		}
	}
	// The DSN spelling of a pinned name is accepted (§5 spike 11: the key carries the '_' prefix).
	if mode, err := Pragma(ctx, db, "_journal_mode"); err != nil || !strings.EqualFold(mode, "wal") {
		t.Fatalf("Pragma(\"_journal_mode\") = %q, %v (§5 accepts the DSN spelling)", mode, err)
	}
}

// TestOpenMemoryPool — §5 + §11: `file:mem…?mode=memory&cache=shared` keeps the schema visible to
// every pooled connection; without cache=shared the pool answers `no such table` (§5 spike 9).
func TestOpenMemoryPool(t *testing.T) {
	db, err := OpenMemory(context.Background(), "pool")
	if err != nil {
		t.Fatalf("OpenMemory of the §11 pool failed: %v", err)
	}
	defer db.Close()

	mode, err := Pragma(context.Background(), db, JournalModePragma)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "memory" {
		t.Fatalf("the memory pool reports journal_mode=%q (§5: WAL is unavailable in memory)", mode)
	}
	if _, err := db.Exec("INSERT INTO chats (chat_id, user_id) VALUES (1, 7)"); err != nil {
		t.Fatal(err)
	}
	// §11 MaxOpenConns(4): every pooled connection sees the same shared cache.
	for i := 0; i < 4; i++ {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM chats").Scan(&count); err != nil {
			t.Fatalf("connection %d of the pool lost the schema (§5 spike 9): %v", i, err)
		}
		if count != 1 {
			t.Fatalf("connection %d of the pool sees %d rows, §5 keeps one shared cache", i, count)
		}
	}
	// §5: cache=shared makes the name the identity of the database — the same name is the same
	// database, which is why a test name has to be unique per test.
	other, err := OpenMemory(context.Background(), "pool")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var shared int
	if err := other.QueryRow("SELECT COUNT(*) FROM chats").Scan(&shared); err != nil {
		t.Fatal(err)
	}
	if shared != 1 {
		t.Fatalf("the same memory name produced a separate database (§5 cache=shared): %d rows", shared)
	}
}

// TestStrictPragmasRejectsMultiStatement — §5 case B + §13 «_pragma through %3B»: the second line of
// defence rejects a `_pragma` value that is more than one statement, in the validation phase, so
// nothing is created on disk.
func TestStrictPragmasRejectsMultiStatement(t *testing.T) {
	if !StrictPragmasEnabled() {
		t.Fatalf("the tests ran without EnableStrictPragmas (§5: main sets it before the first sql.Open)")
	}
	dir := t.TempDir()
	dsn := "file:" + EscapePath(dir+"/bot.db") + "?_pragma=foreign_keys(1)%3BATTACH 'x.db' AS x"
	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatalf("sql.Open is expected to stay silent here (§5 spike 11): %v", err)
	}
	err = db.Ping()
	db.Close()
	if err == nil {
		t.Fatalf("the Ping accepted a multi-statement _pragma (§5 case B: ATTACH would create x.db)")
	}
	if !strings.Contains(err.Error(), "more than one SQL statement") {
		t.Fatalf("the Ping rejected the payload with an unexpected error: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// sqlite3_open creates the file before the validation phase rejects the DSN, so the database
	// itself exists; what must NOT exist is the file ATTACH would have created (§5 case B).
	if _, err := os.Stat(dir + "/bot.db"); err != nil {
		t.Fatalf("the database file was not even created: %v", err)
	}
	if _, err := os.Stat(dir + "/x.db"); err == nil {
		t.Fatalf("x.db exists — the ATTACH of the payload ran (§5 case B)")
	}
	if len(entries) != 1 {
		t.Fatalf("the rejected payload left %d files: %v (§5: the rejection runs in validation, before any parameter)", len(entries), entries)
	}
}

func openTemp(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/bot.db"
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open of %q failed: %v", path, err)
	}
	return db, path
}

// unwritablePath — a path whose parent cannot exist (§5 spike 5: SQLite does not create the location
// and answers SQLITE_CANTOPEN). A regular file used as a directory is that case on every OS.
func unwritablePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	blocker := dir + "/blocker.txt"
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Skip("cannot create the blocker file on this host")
	}
	return blocker + "/bot.db"
}
