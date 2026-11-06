package bot

import (
	"context"
	"testing"

	"bot/internal/config"
)

type fakeMiniAppTickets struct {
	chatID int64
	userID int64
	ticket string
}

func (f *fakeMiniAppTickets) NewLaunchTicket(chatID int64, userID int64) (string, error) {
	f.chatID, f.userID = chatID, userID
	return f.ticket, nil
}

func TestMoreMenuLaunchButtonUsesScopedTicket(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"MINIAPP_ENABLED": "true", "MINIAPP_PUBLIC_URL": "https://example.test/companion",
		"TELEGRAM_ALLOWED_USERS": "11",
	})
	issuer := &fakeMiniAppTickets{ticket: "0123456789abcdef012345"}
	replier := &fakeReplier{}
	d := newTestDispatcher(t, cfg, nil, replier, testStore(t))
	d.deps.MiniAppTickets = issuer
	event := testEvent(100, 11, 180, "/help menu:more")
	if err := d.showMoreMenu(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if issuer.chatID != event.ChatID || issuer.userID != event.UserID {
		t.Fatalf("ticket scope chat=%d user=%d", issuer.chatID, issuer.userID)
	}
	menus := replier.menus
	if len(menus) != 1 {
		t.Fatalf("menus=%d", len(menus))
	}
	found := false
	for _, row := range menus[0].InlineKeyboard {
		for _, button := range row {
			if button.WebApp == nil {
				continue
			}
			found = true
			want := "https://example.test/companion#ticket=" + issuer.ticket
			if button.Text != "Mini App" || button.WebApp.URL != want || button.CallbackData != "" {
				t.Fatalf("WebApp button=%+v", button)
			}
		}
	}
	if !found {
		t.Fatal("More menu has no Mini App WebApp button")
	}
}

func TestMoreMenuOmitsMiniAppWhenDisabled(t *testing.T) {
	cfg := config.Config{}
	replier := &fakeReplier{}
	d := newTestDispatcher(t, cfg, nil, replier, testStore(t))
	if err := d.showMoreMenu(context.Background(), testEvent(100, 11, 180, "/help menu:more")); err != nil {
		t.Fatal(err)
	}
	for _, row := range replier.menus[0].InlineKeyboard {
		for _, button := range row {
			if button.WebApp != nil {
				t.Fatalf("disabled Mini App produced button %+v", button)
			}
		}
	}
}
