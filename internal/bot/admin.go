package bot

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"bot/internal/store"
)

func (d *Dispatcher) admin(ctx context.Context, ev Event, arg string) error {
	if !d.deps.Cfg.IsAdmin(ev.UserID) {
		d.logEvent(ctx, ev.UserID, store.EventDenied, "admin")
		return d.menuReply(ctx, ev, "Команда доступна только администраторам.", simpleBackMarkup("menu:more"))
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
		"Администрирование:",
		fmt.Sprintf("Чатов: %d", stats.Chats),
		fmt.Sprintf("Сообщений: %d", stats.Messages),
		fmt.Sprintf("Событий: %d (команд %d, model_error %d, timeout %d)", stats.Events, stats.Commands, stats.ModelErrors, stats.Timeouts),
		fmt.Sprintf("Вопросов без ответа: %d", stats.Pending),
		fmt.Sprintf("RAG retrieval: %d; calculator calls: %d; read_url calls: %d; web_search calls: %d; документов в RAG: %d", stats.RAGUses, stats.CalculatorUses, stats.URLReaderUses, stats.WebSearchUses, stats.RAGDocuments),
		fmt.Sprintf("Активных model calls: %d; чатов с lock: %d", d.activeModelRequests.Load(), d.deps.Locks.BusyChats()),
		fmt.Sprintf("Размер SQLite: %.1f KiB", float64(info.Size())/1024),
		"Модель по умолчанию: " + d.deps.Cfg.StrataModel,
		"Uptime: " + timeSince(d.created),
	}
	if stats.LastActive != "" {
		lines = append(lines, "Последняя активность: "+stats.LastActive+" UTC")
	}
	return d.menuReply(ctx, ev, strings.Join(lines, "\n"), refreshBackMarkup("menu:admin", "menu:more"))
}

func timeSince(start time.Time) string {
	return time.Since(start).Round(time.Second).String()
}
