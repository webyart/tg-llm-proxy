package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

func ExportMessages(ctx context.Context, db *sql.DB, chatID int64, max int) ([]Message, error) {
	if max <= 0 {
		return nil, errors.New("export limit must be positive")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, chat_id, user_id, role, content, tokens_in, tokens_out, model, latency_ms, first_token_ms, telegram_message_id, created_at
		FROM messages WHERE chat_id = ? ORDER BY id DESC LIMIT ?`, chatID, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

type MessageExportWindow struct {
	Count           int
	OldestID        int64
	NewestID        int64
	OldestCreatedAt string
	NewestCreatedAt string
}

var ErrExportContentTooLarge = errors.New("history export content exceeds the byte limit")

func StreamExportMessages(ctx context.Context, db *sql.DB, chatID int64, max int, onWindow func(MessageExportWindow, bool) error, onMessage func(Message) error) (MessageExportWindow, bool, error) {
	return StreamExportMessagesBounded(ctx, db, chatID, max, 0, onWindow, onMessage)
}

func StreamExportMessagesBounded(ctx context.Context, db *sql.DB, chatID int64, max int, contentLimit int64, onWindow func(MessageExportWindow, bool) error, onMessage func(Message) error) (MessageExportWindow, bool, error) {
	if max <= 0 {
		return MessageExportWindow{}, false, errors.New("export limit must be positive")
	}
	// No transaction here on purpose. The runtime DSN sets `_txlock=immediate`, so ANY transaction of this pool
	// begins by taking the write lock — a 10 000-row export in one transaction froze every write of the bot for
	// its whole duration. The export is instead a sequence of short reads bounded by an explicit id range: the
	// range is fixed by the window queries below, so the file still describes one contiguous slice of history,
	// and under WAL each statement sees a consistent snapshot without blocking writers.
	var total int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT id FROM messages WHERE chat_id = ? ORDER BY id DESC LIMIT ?)`, chatID, max+1).Scan(&total); err != nil {
		return MessageExportWindow{}, false, err
	}
	window := MessageExportWindow{}
	if total == 0 {
		if err := onWindow(window, false); err != nil {
			return window, false, err
		}
		return window, false, nil
	}
	windowCount := total
	if windowCount > int64(max) {
		windowCount = int64(max)
	}
	window.Count = int(windowCount)
	if err := db.QueryRowContext(ctx, `SELECT id, created_at FROM messages WHERE chat_id = ? ORDER BY id DESC LIMIT 1`, chatID).Scan(&window.NewestID, &window.NewestCreatedAt); err != nil {
		return MessageExportWindow{}, false, err
	}
	if err := db.QueryRowContext(ctx, `SELECT id, created_at FROM messages WHERE chat_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?`, chatID, windowCount-1).Scan(&window.OldestID, &window.OldestCreatedAt); err != nil {
		return MessageExportWindow{}, false, err
	}
	truncated := total > int64(max)
	if err := onWindow(window, truncated); err != nil {
		return window, truncated, err
	}
	if contentLimit > 0 {
		var oversized int64
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE chat_id = ? AND id >= ? AND id <= ? AND length(content) > ?`, chatID, window.OldestID, window.NewestID, contentLimit).Scan(&oversized); err != nil {
			return window, truncated, err
		}
		if oversized > 0 {
			return window, truncated, ErrExportContentTooLarge
		}
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, chat_id, role,
			CASE WHEN ? > 0 AND length(content) > ? THEN '' ELSE content END,
			tokens_in, tokens_out, model, latency_ms, first_token_ms, telegram_message_id, created_at, length(content)
		FROM messages WHERE chat_id = ? AND id >= ? AND id <= ? ORDER BY id ASC`, contentLimit, contentLimit, chatID, window.OldestID, window.NewestID)
	if err != nil {
		return window, truncated, err
	}
	defer rows.Close()
	for rows.Next() {
		row, err := scanExportMessage(rows, contentLimit)
		if err != nil {
			return window, truncated, err
		}
		if err := onMessage(row); err != nil {
			return window, truncated, err
		}
	}
	if err := rows.Err(); err != nil {
		return window, truncated, err
	}
	if err := rows.Close(); err != nil {
		return window, truncated, err
	}
	return window, truncated, nil
}

func scanExportMessage(rows *sql.Rows, contentLimit int64) (Message, error) {
	var row Message
	var model sql.NullString
	var telegramID, tokensIn, tokensOut, latency, firstToken sql.NullInt64
	var contentLength int64
	if err := rows.Scan(&row.ID, &row.ChatID, &row.Role, &row.Content, &tokensIn, &tokensOut, &model, &latency, &firstToken, &telegramID, &row.CreatedAt, &contentLength); err != nil {
		return Message{}, err
	}
	if contentLimit > 0 {
		row.ContentLength = contentLength
	}
	if model.Valid {
		row.Model = model.String
	}
	if tokensIn.Valid {
		value := tokensIn.Int64
		row.TokensIn = &value
	}
	if tokensOut.Valid {
		value := tokensOut.Int64
		row.TokensOut = &value
	}
	if latency.Valid {
		row.LatencyMs = latency.Int64
	}
	if firstToken.Valid {
		value := firstToken.Int64
		row.FirstTokenMs = &value
	}
	if telegramID.Valid {
		value := telegramID.Int64
		row.TelegramMessageID = &value
	}
	return row, nil
}

// SearchMessages — full-text search over the history of one chat.
//
// It used to read the newest 10 000 messages of the chat and call strings.Contains on each one: every search
// cost tens of megabytes and a full scan, and a match older than the window could not be found at all. The
// FTS5 index (see fts.go) answers the same question from the index alone, in the whole history.
func SearchMessages(ctx context.Context, db *sql.DB, chatID int64, query string, limit int) ([]Message, error) {
	var expression, ok = messageSearchExpression(query)
	if !ok {
		return []Message{}, nil
	}
	if limit <= 0 {
		return []Message{}, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, m.chat_id, m.user_id, m.role, m.content, m.tokens_in, m.tokens_out, m.model, m.latency_ms, m.first_token_ms, m.telegram_message_id, m.created_at
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		WHERE messages_fts MATCH ? AND m.chat_id = ?
		ORDER BY m.id DESC LIMIT ?`, expression, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	matches := make([]Message, 0, limit)
	for rows.Next() {
		row, scanErr := scanSearchMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		matches = append(matches, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Oldest first: the caller prints the result in conversation order.
	for i, j := 0, len(matches)-1; i < j; i, j = i+1, j-1 {
		matches[i], matches[j] = matches[j], matches[i]
	}
	return matches, nil
}

func scanSearchMessage(rows *sql.Rows) (Message, error) {
	var row Message
	var model sql.NullString
	var telegramID, tokensIn, tokensOut, latency, firstToken sql.NullInt64
	if err := rows.Scan(&row.ID, &row.ChatID, &row.UserID, &row.Role, &row.Content, &tokensIn, &tokensOut,
		&model, &latency, &firstToken, &telegramID, &row.CreatedAt); err != nil {
		return Message{}, err
	}
	if model.Valid {
		row.Model = model.String
	}
	if tokensIn.Valid {
		value := tokensIn.Int64
		row.TokensIn = &value
	}
	if tokensOut.Valid {
		value := tokensOut.Int64
		row.TokensOut = &value
	}
	if latency.Valid {
		row.LatencyMs = latency.Int64
	}
	if firstToken.Valid {
		value := firstToken.Int64
		row.FirstTokenMs = &value
	}
	if telegramID.Valid {
		value := telegramID.Int64
		row.TelegramMessageID = &value
	}
	return row, nil
}

func DeleteMessage(ctx context.Context, db *sql.DB, chatID int64, id int64) (bool, error) {
	if chatID == 0 || id <= 0 {
		return false, nil
	}
	result, err := db.ExecContext(ctx, `DELETE FROM messages WHERE chat_id = ? AND id = ? AND role IN (?, ?)`, chatID, id, RoleUser, RoleAssistant)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
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
