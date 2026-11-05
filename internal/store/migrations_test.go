package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
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
