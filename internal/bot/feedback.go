package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bot/internal/store"
)

const (
	msgFeedbackNoAnswer = "В этом чате ещё нет ответа модели — оценивать нечего."
	msgFeedbackUsage    = "Поставь оценку последнему ответу: /feedback up — 👍, /feedback down — 👎."
)

// feedback — 👍/👎 on the newest assistant answer of the chat. Without an argument it prints the
// counters and the usage line; anything but up/down (or their emoji) is a usage error, never a rating.
func (d *Dispatcher) feedback(ctx context.Context, ev Event, arg string) error {
	var value int
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "":
		up, down, err := store.MessageFeedbackCounts(ctx, d.deps.DB, ev.ChatID)
		if err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("Оценки: 👍 %d / 👎 %d\n%s", up, down, msgFeedbackUsage))
	case "up", "👍":
		value = 1
	case "down", "👎":
		value = -1
	default:
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("Не понял оценку %q. %s", brief(arg, 40), msgFeedbackUsage))
	}

	var messageID, err = store.LastAssistantMessageID(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	if messageID == 0 {
		return d.reply(ctx, ev.ChatID, msgFeedbackNoAnswer)
	}
	var changed bool
	changed, err = store.SetMessageFeedback(ctx, d.deps.DB, ev.ChatID, ev.UserID, messageID, value)
	if errors.Is(err, store.ErrFeedbackNotAllowed) || errors.Is(err, store.ErrFeedbackNotFound) {
		return d.reply(ctx, ev.ChatID, msgFeedbackNoAnswer)
	}
	if err != nil {
		return err
	}
	var label = "👍"
	if value == -1 {
		label = "👎"
	}
	if !changed {
		return d.reply(ctx, ev.ChatID, "Оценка уже стоит: "+label+".")
	}
	return d.reply(ctx, ev.ChatID, "Спасибо! Оценка сохранена: "+label+".")
}
