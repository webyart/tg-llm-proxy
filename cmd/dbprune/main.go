// Command dbprune previews or applies age-based retention. Apply always creates and verifies a new backup before
// opening the source for writing; message retention is disabled unless -messages-days is explicitly positive.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"bot/internal/store"
)

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dbprune: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	var source = flag.String("source", "", "database file to inspect or prune (required)")
	var backup = flag.String("backup", "", "new pre-prune backup path (required with -apply)")
	var eventsDays = flag.Int("events-days", 90, "delete events older than this many days; 0 disables event pruning")
	var messagesDays = flag.Int("messages-days", 0, "delete messages older than this many days; 0 disables message pruning")
	var ragDays = flag.Int("rag-days", 0, "delete RAG documents older than this many days; 0 disables RAG pruning")
	var apply = flag.Bool("apply", false, "delete candidates after a verified backup; default is dry-run")
	var timeout = flag.Duration("timeout", 120*time.Second, "how long backup, pruning, and verification may take")
	flag.Parse()
	if *source == "" {
		die("-source is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := run(ctx, *source, *backup, *eventsDays, *messagesDays, *ragDays, *apply, os.Stdout); err != nil {
		die("%s", err)
	}
}

func run(ctx context.Context, source string, backup string, eventsDays int, messagesDays int, ragDays int, apply bool, output io.Writer) error {
	if eventsDays < 0 || messagesDays < 0 || ragDays < 0 {
		return fmt.Errorf("retention days cannot be negative: events=%d messages=%d rag=%d", eventsDays, messagesDays, ragDays)
	}
	if eventsDays == 0 && messagesDays == 0 && ragDays == 0 {
		return fmt.Errorf("all retention classes are disabled; nothing would be pruned")
	}
	if apply && backup == "" {
		return fmt.Errorf("-backup is required with -apply")
	}

	if apply {
		if err := store.Backup(ctx, source, backup); err != nil {
			return fmt.Errorf("pre-prune backup failed; source was not opened for writing: %w", err)
		}
		if err := store.VerifyBackup(ctx, backup); err != nil {
			return fmt.Errorf("pre-prune backup verification failed; source was not opened for writing: %w", err)
		}
	}

	var options store.PruneOptions
	if eventsDays > 0 {
		options.EventsBefore = time.Now().AddDate(0, 0, -eventsDays)
	}
	if messagesDays > 0 {
		options.MessagesBefore = time.Now().AddDate(0, 0, -messagesDays)
	}
	if ragDays > 0 {
		options.RAGBefore = time.Now().AddDate(0, 0, -ragDays)
	}

	if !apply {
		db, err := store.OpenReadOnly(ctx, source)
		if err != nil {
			return err
		}
		defer db.Close()
		report, err := store.PrunePreview(ctx, db, options)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "prune: mode=dry-run events_candidates=%d messages_candidates=%d rag_candidates=%d; no backup or deletion performed\n", report.Events, report.Messages, report.RAGDocuments)
		return nil
	}

	db, err := store.Open(ctx, source)
	if err != nil {
		return err
	}
	defer db.Close()
	report, err := store.Prune(ctx, db, options)
	if err != nil {
		return err
	}
	verdict, err := store.IntegrityCheck(ctx, db)
	if err != nil {
		return err
	}
	if verdict != "ok" {
		return fmt.Errorf("post-prune integrity_check answered %q; pre-prune backup is %s", verdict, backup)
	}
	fmt.Fprintf(output, "prune: mode=apply backup=%s events_deleted=%d messages_deleted=%d rag_deleted=%d integrity=ok\n", backup, report.Events, report.Messages, report.RAGDocuments)
	return nil
}
