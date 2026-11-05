package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"bot/internal/store"
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
		{"Экспорт", CommandExport},
		{"Поиск", CommandSearch},
		{"Документы", CommandDocs},
		{"Удаление", CommandDelete},
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

func (r *telegramReplier) SendMenuWithID(ctx context.Context, chatID int64, text string, markup models.InlineKeyboardMarkup) (int64, error) {
	if r.bot == nil {
		return 0, errors.New("the telegram bot is not attached ")
	}
	attempt := 0
	for {
		params := &tgbot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: parseModeOf(r.cfg.ParseMode), ReplyMarkup: markup}
		message, err := r.bot.SendMessage(ctx, params)
		if err == nil {
			if message == nil || message.ID == 0 {
				return 0, errors.New("telegram menu response has no message id")
			}
			return int64(message.ID), nil
		}
		if isParseEntitiesError(err) && params.ParseMode != "" {
			params.Text = plainForParseFallback(text, r.cfg.ParseMode)
			params.ParseMode = ""
			message, fallbackErr := r.bot.SendMessage(ctx, params)
			if fallbackErr == nil {
				r.logger.Warn("telegram rejected formatted text; delivered it as plain text", "chat_id", chatID)
				if message == nil || message.ID == 0 {
					return 0, errors.New("telegram menu response has no message id")
				}
				return int64(message.ID), nil
			}
			err = fallbackErr
		}
		var flood *tgbot.TooManyRequestsError
		if !errors.As(err, &flood) || flood.RetryAfter <= 0 || attempt >= 1 {
			return 0, err
		}
		attempt++
		sleep := time.Duration(flood.RetryAfter) * time.Second
		if sleep > FloodSleepMax {
			sleep = FloodSleepMax
		}
		r.d.logEvent(ctx, r.chatUser(chatID), store.EventFlood, fmt.Sprintf("retry_after %d s", flood.RetryAfter))
		r.logger.Warn("telegram answered 429, the chat waits", "chat_id", chatID, "retry_after", flood.RetryAfter)
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(sleep):
		}
	}
}

func (r *telegramReplier) RemoveMenu(ctx context.Context, chatID int64, messageID int64) error {
	if r.bot == nil {
		return errors.New("the telegram bot is not attached ")
	}
	_, err := r.bot.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{
		ChatID:      chatID,
		MessageID:   int(messageID),
		ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}},
	})
	return err
}

func (d *Dispatcher) callbackText(data string) (string, bool) {
	if strings.HasPrefix(data, "cmd:") {
		name := strings.TrimPrefix(data, "cmd:")
		if _, ok := d.index[name]; ok {
			return "/" + name, true
		}
		return "", false
	}
	for _, action := range []string{"confirm", "cancel"} {
		prefix := "delete:" + action
		if data == prefix {
			return "/delete callback " + action + ":", true
		}
		if strings.HasPrefix(data, prefix+":") {
			return "/delete callback " + action + ":" + strings.TrimPrefix(data, prefix+":"), true
		}
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
	ev, ok := d.callbackEventOf(upd)
	if !ok {
		if tg != nil && upd.CallbackQuery.ID != "" {
			params := &tgbot.AnswerCallbackQueryParams{CallbackQueryID: upd.CallbackQuery.ID}
			if strings.HasPrefix(strings.TrimSpace(upd.CallbackQuery.Data), "delete:") {
				params.Text = "Это меню удаления истекло или уже использовано. Отправь /delete заново."
				params.ShowAlert = true
			}
			if _, err := tg.AnswerCallbackQuery(ctx, params); err != nil {
				d.deps.Logger.Error("answerCallbackQuery failed", "error", Sanitize(err, d.deps.Token))
			}
		}
		return
	}
	if tg != nil && upd.CallbackQuery.ID != "" {
		if _, err := tg.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: upd.CallbackQuery.ID}); err != nil {
			d.deps.Logger.Error("answerCallbackQuery failed", "error", Sanitize(err, d.deps.Token))
		}
	}
	d.noteChatUser(ev.ChatID, ev.UserID)
	if err := d.Dispatch(ctx, ev); err != nil {
		return
	}
}
