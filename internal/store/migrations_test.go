package store

import (
	"context"
	"database/sql"
	"path/filepath"
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
	if _, err := db.ExecContext(ctx, `CREATE TABLE chats (chat_id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL)`); err != nil {
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

func TestEventIndexesUpgradePreservesVersion5Data(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	migrations := MigrationSQL
	MigrationSQL = append([]string(nil), migrations[:5]...)
	defer func() { MigrationSQL = migrations }()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("apply version 5 schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO events (id, user_id, kind, detail, created_at)
		VALUES (23, 7, 'usage', 'rag', '2026-01-02 03:04:05')`); err != nil {
		t.Fatalf("seed version 5 event: %v", err)
	}

	MigrationSQL = migrations
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("upgrade version 5 schema: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(migrations) {
		t.Fatalf("upgraded SchemaVersion=%d err=%v, want %d", version, err, len(migrations))
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
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	migrations := MigrationSQL
	MigrationSQL = append([]string(nil), migrations[:9]...)
	defer func() { MigrationSQL = migrations }()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("apply version 9: %v", err)
	}
	if err := EnsureChat(ctx, db, -100, 11, "group"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages
		(chat_id, role, content, tokens_in, tokens_out, model, created_at)
		VALUES (-100, 'assistant', 'historical unknown owner', 1000, 2000, 'old-model', '2026-03-02 12:00:00')`); err != nil {
		t.Fatalf("seed historical assistant: %v", err)
	}

	MigrationSQL = migrations
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("apply accounting migrations: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != 11 {
		t.Fatalf("SchemaVersion=%d err=%v, want 11", version, err)
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
