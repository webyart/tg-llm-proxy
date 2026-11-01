package main

import (
	"context"
	"path/filepath"
	"testing"

	"bot/internal/store"
)

func TestRunBacksUpAndVerifies(t *testing.T) {
	var ctx = context.Background()
	var sourcePath = filepath.Join(t.TempDir(), "bot.db")
	var source, err = store.Open(ctx, sourcePath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer source.Close()
	if _, err := store.Append(ctx, source, store.Message{ChatID: 42, UserID: 7, Role: store.RoleUser, Content: "вопрос"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var destinationPath = filepath.Join(t.TempDir(), "backup.db")
	if err := run(ctx, sourcePath, destinationPath); err != nil {
		t.Fatalf("run: %v", err)
	}
	var backup, backupErr = store.OpenReadOnly(ctx, destinationPath)
	if backupErr != nil {
		t.Fatalf("OpenReadOnly of the verified copy: %v", backupErr)
	}
	defer backup.Close()
	var stats, statsErr = store.Stats(ctx, backup, 42)
	if statsErr != nil || stats.Messages != 1 {
		t.Fatalf("the verified copy must hold the same message count, stats=%+v err=%v", stats, statsErr)
	}
}

func TestRunRefusesToOverwrite(t *testing.T) {
	var sourcePath = filepath.Join(t.TempDir(), "bot.db")
	var source, err = store.Open(context.Background(), sourcePath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer source.Close()
	if err := run(context.Background(), sourcePath, sourcePath); err == nil {
		t.Fatal("a source and destination that are the same file must be refused")
	}
}
