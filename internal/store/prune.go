package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PruneOptions selects rows by their UTC timestamp. A zero time disables that class; message pruning
// therefore stays opt-in even when events are pruned.
//
// The four newer classes exist because the tables grew without a retention path: daily_quota_reservations
// keeps one row per user per day per class forever, and finished tasks, sent reminders and saved links were
// never deleted by anything but the user.
type PruneOptions struct {
	EventsBefore    time.Time
	MessagesBefore  time.Time
	RAGBefore       time.Time
	QuotaBefore     time.Time // whole days before this UTC date
	TasksBefore     time.Time // only terminal tasks, by finished_at (fallback updated_at)
	RemindersBefore time.Time // only terminal reminders, by finished_at (fallback updated_at)
	LinksBefore     time.Time // saved links, by updated_at
}

// PruneReport counts candidate rows for a dry-run or rows deleted by Prune.
type PruneReport struct {
	Events       int64
	Messages     int64
	RAGDocuments int64
	Quota        int64
	Tasks        int64
	Reminders    int64
	Links        int64
}

// Empty reports whether the plan deletes nothing at all.
func (report PruneReport) Empty() bool {
	return report.Events == 0 && report.Messages == 0 && report.RAGDocuments == 0 &&
		report.Quota == 0 && report.Tasks == 0 && report.Reminders == 0 && report.Links == 0
}

func (options PruneOptions) validate() error {
	if options.EventsBefore.IsZero() && options.MessagesBefore.IsZero() && options.RAGBefore.IsZero() &&
		options.QuotaBefore.IsZero() && options.TasksBefore.IsZero() && options.RemindersBefore.IsZero() &&
		options.LinksBefore.IsZero() {
		return fmt.Errorf("store.Prune needs at least one cutoff")
	}
	return nil
}

func pruneTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02 15:04:05")
}

// pruneTerminalTimestamp — the format tasks, reminders and saved links are written in (RFC3339Nano, UTC).
func pruneTerminalTimestamp(value time.Time) string {
	return formatReminderTime(value)
}

// pruneDay — the `day` column of daily_quota_reservations is a UTC date.
func pruneDay(value time.Time) string {
	return utcDayStart(value).Format("2006-01-02")
}

// PrunePreview counts without deleting. The caller chooses a read-only handle for a dry-run.
func PrunePreview(ctx context.Context, db *sql.DB, options PruneOptions) (PruneReport, error) {
	if err := options.validate(); err != nil {
		return PruneReport{}, err
	}
	return pruneCounts(ctx, db, options)
}

type pruneQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func pruneCounts(ctx context.Context, db pruneQuerier, options PruneOptions) (PruneReport, error) {
	var report PruneReport
	var err error
	if !options.EventsBefore.IsZero() {
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE created_at < ?`, pruneTimestamp(options.EventsBefore)).Scan(&report.Events); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune events count: %w", err)
		}
	}
	if !options.MessagesBefore.IsZero() {
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE created_at < ?`, pruneTimestamp(options.MessagesBefore)).Scan(&report.Messages); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune messages count: %w", err)
		}
	}
	if !options.RAGBefore.IsZero() {
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rag_documents WHERE created_at < ?`, pruneTimestamp(options.RAGBefore)).Scan(&report.RAGDocuments); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune RAG count: %w", err)
		}
	}
	if !options.QuotaBefore.IsZero() {
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_quota_reservations WHERE day < ?`, pruneDay(options.QuotaBefore)).Scan(&report.Quota); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune quota count: %w", err)
		}
	}
	if !options.TasksBefore.IsZero() {
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks
			WHERE status IN ('completed', 'cancelled', 'failed')
			AND COALESCE(finished_at, updated_at) < ?`, pruneTerminalTimestamp(options.TasksBefore)).Scan(&report.Tasks); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune tasks count: %w", err)
		}
	}
	if !options.RemindersBefore.IsZero() {
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reminders
			WHERE status IN ('sent', 'cancelled', 'failed')
			AND COALESCE(finished_at, updated_at) < ?`, pruneTerminalTimestamp(options.RemindersBefore)).Scan(&report.Reminders); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune reminders count: %w", err)
		}
	}
	if !options.LinksBefore.IsZero() {
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM saved_links WHERE updated_at < ?`, pruneTerminalTimestamp(options.LinksBefore)).Scan(&report.Links); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune links count: %w", err)
		}
	}
	return report, nil
}

// Prune deletes the selected rows in one immediate transaction and reports the actual number removed.
func Prune(ctx context.Context, db *sql.DB, options PruneOptions) (PruneReport, error) {
	if err := options.validate(); err != nil {
		return PruneReport{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PruneReport{}, fmt.Errorf("store.Prune begin: %w", err)
	}
	defer tx.Rollback()
	report, err := pruneCounts(ctx, tx, options)
	if err != nil {
		return PruneReport{}, err
	}
	if !options.EventsBefore.IsZero() {
		if _, err = tx.ExecContext(ctx, `DELETE FROM events WHERE created_at < ?`, pruneTimestamp(options.EventsBefore)); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune delete events: %w", err)
		}
	}
	if !options.MessagesBefore.IsZero() {
		if _, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE created_at < ?`, pruneTimestamp(options.MessagesBefore)); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune delete messages: %w", err)
		}
	}
	if !options.RAGBefore.IsZero() {
		if _, err = tx.ExecContext(ctx, `DELETE FROM rag_documents WHERE created_at < ?`, pruneTimestamp(options.RAGBefore)); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune delete RAG documents: %w", err)
		}
	}
	if !options.QuotaBefore.IsZero() {
		if _, err = tx.ExecContext(ctx, `DELETE FROM daily_quota_reservations WHERE day < ?`, pruneDay(options.QuotaBefore)); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune delete quota reservations: %w", err)
		}
	}
	if !options.TasksBefore.IsZero() {
		if _, err = tx.ExecContext(ctx, `DELETE FROM tasks
			WHERE status IN ('completed', 'cancelled', 'failed')
			AND COALESCE(finished_at, updated_at) < ?`, pruneTerminalTimestamp(options.TasksBefore)); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune delete terminal tasks: %w", err)
		}
	}
	if !options.RemindersBefore.IsZero() {
		if _, err = tx.ExecContext(ctx, `DELETE FROM reminders
			WHERE status IN ('sent', 'cancelled', 'failed')
			AND COALESCE(finished_at, updated_at) < ?`, pruneTerminalTimestamp(options.RemindersBefore)); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune delete terminal reminders: %w", err)
		}
	}
	if !options.LinksBefore.IsZero() {
		if _, err = tx.ExecContext(ctx, `DELETE FROM saved_links WHERE updated_at < ?`, pruneTerminalTimestamp(options.LinksBefore)); err != nil {
			return PruneReport{}, fmt.Errorf("store.Prune delete saved links: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return PruneReport{}, fmt.Errorf("store.Prune commit: %w", err)
	}
	return report, nil
}

// Vacuum rebuilds the database file so the pages a prune freed go back to the filesystem. DELETE alone only
// moves them to the freelist: the file keeps its size and the next writes reuse the space silently.
func Vacuum(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("store.Vacuum: %w", err)
	}
	return nil
}

// CheckpointWAL folds the write-ahead log back into the main database file and truncates it.
//
// DELETE frees pages for reuse but does not shrink the file, and every page the deletions touched stays
// in the -wal until a checkpoint. Without this call a successful prune reported N deleted rows while both
// bot.db and bot.db-wal kept their size — the operator saw the rows gone from the reports and none of the
// space back on disk. The busy counter says a reader still held the log; that is a warning, not a failure,
// because the next checkpoint reclaims it.
func CheckpointWAL(ctx context.Context, db *sql.DB) (busy int, err error) {
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, new(int), new(int)); err != nil {
		return 0, fmt.Errorf("store.CheckpointWAL: %w", err)
	}
	return busy, nil
}
