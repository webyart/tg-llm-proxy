package bot

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"bot/internal/i18n"
	"bot/internal/store"
	"bot/internal/timefmt"
)

func (d *Dispatcher) admin(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	if !d.deps.Cfg.IsAdmin(ev.UserID) {
		d.logEvent(ctx, ev.UserID, store.EventDenied, "admin")
		return d.menuReply(ctx, ev, i18n.T(lang, "admin.denied"), simpleBackMarkupFor(lang, "menu:more"))
	}
	stats, err := store.AdminStats(ctx, d.deps.DB)
	if err != nil {
		return err
	}
	info, err := os.Stat(d.deps.Cfg.DBPath)
	if err != nil {
		return fmt.Errorf("database file is unavailable: %w", err)
	}
	lines := []string{
		i18n.T(lang, "admin.title"),
		i18n.T(lang, "admin.chats", stats.Chats),
		i18n.T(lang, "admin.messages", stats.Messages),
		i18n.T(lang, "admin.events", stats.Events, stats.Commands, stats.ModelErrors, stats.Timeouts),
		i18n.T(lang, "admin.pending", stats.Pending),
		i18n.T(lang, "admin.tools", stats.RAGUses, stats.CalculatorUses, stats.URLReaderUses, stats.WebSearchUses, stats.RAGDocuments),
		i18n.T(lang, "admin.calls", d.activeModelRequests.Load(), d.deps.Locks.BusyChats()),
		i18n.T(lang, "admin.sqlite", float64(info.Size())/1024),
		i18n.T(lang, "admin.model", d.deps.Cfg.StrataModel),
		i18n.T(lang, "admin.uptime", timefmt.Duration(time.Since(d.created), lang)),
	}
	if stats.LastActive != "" {
		lines = append(lines, i18n.T(lang, "admin.last_active", d.storedTime(ctx, ev.ChatID, lang, stats.LastActive)))
	}
	return d.menuReply(ctx, ev, strings.Join(lines, "\n"), refreshBackMarkupFor(lang, "menu:admin", "menu:more"))
}
