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
		if err != nil || version != 1 {
			t.Fatalf("SchemaVersion=%d err=%v, want 1", version, err)
		}
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("schema_migrations rows=%d err=%v, want exactly one", rows, err)
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
	if err != nil || version != 1 {
		t.Fatalf("adopted SchemaVersion=%d err=%v, want 1", version, err)
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
		t.Fatal("the invalid second migration must fail")
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != 1 {
		t.Fatalf("failed migration advanced SchemaVersion to %d, err=%v", version, err)
	}
	var sentinel int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'migration_sentinel'`).Scan(&sentinel); err != nil || sentinel != 0 {
		t.Fatalf("failed migration left its table behind, present=%d err=%v", sentinel, err)
	}
}
