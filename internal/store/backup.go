package store

import (
	"context"
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
	return nil
}
