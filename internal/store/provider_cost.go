package store

// Provider spend: today's assistant answers priced with MODEL_PRICES and grouped by the stored model.
//
// A provider is an environment row (MODEL_PROVIDERS) with an optional budget in provider_settings, so the
// store cannot know which provider a model belongs to. It therefore answers the one question it can answer
// from its own tables — "how much did each stored model cost today?" — in one grouped query, and the caller
// attributes those numbers with model.ProviderAttribution. That keeps the admin UI, /usage and enforcement
// on exactly the same tokens and prices: a value can never be counted twice or hidden in a different table.
//
// As with the per-chat cost, prices are passed in instead of being stored: MODEL_PRICES is an environment
// row, and a database of this project must stay readable by a binary that does not know about prices.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"bot/internal/config"
)

// ProviderDailyCostWarningPercent is the share of a provider's daily budget at which the administrator gets
// a warning. The exact refusal boundary is 100%; the warning exists so a budget is noticed before it bites.
const ProviderDailyCostWarningPercent = 80

// ModelDailyCostMicros prices today's assistant answers of every chat, grouped by the stored model id. It is
// one grouped query regardless of how many models, providers or chats the day contains. A model that is not
// in MODEL_PRICES costs zero, and an empty price table skips the database read: an unpriced deployment can
// never accumulate spend against a budget.
func ModelDailyCostMicros(ctx context.Context, db *sql.DB, now time.Time, prices config.PriceTable) (map[string]int64, error) {
	var costs = map[string]int64{}
	if !prices.HasPrices() {
		return costs, nil
	}
	var dayStart = utcDayStart(now)
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(model, ''), COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0)
		FROM messages
		WHERE role = ? AND created_at >= ? AND created_at < ?
		GROUP BY model`,
		RoleAssistant,
		dayStart.Format("2006-01-02 15:04:05"),
		dayStart.Add(24*time.Hour).Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, fmt.Errorf("daily model costs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var model string
		var tokensIn, tokensOut int64
		if err := rows.Scan(&model, &tokensIn, &tokensOut); err != nil {
			return nil, fmt.Errorf("daily model cost row: %w", err)
		}
		costs[model] += prices.CostMicros(model, tokensIn, tokensOut)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("daily model cost rows: %w", err)
	}
	return costs, nil
}

// EventKindDetailToday reports whether an event of kind with exactly this detail already exists during the
// current UTC day. It is how the provider 80% warning stays one-per-day without a separate counter table:
// the detail is the provider name, so two providers warned on the same day are two independent rows.
func EventKindDetailToday(ctx context.Context, db *sql.DB, kind, detail string, now time.Time) (bool, error) {
	var dayStart = utcDayStart(now)
	var present int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM events
		WHERE kind = ? AND COALESCE(detail, '') = ? AND created_at >= ? AND created_at < ?`,
		kind, detail,
		dayStart.Format("2006-01-02 15:04:05"),
		dayStart.Add(24*time.Hour).Format("2006-01-02 15:04:05")).Scan(&present)
	if err != nil {
		return false, fmt.Errorf("event %s/%s today: %w", kind, detail, err)
	}
	return present > 0, nil
}
