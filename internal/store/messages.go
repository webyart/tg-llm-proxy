//  1. `uq_messages_chat_tg_id` is the second line of defence against repeated answers after a
//     IsDuplicate says which constraint fired, because the caller has to treat a duplicate as
//     the rows before the mark, `/history` still sees them. ClearChat therefore moves the mark.
//     SQL reaches SQLite — the error text names the allowed set instead of a constraint code.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
)

// StateLastUpdateID — the getUpdates offset of bot_state, read back through bot.WithInitialOffset at every
const StateLastUpdateID = "last_update_id"

const (
	EventDenied     = "denied"
	EventModelError = "model_error"
	EventFlood      = "flood"
	EventCommand    = "command"
	EventUsage      = "usage"
	EventLimit      = "limit"
	// STRATA_TIMEOUT_SECONDS, or the whole answer under TG_REPLY_DEADLINE_SECONDS. Which of the two expired,
	// the detail of the row says by name; measured 2026-10-31 with 15/30 it is the model budget that goes first.
	EventTimeout = "timeout"
)

// ErrDuplicateMessage — the second Append with the same (chat_id, telegram_message_id) hit
var ErrDuplicateMessage = errors.New("this telegram_message_id is already stored for the chat ")

// mistake in the Go layer instead of surfacing as an opaque sqlite constraint violation.
var ErrRole = fmt.Errorf("the message role must be one of %s/%s/%s ", RoleUser, RoleAssistant, RoleSystem)

// answer has no telegram_message_id, and tokens/latency stay NULL when the provider said nothing
type Message struct {
	ID                int64
	ChatID            int64
	UserID            int64
	Role              string
	Content           string
	TokensIn          *int64
	TokensOut         *int64
	Model             string
	LatencyMs         int64
	TelegramMessageID *int64
	CreatedAt         string
}

// operator reads as «nothing failed» unless the two numbers are shown separately.
type ChatStats struct {
	Messages  int64
	TokensIn  int64
	TokensOut int64
	Errors    int64
	Timeouts  int64
	Model     string
	LastAt    string
}

// because an incoming message can arrive before /start: Append calls the same statement, so a
// title stays as it was when the new title is NULL (Telegram gives no title for private chats).
func EnsureChat(ctx context.Context, db *sql.DB, chatID int64, userID int64, title string) error {
	var _, err = db.ExecContext(ctx, `
		INSERT INTO chats (chat_id, user_id, title, created_at, last_active_at)
		VALUES (?, ?, ?, datetime('now'), datetime('now'))
		ON CONFLICT(chat_id) DO UPDATE SET
			user_id      = excluded.user_id,
			title        = COALESCE(excluded.title, chats.title),
			last_active_at = datetime('now')`,
		chatID, userID, nullable(title))
	return err
}

// REFERENCES chats(chat_id) on messages, so an insert into an unregistered chat would be a
func Append(ctx context.Context, db *sql.DB, m Message) (int64, error) {
	if m.Role != RoleUser && m.Role != RoleAssistant && m.Role != RoleSystem {
		return 0, ErrRole
	}
	if strings.TrimSpace(m.Content) == "" {
		return 0, fmt.Errorf("the content of a message is NOT NULL and empty ")
	}
	if err := EnsureChat(ctx, db, m.ChatID, m.UserID, ""); err != nil {
		return 0, err
	}
	var res, err = db.ExecContext(ctx, `
		INSERT INTO messages (chat_id, role, content, tokens_in, tokens_out, model, latency_ms, telegram_message_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ChatID, m.Role, m.Content, m.TokensIn, m.TokensOut, nullable(m.Model), m.LatencyMs, m.TelegramMessageID)
	if err != nil {
		if isUniqueTelegramID(err) {
			return 0, fmt.Errorf("%w: %v", ErrDuplicateMessage, err)
		}
		return 0, err
	}
	var id, idErr = res.LastInsertId()
	if idErr != nil {
		return 0, idErr
	}
	return id, nil
}

func ReplacePendingUserContent(ctx context.Context, db *sql.DB, chatID int64, telegramMessageID int64, content string) error {
	if strings.TrimSpace(content) == "" {
		return errors.New("replacement content is empty")
	}
	res, err := db.ExecContext(ctx, `UPDATE messages SET content = ? WHERE chat_id = ? AND telegram_message_id = ? AND role = ?`, content, chatID, telegramMessageID, RoleUser)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("pending user message update affected %d rows, want 1", count)
	}
	return nil
}

// scanMessages — the one scan of the messages columns, shared by RecentByChat and HistoryOfChat
// nullable columns become pointers, and the caller's newest-first order is reversed here into the
// chronological order the model request needs.
func scanMessages(rows *sql.Rows) ([]Message, error) {
	var out = []Message{}
	for rows.Next() {
		var m = Message{}
		var model sql.NullString // messages.model is TEXT
		var tgID, tokensIn, tokensOut, latency sql.NullInt64
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Role, &m.Content, &tokensIn, &tokensOut,
			&model, &latency, &tgID, &m.CreatedAt); err != nil {
			return nil, err
		}
		if model.Valid {
			m.Model = model.String
		}
		if tokensIn.Valid {
			var v = tokensIn.Int64
			m.TokensIn = &v
		}
		if tokensOut.Valid {
			var v = tokensOut.Int64
			m.TokensOut = &v
		}
		if latency.Valid {
			m.LatencyMs = latency.Int64
		}
		if tgID.Valid {
			var v = tgID.Int64
			m.TelegramMessageID = &v
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ContextResetIDKey — the bot_state key that remembers the id watermark of `/reset` for one chat
// datetime('now') has SECOND resolution, so a message that arrives in the same second as /reset
// watermark is stored as an id and context_reset_at stays the human-readable audit mark.
func ContextResetIDKey(chatID int64) string {
	return fmt.Sprintf("context_reset_id:%d", chatID)
}

// contextFilter — the WHERE fragment of RecentByChat. The id watermark wins when it exists (the
// watermark existed, and "1=1" is a chat that was never reset.
//
// The fragment is built from two fixed strings, never from user input: chatID only ever reaches
func contextFilter(ctx context.Context, db *sql.DB, chatID int64) (string, []any, error) {
	var watermark sql.NullInt64
	var err = db.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM bot_state WHERE key = ?`,
		ContextResetIDKey(chatID)).Scan(&watermark)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	if watermark.Valid {
		return "m.id > ?", []any{watermark.Int64}, nil
	}

	var reset sql.NullString
	err = db.QueryRowContext(ctx, `SELECT context_reset_at FROM chats WHERE chat_id = ?`, chatID).Scan(&reset)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	if reset.Valid {
		return "m.created_at >= ?", []any{reset.String}, nil
	}
	return "1 = 1", nil, nil
}

// oldest → newest so the glued history is chronological. The messages a /reset hid are excluded
func RecentByChat(ctx context.Context, db *sql.DB, chatID int64, limit int) ([]Message, error) {
	if limit <= 0 {
		return []Message{}, nil
	}
	var filter, args, err = contextFilter(ctx, db, chatID)
	if err != nil {
		return nil, err
	}
	var queryArgs = []any{chatID}
	queryArgs = append(queryArgs, args...) // the placeholder of contextFilter, when it has one
	queryArgs = append(queryArgs, limit)   // LIMIT ? is the last placeholder of the statement
	var rows, queryErr = db.QueryContext(ctx, `
		SELECT m.id, m.chat_id, m.role, m.content, m.tokens_in, m.tokens_out, m.model, m.latency_ms, m.telegram_message_id, m.created_at
		FROM messages m
		WHERE m.chat_id = ? AND `+filter+`
		ORDER BY m.id DESC
		LIMIT ?`, queryArgs...)
	if queryErr != nil {
		return nil, queryErr
	}
	defer rows.Close()
	return scanMessages(rows)
}

// different semantics, and one function with a flag would eventually mix them.
func HistoryOfChat(ctx context.Context, db *sql.DB, chatID int64, limit int) ([]Message, error) {
	if limit <= 0 {
		return []Message{}, nil
	}
	var rows, err = db.QueryContext(ctx, `
		SELECT id, chat_id, role, content, tokens_in, tokens_out, model, latency_ms, telegram_message_id, created_at
		FROM messages WHERE chat_id = ? ORDER BY id DESC LIMIT ?`, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// receive an answer? RoleAssistant means it did; RoleUser with this row being the row of an incoming update
// reads this function per chat instead of waiting for an update that never comes. The id is part of the answer,
// not decoration: «the chat ends with a question» and «THIS update's question is still waiting» differ as soon as
// the user asked twice, and only the newest row can be the one awaiting its answer — see RowIDByTelegramMessageID.
// Zero id and empty role with no error
func LastVisibleMessage(ctx context.Context, db *sql.DB, chatID int64) (int64, string, error) {
	var filter, args, err = contextFilter(ctx, db, chatID)
	if err != nil {
		return 0, "", err
	}
	var queryArgs = append([]any{chatID}, args...) // the placeholder of contextFilter, when it has one
	var id int64
	var role string
	var scanErr = db.QueryRowContext(ctx, `
		SELECT m.id, m.role FROM messages m
		WHERE m.chat_id = ? AND `+filter+`
		ORDER BY m.id DESC
		LIMIT 1`, queryArgs...).Scan(&id, &role)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return 0, "", nil
	}
	if scanErr != nil {
		return 0, "", scanErr
	}
	return id, role, nil
}

// the dedup guard used to confuse: an answered question has a newer row after it, an interrupted one is the
// newest row of the chat.
func RowIDByTelegramMessageID(ctx context.Context, db *sql.DB, chatID int64, telegramMessageID int64) (int64, error) {
	var id int64
	var scanErr = db.QueryRowContext(ctx, `
		SELECT id FROM messages WHERE chat_id = ? AND telegram_message_id = ?`,
		chatID, telegramMessageID).Scan(&id)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return 0, nil
	}
	if scanErr != nil {
		return 0, scanErr
	}
	return id, nil
}

// оборванный внутри обработчика». A chat is in that state exactly when its newest VISIBLE row is
// update (LastVisibleMessage), read here per chat and without an update in hand, and that is the point:
// asked for update_id 920551680 from the stored offset 920551679 and got empty batches for 9.5 minutes.
// Nothing ever asks about that question again, so the row would sit unanswered forever.
//
// Only rows with a telegram_message_id qualify. The replay stays idempotent because uq_messages_chat_tg_id
// left out. At most `limit` chats are looked at, newest activity first: the scan must not grow with the database
func UnansweredQuestions(ctx context.Context, db *sql.DB, limit int) ([]Message, error) {
	if limit <= 0 {
		return []Message{}, nil
	}
	// but a scan that keeps a connection open while borrowing another is the kind of cleverness that turns
	// into a deadlock the day someone adds SetMaxOpenConns.
	type chatRow struct {
		chatID int64
		userID int64
	}
	var chats = []chatRow{}
	var chatRows, err = db.QueryContext(ctx, `
		SELECT chat_id, user_id FROM chats
		ORDER BY last_active_at DESC, chat_id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	for chatRows.Next() {
		var one chatRow
		if scanErr := chatRows.Scan(&one.chatID, &one.userID); scanErr != nil {
			chatRows.Close()
			return nil, scanErr
		}
		chats = append(chats, one)
	}
	if err = chatRows.Err(); err != nil {
		chatRows.Close()
		return nil, err
	}
	chatRows.Close()

	var out = []Message{}
	var i = 0
	for i < len(chats) {
		var row, rowErr = lastVisibleRow(ctx, db, chats[i].chatID)
		if rowErr != nil {
			return nil, rowErr
		}
		if row.ID == 0 || row.Role != RoleUser || row.TelegramMessageID == nil {
			i++
			continue
		}
		var skipped, suppress, skipErr = ResumeSkip(ctx, db, chats[i].chatID)
		if skipErr != nil {
			return nil, skipErr
		}
		if suppress && skipped == row.ID {
			i++
			continue
		}
		row.UserID = chats[i].userID
		out = append(out, row)
		i++
	}
	return out, nil
}

// every one of them).
func lastVisibleRow(ctx context.Context, db *sql.DB, chatID int64) (Message, error) {
	var filter, args, err = contextFilter(ctx, db, chatID)
	if err != nil {
		return Message{}, err
	}
	var queryArgs = append([]any{chatID}, args...) // the placeholder of contextFilter, when it has one
	var rows, queryErr = db.QueryContext(ctx, `
		SELECT m.id, m.chat_id, m.role, m.content, m.tokens_in, m.tokens_out, m.model, m.latency_ms, m.telegram_message_id, m.created_at
		FROM messages m
		WHERE m.chat_id = ? AND `+filter+`
		ORDER BY m.id DESC
		LIMIT 1`, queryArgs...)
	if queryErr != nil {
		return Message{}, queryErr
	}
	defer rows.Close()
	var found, scanErr = scanMessages(rows)
	if scanErr != nil {
		return Message{}, scanErr
	}
	if len(found) == 0 {
		return Message{}, nil
	}
	return found[0], nil
}

// `_txlock=immediate` makes the write transaction immediate, so a concurrent writer of another
// connection cannot interleave between the two statements):
//
//	not precise enough: datetime('now') counts seconds).
//
// It returns how many messages the model will no longer see.
func ClearChat(ctx context.Context, db *sql.DB, chatID int64) (int64, error) {
	var tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() // a committed transaction makes this a no-op (database/sql)

	var maxID = int64(0)
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM messages WHERE chat_id = ?`, chatID).Scan(&maxID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chats (chat_id, user_id, created_at, last_active_at)
		VALUES (?, ?, datetime('now'), datetime('now'))
		ON CONFLICT(chat_id) DO UPDATE SET last_active_at = datetime('now')`, chatID, chatID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE chats SET context_reset_at = datetime('now'), last_active_at = datetime('now') WHERE chat_id = ?`, chatID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO bot_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		ContextResetIDKey(chatID), strconv.FormatInt(maxID, 10)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return maxID, nil
}

func Stats(ctx context.Context, db *sql.DB, chatID int64) (ChatStats, error) {
	var stats = ChatStats{}
	var lastAt sql.NullString
	var err = db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0), MAX(created_at)
		FROM messages WHERE chat_id = ?`, chatID).
		Scan(&stats.Messages, &stats.TokensIn, &stats.TokensOut, &lastAt)
	if err != nil {
		return stats, err
	}
	// The model of the LAST answer, not MAX(model). Measured 2026-10-31: a chat answered first by
	// qwen3.8-flash-next-nvfp4-nvfp4-nvidia and later by qwen3.8-flash-next-iq3_s printed the FIRST name
	// in /stats, because MAX over TEXT is lexicographic — and that line is read as «this model serves my
	// No model answered yet → empty string, and the /stats handler prints the line only when it is not empty.
	var lastModel sql.NullString
	if modelErr := db.QueryRowContext(ctx, `
		SELECT m.model FROM messages m
		WHERE m.chat_id = ? AND m.role = ? AND m.model IS NOT NULL
		ORDER BY m.id DESC LIMIT 1`, chatID, RoleAssistant).
		Scan(&lastModel); modelErr != nil && !errors.Is(modelErr, sql.ErrNoRows) {
		return stats, fmt.Errorf("store.Stats: the last answered model of chat %d: %w ", chatID, modelErr)
	}
	if lastModel.Valid {
		stats.Model = lastModel.String
	}
	if lastAt.Valid {
		stats.LastAt = lastAt.String
	}

	var userID sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT user_id FROM chats WHERE chat_id = ?`, chatID).Scan(&userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return stats, err
	}
	if userID.Valid {
		// new service event needs no migration, and `/stats` must not go blind when a new kind appears while the
		var kindRows, rowsErr = db.QueryContext(ctx, `
			SELECT kind, COUNT(*) FROM events WHERE user_id = ? GROUP BY kind`, userID.Int64)
		if rowsErr != nil {
			return stats, fmt.Errorf("store.Stats: the event kinds of user %d: %w ", userID.Int64, rowsErr)
		}
		defer kindRows.Close()
		for kindRows.Next() {
			var kind = ""
			var count = int64(0)
			if scanErr := kindRows.Scan(&kind, &count); scanErr != nil {
				return stats, fmt.Errorf("store.Stats: an events row of user %d: %w ", userID.Int64, scanErr)
			}
			switch kind {
			case EventModelError:
				stats.Errors = count
			case EventTimeout:
				stats.Timeouts = count
			}
		}
		if rowsErr = kindRows.Err(); rowsErr != nil {
			return stats, fmt.Errorf("store.Stats: reading the event kinds of user %d: %w ", userID.Int64, rowsErr)
		}
	}
	return stats, nil
}

// The detail carries the class of the error, NEVER the token and never the full text of the model
func LogEvent(ctx context.Context, db *sql.DB, userID int64, kind string, detail string) error {
	var _, err = db.ExecContext(ctx, `INSERT INTO events (user_id, kind, detail) VALUES (?, ?, ?)`,
		userID, kind, nullable(detail))
	return err
}

// ONLY thing written is events(kind='model_error'), which a test counts with this helper).
func CountEvents(ctx context.Context, db *sql.DB, kind string) (int64, error) {
	var count = int64(0)
	var err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE kind = ?`, kind).Scan(&count)
	return count, err
}

// keeps lastUpdateID in memory only, so the value has to be written after every processed update
func SetState(ctx context.Context, db *sql.DB, key string, value string) error {
	var _, err = db.ExecContext(ctx, `
		INSERT INTO bot_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// GetState — the value of one bot_state key. The bool is "the key exists": a fresh database has no
// 'last_update_id' at all, and starting from 0 is a different decision than starting from a stored
func GetState(ctx context.Context, db *sql.DB, key string) (string, bool, error) {
	var value sql.NullString
	var err = db.QueryRowContext(ctx, `SELECT value FROM bot_state WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value.String, true, nil
}

func SetLastUpdateID(ctx context.Context, db *sql.DB, offset int64) error {
	return SetState(ctx, db, StateLastUpdateID, strconv.FormatInt(offset, 10))
}

// A value that is not an integer is reported as an error rather than treated as 0: a corrupted
func LastUpdateID(ctx context.Context, db *sql.DB) (int64, bool, error) {
	var text, found, err = GetState(ctx, db, StateLastUpdateID)
	if err != nil || !found {
		return 0, false, err
	}
	var offset, parseErr = strconv.ParseInt(text, 10, 64)
	if parseErr != nil {
		return 0, false, fmt.Errorf("bot_state %s = %q is not an integer offset : %w", StateLastUpdateID, text, parseErr)
	}
	return offset, true, nil
}

// and anything else as an error to report.
func IsDuplicate(err error) bool {
	return errors.Is(err, ErrDuplicateMessage)
}

// isUniqueTelegramID — the driver-level recognition of the constraint. The message of SQLite names
// the columns ("UNIQUE constraint failed: messages.chat_id, messages.telegram_message_id"), so the
// code alone (SQLITE_CONSTRAINT) is not enough to tell the telegram_message_id guard apart from,
func isUniqueTelegramID(err error) bool {
	var driverErr = (*sqlite.Error)(nil)
	if !errors.As(err, &driverErr) {
		return false
	}
	var code = driverErr.Code()
	if code != sqlite3.SQLITE_CONSTRAINT_UNIQUE && code&0xff != sqlite3.SQLITE_CONSTRAINT {
		return false
	}
	return strings.Contains(driverErr.Error(), "messages.telegram_message_id")
}

// ответов модели»).
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
