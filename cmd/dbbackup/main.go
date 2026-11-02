// SQLite's VACUUM INTO while the bot may still be writing: the source is opened read-only and the destination
// is verified after the copy, so a supposedly good backup is not trusted just because it has a size.
//
// stays on the host and reads the file, not the container.
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
	fmt.Fprintf(os.Stderr, "dbbackup: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	var source = flag.String("source", "", "database file to copy ")
	var destination = flag.String("dest", "", "new backup file; must not exist yet (required)")
	var timeout = flag.Duration("timeout", 120*time.Second, "how long the whole backup and verification may take")
	flag.Parse()

	if *source == "" {
		die("-source is required ")
	}
	if *destination == "" {
		die("-dest is required — backups never overwrite an existing file")
	}

	var ctx, cancel = context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := run(ctx, *source, *destination); err != nil {
		die("%s", err)
	}
}

func run(ctx context.Context, source string, destination string) error {
	if err := store.Backup(ctx, source, destination); err != nil {
		return err
	}

	if err := store.VerifyBackup(ctx, destination); err != nil {
		return fmt.Errorf("the copy was made but failed read-only verification: %w", err)
	}
	var info, statErr = os.Stat(destination)
	if statErr != nil {
		return statErr
	}
	fmt.Printf("backup: %s -> %s (%d bytes, integrity=ok, schema=ok)\n", source, destination, info.Size())
	return nil
}
