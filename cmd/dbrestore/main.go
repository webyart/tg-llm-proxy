// Command dbrestore verifies a backup and materializes it as a new SQLite file. The PowerShell wrapper
// restricts the destination to zzscratch; replacing a live database is deliberately a separate operator action.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"bot/internal/store"
)

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dbrestore: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	var source = flag.String("source", "", "verified bot backup file (required)")
	var destination = flag.String("dest", "", "new restored file; must not exist yet (required)")
	var timeout = flag.Duration("timeout", 120*time.Second, "how long verification and restore may take")
	flag.Parse()

	if *source == "" {
		die("-source is required (scripts/db_restore.ps1 passes it)")
	}
	if *destination == "" {
		die("-dest is required — restore never overwrites an existing file")
	}

	var ctx, cancel = context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := run(ctx, *source, *destination); err != nil {
		die("%s", err)
	}
}

func run(ctx context.Context, source string, destination string) error {
	if err := store.VerifyBackup(ctx, source); err != nil {
		return fmt.Errorf("source backup failed read-only verification: %w", err)
	}
	if err := store.Backup(ctx, source, destination); err != nil {
		return err
	}
	if err := store.VerifyBackup(ctx, destination); err != nil {
		return fmt.Errorf("restored file failed read-only verification: %w", err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		return err
	}
	fmt.Printf("restore: %s -> %s (%d bytes, integrity=ok, schema=ok)\n", source, destination, info.Size())
	return nil
}
