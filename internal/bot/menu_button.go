package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (d *Dispatcher) PublishMenuButton(ctx context.Context, tg *tgbot.Bot) error {
	if tg == nil {
		return errors.New("PublishMenuButton needs the bot of NewTelegram")
	}
	var button models.InputMenuButton = models.MenuButtonCommands{Type: models.MenuButtonTypeCommands}
	if d.deps.Cfg.MiniAppEnabled && strings.TrimSpace(d.deps.Cfg.MiniAppPublicURL) != "" {
		button = models.MenuButtonWebApp{
			Type:   models.MenuButtonTypeWebApp,
			Text:   "Меню",
			WebApp: models.WebAppInfo{URL: d.deps.Cfg.MiniAppPublicURL},
		}
	}
	if _, err := tg.SetChatMenuButton(ctx, &tgbot.SetChatMenuButtonParams{MenuButton: button}); err != nil {
		return fmt.Errorf("setChatMenuButton: %w", errors.New(Sanitize(err, d.deps.Token)))
	}
	return nil
}
