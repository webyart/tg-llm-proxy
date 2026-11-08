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
	var quotaDays = flag.Int("quota-days", 0, "delete daily quota reservations older than this many days; 0 disables quota pruning")
	var tasksDays = flag.Int("tasks-days", 0, "delete terminal tasks older than this many days; 0 disables task pruning")
	var remindersDays = flag.Int("reminders-days", 0, "delete terminal reminders older than this many days; 0 disables reminder pruning")
	var linksDays = flag.Int("links-days", 0, "delete saved links not updated for this many days; 0 disables link pruning")
	var apply = flag.Bool("apply", false, "delete candidates after a verified backup; default is dry-run")
	var vacuum = flag.Bool("vacuum", true, "after -apply, checkpoint the WAL and VACUUM to return the freed pages to disk")
	var timeout = flag.Duration("timeout", 120*time.Second, "how long backup, pruning, and verification may take")
	flag.Parse()
	if *source == "" {
		die("-source is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var retention = retentionDays{
		events: *eventsDays, messages: *messagesDays, rag: *ragDays,
		quota: *quotaDays, tasks: *tasksDays, reminders: *remindersDays, links: *linksDays,
	}
	if err := run(ctx, *source, *backup, retention, *apply, *vacuum, os.Stdout); err != nil {
		die("%s", err)
	}
}

// retentionDays is the set of -days flags: one class per group of tables that grows without a natural end.
type retentionDays struct {
	events    int
	messages  int
	rag       int
	quota     int
	tasks     int
	reminders int
	links     int
}

func (days retentionDays) allDisabled() bool {
	return days.events == 0 && days.messages == 0 && days.rag == 0 && days.quota == 0 &&
		days.tasks == 0 && days.reminders == 0 && days.links == 0
}

func run(ctx context.Context, source string, backup string, days retentionDays, apply bool, vacuum bool, output io.Writer) error {
	if days.events < 0 || days.messages < 0 || days.rag < 0 || days.quota < 0 || days.tasks < 0 ||
		days.reminders < 0 || days.links < 0 {
		return fmt.Errorf("retention days cannot be negative: %+v", days)
	}
	if days.allDisabled() {
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
	var now = time.Now()
	if days.events > 0 {
		options.EventsBefore = now.AddDate(0, 0, -days.events)
	}
	if days.messages > 0 {
		options.MessagesBefore = now.AddDate(0, 0, -days.messages)
	}
	if days.rag > 0 {
		options.RAGBefore = now.AddDate(0, 0, -days.rag)
	}
	if days.quota > 0 {
		options.QuotaBefore = now.AddDate(0, 0, -days.quota)
	}
	if days.tasks > 0 {
		options.TasksBefore = now.AddDate(0, 0, -days.tasks)
	}
	if days.reminders > 0 {
		options.RemindersBefore = now.AddDate(0, 0, -days.reminders)
	}
	if days.links > 0 {
		options.LinksBefore = now.AddDate(0, 0, -days.links)
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
		fmt.Fprintf(output, "prune: mode=dry-run events_candidates=%d messages_candidates=%d rag_candidates=%d quota_candidates=%d tasks_candidates=%d reminders_candidates=%d links_candidates=%d; no backup or deletion performed\n",
			report.Events, report.Messages, report.RAGDocuments, report.Quota, report.Tasks, report.Reminders, report.Links)
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
	fmt.Fprintf(output, "prune: mode=apply backup=%s events_deleted=%d messages_deleted=%d rag_deleted=%d quota_deleted=%d tasks_deleted=%d reminders_deleted=%d links_deleted=%d integrity=ok\n",
		backup, report.Events, report.Messages, report.RAGDocuments, report.Quota, report.Tasks, report.Reminders, report.Links)
	busy, checkpointErr := store.CheckpointWAL(ctx, db)
	if checkpointErr != nil {
		return checkpointErr
	}
	if busy != 0 {
		fmt.Fprintf(output, "prune: wal_checkpoint busy=%d — a reader still held the log; the next checkpoint reclaims the space\n", busy)
	}
	if vacuum && !report.Empty() {
		// DELETE only moves the pages to the freelist; the file keeps its size until VACUUM rebuilds it.
		if err := store.Vacuum(ctx, db); err != nil {
			return err
		}
		fmt.Fprintf(output, "prune: vacuum=ok\n")
	}
	return nil
}
