package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ChatLanguages are the languages the bot interface can speak. The model answer itself is not translated.
var ChatLanguages = map[string]bool{"ru": true, "en": true}

// SetChatLanguage stores the per-chat interface language; an unknown value is a programming error, not a user
// input error, so it is rejected instead of silently stored.
func SetChatLanguage(ctx context.Context, db *sql.DB, chatID int64, language string) error {
	language = strings.ToLower(strings.TrimSpace(language))
	if !ChatLanguages[language] {
		return fmt.Errorf("unsupported chat language %q", language)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO chat_language (chat_id, language) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET language = excluded.language`, chatID, language)
	return err
}

// ChatLanguage returns the override and whether one exists; the caller falls back to DEFAULT_LANGUAGE.
func ChatLanguage(ctx context.Context, db *sql.DB, chatID int64) (string, bool, error) {
	var language string
	err := db.QueryRowContext(ctx, `SELECT language FROM chat_language WHERE chat_id = ?`, chatID).Scan(&language)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return language, true, nil
}

func ResetChatLanguage(ctx context.Context, db *sql.DB, chatID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM chat_language WHERE chat_id = ?`, chatID)
	return err
}
