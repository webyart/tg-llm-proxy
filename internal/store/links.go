package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SavedLinkMaxTitleRunes   = 200
	SavedLinkMaxContentRunes = 8000
	SavedLinkMaxURLBytes     = 2048
	SavedLinkMaxPerChat      = 50
	SavedLinkListLimit       = 20
	SavedLinkSearchMaxRunes  = 200
)

var (
	ErrSavedLinkLimit = errors.New("saved link chat limit reached")
	ErrSavedLinkInput = errors.New("saved link is invalid")
	ErrSavedLinkQuery = errors.New("saved link search query is invalid")
)

type SavedLink struct {
	ID           int64
	ChatID       int64
	UserID       int64
	Title        string
	CanonicalURL string
	Content      string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func validateSavedLink(link SavedLink) error {
	if link.ChatID == 0 || link.UserID <= 0 {
		return fmt.Errorf("saved link needs a chat and a positive user id")
	}
	if !utf8.ValidString(link.Title) || strings.TrimSpace(link.Title) == "" || utf8.RuneCountInString(link.Title) > SavedLinkMaxTitleRunes || strings.ContainsRune(link.Title, 0) {
		return ErrSavedLinkInput
	}
	if !utf8.ValidString(link.Content) || strings.TrimSpace(link.Content) == "" || utf8.RuneCountInString(link.Content) > SavedLinkMaxContentRunes || strings.ContainsRune(link.Content, 0) {
		return ErrSavedLinkInput
	}
	if len(link.CanonicalURL) == 0 || len(link.CanonicalURL) > SavedLinkMaxURLBytes || strings.ContainsRune(link.CanonicalURL, 0) {
		return ErrSavedLinkInput
	}
	parsed, err := url.Parse(link.CanonicalURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return ErrSavedLinkInput
	}
	return nil
}

const savedLinkColumns = `id, chat_id, user_id, title, canonical_url, content, created_at, updated_at`

func scanSavedLink(scanner reminderScanner) (SavedLink, error) {
	var link SavedLink
	var createdAt, updatedAt string
	if err := scanner.Scan(&link.ID, &link.ChatID, &link.UserID, &link.Title, &link.CanonicalURL, &link.Content, &createdAt, &updatedAt); err != nil {
		return SavedLink{}, err
	}
	var err error
	if link.CreatedAt, err = parseReminderTime(createdAt); err != nil {
		return SavedLink{}, err
	}
	if link.UpdatedAt, err = parseReminderTime(updatedAt); err != nil {
		return SavedLink{}, err
	}
	return link, nil
}

func PutSavedLink(ctx context.Context, db *sql.DB, link SavedLink) (SavedLink, bool, error) {
	link.Title = strings.TrimSpace(link.Title)
	link.Content = strings.TrimSpace(link.Content)
	link.CanonicalURL = strings.TrimSpace(link.CanonicalURL)
	if err := validateSavedLink(link); err != nil {
		return SavedLink{}, false, err
	}
	if err := EnsureChat(ctx, db, link.ChatID, link.UserID, ""); err != nil {
		return SavedLink{}, false, err
	}
	now := time.Now().UTC()
	if !link.CreatedAt.IsZero() {
		now = link.CreatedAt.UTC()
	}
	link.CreatedAt = now
	link.UpdatedAt = now

	var existingID int64
	err := db.QueryRowContext(ctx, `SELECT id FROM saved_links WHERE chat_id = ? AND canonical_url = ?`, link.ChatID, link.CanonicalURL).Scan(&existingID)
	replaced := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SavedLink{}, false, err
	}

	nowText := formatReminderTime(now)
	row := db.QueryRowContext(ctx, `
		INSERT INTO saved_links (chat_id, user_id, title, canonical_url, content, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM saved_links WHERE chat_id = ? AND canonical_url = ?)
			OR (SELECT COUNT(*) FROM saved_links WHERE chat_id = ?) < ?
		ON CONFLICT(chat_id, canonical_url) DO UPDATE SET
			user_id = excluded.user_id,
			title = excluded.title,
			content = excluded.content,
			updated_at = excluded.updated_at
		RETURNING `+savedLinkColumns,
		link.ChatID, link.UserID, link.Title, link.CanonicalURL, link.Content, nowText, nowText,
		link.ChatID, link.CanonicalURL, link.ChatID, SavedLinkMaxPerChat)
	saved, err := scanSavedLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SavedLink{}, false, ErrSavedLinkLimit
	}
	if err != nil {
		return SavedLink{}, false, err
	}
	return saved, replaced, nil
}

func ListSavedLinks(ctx context.Context, db *sql.DB, chatID int64, limit int) ([]SavedLink, error) {
	if limit <= 0 || limit > SavedLinkListLimit {
		limit = SavedLinkListLimit
	}
	rows, err := db.QueryContext(ctx, `SELECT `+savedLinkColumns+` FROM saved_links
		WHERE chat_id = ? ORDER BY updated_at DESC, id DESC LIMIT ?`, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := []SavedLink{}
	for rows.Next() {
		link, scanErr := scanSavedLink(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func GetSavedLink(ctx context.Context, db *sql.DB, chatID int64, id int64) (SavedLink, bool, error) {
	if chatID == 0 || id <= 0 {
		return SavedLink{}, false, nil
	}
	link, err := scanSavedLink(db.QueryRowContext(ctx, `SELECT `+savedLinkColumns+` FROM saved_links WHERE chat_id = ? AND id = ?`, chatID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return SavedLink{}, false, nil
	}
	if err != nil {
		return SavedLink{}, false, err
	}
	return link, true, nil
}

func DeleteSavedLink(ctx context.Context, db *sql.DB, chatID int64, id int64) (bool, error) {
	if chatID == 0 || id <= 0 {
		return false, nil
	}
	result, err := db.ExecContext(ctx, `DELETE FROM saved_links WHERE chat_id = ? AND id = ?`, chatID, id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func ClearSavedLinks(ctx context.Context, db *sql.DB, chatID int64) (int64, error) {
	result, err := db.ExecContext(ctx, `DELETE FROM saved_links WHERE chat_id = ?`, chatID)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

func SearchSavedLinks(ctx context.Context, db *sql.DB, chatID int64, query string, limit int) ([]SavedLink, error) {
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > SavedLinkSearchMaxRunes {
		return nil, ErrSavedLinkQuery
	}
	if limit <= 0 || limit > SavedLinkListLimit {
		limit = SavedLinkListLimit
	}
	rows, err := db.QueryContext(ctx, `SELECT `+savedLinkColumns+` FROM saved_links
		WHERE chat_id = ? ORDER BY updated_at DESC, id DESC LIMIT ?`, chatID, SavedLinkMaxPerChat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	needle := strings.ToLower(query)
	matches := []SavedLink{}
	for rows.Next() && len(matches) < limit {
		link, scanErr := scanSavedLink(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if strings.Contains(strings.ToLower(link.Title), needle) ||
			strings.Contains(strings.ToLower(link.CanonicalURL), needle) ||
			strings.Contains(strings.ToLower(link.Content), needle) {
			matches = append(matches, link)
		}
	}
	return matches, rows.Err()
}
