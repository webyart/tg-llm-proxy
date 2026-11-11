package store

// Per-chat/group limits and restrictions.
//
// One row of chat_limits belongs to one chat: a private chat has a positive chat_id, a group or a
// supergroup a negative one (Telegram's own convention). The user id of the chat lives in chats.user_id
// and is never compared with chat_id anywhere in this file: a negative group id must never be mistaken
// for a user, and a positive private chat id happens to equal its owner's id only by Telegram's design,
// not by contract.
//
// The absence of a row is the default state of v1.63.0 — everything unlimited and every tool enabled.
// `found` from GetChatLimits is therefore a part of the contract: it tells the admin UI and /limits the
// difference between "no explicit limits" and "explicitly unlimited".

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ChatLimitMax is the upper bound of every per-chat numeric limit. It is validated here and by the
// admin API so a typo in the Mini App cannot store a value that overflows an int64 counter in SQL.
const ChatLimitMax = int64(1000000)

// ChatCostLimitMaxMicros is the upper bound of the daily cost budget: 100 000 USD per UTC day, in
// micro-dollars. It is deliberately separate from ChatLimitMax (a call counter) because a dollar amount
// needs six decimals and a much larger integer range.
const ChatCostLimitMaxMicros = int64(100000) * 1_000_000

// The tool names of the chat_limits.disabled_tools allowlist. They are the same names the model sees
// in its tool schemas and the same names events(detail) carries.
const (
	ChatToolReadURL    = "read_url"
	ChatToolWebSearch  = "web_search"
	ChatToolCalculator = "calculator"
)

var chatLimitTools = []string{ChatToolReadURL, ChatToolWebSearch, ChatToolCalculator}

// Errors of this file, mapped to HTTP 404/400 by the admin API and to a localized reply by /limits.
var (
	ErrChatNotFound      = errors.New("chat not found")
	ErrInvalidChatLimits = errors.New("invalid chat limits")
)

// ChatLimits is one row of chat_limits in its Go form. DisabledTools is the parsed, deduplicated and
// canonically ordered form; the database stores it as a comma-separated string.
type ChatLimits struct {
	ChatID               int64
	DailyModelLimit      int64
	DailyNetworkLimit    int64
	PerMinuteLimit       int64
	DailyCostLimitMicros int64
	DisabledTools        []string
}

// IsUnlimited reports whether the row changes nothing: every counter unlimited and every tool enabled.
func (l ChatLimits) IsUnlimited() bool {
	return l.DailyModelLimit == 0 && l.DailyNetworkLimit == 0 && l.PerMinuteLimit == 0 &&
		l.DailyCostLimitMicros == 0 && len(l.DisabledTools) == 0
}

// ToolDisabled reports whether the chat disabled a named tool. Unknown names are simply not disabled.
func (l ChatLimits) ToolDisabled(name string) bool {
	for _, tool := range l.DisabledTools {
		if tool == name {
			return true
		}
	}
	return false
}

func (l ChatLimits) clone() ChatLimits {
	l.DisabledTools = append([]string{}, l.DisabledTools...)
	return l
}

// ChatTools returns the tool names the disabled_tools allowlist accepts.
func ChatTools() []string {
	return append([]string{}, chatLimitTools...)
}

// NormalizeDisabledTools validates and canonicalizes a disabled-tools list: entries may be comma- or
// space-separated, duplicated, upper-case or padded; anything that is not one of ChatTools() is an
// error, because a silently dropped tool would look "disabled" in the UI while the model still gets it.
func NormalizeDisabledTools(tools []string) ([]string, error) {
	var seen = map[string]bool{}
	for _, entry := range tools {
		for _, field := range strings.FieldsFunc(entry, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
		}) {
			name := strings.ToLower(strings.TrimSpace(field))
			if name == "" {
				continue
			}
			if !knownChatTool(name) {
				return nil, fmt.Errorf("%w: unknown tool %q (known: %s)", ErrInvalidChatLimits, name, strings.Join(chatLimitTools, ", "))
			}
			seen[name] = true
		}
	}
	return canonicalTools(seen), nil
}

func knownChatTool(name string) bool {
	for _, tool := range chatLimitTools {
		if tool == name {
			return true
		}
	}
	return false
}

// canonicalTools renders a set of known tools in the stable order of chatLimitTools, so the stored
// string and the API answer do not depend on the order the caller sent.
func canonicalTools(set map[string]bool) []string {
	var tools = make([]string, 0, len(set))
	for _, tool := range chatLimitTools {
		if set[tool] {
			tools = append(tools, tool)
		}
	}
	if len(tools) == 0 {
		return nil
	}
	return tools
}

// ParseDisabledTools reads the database form of disabled_tools. Unknown values (a hand-edited row, a
// tool removed in a later release) are ignored instead of failing a read: enforcement of the remaining
// tools must not go blind because one stored name is stale.
func ParseDisabledTools(raw string) []string {
	var set = map[string]bool{}
	for _, field := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(field))
		if knownChatTool(name) {
			set[name] = true
		}
	}
	return canonicalTools(set)
}

// FormatDisabledTools writes the database form of the list, in the same canonical order.
func FormatDisabledTools(tools []string) string {
	if len(tools) == 0 {
		return ""
	}
	return strings.Join(tools, ",")
}

// ChatUsage is the number of counted actions of one chat during one UTC day.
type ChatUsage struct {
	ModelCalls int64
	ReadURL    int64
	WebSearch  int64
	Calculator int64
	// CostUSD is today's spend computed from the stored assistant tokens and MODEL_PRICES. It stays zero
	// when the chat has no prices configured or no answer has been stored yet.
	CostUSD float64
	// CostWarning is true at or above 80% of an active daily cost budget. The admin API exposes it as
	// "chat_cost_budget_warning"; it is derived from the same numbers enforcement reads.
	CostWarning bool
}

// NetworkToolCalls is the counter the daily network limit is compared with: read_url plus web_search,
// exactly the set the per-user daily network quota already counts.
func (u ChatUsage) NetworkToolCalls() int64 { return u.ReadURL + u.WebSearch }

// ChatDailyUsage counts today's model_call and tool-usage events of one chat. The day boundaries are
// the same UTC boundaries of the per-user daily quota (daily_quota_reservations, events.created_at):
// an event at 00:00:00 UTC belongs to the new day.
func ChatDailyUsage(ctx context.Context, db *sql.DB, chatID int64, now time.Time) (ChatUsage, error) {
	var usage ChatUsage
	if chatID == 0 {
		return usage, fmt.Errorf("%w: empty chat id", ErrInvalidChatLimits)
	}
	var dayStart = utcDayStart(now)
	start := dayStart.Format("2006-01-02 15:04:05")
	end := dayStart.Add(24 * time.Hour).Format("2006-01-02 15:04:05")
	err := db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN kind = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN kind = ? AND detail = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN kind = ? AND detail = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN kind = ? AND detail = ? THEN 1 ELSE 0 END), 0)
		FROM events
		WHERE chat_id = ? AND created_at >= ? AND created_at < ?
			AND kind IN (?, ?)`,
		EventModelCall,
		EventUsage, ChatToolReadURL,
		EventUsage, ChatToolWebSearch,
		EventUsage, ChatToolCalculator,
		chatID, start, end,
		EventModelCall, EventUsage).Scan(
		&usage.ModelCalls, &usage.ReadURL, &usage.WebSearch, &usage.Calculator)
	if err != nil {
		return usage, fmt.Errorf("chat daily usage of %d: %w", chatID, err)
	}
	return usage, nil
}

// CountChatUserMessagesSince counts stored user questions of one chat at or after `since`. This is the
// per-minute limit's counter: it looks only at role='user' rows (a model answer is not a new request)
// and it uses idx_messages_chat_created_at, so the check is an index range scan of one chat.
func CountChatUserMessagesSince(ctx context.Context, db *sql.DB, chatID int64, since time.Time) (int64, error) {
	if chatID == 0 {
		return 0, fmt.Errorf("%w: empty chat id", ErrInvalidChatLimits)
	}
	var count int64
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM messages
		WHERE chat_id = ? AND role = ? AND created_at >= ?`,
		chatID, RoleUser, since.UTC().Format("2006-01-02 15:04:05")).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("per-minute messages of chat %d: %w", chatID, err)
	}
	return count, nil
}

// GetChatLimits reads the row of one chat. A missing row is (zero limits, false, nil): the caller must
// keep the default unlimited behaviour instead of inventing a limit.
func GetChatLimits(ctx context.Context, db *sql.DB, chatID int64) (ChatLimits, bool, error) {
	var limits = ChatLimits{ChatID: chatID}
	if chatID == 0 {
		return limits, false, fmt.Errorf("%w: empty chat id", ErrInvalidChatLimits)
	}
	var disabled string
	err := db.QueryRowContext(ctx, `
		SELECT daily_model_limit, daily_network_limit, per_minute_limit, daily_cost_limit_micros, disabled_tools
		FROM chat_limits WHERE chat_id = ?`, chatID).
		Scan(&limits.DailyModelLimit, &limits.DailyNetworkLimit, &limits.PerMinuteLimit,
			&limits.DailyCostLimitMicros, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ChatLimits{ChatID: chatID}, false, nil
	}
	if err != nil {
		return limits, false, fmt.Errorf("read chat limits of %d: %w", chatID, err)
	}
	limits.DisabledTools = ParseDisabledTools(disabled)
	return limits, true, nil
}

// SetChatLimits validates and upserts one row. The chat must exist: chat_limits references chats and a
// limit for an unknown chat would otherwise be a foreign-key error at the API boundary.
func SetChatLimits(ctx context.Context, db *sql.DB, limits ChatLimits) error {
	if limits.ChatID == 0 {
		return fmt.Errorf("%w: empty chat id", ErrInvalidChatLimits)
	}
	for _, value := range []int64{limits.DailyModelLimit, limits.DailyNetworkLimit, limits.PerMinuteLimit} {
		if value < 0 || value > ChatLimitMax {
			return fmt.Errorf("%w: a limit of %d is outside 0..%d", ErrInvalidChatLimits, value, ChatLimitMax)
		}
	}
	if limits.DailyCostLimitMicros < 0 || limits.DailyCostLimitMicros > ChatCostLimitMaxMicros {
		return fmt.Errorf("%w: a cost budget of %d micros is outside 0..%d", ErrInvalidChatLimits,
			limits.DailyCostLimitMicros, ChatCostLimitMaxMicros)
	}
	tools, err := NormalizeDisabledTools(limits.DisabledTools)
	if err != nil {
		return err
	}
	exists, err := ChatExists(ctx, db, limits.ChatID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: %d", ErrChatNotFound, limits.ChatID)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO chat_limits (chat_id, daily_model_limit, daily_network_limit, per_minute_limit,
			daily_cost_limit_micros, disabled_tools)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET
			daily_model_limit = excluded.daily_model_limit,
			daily_network_limit = excluded.daily_network_limit,
			per_minute_limit = excluded.per_minute_limit,
			daily_cost_limit_micros = excluded.daily_cost_limit_micros,
			disabled_tools = excluded.disabled_tools`,
		limits.ChatID, limits.DailyModelLimit, limits.DailyNetworkLimit, limits.PerMinuteLimit,
		limits.DailyCostLimitMicros, FormatDisabledTools(tools))
	if err != nil {
		return fmt.Errorf("write chat limits of %d: %w", limits.ChatID, err)
	}
	return nil
}

// ResetChatLimits deletes the row and reports whether there was one. A reset on a chat without limits
// is not an error: /limits reset and the admin API both answer it as "no explicit limits now".
func ResetChatLimits(ctx context.Context, db *sql.DB, chatID int64) (bool, error) {
	if chatID == 0 {
		return false, fmt.Errorf("%w: empty chat id", ErrInvalidChatLimits)
	}
	result, err := db.ExecContext(ctx, `DELETE FROM chat_limits WHERE chat_id = ?`, chatID)
	if err != nil {
		return false, fmt.Errorf("reset chat limits of %d: %w", chatID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reset chat limits of %d: rows affected: %w", chatID, err)
	}
	return affected > 0, nil
}

// ChatExists reports whether the chats row of chatID exists.
func ChatExists(ctx context.Context, db *sql.DB, chatID int64) (bool, error) {
	var present int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chats WHERE chat_id = ?`, chatID).Scan(&present)
	if err != nil {
		return false, fmt.Errorf("chat existence of %d: %w", chatID, err)
	}
	return present > 0, nil
}

// ChatOwner returns the user_id stored for a chat. It is how a private chat decides whether the sender
// may manage its limits: the owner of the row, not "chat_id equals user_id".
func ChatOwner(ctx context.Context, db *sql.DB, chatID int64) (int64, bool, error) {
	var userID int64
	err := db.QueryRowContext(ctx, `SELECT user_id FROM chats WHERE chat_id = ?`, chatID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("chat owner of %d: %w", chatID, err)
	}
	return userID, true, nil
}

// LogChatEvent writes one service event attributed to a chat as well as to its user. A zero chat id is
// stored as NULL: the dispatcher's own unit tests and the Mini App call paths without a Telegram chat
// must keep writing events exactly as LogEvent did. Per-chat counters read the chat_id column.
func LogChatEvent(ctx context.Context, db *sql.DB, chatID int64, userID int64, kind string, detail string) error {
	var chat any
	if chatID != 0 {
		chat = chatID
	}
	var _, err = db.ExecContext(ctx,
		`INSERT INTO events (user_id, chat_id, kind, detail) VALUES (?, ?, ?, ?)`,
		userID, chat, kind, nullable(detail))
	return err
}

// ActiveChat is one row of the admin chat list: identity, today's counters and the current limits.
type ActiveChat struct {
	ID           int64
	Title        string
	Type         string
	UserID       int64
	LastActivity string
	Today        ChatUsage
	Limits       ChatLimits
	HasLimits    bool
}

// ListActiveChats returns the chats ordered by their last activity (the newest message, falling back to
// chats.last_active_at) with today's model/tool counters and the current limits. The counters come from
// events.chat_id, the same source enforcement reads, so the admin UI shows the number the bot will
// compare with the limit and not an estimate from a different table.
func ListActiveChats(ctx context.Context, db *sql.DB, limit int) ([]ActiveChat, error) {
	return ListActiveChatsAt(ctx, db, time.Now(), limit)
}

// ListActiveChatsAt is ListActiveChats with an explicit clock, so the UTC day a test asserts on is the
// day the counters really read.
func ListActiveChatsAt(ctx context.Context, db *sql.DB, now time.Time, limit int) ([]ActiveChat, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var dayStart = utcDayStart(now)
	start := dayStart.Format("2006-01-02 15:04:05")
	end := dayStart.Add(24 * time.Hour).Format("2006-01-02 15:04:05")
	rows, err := db.QueryContext(ctx, `
		SELECT c.chat_id, COALESCE(c.title, ''), c.user_id,
			COALESCE((SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.chat_id), c.last_active_at) AS last_activity,
			COALESCE(SUM(CASE WHEN e.kind = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN e.kind = ? AND e.detail = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN e.kind = ? AND e.detail = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN e.kind = ? AND e.detail = ? THEN 1 ELSE 0 END), 0),
			CASE WHEN l.chat_id IS NULL THEN 0 ELSE 1 END,
			COALESCE(l.daily_model_limit, 0),
			COALESCE(l.daily_network_limit, 0),
			COALESCE(l.per_minute_limit, 0),
			COALESCE(l.daily_cost_limit_micros, 0),
			COALESCE(l.disabled_tools, '')
		FROM chats c
		LEFT JOIN chat_limits l ON l.chat_id = c.chat_id
		LEFT JOIN events e ON e.chat_id = c.chat_id AND e.created_at >= ? AND e.created_at < ?
		GROUP BY c.chat_id
		ORDER BY last_activity DESC, c.chat_id DESC
		LIMIT ?`,
		EventModelCall,
		EventUsage, ChatToolReadURL,
		EventUsage, ChatToolWebSearch,
		EventUsage, ChatToolCalculator,
		start, end,
		limit)
	if err != nil {
		return nil, fmt.Errorf("list active chats: %w", err)
	}
	defer rows.Close()

	var chats = []ActiveChat{}
	for rows.Next() {
		var chat ActiveChat
		var hasLimits int
		var disabled string
		if err := rows.Scan(
			&chat.ID, &chat.Title, &chat.UserID, &chat.LastActivity,
			&chat.Today.ModelCalls, &chat.Today.ReadURL, &chat.Today.WebSearch, &chat.Today.Calculator,
			&hasLimits, &chat.Limits.DailyModelLimit, &chat.Limits.DailyNetworkLimit,
			&chat.Limits.PerMinuteLimit, &chat.Limits.DailyCostLimitMicros, &disabled); err != nil {
			return nil, fmt.Errorf("list active chats row: %w", err)
		}
		chat.Type = "private"
		if chat.ID < 0 {
			chat.Type = chatTypeGroupName
		}
		chat.HasLimits = hasLimits == 1
		chat.Limits.ChatID = chat.ID
		chat.Limits.DisabledTools = ParseDisabledTools(disabled)
		chats = append(chats, chat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list active chats rows: %w", err)
	}
	return chats, nil
}

// chatTypeGroupName is the API/UI name of a group chat. A separate constant keeps the sign rule in one
// place: Telegram's negative chat id is the only signal the database has.
const chatTypeGroupName = "group"
