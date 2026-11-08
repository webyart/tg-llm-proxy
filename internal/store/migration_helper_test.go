package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// openMemoryAtVersion — a database migrated only up to `version`: the state an older release left behind.
//
// ApplySchema reads the global MigrationSQL at call time, so the slice is truncated BEFORE the database is
// opened and restored before the test applies the remaining migrations. Truncating after the open would be
// a downgrade of an already-migrated database, which is exactly what GuardSchemaVersion refuses.
func openMemoryAtVersion(t *testing.T, version int) (*sql.DB, func()) {
	t.Helper()
	var all = MigrationSQL
	MigrationSQL = append([]string(nil), all[:version]...)
	var restore = func() { MigrationSQL = all }
	t.Cleanup(restore)
	var db, err = OpenMemory(context.Background(), strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	if err != nil {
		t.Fatalf("OpenMemory at version %d: %v", version, err)
	}
	t.Cleanup(func() { db.Close() })
	return db, restore
}
