package bot

import (
	"context"
	"errors"
	"strings"

	"bot/internal/i18n"
	"bot/internal/store"
)

// feedback — 👍/👎 on the newest assistant answer of the chat. Without an argument it prints the
// counters and the usage line; anything but up/down (or their emoji) is a usage error, never a rating.
func (d *Dispatcher) feedback(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	var value int
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "":
		up, down, err := store.MessageFeedbackCounts(ctx, d.deps.DB, ev.ChatID)
		if err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "feedback.count", up, down, i18n.T(lang, "feedback.usage")))
	case "up", "👍":
		value = 1
	case "down", "👎":
		value = -1
	default:
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "feedback.unknown", brief(arg, 40), i18n.T(lang, "feedback.usage")))
	}

	var messageID, err = store.LastAssistantMessageID(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	if messageID == 0 {
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "feedback.no_answer"))
	}
	var changed bool
	changed, err = store.SetMessageFeedback(ctx, d.deps.DB, ev.ChatID, ev.UserID, messageID, value)
	if errors.Is(err, store.ErrFeedbackNotAllowed) || errors.Is(err, store.ErrFeedbackNotFound) {
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "feedback.no_answer"))
	}
	if err != nil {
		return err
	}
	var label = "👍"
	if value == -1 {
		label = "👎"
	}
	if !changed {
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "feedback.already", label))
	}
	return d.reply(ctx, ev.ChatID, i18n.T(lang, "feedback.thanks", label))
}
