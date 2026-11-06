package bot

import (
	"errors"
	"net/url"

	"github.com/go-telegram/bot/models"
)

func (d *Dispatcher) miniAppButton(ev Event) (models.InlineKeyboardButton, error) {
	if !d.deps.Cfg.MiniAppEnabled {
		return models.InlineKeyboardButton{}, errors.New("Mini App is disabled")
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
