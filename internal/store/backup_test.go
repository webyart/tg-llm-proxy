package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupCopiesAConsistentSnapshotWhileTheWriterIsOpen(t *testing.T) {
	var sourcePath = filepath.Join(t.TempDir(), "source", "bot.db")
	var writer, openErr = Open(context.Background(), sourcePath)
	if openErr != nil {
		t.Fatalf("Open: %v", openErr)
	}
	defer writer.Close()
	var ctx = context.Background()

	if _, err := Append(ctx, writer, Message{ChatID: 42, UserID: 7, Role: RoleUser, Content: "вопрос", TelegramMessageID: intPtr(700)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := Append(ctx, writer, Message{ChatID: 42, UserID: 7, Role: RoleAssistant, Content: "ответ", Model: "qwen-test"}); err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	if err := SetLastUpdateID(ctx, writer, 4242); err != nil {
		t.Fatalf("SetLastUpdateID: %v", err)
	}
	if err := LogEvent(ctx, writer, 7, EventCommand, "ask"); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}

	var destinationPath = filepath.Join(t.TempDir(), "backups", "bot.db")
	if err := Backup(ctx, sourcePath, destinationPath); err != nil {
		t.Fatalf("Backup while the writer is open: %v", err)
	}
	if err := VerifyBackup(ctx, destinationPath); err != nil {
		t.Fatalf("VerifyBackup: %v", err)
	}
	var info, statErr = os.Stat(destinationPath)
	if statErr != nil || info.Size() == 0 {
		t.Fatalf("the backup file must be created and non-empty, stat=%v err=%v", info, statErr)
	}

	var copyDB, copyErr = OpenReadOnly(ctx, destinationPath)
	if copyErr != nil {
		t.Fatalf("OpenReadOnly of the backup: %v", copyErr)
	}
	defer copyDB.Close()
	var stats, statsErr = Stats(ctx, copyDB, 42)
	if statsErr != nil || stats.Messages != 2 || stats.TokensOut != 0 {
		t.Fatalf("the backup must contain the committed conversation, stats=%+v err=%v", stats, statsErr)
	}
	var offset, found, offsetErr = LastUpdateID(ctx, copyDB)
	if offsetErr != nil || !found || offset != 4242 {
		t.Fatalf("bot_state in the backup = %d found=%v err=%v, want 4242/true/nil", offset, found, offsetErr)
	}
	if events, eventErr := CountEvents(ctx, copyDB, EventCommand); eventErr != nil || events != 1 {
		t.Fatalf("events in the backup = %d err=%v, want 1/nil", events, eventErr)
	}

	var live, liveErr = Stats(ctx, writer, 42)
	if liveErr != nil || live.Messages != 2 {
		t.Fatalf("Backup must not remove or change live rows, live=%+v err=%v", live, liveErr)
	}
}

func TestBackupRefusesUnsafeDestinations(t *testing.T) {
	var ctx = context.Background()
	var sourcePath = filepath.Join(t.TempDir(), "bot.db")
	var source, err = Open(ctx, sourcePath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer source.Close()

	if err := Backup(ctx, sourcePath, sourcePath); err == nil {
		t.Fatal("Backup must refuse source and destination being the same file")
	}
	var existing = filepath.Join(t.TempDir(), "already.db")
	var file, createErr = os.Create(existing)
	if createErr != nil {
		t.Fatalf("Create the existing destination: %v", createErr)
	}
	_ = file.Close()
	if err := Backup(ctx, sourcePath, existing); err == nil {
		t.Fatal("Backup must refuse to overwrite an existing file")
	}
	if err := Backup(ctx, "file::memory:?cache=shared", filepath.Join(t.TempDir(), "memory.db")); err == nil {
		t.Fatal("Backup must refuse the in-memory database form")
	}
}

func TestVerifyBackupRequiresTheBotSchema(t *testing.T) {
	var ctx = context.Background()
	var path = filepath.Join(t.TempDir(), "wrong-schema.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE events`); err != nil {
		t.Fatalf("DROP TABLE events: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := VerifyBackup(ctx, path); err == nil {
		t.Fatal("VerifyBackup must reject a sound SQLite file without the bot schema")
	}
}

func TestVerifyBackupRejectsOrphanRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "orphan.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := Append(ctx, db, Message{ChatID: 42, UserID: 7, Role: RoleUser, Content: "вопрос"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// PRAGMA foreign_keys is per connection, so the raw single-connection handle can switch enforcement off
	// and manufacture the state a page-level integrity_check does not see: a message without its chat.
	dsn, err := BuildDSN(path)
	if err != nil {
		t.Fatalf("BuildDSN: %v", err)
	}
	raw, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	raw.SetMaxOpenConns(1)
	raw.SetMaxIdleConns(1)
	if err := raw.PingContext(ctx); err != nil {
		raw.Close()
		t.Fatalf("Ping: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		raw.Close()
		t.Fatalf("disable foreign_keys: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `DELETE FROM chats WHERE chat_id = 42`); err != nil {
		raw.Close()
		t.Fatalf("delete the parent chat: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close the raw handle: %v", err)
	}
	err = VerifyBackup(ctx, path)
	if err == nil || !strings.Contains(err.Error(), "foreign_key_check") {
		t.Fatalf("VerifyBackup on an orphaned message = %v, want a foreign_key_check error", err)
	}
}

func TestVerifyBackupRejectsCorruptSearchIndexes(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		table string
		seed  func(context.Context, *sql.DB) error
	}{
		{name: "messages", table: "messages_fts", seed: func(ctx context.Context, db *sql.DB) error {
			_, err := Append(ctx, db, Message{ChatID: 42, UserID: 7, Role: RoleUser, Content: "searchable text"})
			return err
		}},
		{name: "rag_chunks", table: "rag_chunks_fts", seed: func(ctx context.Context, db *sql.DB) error {
			if _, err := Append(ctx, db, Message{ChatID: 43, UserID: 7, Role: RoleUser, Content: "unrelated"}); err != nil {
				return err
			}
			_, _, err := PutRAGDocument(ctx, db, 43, 7, "doc.txt", "текст для поиска")
			return err
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), testCase.name+".db")
			db, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := testCase.seed(ctx, db); err != nil {
				db.Close()
				t.Fatalf("seed: %v", err)
			}
			// Keep PRAGMA integrity_check happy while making the external-content index disagree with its
			// content table: this is the corruption the FTS5 integrity-check exists for.
			if _, err := db.ExecContext(ctx, `INSERT INTO `+testCase.table+`(`+testCase.table+`) VALUES ('delete-all')`); err != nil {
				db.Close()
				t.Fatalf("clear %s: %v", testCase.table, err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			err = VerifyBackup(ctx, path)
			if err == nil || !strings.Contains(err.Error(), testCase.table) || !strings.Contains(err.Error(), "integrity-check") {
				t.Fatalf("VerifyBackup on a corrupt %s index = %v, want an FTS integrity-check error", testCase.table, err)
			}
		})
	}
}

func TestVerifyBackupRequiresTheSearchTriggersAndIndexes(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		statement string
		object    string
	}{
		{name: "fts_table", statement: `DROP TABLE messages_fts`, object: "messages_fts"},
		{name: "trigger", statement: `DROP TRIGGER messages_fts_insert`, object: "messages_fts_insert"},
		{name: "index", statement: `DROP INDEX idx_chats_last_active`, object: "idx_chats_last_active"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), testCase.name+".db")
			db, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if _, err := db.ExecContext(ctx, testCase.statement); err != nil {
				db.Close()
				t.Fatalf("drop the object: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			err = VerifyBackup(ctx, path)
			if err == nil || !strings.Contains(err.Error(), testCase.object) {
				t.Fatalf("VerifyBackup without %s = %v, want a missing-object error", testCase.object, err)
			}
		})
	}
}

func TestContentFingerprintFollowsTheStoredRows(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "fingerprint.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	first, err := Append(ctx, db, Message{ChatID: 42, UserID: 7, Role: RoleUser, Content: "одинаковая длина"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	before, err := ContentFingerprint(ctx, db)
	if err != nil {
		t.Fatalf("ContentFingerprint: %v", err)
	}
	if _, err := Append(ctx, db, Message{ChatID: 42, UserID: 7, Role: RoleAssistant, Content: "ответ"}); err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	more, err := ContentFingerprint(ctx, db)
	if err != nil || more == before {
		t.Fatalf("a new row did not change the fingerprint: before=%s after=%s err=%v", before, more, err)
	}
	// The same number of rows must still change the hash when the text changes.
	if _, err := db.ExecContext(ctx, `UPDATE messages SET content = 'ДРУГАЯ длина!!' WHERE id = ?`, first); err != nil {
		t.Fatalf("update the content: %v", err)
	}
	changed, err := ContentFingerprint(ctx, db)
	if err != nil || changed == more {
		t.Fatalf("a content change with an equal row count did not change the fingerprint: before=%s after=%s err=%v", more, changed, err)
	}
}
