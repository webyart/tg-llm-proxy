package bot

import (
	"context"
	"fmt"
	"strings"

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
		formatDailyLimit("Модель", usage.ModelCalls(), d.deps.Cfg.DailyLimitsFor(userID).Model),
		formatDailyLimit("Сетевые инструменты", usage.NetworkToolCalls(), d.deps.Cfg.DailyLimitsFor(userID).NetworkTools),
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
