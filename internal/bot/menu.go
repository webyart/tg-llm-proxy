package bot

import (
	"context"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// menuMarkup is the inline command menu shown by /start and /help. Every callback carries only a command name;
// validation against the registry happens again when the callback arrives.
func (d *Dispatcher) menuMarkup() models.InlineKeyboardMarkup {
	labels := []struct {
		text string
		name string
	}{
		{"Спросить", CommandAsk},
		{"Модель", CommandModel},
		{"Стриминг", CommandStream},
		{"Persona", CommandPersona},
		{"Статус", CommandStatus},
		{"История", CommandHistory},
		{"Сброс", CommandReset},
		{"Пропуск", CommandSkip},
		{"Возобновить", CommandUnskip},
		{"Статистика", CommandStats},
		{"Кто я", CommandWhoAmI},
		{"Ping", CommandPing},
		{"Помощь", CommandHelp},
		{"Старт", CommandStart},
		{"Админ", CommandAdmin},
	}
	rows := make([][]models.InlineKeyboardButton, 0, (len(labels)+1)/2)
	for index := 0; index < len(labels); index += 2 {
		row := []models.InlineKeyboardButton{{Text: labels[index].text, CallbackData: "cmd:" + labels[index].name}}
		if index+1 < len(labels) {
			row = append(row, models.InlineKeyboardButton{Text: labels[index+1].text, CallbackData: "cmd:" + labels[index+1].name})
		}
		rows = append(rows, row)
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (d *Dispatcher) sendMenu(ctx context.Context, chatID int64, text string) error {
	if sender, ok := d.deps.Replier.(interface {
		SendMenu(context.Context, int64, string, models.InlineKeyboardMarkup) error
	}); ok {
		return sender.SendMenu(ctx, chatID, Escape(text, d.deps.Cfg.ParseMode), d.menuMarkup())
	}
	return d.reply(ctx, chatID, text)
}

func (d *Dispatcher) callbackText(data string) (string, bool) {
	if strings.HasPrefix(data, "cmd:") {
		name := strings.TrimPrefix(data, "cmd:")
		if _, ok := d.index[name]; ok {
			return "/" + name, true
		}
		return "", false
	}
	if strings.HasPrefix(data, "persona:") {
		action := strings.TrimPrefix(data, "persona:")
		if action == "reset" || action == "custom" {
			return "/persona " + action, true
		}
		for _, preset := range personaPresets {
			if action == preset.name {
				return "/persona " + action, true
			}
		}
	}
	return "", false
}

func (d *Dispatcher) callbackEventOf(upd *models.Update) (Event, bool) {
	if upd == nil || upd.CallbackQuery == nil || upd.CallbackQuery.Message.Message == nil {
		return Event{}, false
	}
	text, ok := d.callbackText(strings.TrimSpace(upd.CallbackQuery.Data))
	if !ok {
		return Event{}, false
	}
	callback := upd.CallbackQuery
	message := callback.Message.Message
	ev := Event{
		UpdateID:          upd.ID,
		TelegramMessageID: int64(message.ID),
		ChatID:            message.Chat.ID,
		ChatType:          string(message.Chat.Type),
		ChatTitle:         chatTitle(message.Chat),
		Text:              text,
	}
	if callback.From.ID != 0 {
		ev.UserID = callback.From.ID
		ev.Username = callback.From.Username
	}
	return ev, true
}

func (d *Dispatcher) handleCallback(ctx context.Context, tg *tgbot.Bot, upd *models.Update) {
	if upd == nil || upd.CallbackQuery == nil {
		return
	}
	if tg != nil && upd.CallbackQuery.ID != "" {
		if _, err := tg.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: upd.CallbackQuery.ID}); err != nil {
			d.deps.Logger.Error("answerCallbackQuery failed", "error", Sanitize(err, d.deps.Token))
		}
	}
	ev, ok := d.callbackEventOf(upd)
	if !ok {
		return
	}
	d.noteChatUser(ev.ChatID, ev.UserID)
	if err := d.Dispatch(ctx, ev); err != nil {
		return
	}
}
