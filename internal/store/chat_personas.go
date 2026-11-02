package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const MaxPersonaPromptRunes = 4000

func SetChatPersona(ctx context.Context, db *sql.DB, chatID int64, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errors.New("persona prompt cannot be empty")
	}
	if len([]rune(prompt)) > MaxPersonaPromptRunes {
		return fmt.Errorf("persona prompt is too long: maximum %d symbols", MaxPersonaPromptRunes)
	}
	if strings.ContainsRune(prompt, '\x00') {
		return errors.New("persona prompt cannot contain NUL")
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO chat_personas (chat_id, prompt) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET prompt = excluded.prompt`, chatID, prompt)
	return err
}

func ChatPersona(ctx context.Context, db *sql.DB, chatID int64) (string, bool, error) {
	var prompt string
	err := db.QueryRowContext(ctx, `SELECT prompt FROM chat_personas WHERE chat_id = ?`, chatID).Scan(&prompt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return prompt, true, nil
}

func ClearChatPersona(ctx context.Context, db *sql.DB, chatID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM chat_personas WHERE chat_id = ?`, chatID)
	return err
}
