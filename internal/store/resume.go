package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

const StateResumeSkipPrefix = "resume_skip_"

func resumeSkipKey(chatID int64) string {
	return StateResumeSkipPrefix + strconv.FormatInt(chatID, 10)
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

// ClearResumeSkip removes only this chat's suppression. Repeating it is harmless; message history is not touched.
func ClearResumeSkip(ctx context.Context, db *sql.DB, chatID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM bot_state WHERE key = ?`, resumeSkipKey(chatID))
	return err
}
