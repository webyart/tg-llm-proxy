package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
)

// the first run. Every pragma of the reference DSN is read back, including the two pragms that only
func TestOpenFileWAL(t *testing.T) {
	db, path := openTemp(t)
	defer db.Close()

	ctx := context.Background()
	want := map[string]string{
		JournalModePragma: "wal",
		ForeignKeysPragma: RuntimeForeignKeys,
		BusyTimeoutPragma: RuntimeBusyTimeout,
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
			t.Fatalf("PRAGMA %s = %q, the reference DSN of promises %q", name, got, expected)
		}
	}

	if _, err := db.ExecContext(ctx, "INSERT INTO chats (chat_id, user_id) VALUES (1, 7)"); err != nil {
		t.Fatalf("the first write failed: %v", err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err != nil {
			t.Fatalf("%s is absent, WAL needs it while the database is open: %v", sidecar, err)
		}
	}
}

func TestOpenAppliesSchema(t *testing.T) {
	db, _ := openTemp(t)
	defer db.Close()
	ctx := context.Background()

	for _, table := range []string{"chats", "messages", "bot_state", "events"} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("the table %s of is missing after Open: %v", table, err)
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
	if _, err := db.ExecContext(ctx, "INSERT INTO messages (chat_id, telegram_message_id, role, content) VALUES (1, 7, 'assistant', 'again')"); err == nil {
		t.Fatalf("uq_messages_chat_tg_id of accepted a second row with telegram_message_id=7")
	}
	// The answer of the model carries no telegram_message_id, so the partial index leaves it alone.
	if _, err := db.ExecContext(ctx, "INSERT INTO messages (chat_id, role, content, model) VALUES (1, 'assistant', 'reply', 'qwen')"); err != nil {
		t.Fatalf("the answer row was refused, stores answers without telegram_message_id: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO messages (chat_id, role, content) VALUES (999, 'user', 'x')"); err == nil {
		t.Fatalf("the REFERENCES of are not enforced: a message for an absent chat was accepted")
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO messages (chat_id, role, content) VALUES (1, 'robot', 'x')"); err == nil {
		t.Fatalf("the CHECK (role IN ('user','assistant','system')) of accepted role=robot")
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO bot_state (key, value) VALUES ('last_update_id', '42')"); err != nil {
		t.Fatalf("bot_state refused the offset row : %v", err)
	}
}

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
		t.Fatalf("001_init.sql split into %d statements, ships 4 tables and 2 indexes: %v", len(statements), statements)
	}
	for _, statement := range statements {
		// semicolon inside one query), so the check runs on the code of the statement.
		if strings.Contains(codeText(statement), ";") {
			t.Fatalf("statement %q carries a ';' in its code — the driver refuses it ", firstLine(statement))
		}
	}
	for round := 1; round <= 3; round++ {
		if err := ApplySchema(ctx, db); err != nil {
			t.Fatalf("ApplySchema round %d failed : %v", round, err)
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
		t.Fatalf("the repeated migration changed the rows: %d kept, keeps 1", kept)
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
		t.Fatalf("Statements accepted an unterminated quoted token ")
	}
}

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
		t.Fatalf("the second start failed (%v), applies the idempotent migration on every start", err)
	}
	mode, err := Pragma(ctx, again, JournalModePragma)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("after a restart journal_mode = %q, stores wal in the file itself", mode)
	}
	var kept int
	if err := again.QueryRowContext(ctx, "SELECT COUNT(*) FROM chats WHERE chat_id = 42").Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("the restart kept %d rows, keeps them all", kept)
	}
	again.Close()
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			t.Fatalf("%s survived db.Close ", sidecar)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the database file itself disappeared: %v", err)
	}
}

// driver copies it into every connection at Connect), so it is turned on before the first sql.Open.
// main.go does the same through store.EnableStrictPragmas; the test binary mirrors that wiring.
func TestMain(m *testing.M) {
	EnableStrictPragmas()
	os.Exit(m.Run())
}

func TestOpenRejectsBadDSN(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	if _, err := Open(ctx, "file:/data/bot.db?_journal_mode=666"); err == nil {
		t.Fatalf("Open accepted a file: DSN the operator owns ( rule (c))")
	}

	// place the problem can surface — and Open quotes the DSN in its error.
	dsn := "file:" + EscapePath(dir+"/bot.db") + "?_journal_mode=666"
	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatalf("sql.Open reported the invalid value itself : %v", err)
	}
	pingErr := db.PingContext(ctx)
	db.Close()
	if pingErr == nil {
		t.Fatalf("the Ping accepted _journal_mode=666, spike 11 rejects it in connect")
	}
	if _, err := Open(ctx, dir+"/bot.db?_journal_mode=666"); err == nil {
		t.Fatalf("Open accepted a path carrying a query ( rule (b): ? hijacks the query part)")
	}

	if bad := unwritablePath(t); bad != "" {
		if _, err := Open(ctx, bad); err == nil {
			t.Fatalf("Open of %q succeeded, expects an error that quotes the DSN", bad)
		}
	}
}

func TestOpenRejectsPragmaTypo(t *testing.T) {
	ctx := context.Background()
	db, _ := openTemp(t)
	defer db.Close()

	for _, name := range []string{"journal_mod", "foriegn_keys", "sync", "_journal_size_limit", "_pragma", "dqs", "txlock", "pragma", "", "ATTACH 'x.db' AS x"} {
		if _, err := Pragma(ctx, db, name); err == nil {
			t.Fatalf("Pragma(%q) reported nothing; keeps this name outside the whitelist, so a typo would read as an empty value", name)
		}
	}
	if mode, err := Pragma(ctx, db, "_journal_mode"); err != nil || !strings.EqualFold(mode, "wal") {
		t.Fatalf("Pragma(\"_journal_mode\") = %q, %v ", mode, err)
	}
}

func TestOpenMemoryPool(t *testing.T) {
	db, err := OpenMemory(context.Background(), "pool")
	if err != nil {
		t.Fatalf("OpenMemory of the pool failed: %v", err)
	}
	defer db.Close()

	mode, err := Pragma(context.Background(), db, JournalModePragma)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "memory" {
		t.Fatalf("the memory pool reports journal_mode=%q ", mode)
	}
	if _, err := db.Exec("INSERT INTO chats (chat_id, user_id) VALUES (1, 7)"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM chats").Scan(&count); err != nil {
			t.Fatalf("connection %d of the pool lost the schema : %v", i, err)
		}
		if count != 1 {
			t.Fatalf("connection %d of the pool sees %d rows, keeps one shared cache", i, count)
		}
	}
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
		t.Fatalf("the same memory name produced a separate database : %d rows", shared)
	}
}

// defence rejects a `_pragma` value that is more than one statement, in the validation phase, so
// nothing is created on disk.
func TestStrictPragmasRejectsMultiStatement(t *testing.T) {
	if !StrictPragmasEnabled() {
		t.Fatalf("the tests ran without EnableStrictPragmas ")
	}
	dir := t.TempDir()
	dsn := "file:" + EscapePath(dir+"/bot.db") + "?_pragma=foreign_keys(1)%3BATTACH 'x.db' AS x"
	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatalf("sql.Open is expected to stay silent here : %v", err)
	}
	err = db.Ping()
	db.Close()
	if err == nil {
		t.Fatalf("the Ping accepted a multi-statement _pragma ")
	}
	if !strings.Contains(err.Error(), "more than one SQL statement") {
		t.Fatalf("the Ping rejected the payload with an unexpected error: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// sqlite3_open creates the file before the validation phase rejects the DSN, so the database
	if _, err := os.Stat(dir + "/bot.db"); err != nil {
		t.Fatalf("the database file was not even created: %v", err)
	}
	if _, err := os.Stat(dir + "/x.db"); err == nil {
		t.Fatalf("x.db exists — the ATTACH of the payload ran ")
	}
	if len(entries) != 1 {
		t.Fatalf("the rejected payload left %d files: %v ", len(entries), entries)
	}
}

// db.Ping() and the file stays journal_mode=delete with no -wal/-shm. The rejection runs in the
func TestOpenRejectsBadDSNLeavesPlainFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/bot.db"
	dsn := "file:" + EscapePath(path) + "?_journal_mode=666"
	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatalf("sql.Open is expected to stay silent : %v", err)
	}
	if err := db.Ping(); err == nil {
		db.Close()
		t.Fatalf("the Ping accepted _journal_mode=666")
	}
	db.Close()

	if _, err := os.Stat(path + "-wal"); err == nil {
		t.Fatalf("-wal exists after a rejected DSN ")
	}
	if _, err := os.Stat(path + "-shm"); err == nil {
		t.Fatalf("-shm exists after a rejected DSN ")
	}
	plain, err := sql.Open(sqliteDriver, "file:"+EscapePath(path))
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	mode, err := Pragma(context.Background(), plain, JournalModePragma)
	if err != nil {
		t.Fatalf("the file of the rejected DSN cannot be read: %v", err)
	}
	if mode != "delete" {
		t.Fatalf("journal_mode of the rejected file = %q, DoD: the file stays delete", mode)
	}
}

func TestMemoryStoreSharedAcrossPool(t *testing.T) {
	db, err := OpenMemory(context.Background(), "sharedpool")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO chats (chat_id, user_id) VALUES (11, 7)"); err != nil {
		t.Fatal(err)
	}
	rows1, err := db.Query("SELECT chat_id FROM chats")
	if err != nil {
		t.Fatal(err)
	}
	defer rows1.Close()
	rows2, err := db.Query("SELECT chat_id FROM chats")
	if err != nil {
		t.Fatalf("the second pooled connection lost the table : %v", err)
	}
	defer rows2.Close()
	for _, pair := range []struct {
		rows *sql.Rows
		name string
	}{{rows1, "connection A"}, {rows2, "connection B"}} {
		var seen []int
		for pair.rows.Next() {
			var id int
			if err := pair.rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			seen = append(seen, id)
		}
		if err := pair.rows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != 11 {
			t.Fatalf("%s sees %v, keeps one shared cache of the same rows", pair.name, seen)
		}
	}
	if open := db.Stats().OpenConnections; open < 2 {
		t.Fatalf("the pool kept %d open connections, MaxOpenConns(4) allows two at once", open)
	}
}

// schema across the pool.
func TestMemoryDSNNeedsCacheShared(t *testing.T) {
	db, err := sql.Open(sqliteDriver, "file:memshared?mode=memory&cache=shared&_foreign_keys=1&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(MaxOpenConns)
	db.SetMaxIdleConns(MaxIdleConns)
	if err := ApplySchema(context.Background(), db); err != nil {
		t.Fatalf("the schema of did not reach the shared memory pool: %v", err)
	}
	if _, err := db.Exec("INSERT INTO chats (chat_id, user_id) VALUES (3, 7)"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM chats").Scan(&count); err != nil {
		t.Fatalf("the shared cache pool lost the table : %v", err)
	}
	if count != 1 {
		t.Fatalf("the shared cache sees %d rows ", count)
	}
}

func TestPrivateMemoryNotSharedAcrossPool(t *testing.T) {
	db, err := sql.Open(sqliteDriver, "file:membroken?mode=memory&_foreign_keys=1&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(MaxOpenConns)
	db.SetMaxIdleConns(MaxIdleConns)
	if err := ApplySchema(context.Background(), db); err != nil {
		t.Fatalf("the first connection created the schema: %v", err)
	}
	rows1, err := db.Query("SELECT COUNT(*) FROM chats") // holds connection A open
	if err != nil {
		t.Fatalf("the connection that created the schema lost it: %v", err)
	}
	defer rows1.Close()
	_, err = db.Query("SELECT COUNT(*) FROM chats")
	if err == nil {
		t.Fatalf("a memory database without cache=shared was visible on the second connection ")
	}
	if !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("the second connection failed with an unexpected error: %v", err)
	}
}

// repeated rounds; with idle=0 the pool closes the connection on returning it and the shared-cache
func TestMemoryPoolKeepsIdleConn(t *testing.T) {
	ctx := context.Background()
	dsn, err := MemoryDSN("idle")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(MaxOpenConns)
	db.SetMaxIdleConns(MaxIdleConns)
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO chats (chat_id, user_id) VALUES (9, 7)"); err != nil {
		t.Fatal(err)
	}
	// MaxIdleConns field on this toolchain — MaxIdleClosed is the counter that proves it).
	stats := db.Stats()
	if stats.MaxIdleClosed != 0 {
		t.Fatalf("MaxIdleClosed = %d, pins MaxIdleConns(2): the pool must not close idle connections", stats.MaxIdleClosed)
	}
	if stats.MaxOpenConnections != MaxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, pins %d", stats.MaxOpenConnections, MaxOpenConns)
	}
	for round := 0; round < 5; round++ {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chats").Scan(&count); err != nil {
			t.Fatalf("the pooled database vanished after round %d : %v", round, err)
		}
		if count != 1 {
			t.Fatalf("round %d sees %d rows, keeps 1", round, count)
		}
	}

	brokenDSN, err := MemoryDSN("idlezero")
	if err != nil {
		t.Fatal(err)
	}
	broken, err := sql.Open(sqliteDriver, brokenDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Close()
	broken.SetMaxOpenConns(1)
	broken.SetMaxIdleConns(0)
	err = ApplySchema(ctx, broken)
	if err == nil {
		t.Fatalf("idle=0 applied the schema of cleanly, spike 9 says the pool drops the memory database")
	}
	if !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("idle=0 failed with an unexpected error: %v", err)
	}
	if broken.Stats().MaxIdleClosed == 0 {
		t.Fatalf("MaxIdleClosed = 0 with idle=0,: the pool closes the returned connection")
	}
}

func TestMemoryStoreNoWAL(t *testing.T) {
	db, err := OpenMemory(context.Background(), "nowal")
	if err != nil {
		t.Fatalf("OpenMemory must not fall on the WAL request of: %v", err)
	}
	defer db.Close()
	mode, err := Pragma(context.Background(), db, JournalModePragma)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "memory" {
		t.Fatalf("journal_mode = %q in memory, reports memory", mode)
	}
	if _, err := Pragma(context.Background(), db, BusyTimeoutPragma); err != nil {
		t.Fatalf("busy_timeout stays readable in memory : %v", err)
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
