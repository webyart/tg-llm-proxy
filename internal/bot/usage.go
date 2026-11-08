package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"bot/internal/store"
)

func (d *Dispatcher) reserveDailyModelCall(ctx context.Context, userID int64) (bool, error) {
	limit := d.deps.Cfg.DailyLimitsFor(userID).Model
	return store.ReserveDailyQuota(ctx, d.deps.DB, userID, d.now(), store.DailyQuotaModel, limit)
}

func (d *Dispatcher) recordModelCall(ctx context.Context, userID int64, modelName string) error {
	if strings.TrimSpace(modelName) == "" {
		modelName = d.deps.Cfg.StrataModel
	}
	return store.LogEvent(ctx, d.deps.DB, userID, store.EventModelCall, modelName)
}

func (d *Dispatcher) reserveDailyNetworkToolCall(ctx context.Context, userID int64) (bool, error) {
	limit := d.deps.Cfg.DailyLimitsFor(userID).NetworkTools
	return store.ReserveDailyQuota(ctx, d.deps.DB, userID, d.now(), store.DailyQuotaNetworkTools, limit)
}

// releaseDailyQuotaBudget — the refund runs even when the caller's context is already spent (the failure
// that caused it usually IS the deadline), so it gets its own short budget instead of inheriting cancellation.
const releaseDailyQuotaBudget = 2 * time.Second

func (d *Dispatcher) releaseDailyModelCall(ctx context.Context, userID int64) {
	d.releaseDailyQuota(ctx, userID, store.DailyQuotaModel)
}

func (d *Dispatcher) releaseDailyNetworkToolCall(ctx context.Context, userID int64) {
	d.releaseDailyQuota(ctx, userID, store.DailyQuotaNetworkTools)
}

func (d *Dispatcher) releaseDailyQuota(ctx context.Context, userID int64, class string) {
	var releaseCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), releaseDailyQuotaBudget)
	defer cancel()
	if err := store.ReleaseDailyQuota(releaseCtx, d.deps.DB, userID, d.now(), class); err != nil {
		d.deps.Logger.Error("the daily quota reservation was not returned",
			"user_id", userID, "class", class, "error", Sanitize(err, d.deps.Token))
	}
}

func (d *Dispatcher) usage(ctx context.Context, ev Event, arg string) error {
	text, err := d.dailyUsageText(ctx, ev.UserID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(arg) == "menu" {
		return d.renderMenu(ctx, ev, text, usageMenuMarkup())
	}
	return d.reply(ctx, ev.ChatID, text)
}

func (d *Dispatcher) dailyUsageText(ctx context.Context, userID int64) (string, error) {
	now := d.now().UTC()
	usage, err := store.UserDailyUsage(ctx, d.deps.DB, userID, now)
	if err != nil {
		return "", err
	}
	lines := []string{
		fmt.Sprintf("Использование за %s (UTC):", now.Format("2006-01-02")),
		"Модели:",
	}
	if len(usage.Models) == 0 {
		lines = append(lines, "- обращений не было")
	}
	for _, model := range usage.Models {
		name := model.Model
		if name == "" {
			name = "модель не указана"
		}
		lines = append(lines, fmt.Sprintf("- %s: вызовов %d; токены %d входящих / %d исходящих",
			name, model.Calls, model.TokensIn, model.TokensOut))
	}
	lines = append(lines,
		"Инструменты:",
		fmt.Sprintf("- read_url: %d", usage.Tools.ReadURL),
		fmt.Sprintf("- web_search: %d", usage.Tools.WebSearch),
		fmt.Sprintf("- calculator: %d", usage.Tools.Calculator),
		fmt.Sprintf("- RAG: %d", usage.Tools.RAG),
		"Дневные лимиты:",
	)
	// The limits are enforced on daily_quota_reservations, so the numbers shown here must come from the same
	// table: «3 из 10» computed from messages once told a user they had room while the reservation table had
	// already refused them.
	modelQuota, quotaErr := store.DailyQuotaUsed(ctx, d.deps.DB, userID, now, store.DailyQuotaModel)
	if quotaErr != nil {
		return "", quotaErr
	}
	networkQuota, quotaErr := store.DailyQuotaUsed(ctx, d.deps.DB, userID, now, store.DailyQuotaNetworkTools)
	if quotaErr != nil {
		return "", quotaErr
	}
	lines = append(lines,
		formatDailyLimit("Модель", modelQuota, d.deps.Cfg.DailyLimitsFor(userID).Model),
		formatDailyLimit("Сетевые инструменты", networkQuota, d.deps.Cfg.DailyLimitsFor(userID).NetworkTools),
	)
	return strings.Join(lines, "\n"), nil
}

func formatDailyLimit(name string, used int64, limit int64) string {
	if limit == 0 {
		return fmt.Sprintf("%s: лимит 0 (без ограничений); использовано %d; осталось без ограничений", name, used)
	}
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Sprintf("%s: лимит %d; использовано %d; осталось %d", name, limit, used, remaining)
}
