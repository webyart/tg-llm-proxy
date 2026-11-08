package bot

import (
	"errors"
	"net/url"

	"github.com/go-telegram/bot/models"
)

// ErrMiniAppPrivateOnly — the Mini App is a private-chat companion, so a group chat gets no ticket.
var ErrMiniAppPrivateOnly = errors.New("the Mini App is available only in private chats")

func (d *Dispatcher) miniAppButton(ev Event) (models.InlineKeyboardButton, error) {
	if !d.deps.Cfg.MiniAppEnabled {
		return models.InlineKeyboardButton{}, errors.New("Mini App is disabled")
	}
	if ev.ChatID != ev.UserID {
		// A group chat is not a Mini App tenant: the session is scoped to the chat, so a second member of
		// the same group would see the first member's history, documents and reminders.
		return models.InlineKeyboardButton{}, ErrMiniAppPrivateOnly
	}
	if d.deps.MiniAppTickets == nil {
		return models.InlineKeyboardButton{}, errors.New("Mini App ticket issuer is not configured")
	}
	ticket, err := d.deps.MiniAppTickets.NewLaunchTicket(ev.ChatID, ev.UserID)
	if err != nil {
		return models.InlineKeyboardButton{}, err
	}
	return models.InlineKeyboardButton{
		Text:   "Mini App",
		WebApp: &models.WebAppInfo{URL: d.deps.Cfg.MiniAppPublicURL + "#ticket=" + url.QueryEscape(ticket)},
	}, nil
}
