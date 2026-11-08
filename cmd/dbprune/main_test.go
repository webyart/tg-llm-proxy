package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bot/internal/store"
)

func TestRunDryRunDoesNotDeleteOrBackUp(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source.db")
	db, err := store.Open(ctx, source)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := store.LogEvent(ctx, db, 7, store.EventCommand, "old"); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE events SET created_at = '2000-01-01 00:00:00'`); err != nil {
		t.Fatalf("age event: %v", err)
	}
	backup := filepath.Join(t.TempDir(), "unused.db")
	var output bytes.Buffer
	if err := run(ctx, source, backup, retentionDays{events: 90}, false, true, &output); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !strings.Contains(output.String(), "events_candidates=1") {
		t.Fatalf("dry-run must count the old event, got %q", output.String())
	}
	var count int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("dry-run deleted rows, count=%d err=%v", count, err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("dry-run created a backup, err=%v", err)
	}
}

func TestRunApplyBacksUpBeforeDeleting(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source.db")
	db, err := store.Open(ctx, source)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.LogEvent(ctx, db, 7, store.EventCommand, "old"); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE events SET created_at = '2000-01-01 00:00:00'`); err != nil {
		t.Fatalf("age event: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	backup := filepath.Join(t.TempDir(), "pre-prune.db")
	var output bytes.Buffer
	if err := run(ctx, source, backup, retentionDays{events: 90}, true, true, &output); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := store.VerifyBackup(ctx, backup); err != nil {
		t.Fatalf("pre-prune backup is not usable: %v", err)
	}
	backupDB, err := store.OpenReadOnly(ctx, backup)
	if err != nil {
		t.Fatalf("OpenReadOnly backup: %v", err)
	}
	defer backupDB.Close()
	var backupEvents int64
	if err := backupDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&backupEvents); err != nil || backupEvents != 1 {
		t.Fatalf("backup must retain the old event, count=%d err=%v", backupEvents, err)
	}
	liveDB, err := store.OpenReadOnly(ctx, source)
	if err != nil {
		t.Fatalf("OpenReadOnly source: %v", err)
	}
	defer liveDB.Close()
	var liveEvents int64
	if err := liveDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&liveEvents); err != nil || liveEvents != 0 {
		t.Fatalf("old event must be deleted after backup, count=%d err=%v", liveEvents, err)
	}
}

func TestRunRejectsApplyWithoutBackup(t *testing.T) {
	if err := run(context.Background(), "unused.db", "", retentionDays{events: 90}, true, true, &bytes.Buffer{}); err == nil {
		t.Fatal("apply without a backup path must be refused before opening the source")
	}
}
