package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"bot/internal/store"
)

const (
	ExportRowLimit = 10000
	SearchRowLimit = 20
)

type exportMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	Model     string `json:"model,omitempty"`
	TokensIn  *int64 `json:"tokens_in,omitempty"`
	TokensOut *int64 `json:"tokens_out,omitempty"`
}

type historyExport struct {
	ChatID     int64           `json:"chat_id"`
	ExportedAt string          `json:"exported_at"`
	Messages   []exportMessage `json:"messages"`
}

func (d *Dispatcher) exportHistory(ctx context.Context, ev Event, arg string) error {
	format := strings.ToLower(strings.TrimSpace(arg))
	if format == "" {
		format = "md"
	}
	if format != "md" && format != "json" {
		return d.reply(ctx, ev.ChatID, "Используй /export md или /export json.")
	}
	rows, err := store.ExportMessages(ctx, d.deps.DB, ev.ChatID, ExportRowLimit)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return d.reply(ctx, ev.ChatID, msgHistoryEmpty)
	}
	var data []byte
	if format == "json" {
		payload := historyExport{ChatID: ev.ChatID, ExportedAt: time.Now().UTC().Format(time.RFC3339), Messages: make([]exportMessage, 0, len(rows))}
		for _, row := range rows {
			payload.Messages = append(payload.Messages, exportMessage{Role: row.Role, Content: row.Content, CreatedAt: row.CreatedAt, Model: row.Model, TokensIn: row.TokensIn, TokensOut: row.TokensOut})
		}
		data, err = json.MarshalIndent(payload, "", "  ")
	} else {
		var text strings.Builder
		fmt.Fprintf(&text, "# История чата %d\n\n", ev.ChatID)
		for _, row := range rows {
			fmt.Fprintf(&text, "## %s · %s\n\n%s\n\n", row.Role, row.CreatedAt, row.Content)
		}
		data = []byte(text.String())
	}
	if err != nil {
		return err
	}
	if len(data) > ExportMaxBytes {
		return d.reply(ctx, ev.ChatID, "Экспорт превышает 5 МиБ. Удали старую историю или используй более поздний чат.")
	}
	sender, ok := d.deps.Replier.(DocumentSender)
	if !ok {
		return errors.New("telegram document sender is unavailable")
	}
	return sender.SendDocument(ctx, ev.ChatID, Document{Name: fmt.Sprintf("history-%d.%s", ev.ChatID, format), Data: data, MIME: exportMIME(format)})
}

func (d *Dispatcher) searchHistory(ctx context.Context, ev Event, arg string) error {
	query := strings.TrimSpace(arg)
	if query == "" {
		return d.reply(ctx, ev.ChatID, "Нужен текст поиска: /search <текст>.")
	}
	rows, err := store.SearchMessages(ctx, d.deps.DB, ev.ChatID, query, SearchRowLimit)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return d.reply(ctx, ev.ChatID, "Совпадений не найдено.")
	}
	lines := []string{fmt.Sprintf("Найдено сообщений: %d", len(rows))}
	for _, row := range rows {
		lines = append(lines, fmt.Sprintf("%d. %s · %s: %s", row.ID, row.Role, row.CreatedAt, brief(row.Content, 160)))
	}
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

func (d *Dispatcher) deleteHistory(ctx context.Context, ev Event, arg string) error {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "":
		markup := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "Подтвердить удаление", CallbackData: "delete:confirm"},
			{Text: "Отмена", CallbackData: "delete:cancel"},
		}}}
		if sender, ok := d.deps.Replier.(interface {
			SendMenu(context.Context, int64, string, models.InlineKeyboardMarkup) error
		}); ok {
			return sender.SendMenu(ctx, ev.ChatID, Escape("Удалить всю историю этого чата? Настройки модели и persona сохранятся.", d.deps.Cfg.ParseMode), markup)
		}
		return d.reply(ctx, ev.ChatID, "Для подтверждения отправь /delete confirm. Для отмены — /delete cancel.")
	case "cancel":
		return d.reply(ctx, ev.ChatID, "Удаление истории отменено.")
	case "confirm":
		deleted, err := store.DeleteChatHistory(ctx, d.deps.DB, ev.ChatID)
		if err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("Удалено сообщений: %d. Настройки чата сохранены.", deleted))
	default:
		return d.reply(ctx, ev.ChatID, "Используй /delete, затем подтверди или отмени удаление кнопкой.")
	}
}

func exportMIME(format string) string {
	if format == "json" {
		return "application/json"
	}
	return "text/markdown"
}
