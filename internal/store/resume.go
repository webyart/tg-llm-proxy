package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const StateResumeSkipPrefix = "resume_skip_"
const StateResumeRecoveryPrefix = "resume_recovery_"

type ResumeRecoveryState struct {
	QuestionID     int64
	Attempts       int
	NextRecoveryAt time.Time
}

func resumeSkipKey(chatID int64) string {
	return StateResumeSkipPrefix + strconv.FormatInt(chatID, 10)
}

func resumeRecoveryKey(chatID int64) string {
	return StateResumeRecoveryPrefix + strconv.FormatInt(chatID, 10)
}

// SetResumeSkip suppresses startup recovery of one stored question without hiding or deleting it. A newer
// question has another row id and is therefore unaffected.
func SetResumeSkip(ctx context.Context, db *sql.DB, chatID int64, questionID int64) error {
	if questionID <= 0 {
		return fmt.Errorf("resume skip needs a positive question row id, got %d", questionID)
	}
	return SetState(ctx, db, resumeSkipKey(chatID), strconv.FormatInt(questionID, 10))
}

// ResumeSkip returns the question row suppressed by the operator, if any. Corrupt state is an error rather
// than silently enabling or disabling recovery.
func ResumeSkip(ctx context.Context, db *sql.DB, chatID int64) (int64, bool, error) {
	var key = resumeSkipKey(chatID)
	var text, found, err = GetState(ctx, db, key)
	if err != nil || !found {
		return 0, false, err
	}
	var questionID, parseErr = strconv.ParseInt(text, 10, 64)
	if parseErr != nil || questionID <= 0 {
		if parseErr == nil {
			parseErr = fmt.Errorf("the row id must be positive")
		}
		return 0, false, fmt.Errorf("bot_state %s = %q is not a valid question row id: %w", key, text, parseErr)
	}
	return questionID, true, nil
}

// ClearResumeSkip removes this chat's suppression and resets its recovery backoff. Message history is not touched.
func ClearResumeSkip(ctx context.Context, db *sql.DB, chatID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM bot_state WHERE key IN (?, ?)`, resumeSkipKey(chatID), resumeRecoveryKey(chatID))
	return err
}

func ResumeRecovery(ctx context.Context, db *sql.DB, chatID int64, questionID int64) (ResumeRecoveryState, bool, error) {
	key := resumeRecoveryKey(chatID)
	text, found, err := GetState(ctx, db, key)
	if err != nil || !found {
		return ResumeRecoveryState{}, false, err
	}
	parts := strings.Split(text, "|")
	if len(parts) != 3 {
		return ResumeRecoveryState{}, false, fmt.Errorf("bot_state %s = %q is not valid recovery state", key, text)
	}
	storedQuestionID, questionErr := strconv.ParseInt(parts[0], 10, 64)
	attempts, attemptsErr := strconv.Atoi(parts[1])
	nextRecoveryAt, timeErr := time.Parse(time.RFC3339Nano, parts[2])
	if questionErr != nil || storedQuestionID <= 0 || attemptsErr != nil || attempts <= 0 || timeErr != nil {
		return ResumeRecoveryState{}, false, fmt.Errorf("bot_state %s = %q is not valid recovery state", key, text)
	}
	state := ResumeRecoveryState{QuestionID: storedQuestionID, Attempts: attempts, NextRecoveryAt: nextRecoveryAt}
	if storedQuestionID != questionID {
		return ResumeRecoveryState{}, false, nil
	}
	return state, true, nil
}

func SetResumeRecovery(ctx context.Context, db *sql.DB, chatID int64, questionID int64, attempts int, nextRecoveryAt time.Time) error {
	if questionID <= 0 {
		return fmt.Errorf("resume recovery needs a positive question row id, got %d", questionID)
	}
	if attempts <= 0 {
		return fmt.Errorf("resume recovery needs a positive attempt count, got %d", attempts)
	}
	if nextRecoveryAt.IsZero() {
		return fmt.Errorf("resume recovery needs next_recovery_at")
	}
	value := fmt.Sprintf("%d|%d|%s", questionID, attempts, nextRecoveryAt.UTC().Format(time.RFC3339Nano))
	return SetState(ctx, db, resumeRecoveryKey(chatID), value)
}

func ClearResumeRecovery(ctx context.Context, db *sql.DB, chatID int64, questionID int64) error {
	if questionID <= 0 {
		return fmt.Errorf("resume recovery needs a positive question row id, got %d", questionID)
	}
	_, err := db.ExecContext(ctx, `DELETE FROM bot_state WHERE key = ? AND value LIKE ?`,
		resumeRecoveryKey(chatID), strconv.FormatInt(questionID, 10)+"|%")
	return err
}
