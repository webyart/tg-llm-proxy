package bot

import (
	"context"
	"strings"
	"time"

	"bot/internal/i18n"
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
	text, err := d.dailyUsageTextFor(ctx, ev.UserID, d.chatLanguage(ctx, ev.ChatID))
	if err != nil {
		return err
	}
	if strings.TrimSpace(arg) == "menu" {
		return d.renderMenu(ctx, ev, text, usageMenuMarkup())
	}
	return d.reply(ctx, ev.ChatID, text)
}

// dailyUsageText keeps the historical Russian rendering for callers without a chat language.
func (d *Dispatcher) dailyUsageText(ctx context.Context, userID int64) (string, error) {
	return d.dailyUsageTextFor(ctx, userID, i18n.Default)
}

func (d *Dispatcher) dailyUsageTextFor(ctx context.Context, userID int64, lang string) (string, error) {
	now := d.now().UTC()
	usage, err := store.UserDailyUsage(ctx, d.deps.DB, userID, now)
	if err != nil {
		return "", err
	}
	lines := []string{
		i18n.T(lang, "usage.title", now.Format("2006-01-02")),
		i18n.T(lang, "usage.models"),
	}
	if len(usage.Models) == 0 {
		lines = append(lines, i18n.T(lang, "usage.no_calls"))
	}
	for _, model := range usage.Models {
		name := model.Model
		if name == "" {
			name = i18n.T(lang, "usage.unnamed_model")
		}
		lines = append(lines, i18n.T(lang, "usage.model_line",
			name, model.Calls, model.TokensIn, model.TokensOut))
	}
	lines = append(lines,
		i18n.T(lang, "usage.tools"),
		i18n.T(lang, "usage.tool_line", "read_url", usage.Tools.ReadURL),
		i18n.T(lang, "usage.tool_line", "web_search", usage.Tools.WebSearch),
		i18n.T(lang, "usage.tool_line", "calculator", usage.Tools.Calculator),
		i18n.T(lang, "usage.tool_line", "RAG", usage.Tools.RAG),
		i18n.T(lang, "usage.limits"),
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
		dailyLimitLine(lang, i18n.T(lang, "usage.limit_model"), modelQuota, d.deps.Cfg.DailyLimitsFor(userID).Model),
		dailyLimitLine(lang, i18n.T(lang, "usage.limit_network"), networkQuota, d.deps.Cfg.DailyLimitsFor(userID).NetworkTools),
	)
	return strings.Join(lines, "\n"), nil
}

// dailyLimitLine renders one daily-limit row in lang; a zero limit is reported as unlimited.
func dailyLimitLine(lang string, name string, used int64, limit int64) string {
	if limit == 0 {
		return i18n.T(lang, "usage.limit_zero", name, used)
	}
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	return i18n.T(lang, "usage.limit", name, limit, used, remaining)
}
