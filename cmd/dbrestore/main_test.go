package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"bot/internal/store"
)

func TestRunRestoresAVerifiedBackup(t *testing.T) {
	var ctx = context.Background()
	var livePath = filepath.Join(t.TempDir(), "live.db")
	var live, err = store.Open(ctx, livePath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer live.Close()
	if _, err := store.Append(ctx, live, store.Message{ChatID: 42, UserID: 7, Role: store.RoleUser, Content: "вопрос"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := store.Append(ctx, live, store.Message{ChatID: 42, UserID: 7, Role: store.RoleAssistant, Content: "ответ"}); err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	if err := store.SetLastUpdateID(ctx, live, 4242); err != nil {
		t.Fatalf("SetLastUpdateID: %v", err)
	}
	var backupPath = filepath.Join(t.TempDir(), "backup.db")
	if err := store.Backup(ctx, livePath, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	var restoredPath = filepath.Join(t.TempDir(), "scratch", "restored.db")
	if err := run(ctx, backupPath, restoredPath); err != nil {
		t.Fatalf("run: %v", err)
	}
	restored, err := store.OpenReadOnly(ctx, restoredPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer restored.Close()
	stats, err := store.Stats(ctx, restored, 42)
	if err != nil || stats.Messages != 2 {
		t.Fatalf("restored stats=%+v err=%v, want two messages", stats, err)
	}
	offset, found, err := store.LastUpdateID(ctx, restored)
	if err != nil || !found || offset != 4242 {
		t.Fatalf("restored offset=%d found=%v err=%v, want 4242/true/nil", offset, found, err)
	}
}

func TestRunRefusesInvalidSourceAndExistingDestination(t *testing.T) {
	var ctx = context.Background()
	var invalidPath = filepath.Join(t.TempDir(), "invalid.db")
	if err := os.WriteFile(invalidPath, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var restoredPath = filepath.Join(t.TempDir(), "restored.db")
	if err := run(ctx, invalidPath, restoredPath); err == nil {
		t.Fatal("an invalid source must be refused")
	}
	if _, err := os.Stat(restoredPath); !os.IsNotExist(err) {
		t.Fatalf("an invalid source must not leave a destination, stat err=%v", err)
	}

	var livePath = filepath.Join(t.TempDir(), "live.db")
	live, err := store.Open(ctx, livePath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer live.Close()
	var backupPath = filepath.Join(t.TempDir(), "backup.db")
	if err := store.Backup(ctx, livePath, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := os.WriteFile(restoredPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("WriteFile existing destination: %v", err)
	}
	if err := run(ctx, backupPath, restoredPath); err == nil {
		t.Fatal("an existing destination must be refused")
	}
	content, err := os.ReadFile(restoredPath)
	if err != nil || string(content) != "keep" {
		t.Fatalf("existing destination changed: %q err=%v", content, err)
	}
	if err := run(ctx, backupPath, backupPath); err == nil {
		t.Fatal("source and destination being the same file must be refused")
	}
}
