// Command dbclean rewrites historical assistant messages whose stored content still contains the raw
// text/XML tool-call markup that the runtime sanitizer (bot.SanitizeToolMarkup) removes at read time.
//
// This is a one-off maintenance command, deliberately not a migration and not part of startup:
//
//   - it touches ONLY messages.role = 'assistant';
//   - a row is rewritten only when SanitizeToolMarkup returns different content, so a clean message is
//     never normalised, re-encoded or trimmed just because it was visited;
//   - -DryRun (the default) opens the database read-only and reports what would change;
//   - -Apply rewrites the changed rows in one transaction and runs integrity_check afterwards;
//   - it does NOT create a backup. Stop the bot (or make a verified snapshot with dbbackup) before
//     -Apply; the operator owns that step, exactly as the command name says.
//
// The report contains counts, byte lengths and row ids only. Message content, model ids and any part of
// an answer are never printed.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"bot/internal/bot"
	"bot/internal/store"
)

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dbclean: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	var source = flag.String("source", "", "database file to inspect or clean (required)")
	var dryRun = flag.Bool("DryRun", false, "preview only; this is the default when neither flag is given")
	var apply = flag.Bool("Apply", false, "rewrite the changed assistant messages in one transaction")
	var samples = flag.Int("samples", 10, "how many changed rows to list as id and before/after lengths (0 disables samples)")
	var timeout = flag.Duration("timeout", 120*time.Second, "how long the inspection, rewrite and integrity check may take")
	flag.Parse()

	if *source == "" {
		die("-source is required")
	}
	if *dryRun && *apply {
		die("-DryRun and -Apply are mutually exclusive")
	}
	if *samples < 0 || *samples > 100 {
		die("-samples must be between 0 and 100")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := run(ctx, *source, *apply, *samples, os.Stdout); err != nil {
		die("%s", err)
	}
}

// cleanSample is one changed row in the report. It carries the id and byte lengths of the old and new
// content, never either string: the whole point of the maintenance command is to remove private text,
// so printing it here would leak exactly what is being cleaned.
type cleanSample struct {
	ID         int64
	BeforeLen  int
	AfterLen   int
	SavedBytes int
}

type cleanReport struct {
	Scanned     int
	Candidates  int
	Updated     int
	Skipped     int
	BytesBefore int64
	BytesAfter  int64
	Samples     []cleanSample
	Integrity   string
}

func (r cleanReport) savedBytes() int64 { return r.BytesBefore - r.BytesAfter }

// run is the testable entry point. apply=false opens the source read-only and never writes or creates a
// backup; apply=true opens it read-write, rewrites candidates in one transaction and verifies integrity.
func run(ctx context.Context, source string, apply bool, samples int, output io.Writer) error {
	if !apply {
		db, err := store.OpenReadOnly(ctx, source)
		if err != nil {
			return fmt.Errorf("open read-only: %w", err)
		}
		defer db.Close()
		report, err := cleanMessages(ctx, db, false)
		if err != nil {
			return err
		}
		printReport(output, "dry-run", report, samples)
		return nil
	}

	db, err := store.Open(ctx, source)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin clean transaction: %w", err)
	}
	defer tx.Rollback()
	report, err := cleanMessages(ctx, tx, true)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit clean transaction: %w", err)
	}
	verdict, err := store.IntegrityCheck(ctx, db)
	if err != nil {
		return err
	}
	if verdict != "ok" {
		return fmt.Errorf("post-clean integrity_check answered %q", verdict)
	}
	report.Integrity = verdict
	if busy, checkpointErr := store.CheckpointWAL(ctx, db); checkpointErr != nil {
		return checkpointErr
	} else if busy != 0 {
		fmt.Fprintf(output, "dbclean: wal_checkpoint busy=%d — a reader still held the log\n", busy)
	}
	printReport(output, "apply", report, samples)
	return nil
}

// cleanMessages reads the prefixed rows, sanitizes each one with the SAME function the history/API layer
// uses, and (when apply is true) updates only rows whose content actually changed. Rows are collected
// before any UPDATE so the read cursor is closed before the transaction starts writing.
func cleanMessages(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, apply bool) (cleanReport, error) {
	var report cleanReport
	rows, err := db.QueryContext(ctx, `
		SELECT id, content
		FROM messages
		WHERE role = ?
		  AND (instr(lower(content), '<tool_call') > 0
		       OR instr(lower(content), '<function') > 0
		       OR instr(lower(content), '<parameter') > 0)
		ORDER BY id`, store.RoleAssistant)
	if err != nil {
		return report, fmt.Errorf("read assistant messages: %w", err)
	}
	type candidate struct {
		id     int64
		before string
		after  string
	}
	var candidates []candidate
	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			rows.Close()
			return report, fmt.Errorf("scan assistant message: %w", err)
		}
		report.Scanned++
		after := bot.SanitizeToolMarkup(content)
		if after == content {
			continue
		}
		candidates = append(candidates, candidate{id: id, before: content, after: after})
		report.BytesBefore += int64(len(content))
		report.BytesAfter += int64(len(after))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return report, fmt.Errorf("read assistant message rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return report, fmt.Errorf("close assistant message rows: %w", err)
	}
	report.Candidates = len(candidates)
	for _, item := range candidates {
		if len(report.Samples) < 100 {
			report.Samples = append(report.Samples, cleanSample{
				ID:         item.id,
				BeforeLen:  len(item.before),
				AfterLen:   len(item.after),
				SavedBytes: len(item.before) - len(item.after),
			})
		}
	}
	if !apply {
		return report, nil
	}
	for _, item := range candidates {
		result, err := db.ExecContext(ctx,
			`UPDATE messages SET content = ? WHERE id = ? AND role = ?`,
			item.after, item.id, store.RoleAssistant)
		if err != nil {
			return report, fmt.Errorf("update assistant message %d: %w", item.id, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return report, fmt.Errorf("rows affected of assistant message %d: %w", item.id, err)
		}
		if affected != 1 {
			report.Skipped++
			continue
		}
		report.Updated++
	}
	return report, nil
}

func printReport(output io.Writer, mode string, report cleanReport, samples int) {
	fmt.Fprintf(output,
		"dbclean: mode=%s scanned=%d candidates=%d updated=%d skipped=%d bytes_before=%d bytes_after=%d saved_bytes=%d samples=%d",
		mode, report.Scanned, report.Candidates, report.Updated, report.Skipped,
		report.BytesBefore, report.BytesAfter, report.savedBytes(), len(report.Samples))
	if report.Integrity != "" {
		fmt.Fprintf(output, " integrity=%s", report.Integrity)
	}
	fmt.Fprintln(output)
	limit := samples
	if limit > len(report.Samples) {
		limit = len(report.Samples)
	}
	for index := 0; index < limit; index++ {
		sample := report.Samples[index]
		fmt.Fprintf(output, "dbclean: sample id=%d before_len=%d after_len=%d saved_bytes=%d\n",
			sample.ID, sample.BeforeLen, sample.AfterLen, sample.SavedBytes)
	}
	if mode == "dry-run" && report.Candidates > 0 {
		fmt.Fprintln(output, "dbclean: no backup was created; make a verified snapshot before running -Apply")
	}
	if mode == "apply" && report.Candidates == 0 {
		fmt.Fprintln(output, "dbclean: nothing to rewrite")
	}
}
