package store

// Per-chat daily cost, computed from the assistant messages already stored for the UTC day and the
// MODEL_PRICES table. There is no cost counter column and no cost event per answer: the same tokens that
// /usage reads are priced on demand, so the admin UI, /limits and enforcement cannot disagree.
//
// Prices are passed in instead of being stored: the table is an environment row, and a database of the
// project must stay readable by a binary that does not know about prices at all.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"bot/internal/config"
)

// ChatDailyCostWarningPercent is the share of the daily cost budget at which the administrator gets a
// warning. The exact refusal boundary is 100%; the warning exists so a budget is noticed before it bites.
const ChatDailyCostWarningPercent = 80

// ChatDailyCostMicros prices today's assistant answers of one chat. A chat without stored answers, an
// empty price table and a model that is not in the table all cost zero. The UTC day boundaries are the
// same ones the other per-chat counters use.
func ChatDailyCostMicros(ctx context.Context, db *sql.DB, chatID int64, now time.Time, prices config.PriceTable) (int64, error) {
	if chatID == 0 {
		return 0, fmt.Errorf("%w: empty chat id", ErrInvalidChatLimits)
	}
	if !prices.HasPrices() {
		return 0, nil
	}
	dayStart := utcDayStart(now)
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(model, ''), COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0)
		FROM messages
		WHERE chat_id = ? AND role = ? AND created_at >= ? AND created_at < ?
		GROUP BY model`,
		chatID, RoleAssistant,
		dayStart.Format("2006-01-02 15:04:05"),
		dayStart.Add(24*time.Hour).Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, fmt.Errorf("chat daily cost of %d: %w", chatID, err)
	}
	defer rows.Close()
	var total int64
	for rows.Next() {
		var model string
		var tokensIn, tokensOut int64
		if err := rows.Scan(&model, &tokensIn, &tokensOut); err != nil {
			return 0, fmt.Errorf("chat daily cost row of %d: %w", chatID, err)
		}
		total += prices.CostMicros(model, tokensIn, tokensOut)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("chat daily cost rows of %d: %w", chatID, err)
	}
	return total, nil
}

// chatDailyCostsToday prices today's answers of every chat in one grouped query. It is what the admin chat
// list uses instead of one query per row. A zero price table skips the database read: every cost is zero.
func chatDailyCostsToday(ctx context.Context, db *sql.DB, now time.Time, prices config.PriceTable) (map[int64]int64, error) {
	costs := map[int64]int64{}
	if !prices.HasPrices() {
		return costs, nil
	}
	dayStart := utcDayStart(now)
	rows, err := db.QueryContext(ctx, `
		SELECT chat_id, COALESCE(model, ''), COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0)
		FROM messages
		WHERE role = ? AND chat_id IS NOT NULL AND created_at >= ? AND created_at < ?
		GROUP BY chat_id, model`,
		RoleAssistant,
		dayStart.Format("2006-01-02 15:04:05"),
		dayStart.Add(24*time.Hour).Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, fmt.Errorf("daily chat costs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var chatID int64
		var model string
		var tokensIn, tokensOut int64
		if err := rows.Scan(&chatID, &model, &tokensIn, &tokensOut); err != nil {
			return nil, fmt.Errorf("daily chat cost row: %w", err)
		}
		costs[chatID] += prices.CostMicros(model, tokensIn, tokensOut)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("daily chat cost rows: %w", err)
	}
	return costs, nil
}

// ChatEventKindToday reports whether a chat already has an event of this kind during the current UTC day.
// It is how the 80% warning stays one-per-day without a separate counter table.
func ChatEventKindToday(ctx context.Context, db *sql.DB, chatID int64, kind string, now time.Time) (bool, error) {
	if chatID == 0 {
		return false, fmt.Errorf("%w: empty chat id", ErrInvalidChatLimits)
	}
	dayStart := utcDayStart(now)
	var present int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM events
		WHERE chat_id = ? AND kind = ? AND created_at >= ? AND created_at < ?`,
		chatID, kind,
		dayStart.Format("2006-01-02 15:04:05"),
		dayStart.Add(24*time.Hour).Format("2006-01-02 15:04:05")).Scan(&present)
	if err != nil {
		return false, fmt.Errorf("event %s of chat %d today: %w", kind, chatID, err)
	}
	return present > 0, nil
}

// ListActiveChatsWithCost is ListActiveChats with today's spend and the 80% warning filled in. The base
// list stays one query; the costs are one more grouped query for every chat, not one per chat.
func ListActiveChatsWithCost(ctx context.Context, db *sql.DB, now time.Time, limit int, prices config.PriceTable) ([]ActiveChat, error) {
	chats, err := ListActiveChatsAt(ctx, db, now, limit)
	if err != nil {
		return nil, err
	}
	if len(chats) == 0 {
		return chats, nil
	}
	costs, err := chatDailyCostsToday(ctx, db, now, prices)
	if err != nil {
		return nil, err
	}
	for index := range chats {
		cost := costs[chats[index].ID]
		chats[index].Today.CostUSD = float64(cost) / 1_000_000
		if budget := chats[index].Limits.DailyCostLimitMicros; budget > 0 {
			warningAt := (budget*ChatDailyCostWarningPercent + 99) / 100
			if cost >= warningAt {
				chats[index].Today.CostWarning = true
			}
		}
	}
	return chats, nil
}
