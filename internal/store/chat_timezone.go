package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	// The zone names written by SetChatTimezone must load in a minimal container as well. The run image
	// installs tzdata; embedding the database keeps the store package self-sufficient in tests and CLIs.
	_ "time/tzdata"

	"bot/internal/config"
)

// ErrInvalidTimezone is returned for an empty, oversized, path-shaped or unknown IANA zone. Callers map it
// to a 400 / a usage reply; a programming error is not silently stored.
var ErrInvalidTimezone = errors.New("invalid IANA timezone")

// NormalizeTimezone validates an IANA zone name. "Local" is refused on purpose: it depends on the host and
// would make the same chat render differently on two deployments. Path separators and ".." are refused
// before time.LoadLocation so a crafted name cannot ask the loader to leave the zone database.
func NormalizeTimezone(value string) (string, *time.Location, error) {
	name := strings.TrimSpace(value)
	if !config.ValidTimezoneName(name) {
		return "", nil, fmt.Errorf("%w: %q is not an IANA zone name", ErrInvalidTimezone, name)
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %q", ErrInvalidTimezone, name)
	}
	return name, location, nil
}

// SetChatTimezone upserts the per-chat override. The chats row must exist: the foreign key is the same
// ownership boundary the Mini App session and a Telegram private chat use.
func SetChatTimezone(ctx context.Context, db *sql.DB, chatID int64, timezone string) error {
	if chatID == 0 {
		return fmt.Errorf("%w: a chat id is required", ErrInvalidTimezone)
	}
	name, _, err := NormalizeTimezone(timezone)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO chat_timezone (chat_id, timezone, updated_at) VALUES (?, ?, datetime('now'))
		ON CONFLICT(chat_id) DO UPDATE SET timezone = excluded.timezone, updated_at = excluded.updated_at`,
		chatID, name)
	return err
}

// GetChatTimezone returns the override and whether one exists; a missing row means DEFAULT_TIMEZONE.
func GetChatTimezone(ctx context.Context, db *sql.DB, chatID int64) (string, bool, error) {
	var timezone string
	err := db.QueryRowContext(ctx, `SELECT timezone FROM chat_timezone WHERE chat_id = ?`, chatID).Scan(&timezone)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return timezone, true, nil
}

// DeleteChatTimezone removes the override, so the chat falls back to DEFAULT_TIMEZONE. Deleting a missing row
// is not an error: "reset" is idempotent.
func DeleteChatTimezone(ctx context.Context, db *sql.DB, chatID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM chat_timezone WHERE chat_id = ?`, chatID)
	return err
}
