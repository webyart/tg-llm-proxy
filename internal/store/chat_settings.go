package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func validModelName(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", errors.New("model name cannot be empty")
	}
	if len(model) > 200 {
		return "", fmt.Errorf("model name is too long: %d bytes, maximum 200", len(model))
	}
	for _, symbol := range model {
		if symbol < 0x20 || symbol == 0x7f {
			return "", errors.New("model name cannot contain control characters")
		}
	}
	return model, nil
}

func SetChatModel(ctx context.Context, db *sql.DB, chatID int64, model string) error {
	model, err := validModelName(model)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO chat_settings (chat_id, model) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET model = excluded.model`, chatID, model)
	return err
}

func ChatModel(ctx context.Context, db *sql.DB, chatID int64) (string, bool, error) {
	var model string
	err := db.QueryRowContext(ctx, `SELECT model FROM chat_settings WHERE chat_id = ?`, chatID).Scan(&model)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return model, true, nil
}

func ClearChatModel(ctx context.Context, db *sql.DB, chatID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM chat_settings WHERE chat_id = ?`, chatID)
	return err
}
