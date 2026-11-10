package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Backup copies a live SQLite file into a new destination through SQLite's VACUUM INTO. The source is opened
// read-only, so the running bot's writer is not interrupted, and the copy is a consistent file rather than a
// hand-assembled page copy.
func Backup(ctx context.Context, sourcePath string, destinationPath string) error {
	if IsMemory(sourcePath) {
		return fmt.Errorf("store.Backup needs a file database, not the in-memory form %q", sourcePath)
	}
	if strings.TrimSpace(destinationPath) == "" {
		return fmt.Errorf("store.Backup needs a non-empty destination path")
	}
	var sourceAbs, sourceErr = filepath.Abs(sourcePath)
	var destinationAbs, destinationErr = filepath.Abs(destinationPath)
	if sourceErr != nil || destinationErr != nil {
		return fmt.Errorf("store.Backup cannot resolve paths %q/%q (%v / %v)", sourcePath, destinationPath, sourceErr, destinationErr)
	}
	if sourceAbs == destinationAbs {
		return fmt.Errorf("store.Backup refuses to use one file as both source and destination: %q", sourceAbs)
	}
	if _, statErr := os.Stat(destinationPath); statErr == nil {
		return fmt.Errorf("store.Backup refuses to overwrite an existing file: %q", destinationPath)
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("store.Backup cannot inspect the destination %q: %w", destinationPath, statErr)
	}
	if err := prepareParent(destinationPath); err != nil {
		return err
	}

	var source, err = OpenReadOnly(ctx, sourcePath)
	if err != nil {
		return fmt.Errorf("store.Backup cannot open %q read-only: %w", sourcePath, err)
	}
	defer source.Close()

	var target = filepath.ToSlash(destinationAbs)
	if _, err := source.ExecContext(ctx, `VACUUM INTO ?`, target); err != nil {
		_ = os.Remove(destinationPath)
		return fmt.Errorf("store.Backup VACUUM INTO %q: %w", target, err)
	}
	return nil
}

// VerifyBackup checks that path is a self-contained, readable bot database without migrating or writing to it.
func VerifyBackup(ctx context.Context, path string) error {
	db, err := OpenReadOnly(ctx, path)
	if err != nil {
		return fmt.Errorf("store.VerifyBackup: %w", err)
	}
	defer db.Close()

	verdict, err := IntegrityCheck(ctx, db)
	if err != nil {
		return fmt.Errorf("store.VerifyBackup: %w", err)
	}
	if verdict != "ok" {
		return fmt.Errorf("store.VerifyBackup: integrity_check answered %q", verdict)
	}

	names, err := TableNames(ctx, db)
	if err != nil {
		return fmt.Errorf("store.VerifyBackup: %w", err)
	}
	present := make(map[string]bool, len(names))
	for _, name := range names {
		present[name] = true
	}
	missing := []string{}
	for _, name := range []string{"bot_state", "chats", "events", "messages"} {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("store.VerifyBackup: %q is not a bot backup: missing tables %s", path, strings.Join(missing, ", "))
	}
	// The same guard the runtime applies: a backup written by a newer release is reported by version
	// instead of being restored over an older binary and failing later, at the first write.
	if err := GuardSchemaVersion(ctx, db); err != nil {
		return fmt.Errorf("store.VerifyBackup: %w", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("store.VerifyBackup: %w", err)
	}
	// A backup whose child rows point at missing parents is not restorable as a consistent conversation,
	// even when the page-level integrity_check says "ok": integrity_check does not enforce REFERENCES.
	if err := verifyForeignKeyCheck(ctx, db); err != nil {
		return fmt.Errorf("store.VerifyBackup: %w", err)
	}
	// The FTS triggers are created at open time, not by a migration, so a hand-edited or partially copied
	// file can carry the tables and lose the triggers. The indexes below are the schema's own promises.
	if err := verifyRequiredSchemaObjects(ctx, db, version); err != nil {
		return fmt.Errorf("store.VerifyBackup: %w", err)
	}
	// FTS5's integrity-check is an INSERT into the virtual table and is refused by a mode=ro handle, so it
	// runs on a temporary VACUUM INTO copy. The backup itself stays untouched and is still verified read-only.
	if err := verifySearchIndexes(ctx, path, present); err != nil {
		return fmt.Errorf("store.VerifyBackup: %w", err)
	}
	return nil
}

// verifyForeignKeyCheck reports every row PRAGMA foreign_key_check names. The pragma is a read, so it works
// on the read-only handle; unlike integrity_check it walks the REFERENCES graph and answers the backup
// question "can this file be restored without an orphan?".
func verifyForeignKeyCheck(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign_key_check failed: %w", err)
	}
	defer rows.Close()
	violations := []string{}
	for rows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var foreignKey int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKey); err != nil {
			return fmt.Errorf("foreign_key_check returned an unreadable row: %w", err)
		}
		row := "NULL"
		if rowID.Valid {
			row = fmt.Sprint(rowID.Int64)
		}
		violations = append(violations, fmt.Sprintf("%s rowid=%s -> %s (fk %d)", table, row, parent, foreignKey))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("foreign_key_check failed: %w", err)
	}
	if len(violations) > 0 {
		return fmt.Errorf("foreign_key_check found %d violation(s): %s", len(violations), strings.Join(violations, "; "))
	}
	return nil
}

// requiredSchemaObject is one object a database at minVersion must carry. The history is append-only, so the
// version a migration wrote is a stable promise about its indexes and triggers.
type requiredSchemaObject struct {
	kind       string
	name       string
	minVersion int
}

var requiredBackupObjects = []requiredSchemaObject{
	// 001_init.sql
	{kind: "index", name: "idx_messages_chat_time", minVersion: 1},
	{kind: "index", name: "uq_messages_chat_tg_id", minVersion: 1},
	// 005_rag_documents.sql
	{kind: "index", name: "idx_rag_chunks_chat", minVersion: 5},
	// 006_event_indexes.sql
	{kind: "index", name: "idx_events_user_kind", minVersion: 6},
	{kind: "index", name: "idx_events_kind_detail", minVersion: 6},
	{kind: "index", name: "idx_events_created_at", minVersion: 6},
	// 007_reminders.sql
	{kind: "index", name: "idx_reminders_due", minVersion: 7},
	{kind: "index", name: "idx_reminders_lease", minVersion: 7},
	{kind: "index", name: "idx_reminders_chat_active", minVersion: 7},
	// 008_saved_links.sql
	{kind: "index", name: "idx_saved_links_chat_updated", minVersion: 8},
	// 009_tasks.sql
	{kind: "index", name: "uq_tasks_chat_active", minVersion: 9},
	{kind: "index", name: "uq_tasks_approval_token", minVersion: 9},
	{kind: "index", name: "idx_tasks_due", minVersion: 9},
	{kind: "index", name: "idx_tasks_lease", minVersion: 9},
	{kind: "index", name: "idx_tasks_chat", minVersion: 9},
	// 010_daily_quota_reservations.sql and 011_message_user_attribution.sql
	{kind: "index", name: "idx_daily_quota_day_class", minVersion: 10},
	{kind: "index", name: "idx_messages_user_role_created_at", minVersion: 11},
	// 012_hot_path_indexes.sql
	{kind: "index", name: "idx_messages_created_at", minVersion: 12},
	{kind: "index", name: "idx_chats_last_active", minVersion: 12},
	// 013_message_search_index.sql: the triggers are created at open time, not by the migration.
	{kind: "table", name: "messages_fts", minVersion: 13},
	{kind: "table", name: "search_index_meta", minVersion: 13},
	{kind: "trigger", name: "messages_fts_insert", minVersion: 13},
	{kind: "trigger", name: "messages_fts_delete", minVersion: 13},
	{kind: "trigger", name: "messages_fts_update", minVersion: 13},
	// 014_rag_search_index.sql
	{kind: "table", name: "rag_chunks_fts", minVersion: 14},
	{kind: "trigger", name: "rag_chunks_fts_insert", minVersion: 14},
	{kind: "trigger", name: "rag_chunks_fts_delete", minVersion: 14},
	{kind: "trigger", name: "rag_chunks_fts_update", minVersion: 14},
	// 015_message_feedback.sql
	{kind: "index", name: "idx_message_feedback_chat_created", minVersion: 15},
	// 016_rag_chunk_embeddings.sql (the index survives the 018 rebuild under the same name)
	{kind: "index", name: "idx_rag_chunk_embeddings_chat_model", minVersion: 16},
}

func verifyRequiredSchemaObjects(ctx context.Context, db *sql.DB, version int) error {
	for _, object := range requiredBackupObjects {
		if version >= 1 && version < object.minVersion {
			continue
		}
		if version == 0 {
			// A pre-versioning backup predates this history; only the four tables above are guaranteed.
			continue
		}
		var found int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?`, object.kind, object.name).Scan(&found); err != nil {
			return fmt.Errorf("the required %s %s could not be inspected: %w", object.kind, object.name, err)
		}
		if found != 1 {
			return fmt.Errorf("the backup is missing its required %s %s (schema version %d)", object.kind, object.name, version)
		}
	}
	return nil
}

// verifySearchIndexes runs the FTS5 integrity check for each index present in the backup. The check needs a
// write-capable connection by SQLite's own rule, so the file is copied with VACUUM INTO into a temp directory
// first: the backup stays a read-only input, and a corrupt index still fails verification.
func verifySearchIndexes(ctx context.Context, path string, present map[string]bool) error {
	tables := []string{}
	for _, table := range []string{"messages_fts", "rag_chunks_fts"} {
		if present[table] {
			tables = append(tables, table)
		}
	}
	if len(tables) == 0 {
		return nil // a backup older than migration 013/014 has no FTS index to check
	}

	dir, err := os.MkdirTemp("", "store-verify-fts-")
	if err != nil {
		return fmt.Errorf("the FTS integrity check needs a temporary directory: %w", err)
	}
	defer os.RemoveAll(dir)
	copyPath := filepath.Join(dir, "verify.db")
	if err := Backup(ctx, path, copyPath); err != nil {
		return fmt.Errorf("the FTS integrity check could not copy the backup: %w", err)
	}
	dsn, err := BuildDSN(copyPath)
	if err != nil {
		return fmt.Errorf("the FTS integrity check copy has an unusable DSN: %w", err)
	}
	copyDB, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		return fmt.Errorf("the FTS integrity check copy could not be opened: %w", err)
	}
	defer copyDB.Close()
	copyDB.SetMaxOpenConns(1)
	copyDB.SetMaxIdleConns(1)
	if err := copyDB.PingContext(ctx); err != nil {
		return fmt.Errorf("the FTS integrity check copy does not answer: %w", err)
	}
	for _, table := range tables {
		if _, err := copyDB.ExecContext(ctx,
			`INSERT INTO `+table+`(`+table+`, rank) VALUES('integrity-check', 1)`); err != nil {
			return fmt.Errorf("FTS integrity-check of %s failed: %w", table, err)
		}
	}
	return nil
}
