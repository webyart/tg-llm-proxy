package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestGuardSchemaVersionRefusesAFutureDatabase(t *testing.T) {
	var ctx = context.Background()
	var db, err = OpenMemory(ctx, "guard-future")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	if err := GuardSchemaVersion(ctx, db); err != nil {
		t.Fatalf("a freshly migrated database must pass the guard: %v", err)
	}

	var version, versionErr = SchemaVersion(ctx, db)
	if versionErr != nil {
		t.Fatalf("SchemaVersion: %v", versionErr)
	}
	if version != SupportedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", version, SupportedSchemaVersion())
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version+1); err != nil {
		t.Fatalf("insert future version: %v", err)
	}

	if err := GuardSchemaVersion(ctx, db); !errors.Is(err, ErrSchemaDowngrade) {
		t.Fatalf("GuardSchemaVersion on a future database = %v, want ErrSchemaDowngrade", err)
	}
}

// A pre-versioning file (no schema_migrations rows) is what an old backup looks like: version 0 must be
// accepted, because the next writable open adopts it at version 1.
func TestGuardSchemaVersionAcceptsAPreVersioningDatabase(t *testing.T) {
	var ctx = context.Background()
	var db, err = OpenMemory(ctx, "guard-pre-versioning")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	if _, execErr := db.ExecContext(ctx, `DELETE FROM schema_migrations`); execErr != nil {
		t.Fatalf("clear schema_migrations: %v", execErr)
	}
	if err := GuardSchemaVersion(ctx, db); err != nil {
		t.Fatalf("version 0 must pass the guard: %v", err)
	}
}

// The end-to-end path an operator hits on a rollback: the second Open of a database migrated by a newer
// binary must fail before it applies anything, so no handler ever runs against an unknown schema.
func TestOpenRefusesADatabaseFromAFutureRelease(t *testing.T) {
	var ctx = context.Background()
	var path = filepath.Join(t.TempDir(), "future.db")

	var writer, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var version, versionErr = SchemaVersion(ctx, writer)
	if versionErr != nil {
		t.Fatalf("SchemaVersion: %v", versionErr)
	}
	if _, execErr := writer.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version+1); execErr != nil {
		t.Fatalf("insert future version: %v", execErr)
	}
	writer.Close()

	var downgraded, reopenErr = Open(ctx, path)
	if reopenErr == nil {
		if downgraded != nil {
			downgraded.Close()
		}
		t.Fatal("Open accepted a database written by a newer release")
	}
	if !errors.Is(reopenErr, ErrSchemaDowngrade) {
		t.Fatalf("Open error = %v, want ErrSchemaDowngrade", reopenErr)
	}
}

// VerifyBackup is the gate of dbrestore and of every -Apply path: a future backup must be refused by
// version, not discovered after the restore.
func TestVerifyBackupRefusesAFutureSchema(t *testing.T) {
	var ctx = context.Background()
	var path = filepath.Join(t.TempDir(), "backup-future.db")

	var writer, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var version, versionErr = SchemaVersion(ctx, writer)
	if versionErr != nil {
		t.Fatalf("SchemaVersion: %v", versionErr)
	}
	if _, execErr := writer.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version+1); execErr != nil {
		t.Fatalf("insert future version: %v", execErr)
	}
	writer.Close()

	if err := VerifyBackup(ctx, path); !errors.Is(err, ErrSchemaDowngrade) {
		t.Fatalf("VerifyBackup on a future backup = %v, want ErrSchemaDowngrade", err)
	}
}
