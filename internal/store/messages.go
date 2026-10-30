// messages.go — the CRUD of §10 День 2 over the schema of §5: Append, RecentByChat, ClearChat,
// Stats (§10 День 2 DoD names exactly these four), plus the two service tables the same §5
// defines — `bot_state` (the offset of getUpdates that survives a restart, §5 «Offset long
// polling») and `events` (kind='denied' | 'model_error' | 'flood', §6 and §10 День 3).
//
// Three rules of §5/§10 shape this file:
//  1. `uq_messages_chat_tg_id` is the second line of defence against repeated answers after a
//     restart (§2 п.7): a second Append with the same telegram_message_id must fail loudly, and
//     IsDuplicate says which constraint fired, because the caller has to treat a duplicate as
//     "already answered" rather than as a broken database (§13 risk "повторные ответы").
//  2. `/reset` does not delete history (§5 `context_reset_at`, §6 «/reset»): RecentByChat hides
//     the rows before the mark, `/history` still sees them. ClearChat therefore moves the mark.
//  3. role is checked by the schema CHECK (§5), so an unknown role is rejected here before the
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

// The roles of the §5 CHECK constraint on messages.role.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
)

// StateLastUpdateID — the only key of bot_state in v1 (§5: the offset of getUpdates, read back
// through bot.WithInitialOffset at every start, §10 День 4).
const StateLastUpdateID = "last_update_id"

// The kinds of events of §6 and §10 День 3/День 4: a denied user, a model failure after every
// retry, a Telegram 429. The kind is free TEXT in §5, so the constants live here.
const (
	EventDenied     = "denied"
	EventModelError = "model_error"
	EventFlood      = "flood"
	EventCommand    = "command"
)

// ErrDuplicateMessage — the second Append with the same (chat_id, telegram_message_id) hit
// uq_messages_chat_tg_id (§5). IsDuplicate reports it to the caller, which answers "already
// processed" instead of retrying an insert that will never succeed (§13 risk).
var ErrDuplicateMessage = errors.New("this telegram_message_id is already stored for the chat (§5 uq_messages_chat_tg_id)")

// ErrRole — a role outside the §5 CHECK ('user','assistant','system'). Naming it here keeps the
// mistake in the Go layer instead of surfacing as an opaque sqlite constraint violation.
var ErrRole = fmt.Errorf("the message role must be one of %s/%s/%s (§5 CHECK on messages.role)", RoleUser, RoleAssistant, RoleSystem)

// Message — one row of the messages table of §5. The pointers are the nullable columns: a model
// answer has no telegram_message_id, and tokens/latency stay NULL when the provider said nothing
// (§5 «NULL для ответов модели»). CreatedAt is filled by SQLite (datetime('now'), UTC — §5).
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

// ChatStats — the counters of `/stats` (§6: messages, tokens, errors of one chat).
type ChatStats struct {
	Messages  int64
	TokensIn  int64
	TokensOut int64
	Errors    int64
	Model     string
	LastAt    string
}

// EnsureChat — the registration of `/start` (§6: "регистрация чата в SQLite"). It is an upsert
// because an incoming message can arrive before /start: Append calls the same statement, so a
// chat row always exists and foreign_keys=1 of the DSN (§5) cannot reject the insert.
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

// Append — one message of one chat (§5). The chat row is ensured in the same call: §5 puts
// REFERENCES chats(chat_id) on messages, so an insert into an unregistered chat would be a
// foreign-key violation, and the DSN turns foreign_keys on for every connection (§5 «Прагмы»).
// A duplicate telegram_message_id returns an error for which IsDuplicate is true (§5 UNIQUE index).
func Append(ctx context.Context, db *sql.DB, m Message) (int64, error) {
	if m.Role != RoleUser && m.Role != RoleAssistant && m.Role != RoleSystem {
		return 0, ErrRole
	}
	if strings.TrimSpace(m.Content) == "" {
		return 0, fmt.Errorf("the content of a message is NOT NULL and empty (§5 messages.content)")
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

// scanMessages — the one scan of the messages columns, shared by RecentByChat and HistoryOfChat
// (§5 has nine columns; a second copy of the Scan would drift the moment a column is added). The
// nullable columns become pointers, and the caller's newest-first order is reversed here into the
// chronological order the model request needs.
func scanMessages(rows *sql.Rows) ([]Message, error) {
	var out = []Message{}
	for rows.Next() {
		var m = Message{}
		var model sql.NullString // messages.model is TEXT (§5)
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
// ("context_reset_id:<chat_id>"). §5 marks the reset with the TEXT column context_reset_at, but
// datetime('now') has SECOND resolution, so a message that arrives in the same second as /reset
// would be hidden from the context forever — §5 also gives messages an AUTOINCREMENT id, so the
// watermark is stored as an id and context_reset_at stays the human-readable audit mark.
func ContextResetIDKey(chatID int64) string {
	return fmt.Sprintf("context_reset_id:%d", chatID)
}

// contextFilter — the WHERE fragment of RecentByChat. The id watermark wins when it exists (the
// precise rule of §10 День 2), context_reset_at is the fallback for a chat reset before this
// watermark existed, and "1=1" is a chat that was never reset.
//
// The fragment is built from two fixed strings, never from user input: chatID only ever reaches
// the query as a bound parameter (§12 security).
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

// RecentByChat — the context of §5 «Правила контекста (v1)»: the last `limit` messages of the
// chat, read newest-first because that is what idx_messages_chat_time of §5 is for, and returned
// oldest → newest so the glued history is chronological. The messages a /reset hid are excluded
// (contextFilter), which is why ClearChat deletes nothing and /history still sees them (§6).
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

// HistoryOfChat — `/history [N]` of §6: the last N rows INCLUDING the ones hidden by /reset,
// oldest → newest. It is deliberately a separate function: §5 gives RecentByChat and /history two
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

// ClearChat — `/reset` of §6: the context starts from now, nothing is deleted («история не
// удаляется — /history её видит», §5). Both marks are written in one transaction (§5 WAL with
// `_txlock=immediate` makes the write transaction immediate, so a concurrent writer of another
// connection cannot interleave between the two statements):
//   - context_reset_at of §5, the human-readable audit mark;
//   - the id watermark in bot_state, the precise rule (see ContextResetIDKey for why §5 alone is
//     not precise enough: datetime('now') counts seconds).
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

// Stats — the counters of `/stats` (§6: «число сообщений, токенов, ошибок»). Errors come from the
// events table of §5, attributed through the user of the chat, because §5 gives events a user_id
// and no chat_id — the same attribution the denial events of §6 use.
func Stats(ctx context.Context, db *sql.DB, chatID int64) (ChatStats, error) {
	var stats = ChatStats{}
	var model sql.NullString
	var lastAt sql.NullString
	var err = db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0), MAX(model), MAX(created_at)
		FROM messages WHERE chat_id = ?`, chatID).
		Scan(&stats.Messages, &stats.TokensIn, &stats.TokensOut, &model, &lastAt)
	if err != nil {
		return stats, err
	}
	if model.Valid {
		stats.Model = model.String
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
		if err := db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM events WHERE user_id = ? AND kind = ?`, userID.Int64, EventModelError).
			Scan(&stats.Errors); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// LogEvent — one service row of the events table of §5 (kind: denied, model_error, flood, command).
// The detail carries the class of the error, NEVER the token and never the full text of the model
// (§12 security: logs and service rows stay free of secrets and of user content at LOG_LEVEL=info).
func LogEvent(ctx context.Context, db *sql.DB, userID int64, kind string, detail string) error {
	var _, err = db.ExecContext(ctx, `INSERT INTO events (user_id, kind, detail) VALUES (?, ?, ?)`,
		userID, kind, nullable(detail))
	return err
}

// CountEvents — how many rows of one kind exist (§10 День 3 DoD: after every failed attempt the
// ONLY thing written is events(kind='model_error'), which a test counts with this helper).
func CountEvents(ctx context.Context, db *sql.DB, kind string) (int64, error) {
	var count = int64(0)
	var err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE kind = ?`, kind).Scan(&count)
	return count, err
}

// SetState — one row of bot_state of §5 (the service state of the process, TEXT value). The upsert
// is what makes the offset of getUpdates survive a restart (§5 «Offset long polling»): the library
// keeps lastUpdateID in memory only, so the value has to be written after every processed update
// and read back with bot.WithInitialOffset on the next start (§10 День 4).
func SetState(ctx context.Context, db *sql.DB, key string, value string) error {
	var _, err = db.ExecContext(ctx, `
		INSERT INTO bot_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// GetState — the value of one bot_state key. The bool is "the key exists": a fresh database has no
// 'last_update_id' at all, and starting from 0 is a different decision than starting from a stored
// offset of 0 (§5: an unset offset must not silently become offset=1).
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

// SetLastUpdateID — the 'last_update_id' row of §5 through SetState.
func SetLastUpdateID(ctx context.Context, db *sql.DB, offset int64) error {
	return SetState(ctx, db, StateLastUpdateID, strconv.FormatInt(offset, 10))
}

// LastUpdateID — the stored offset of getUpdates (§5). found=false means "the process has never
// processed an update", which §10 День 4 reads as "no WithInitialOffset, start from the beginning".
// A value that is not an integer is reported as an error rather than treated as 0: a corrupted
// offset would silently replay or skip messages (§13 risk «повторные ответы»).
func LastUpdateID(ctx context.Context, db *sql.DB) (int64, bool, error) {
	var text, found, err = GetState(ctx, db, StateLastUpdateID)
	if err != nil || !found {
		return 0, false, err
	}
	var offset, parseErr = strconv.ParseInt(text, 10, 64)
	if parseErr != nil {
		return 0, false, fmt.Errorf("bot_state %s = %q is not an integer offset (§5: the offset of getUpdates): %w", StateLastUpdateID, text, parseErr)
	}
	return offset, true, nil
}

// IsDuplicate — whether an Append error is the UNIQUE guard of §5 rather than a real database
// failure. §10 День 4 treats it as "this update was already answered" (the restart case of §2 п.7)
// and anything else as an error to report.
func IsDuplicate(err error) bool {
	return errors.Is(err, ErrDuplicateMessage)
}

// isUniqueTelegramID — the driver-level recognition of the constraint. The message of SQLite names
// the columns ("UNIQUE constraint failed: messages.chat_id, messages.telegram_message_id"), so the
// code alone (SQLITE_CONSTRAINT) is not enough to tell the telegram_message_id guard apart from,
// say, a foreign-key violation of the same class (§5 lists both).
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

// nullable — an empty Go string as a SQL NULL. §5 keeps title, model and detail nullable, and an
// empty string there would mean "the answer was empty", which is a different fact (§5 «NULL для
// ответов модели»).
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
