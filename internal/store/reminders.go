package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ReminderCadenceOnce      = "once"
	ReminderCadenceDaily     = "daily"
	ReminderStatusActive     = "active"
	ReminderStatusSending    = "sending"
	ReminderStatusSent       = "sent"
	ReminderStatusCancelled  = "cancelled"
	ReminderStatusFailed     = "failed"
	ReminderMaxTextRunes     = 500
	ReminderMaxActivePerChat = 20
	ReminderListLimit        = 20
)

var (
	ErrReminderLimit   = errors.New("reminder active limit reached")
	ErrReminderText    = errors.New("reminder text is invalid")
	ErrReminderCadence = errors.New("reminder cadence is invalid")
	ErrReminderTime    = errors.New("reminder time is invalid")
)

const reminderTimeLayout = "2006-01-02T15:04:05.000000000Z"

type Reminder struct {
	ID         int64
	ChatID     int64
	UserID     int64
	Text       string
	Cadence    string
	DailyTime  string
	NextRunAt  time.Time
	Status     string
	Attempts   int
	ClaimToken string
	LeaseUntil *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastSentAt *time.Time
	FinishedAt *time.Time
}

func formatReminderTime(value time.Time) string {
	return value.UTC().Format(reminderTimeLayout)
}

func parseReminderTime(value string) (time.Time, error) {
	parsed, err := time.Parse(reminderTimeLayout, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid stored reminder timestamp: %w", err)
	}
	return parsed.UTC(), nil
}

func validateDailyTime(value string) error {
	if len(value) != 5 || value[2] != ':' {
		return ErrReminderTime
	}
	parsed, err := time.Parse("15:04", value)
	if err != nil || parsed.Format("15:04") != value {
		return ErrReminderTime
	}
	return nil
}

func validateReminder(reminder Reminder) error {
	if reminder.ChatID == 0 || reminder.UserID <= 0 {
		return fmt.Errorf("reminder needs a chat and a positive user id")
	}
	if strings.TrimSpace(reminder.Text) == "" || len([]rune(reminder.Text)) > ReminderMaxTextRunes || strings.ContainsRune(reminder.Text, '\x00') {
		return ErrReminderText
	}
	if reminder.NextRunAt.IsZero() {
		return ErrReminderTime
	}
	switch reminder.Cadence {
	case ReminderCadenceOnce:
		if reminder.DailyTime != "" {
			return ErrReminderCadence
		}
	case ReminderCadenceDaily:
		if err := validateDailyTime(reminder.DailyTime); err != nil {
			return err
		}
	default:
		return ErrReminderCadence
	}
	return nil
}

func CreateReminder(ctx context.Context, db *sql.DB, reminder Reminder) (Reminder, error) {
	reminder.Text = strings.TrimSpace(reminder.Text)
	if err := validateReminder(reminder); err != nil {
		return Reminder{}, err
	}
	now := time.Now().UTC()
	if !reminder.CreatedAt.IsZero() {
		now = reminder.CreatedAt.UTC()
	}
	reminder.CreatedAt = now
	reminder.UpdatedAt = now
	reminder.Status = ReminderStatusActive
	reminder.Attempts = 0
	reminder.ClaimToken = ""
	reminder.LeaseUntil = nil
	var daily any
	if reminder.Cadence == ReminderCadenceDaily {
		daily = reminder.DailyTime
	}
	result, err := db.ExecContext(ctx, `
		INSERT INTO reminders (chat_id, user_id, text, cadence, daily_time, next_run_at, status, attempts, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, 'active', 0, ?, ?
		WHERE (SELECT COUNT(*) FROM reminders WHERE chat_id = ? AND status IN ('active', 'sending')) < ?`,
		reminder.ChatID, reminder.UserID, reminder.Text, reminder.Cadence, daily,
		formatReminderTime(reminder.NextRunAt), formatReminderTime(now), formatReminderTime(now),
		reminder.ChatID, ReminderMaxActivePerChat)
	if err != nil {
		return Reminder{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Reminder{}, err
	}
	if affected != 1 {
		return Reminder{}, ErrReminderLimit
	}
	reminder.ID, err = result.LastInsertId()
	if err != nil {
		return Reminder{}, err
	}
	return reminder, nil
}

type reminderScanner interface {
	Scan(dest ...any) error
}

const reminderColumns = `id, chat_id, user_id, text, cadence, COALESCE(daily_time, ''), next_run_at, status, attempts,
	COALESCE(claim_token, ''), COALESCE(lease_until, ''), created_at, updated_at, COALESCE(last_sent_at, ''), COALESCE(finished_at, '')`

func scanReminder(scanner reminderScanner) (Reminder, error) {
	var reminder Reminder
	var nextRun, leaseUntil, createdAt, updatedAt, lastSentAt, finishedAt string
	if err := scanner.Scan(&reminder.ID, &reminder.ChatID, &reminder.UserID, &reminder.Text, &reminder.Cadence,
		&reminder.DailyTime, &nextRun, &reminder.Status, &reminder.Attempts, &reminder.ClaimToken, &leaseUntil,
		&createdAt, &updatedAt, &lastSentAt, &finishedAt); err != nil {
		return Reminder{}, err
	}
	var err error
	if reminder.NextRunAt, err = parseReminderTime(nextRun); err != nil {
		return Reminder{}, err
	}
	if reminder.CreatedAt, err = parseReminderTime(createdAt); err != nil {
		return Reminder{}, err
	}
	if reminder.UpdatedAt, err = parseReminderTime(updatedAt); err != nil {
		return Reminder{}, err
	}
	if leaseUntil != "" {
		value, parseErr := parseReminderTime(leaseUntil)
		if parseErr != nil {
			return Reminder{}, parseErr
		}
		reminder.LeaseUntil = &value
	}
	if lastSentAt != "" {
		value, parseErr := parseReminderTime(lastSentAt)
		if parseErr != nil {
			return Reminder{}, parseErr
		}
		reminder.LastSentAt = &value
	}
	if finishedAt != "" {
		value, parseErr := parseReminderTime(finishedAt)
		if parseErr != nil {
			return Reminder{}, parseErr
		}
		reminder.FinishedAt = &value
	}
	return reminder, nil
}

func ListReminders(ctx context.Context, db *sql.DB, chatID int64, limit int) ([]Reminder, error) {
	if limit <= 0 || limit > ReminderListLimit {
		limit = ReminderListLimit
	}
	rows, err := db.QueryContext(ctx, `SELECT `+reminderColumns+` FROM reminders
		WHERE chat_id = ? AND status IN ('active', 'sending') ORDER BY next_run_at, id LIMIT ?`, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reminders := []Reminder{}
	for rows.Next() {
		reminder, scanErr := scanReminder(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		reminders = append(reminders, reminder)
	}
	return reminders, rows.Err()
}

func CancelReminder(ctx context.Context, db *sql.DB, chatID int64, reminderID int64, now time.Time) (bool, error) {
	if chatID == 0 || reminderID <= 0 {
		return false, nil
	}
	result, err := db.ExecContext(ctx, `UPDATE reminders
		SET status = 'cancelled', updated_at = ?, finished_at = ?, claim_token = NULL, lease_until = NULL
		WHERE id = ? AND chat_id = ? AND status IN ('active', 'sending')`,
		formatReminderTime(now), formatReminderTime(now), reminderID, chatID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func reminderClaimToken() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func ClaimNextReminder(ctx context.Context, db *sql.DB, now time.Time, lease time.Duration) (Reminder, bool, error) {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	token, err := reminderClaimToken()
	if err != nil {
		return Reminder{}, false, err
	}
	nowText := formatReminderTime(now)
	leaseText := formatReminderTime(now.Add(lease))
	row := db.QueryRowContext(ctx, `UPDATE reminders
		SET status = 'sending', attempts = attempts + 1, claim_token = ?, lease_until = ?, updated_at = ?
		WHERE id = (
			SELECT id FROM reminders
			WHERE next_run_at <= ? AND (status = 'active' OR (status = 'sending' AND lease_until <= ?))
			ORDER BY next_run_at, id LIMIT 1
		) AND next_run_at <= ? AND (status = 'active' OR (status = 'sending' AND lease_until <= ?))
		RETURNING `+reminderColumns,
		token, leaseText, nowText, nowText, nowText, nowText, nowText)
	reminder, err := scanReminder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Reminder{}, false, nil
	}
	if err != nil {
		return Reminder{}, false, err
	}
	return reminder, true, nil
}

func CompleteReminder(ctx context.Context, db *sql.DB, reminder Reminder, now time.Time, dailyNext time.Time) (bool, error) {
	if reminder.ID <= 0 || reminder.ClaimToken == "" {
		return false, fmt.Errorf("reminder completion needs a claimed row")
	}
	status := ReminderStatusSent
	nextRun := reminder.NextRunAt
	var finished any = formatReminderTime(now)
	attempts := reminder.Attempts
	if reminder.Cadence == ReminderCadenceDaily {
		if dailyNext.IsZero() || !dailyNext.After(now) {
			return false, ErrReminderTime
		}
		status = ReminderStatusActive
		nextRun = dailyNext
		finished = nil
		attempts = 0
	}
	result, err := db.ExecContext(ctx, `UPDATE reminders
		SET status = ?, next_run_at = ?, attempts = ?, updated_at = ?, last_sent_at = ?, finished_at = ?,
			claim_token = NULL, lease_until = NULL
		WHERE id = ? AND status = 'sending' AND claim_token = ?`,
		status, formatReminderTime(nextRun), attempts, formatReminderTime(now), formatReminderTime(now), finished,
		reminder.ID, reminder.ClaimToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func RetryReminder(ctx context.Context, db *sql.DB, reminder Reminder, now time.Time, retryAt time.Time, exhausted bool, dailyNext time.Time) (bool, error) {
	if reminder.ID <= 0 || reminder.ClaimToken == "" {
		return false, fmt.Errorf("reminder retry needs a claimed row")
	}
	status := ReminderStatusActive
	nextRun := retryAt
	attempts := reminder.Attempts
	var finished any
	if exhausted && reminder.Cadence == ReminderCadenceOnce {
		status = ReminderStatusFailed
		nextRun = reminder.NextRunAt
		finished = formatReminderTime(now)
	} else if exhausted {
		if dailyNext.IsZero() || !dailyNext.After(now) {
			return false, ErrReminderTime
		}
		nextRun = dailyNext
		attempts = 0
	}
	result, err := db.ExecContext(ctx, `UPDATE reminders
		SET status = ?, next_run_at = ?, attempts = ?, updated_at = ?, finished_at = ?, claim_token = NULL, lease_until = NULL
		WHERE id = ? AND status = 'sending' AND claim_token = ?`,
		status, formatReminderTime(nextRun), attempts, formatReminderTime(now), finished, reminder.ID, reminder.ClaimToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func CancelClaimedReminder(ctx context.Context, db *sql.DB, reminder Reminder, now time.Time) (bool, error) {
	if reminder.ID <= 0 || reminder.ClaimToken == "" {
		return false, fmt.Errorf("reminder cancellation needs a claimed row")
	}
	result, err := db.ExecContext(ctx, `UPDATE reminders
		SET status = 'cancelled', updated_at = ?, finished_at = ?, claim_token = NULL, lease_until = NULL
		WHERE id = ? AND status = 'sending' AND claim_token = ?`,
		formatReminderTime(now), formatReminderTime(now), reminder.ID, reminder.ClaimToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}
