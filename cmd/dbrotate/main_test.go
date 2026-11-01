package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunIsADryRunThenDeletesOnlyExpiredGeneratedNames(t *testing.T) {
	var directory = t.TempDir()
	var names = []string{
		"bot-20261001-120000.db",
		"bot-20261002-120000.db",
		"bot-20261003-120000.db",
		"bot-20261004-120000.db",
		"bot-20261005-120000.db",
	}
	for _, name := range append(names, "bot-manual.db", "notes.txt") {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(directory, "bot-20261006-120000.db"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	var dryRun bytes.Buffer
	if err := run(directory, 2, false, &dryRun); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !strings.Contains(dryRun.String(), "mode=dry-run") || !strings.Contains(dryRun.String(), "candidates=3") {
		t.Fatalf("dry-run must show its mode and candidate count, output:\n%s", dryRun.String())
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("dry-run changed %s: %v", name, err)
		}
	}

	var applied bytes.Buffer
	if err := run(directory, 2, true, &applied); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, name := range names[:3] {
		if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Fatalf("expired backup %s still exists, err=%v", name, err)
		}
	}
	for _, name := range []string{names[3], names[4], "bot-manual.db", "notes.txt", "bot-20261006-120000.db"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("retained path %s is missing: %v", name, err)
		}
	}
}

func TestRunRejectsZeroRetentionAndMissingDirectory(t *testing.T) {
	if err := run(t.TempDir(), 0, false, &bytes.Buffer{}); err == nil {
		t.Fatal("keep=0 must be refused")
	}
	if err := run(filepath.Join(t.TempDir(), "missing"), 1, false, &bytes.Buffer{}); err == nil {
		t.Fatal("a missing backup directory must be reported")
	}
}

func TestRunWithRetentionAboveTheBackupCountDeletesNothing(t *testing.T) {
	var directory = t.TempDir()
	var path = filepath.Join(directory, "bot-20261001-120000.db")
	if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := run(directory, 7, true, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the only backup must survive rotation: %v", err)
	}
}
