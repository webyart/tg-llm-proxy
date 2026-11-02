package store

import (
	"context"
	"database/sql"
	"errors"
)

func SetChatStreaming(ctx context.Context, db *sql.DB, chatID int64, enabled bool) error {
	value := 0
	if enabled {
		value = 1
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO chat_streaming (chat_id, enabled) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET enabled = excluded.enabled`, chatID, value)
	return err
}

func ChatStreaming(ctx context.Context, db *sql.DB, chatID int64) (bool, bool, error) {
	var value int
	err := db.QueryRowContext(ctx, `SELECT enabled FROM chat_streaming WHERE chat_id = ?`, chatID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return value == 1, true, nil
}

func ClearChatStreaming(ctx context.Context, db *sql.DB, chatID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM chat_streaming WHERE chat_id = ?`, chatID)
	return err
}
