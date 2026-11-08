package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestHotPathIndexesExist pins migration 012: without them every prune scanned the whole table and every
// chat listing sorted it, which is invisible on a 0.8 MB database and obvious on a GB one.
func TestHotPathIndexesExist(t *testing.T) {
	var ctx = context.Background()
	var db, err = OpenMemory(ctx, "hot-path-indexes")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	for _, name := range []string{"idx_messages_created_at", "idx_chats_last_active"} {
		var found int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&found); err != nil {
			t.Fatalf("read sqlite_master: %v", err)
		}
		if found != 1 {
			t.Fatalf("index %s is missing: the hot path of prune and of the chat listing has no index", name)
		}
	}
}

// TestSchemaVersionTracksTheNewMigration keeps the version constant honest: the reported version is the
// number of embedded migrations, and the newest one is the index migration.
func TestSchemaVersionTracksTheNewMigration(t *testing.T) {
	var ctx = context.Background()
	var db, err = OpenMemory(ctx, "schema-version-012")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	version, versionErr := SchemaVersion(ctx, db)
	if versionErr != nil {
		t.Fatalf("SchemaVersion: %v", versionErr)
	}
	if version != SupportedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", version, SupportedSchemaVersion())
	}
}

// TestCheckpointWALTruncatesTheLog: after a prune the rows are gone but the pages are not, unless the log
// is checkpointed and truncated.
func TestCheckpointWALTruncatesTheLog(t *testing.T) {
	var ctx = context.Background()
	var path = filepath.Join(t.TempDir(), "checkpoint.db")
	var db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for i := 0; i < 32; i++ {
		if _, appendErr := Append(ctx, db, Message{ChatID: 1, UserID: 1, Role: RoleUser, Content: "строка для журнала"}); appendErr != nil {
			t.Fatalf("Append: %v", appendErr)
		}
	}
	if _, statErr := os.Stat(WALSideFile(path)); statErr != nil {
		t.Fatalf("no -wal file before the checkpoint: %v", statErr)
	}

	busy, checkpointErr := CheckpointWAL(ctx, db)
	if checkpointErr != nil {
		t.Fatalf("CheckpointWAL: %v", checkpointErr)
	}
	if busy != 0 {
		t.Fatalf("wal_checkpoint busy = %d, want 0: this test holds the only connection", busy)
	}

	info, statErr := os.Stat(WALSideFile(path))
	if statErr != nil {
		return // SQLite removed the -wal entirely, which is the fully truncated state
	}
	if info.Size() != 0 {
		t.Fatalf("-wal is %d bytes after wal_checkpoint(TRUNCATE), want 0", info.Size())
	}
}
