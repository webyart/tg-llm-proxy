package store

import (
	"context"
	"database/sql"
	"errors"
)

func SetChatMiniAppFallback(ctx context.Context, db *sql.DB, chatID int64, enabled bool) error {
	value := 0
	if enabled {
		value = 1
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO chat_miniapp_fallback (chat_id, enabled) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET enabled = excluded.enabled`, chatID, value)
	return err
}

func ChatMiniAppFallback(ctx context.Context, db *sql.DB, chatID int64) (bool, bool, error) {
	var value int
	err := db.QueryRowContext(ctx, `SELECT enabled FROM chat_miniapp_fallback WHERE chat_id = ?`, chatID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return value == 1, true, nil
}

func ResetChatMiniAppFallback(ctx context.Context, db *sql.DB, chatID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM chat_miniapp_fallback WHERE chat_id = ?`, chatID)
	return err
}
