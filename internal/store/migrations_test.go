package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSchemaVersionIsRecordedOnce(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for round := 0; round < 3; round++ {
		if err := ApplySchema(ctx, db); err != nil {
			t.Fatalf("ApplySchema round %d: %v", round, err)
		}
		version, err := SchemaVersion(ctx, db)
		if err != nil || version != len(MigrationSQL) {
			t.Fatalf("SchemaVersion=%d err=%v, want %d", len(MigrationSQL), version, err)
		}
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&rows); err != nil || rows != len(MigrationSQL) {
		t.Fatalf("schema_migrations rows=%d err=%v, want exactly %d", rows, len(MigrationSQL), err)
	}
}

func TestSchemaVersionAdoptsAPreVersioningDatabaseWithoutLosingRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	dsn, err := BuildDSN(path)
	if err != nil {
		t.Fatalf("BuildDSN: %v", err)
	}
	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// A pre-versioning database is one this project's own first schema wrote: `chats` already carries the
	// columns later migrations index and read, and only `schema_migrations` is missing.
	if _, err := db.ExecContext(ctx, `CREATE TABLE chats (
		chat_id        INTEGER PRIMARY KEY,
		user_id        INTEGER NOT NULL,
		title          TEXT NOT NULL DEFAULT '',
		created_at     TEXT NOT NULL DEFAULT (datetime('now')),
		last_active_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO chats (chat_id, user_id) VALUES (42, 7)`); err != nil {
		t.Fatalf("seed old row: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != 0 {
		t.Fatalf("pre-versioning SchemaVersion=%d err=%v, want 0", version, err)
	}
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("adopt old database: %v", err)
	}
	version, err = SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) {
		t.Fatalf("adopted SchemaVersion=%d err=%v, want %d", len(MigrationSQL), version, err)
	}
	var kept int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chats WHERE chat_id = 42 AND user_id = 7`).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("old row was not preserved, count=%d err=%v", kept, err)
	}
}

func TestMigrationFailureRollsBackStatementsAndVersion(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	original := MigrationSQL
	MigrationSQL = append(append([]string{}, original...), `CREATE TABLE migration_sentinel (id INTEGER); SELECT invalid_migration_statement;`)
	defer func() { MigrationSQL = original }()
	if err := ApplySchema(ctx, db); err == nil {
		t.Fatal("the invalid migration must fail")
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(original) {
		t.Fatalf("failed migration advanced SchemaVersion to %d, err=%v", version, err)
	}
	var sentinel int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'migration_sentinel'`).Scan(&sentinel); err != nil || sentinel != 0 {
		t.Fatalf("failed migration left its table behind, present=%d err=%v", sentinel, err)
	}
}

// TestMigration011ToleratesAnExistingAttributionColumn reproduces a database whose messages already have
// user_id but whose 011 row is missing (an interrupted older release, or an operator's repair). The guarded
// runner must skip only the ALTER and still create the index instead of failing with "duplicate column".
func TestMigration011ToleratesAnExistingAttributionColumn(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "migration-011-guard")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if err := EnsureChat(ctx, db, 70, 7, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages (chat_id, user_id, role, content) VALUES (70, 7, 'user', 'kept')`); err != nil {
		t.Fatalf("seed a message: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 11`); err != nil {
		t.Fatalf("forget version 11: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_messages_user_role_created_at`); err != nil {
		t.Fatalf("drop the 011 index: %v", err)
	}
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema over an existing user_id column: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) {
		t.Fatalf("SchemaVersion=%d err=%v, want %d", version, err, len(MigrationSQL))
	}
	var kept int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE chat_id = 70 AND user_id = 7`).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("the guarded migration changed the existing row: count=%d err=%v", kept, err)
	}
	var index int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_messages_user_role_created_at'`).Scan(&index); err != nil || index != 1 {
		t.Fatalf("the index statement of 011 did not run: count=%d err=%v", index, err)
	}
}

// TestConcurrentOpenAppliesPendingMigrationsOnOneFile is the race of two processes (the bot and ragembed/
// dbprune) opening the same file while migrations 011..018 are still pending. Both Opens must succeed; the
// historical non-idempotent ALTER of 011 is the statement that used to fail with "duplicate column".
func TestConcurrentOpenAppliesPendingMigrationsOnOneFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent-open.db")
	all := MigrationSQL
	MigrationSQL = append([]string(nil), all[:10]...)
	seed, err := Open(ctx, path)
	if err != nil {
		MigrationSQL = all
		t.Fatalf("seed Open at version 10: %v", err)
	}
	if err := seed.Close(); err != nil {
		MigrationSQL = all
		t.Fatalf("close the seed: %v", err)
	}
	MigrationSQL = all
	t.Cleanup(func() { MigrationSQL = all })

	const workers = 4
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			db, err := Open(ctx, path)
			if err == nil {
				err = db.Close()
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent Open failed: %v", err)
		}
	}
	final, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("final Open: %v", err)
	}
	defer final.Close()
	version, err := SchemaVersion(ctx, final)
	if err != nil || version != len(MigrationSQL) {
		t.Fatalf("final SchemaVersion=%d err=%v, want %d", version, err, len(MigrationSQL))
	}
	var rows int
	if err := final.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&rows); err != nil || rows != len(MigrationSQL) {
		t.Fatalf("schema_migrations rows=%d err=%v, want %d", rows, err, len(MigrationSQL))
	}
}

func TestEventIndexesUpgradePreservesVersion5Data(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 5)

	if _, err := db.ExecContext(ctx, `INSERT INTO events (id, user_id, kind, detail, created_at)
		VALUES (23, 7, 'usage', 'rag', '2026-01-02 03:04:05')`); err != nil {
		t.Fatalf("seed version 5 event: %v", err)
	}

	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("upgrade version 5 schema: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) {
		t.Fatalf("upgraded SchemaVersion=%d err=%v, want %d", version, err, len(MigrationSQL))
	}
	var event struct {
		id        int64
		userID    int64
		kind      string
		detail    string
		createdAt string
	}
	if err := db.QueryRowContext(ctx, `SELECT id, user_id, kind, detail, created_at FROM events`).Scan(
		&event.id, &event.userID, &event.kind, &event.detail, &event.createdAt,
	); err != nil {
		t.Fatalf("read upgraded event: %v", err)
	}
	if event.id != 23 || event.userID != 7 || event.kind != "usage" || event.detail != "rag" || event.createdAt != "2026-01-02 03:04:05" {
		t.Fatalf("upgraded event changed: %+v", event)
	}

	for _, index := range []struct {
		name    string
		columns []string
	}{
		{"idx_events_user_kind", []string{"user_id", "kind"}},
		{"idx_events_kind_detail", []string{"kind", "detail"}},
		{"idx_events_created_at", []string{"created_at"}},
	} {
		rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index.name)
		if err != nil {
			t.Fatalf("inspect %s: %v", index.name, err)
		}
		var columns []string
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				rows.Close()
				t.Fatalf("scan %s column: %v", index.name, err)
			}
			columns = append(columns, column)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate %s columns: %v", index.name, err)
		}
		rows.Close()
		if len(columns) != len(index.columns) {
			t.Fatalf("%s columns=%v, want %v", index.name, columns, index.columns)
		}
		for i := range columns {
			if columns[i] != index.columns[i] {
				t.Fatalf("%s columns=%v, want %v", index.name, columns, index.columns)
			}
		}
	}
}

func TestAccountingMigrationsPreserveHistoryAndAddDailyIndexes(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 9)

	if err := EnsureChat(ctx, db, -100, 11, "group"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages
		(chat_id, role, content, tokens_in, tokens_out, model, created_at)
		VALUES (-100, 'assistant', 'historical unknown owner', 1000, 2000, 'old-model', '2026-03-02 12:00:00')`); err != nil {
		t.Fatalf("seed historical assistant: %v", err)
	}

	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("apply accounting migrations: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) {
		t.Fatalf("SchemaVersion=%d err=%v, want %d", version, err, len(MigrationSQL))
	}
	var historicalUser int64
	if err := db.QueryRowContext(ctx, `SELECT user_id FROM messages WHERE content = 'historical unknown owner'`).Scan(&historicalUser); err != nil || historicalUser != 0 {
		t.Fatalf("historical user_id=%d err=%v, want unknown", historicalUser, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO daily_quota_reservations (user_id, day, class, used) VALUES (11, '2026-03-02', 'model', 0)`); err == nil {
		t.Fatal("quota reservation accepted a non-positive counter")
	}
	for _, index := range []string{
		"idx_daily_quota_day_class",
		"idx_events_user_kind_created_at",
		"idx_events_user_kind_detail_created_at",
		"idx_messages_user_role_created_at",
	} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s count=%d err=%v", index, count, err)
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_index_info('idx_messages_user_role_created_at') ORDER BY seqno`)
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		columns = append(columns, column)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(columns) != 3 || columns[0] != "user_id" || columns[1] != "role" || columns[2] != "created_at" {
		t.Fatalf("message usage index columns=%v", columns)
	}

	if err := EnsureChat(ctx, db, -100, 99, "group"); err != nil {
		t.Fatal(err)
	}
	usage, err := UserDailyUsage(ctx, db, 99, time.Date(2026, 3, 2, 18, 0, 0, 0, time.UTC))
	if err != nil || len(usage.Models) != 0 {
		t.Fatalf("historical unknown tokens attributed to latest chat owner: usage=%+v err=%v", usage, err)
	}
	id, err := Append(ctx, db, Message{ChatID: -100, UserID: 99, Role: RoleAssistant, Content: "new attributed answer", Model: "new-model", TokensIn: int64Pointer(3), TokensOut: int64Pointer(4)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE messages SET created_at = '2026-03-02 12:30:00' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	usage, err = UserDailyUsage(ctx, db, 99, time.Date(2026, 3, 2, 18, 0, 0, 0, time.UTC))
	if err != nil || len(usage.Models) != 1 || usage.Models[0].TokensIn != 3 || usage.Models[0].TokensOut != 4 {
		t.Fatalf("new immutable attribution usage=%+v err=%v", usage, err)
	}
}
