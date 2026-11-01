package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PruneOptions selects rows by their UTC created_at timestamp. A zero time disables that class; message pruning
// therefore stays opt-in even when events are pruned.
type PruneOptions struct {
	EventsBefore   time.Time
	MessagesBefore time.Time
}

// PruneReport counts candidate rows for a dry-run or rows deleted by Prune.
type PruneReport struct {
	Events   int64
	Messages int64
}

func (options PruneOptions) validate() error {
	if options.EventsBefore.IsZero() && options.MessagesBefore.IsZero() {
		return fmt.Errorf("store.Prune needs an events cutoff, a messages cutoff, or both")
	}
	return nil
}

func pruneTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02 15:04:05")
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
	if err := tx.Commit(); err != nil {
		return PruneReport{}, fmt.Errorf("store.Prune commit: %w", err)
	}
	return report, nil
}
