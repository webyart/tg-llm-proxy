package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const feedbackIDChunk = 400

var (
	// ErrFeedbackValue — a rating is exactly one of the two buttons.
	ErrFeedbackValue = errors.New("the feedback value must be 1 or -1")
	// ErrFeedbackNotAllowed — only an assistant message of the rating chat can be rated.
	ErrFeedbackNotAllowed = errors.New("feedback is allowed only for an assistant message of this chat")
	// ErrFeedbackNotFound — the message id does not exist at all.
	ErrFeedbackNotFound = errors.New("the message to rate does not exist")
)

// SetMessageFeedback stores the rating of one user on one assistant message.
//
// The primary key is (message_id, user_id), so a second click replaces the first instead of adding a
// row. changed is false when the stored value already equals the requested one.
func SetMessageFeedback(ctx context.Context, db *sql.DB, chatID, userID, messageID int64, value int) (changed bool, err error) {
	if value != 1 && value != -1 {
		return false, ErrFeedbackValue
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var ownerChat int64
	var role string
	err = tx.QueryRowContext(ctx, `SELECT chat_id, role FROM messages WHERE id = ?`, messageID).Scan(&ownerChat, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrFeedbackNotFound
	}
	if err != nil {
		return false, err
	}
	if ownerChat != chatID || role != RoleAssistant {
		return false, ErrFeedbackNotAllowed
	}

	var previous int
	err = tx.QueryRowContext(ctx, `SELECT value FROM message_feedback WHERE message_id = ? AND user_id = ?`, messageID, userID).Scan(&previous)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `
			INSERT INTO message_feedback (message_id, chat_id, user_id, value, created_at)
			VALUES (?, ?, ?, ?, datetime('now'))`, messageID, chatID, userID, value)
		changed = true
	case err != nil:
		return false, err
	case previous != value:
		_, err = tx.ExecContext(ctx, `
			UPDATE message_feedback SET value = ?, created_at = datetime('now')
			WHERE message_id = ? AND user_id = ?`, value, messageID, userID)
		changed = true
	}
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed, nil
}

// MessageFeedbackFor — the current user's ratings of the given messages, keyed by message id.
// Messages without a rating are simply absent from the map.
func MessageFeedbackFor(ctx context.Context, db *sql.DB, chatID, userID int64, messageIDs []int64) (map[int64]int, error) {
	ratings := map[int64]int{}
	for start := 0; start < len(messageIDs); start += feedbackIDChunk {
		end := start + feedbackIDChunk
		if end > len(messageIDs) {
			end = len(messageIDs)
		}
		chunk := messageIDs[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, 2+len(chunk))
		args = append(args, chatID, userID)
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := db.QueryContext(ctx, `
			SELECT message_id, value FROM message_feedback
			WHERE chat_id = ? AND user_id = ? AND message_id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var messageID int64
			var value int
			if err := rows.Scan(&messageID, &value); err != nil {
				rows.Close()
				return nil, err
			}
			ratings[messageID] = value
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return ratings, nil
}

// MessageFeedbackCounts — 👍 and 👎 of the whole chat, across all its users.
func MessageFeedbackCounts(ctx context.Context, db *sql.DB, chatID int64) (up, down int64, err error) {
	err = db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN value = 1 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN value = -1 THEN 1 ELSE 0 END), 0)
		FROM message_feedback WHERE chat_id = ?`, chatID).Scan(&up, &down)
	return up, down, err
}

// LastAssistantMessageID — the newest model answer of the chat, or 0 when the model never answered.
func LastAssistantMessageID(ctx context.Context, db *sql.DB, chatID int64) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `
		SELECT id FROM messages WHERE chat_id = ? AND role = ?
		ORDER BY id DESC LIMIT 1`, chatID, RoleAssistant).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}
