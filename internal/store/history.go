package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

func ExportMessages(ctx context.Context, db *sql.DB, chatID int64, max int) ([]Message, error) {
	if max <= 0 {
		return nil, errors.New("export limit must be positive")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, chat_id, role, content, tokens_in, tokens_out, model, latency_ms, telegram_message_id, created_at
		FROM messages WHERE chat_id = ? ORDER BY id DESC LIMIT ?`, chatID, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func SearchMessages(ctx context.Context, db *sql.DB, chatID int64, query string, limit int) ([]Message, error) {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return []Message{}, nil
	}
	if limit <= 0 {
		return []Message{}, nil
	}
	rows, err := ExportMessages(ctx, db, chatID, 10000)
	if err != nil {
		return nil, err
	}
	matches := make([]Message, 0, limit)
	for i := len(rows) - 1; i >= 0 && len(matches) < limit; i-- {
		if strings.Contains(strings.ToLower(rows[i].Content), needle) {
			matches = append(matches, rows[i])
		}
	}
	for i, j := 0, len(matches)-1; i < j; i, j = i+1, j-1 {
		matches[i], matches[j] = matches[j], matches[i]
	}
	return matches, nil
}

func DeleteChatHistory(ctx context.Context, db *sql.DB, chatID int64) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE chat_id = ?`, chatID)
	if err != nil {
		return 0, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM bot_state WHERE key IN (?, ?)`, ContextResetIDKey(chatID), StateResumeSkipPrefix+strconv.FormatInt(chatID, 10)); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET context_reset_at = NULL WHERE chat_id = ?`, chatID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}
