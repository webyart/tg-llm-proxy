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
