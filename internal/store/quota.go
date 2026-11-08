package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	DailyQuotaModel        = "model"
	DailyQuotaNetworkTools = "network_tools"
)

func ReserveDailyQuota(ctx context.Context, db *sql.DB, userID int64, now time.Time, class string, limit int64) (bool, error) {
	if limit == 0 {
		return true, nil
	}
	if userID <= 0 || limit < 0 || class != DailyQuotaModel && class != DailyQuotaNetworkTools {
		return false, fmt.Errorf("invalid daily quota reservation")
	}
	day := utcDayStart(now).Format("2006-01-02")
	var used int64
	err := db.QueryRowContext(ctx, `
		INSERT INTO daily_quota_reservations (user_id, day, class, used)
		VALUES (?, ?, ?, 1)
		ON CONFLICT(user_id, day, class) DO UPDATE SET used = used + 1
		WHERE daily_quota_reservations.used < ?
		RETURNING used`, userID, day, class, limit).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reserve %s quota for %s: %w", class, day, err)
	}
	return true, nil
}

// ReleaseDailyQuota returns one reservation that never turned into work: a provider that is down, a call
// that timed out before answering, or a tool that failed before reaching the network must not spend the
// user's daily budget. `used` has a CHECK (used > 0), so the last reservation is deleted rather than set
// to zero, in one transaction with the decrement of the others.
func ReleaseDailyQuota(ctx context.Context, db *sql.DB, userID int64, now time.Time, class string) error {
	if userID <= 0 || class != DailyQuotaModel && class != DailyQuotaNetworkTools {
		return fmt.Errorf("invalid daily quota release")
	}
	day := utcDayStart(now).Format("2006-01-02")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("release %s quota for %s: begin: %w", class, day, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM daily_quota_reservations
		WHERE user_id = ? AND day = ? AND class = ? AND used = 1`, userID, day, class); err != nil {
		return fmt.Errorf("release %s quota for %s: delete the last reservation: %w", class, day, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE daily_quota_reservations SET used = used - 1
		WHERE user_id = ? AND day = ? AND class = ? AND used > 1`, userID, day, class); err != nil {
		return fmt.Errorf("release %s quota for %s: decrement: %w", class, day, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("release %s quota for %s: commit: %w", class, day, err)
	}
	return nil
}

func DailyQuotaUsed(ctx context.Context, db *sql.DB, userID int64, now time.Time, class string) (int64, error) {
	if userID <= 0 || class != DailyQuotaModel && class != DailyQuotaNetworkTools {
		return 0, fmt.Errorf("invalid daily quota lookup")
	}
	day := utcDayStart(now).Format("2006-01-02")
	var used int64
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT used FROM daily_quota_reservations
		WHERE user_id = ? AND day = ? AND class = ?), 0)`, userID, day, class).Scan(&used); err != nil {
		return 0, fmt.Errorf("read %s quota for %s: %w", class, day, err)
	}
	return used, nil
}
