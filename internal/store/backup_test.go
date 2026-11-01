package store

import (
	"context"
	"os"
	"path/filepath"
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
