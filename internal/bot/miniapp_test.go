package bot

import (
	"context"
	"errors"
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
	// A private chat: Telegram makes the chat id equal to the user id there.
	event := testEvent(11, 11, 180, "/help menu:more")
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

// TestMiniAppIsPrivateChatOnly: a group ticket would scope one session to a chat shared by several people,
// so the button is not offered there and the ticket issuer refuses the chat.
func TestMiniAppIsPrivateChatOnly(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"MINIAPP_ENABLED": "true", "MINIAPP_PUBLIC_URL": "https://example.test/companion",
		"TELEGRAM_ALLOWED_USERS": "11",
	})
	issuer := &fakeMiniAppTickets{ticket: "0123456789abcdef012345"}
	replier := &fakeReplier{}
	d := newTestDispatcher(t, cfg, nil, replier, testStore(t))
	d.deps.MiniAppTickets = issuer

	group := testEvent(-100500, 11, 181, "/help menu:more")
	group.ChatType = "supergroup"
	if err := d.showMoreMenu(context.Background(), group); err != nil {
		t.Fatal(err)
	}
	if len(replier.menus) != 1 {
		t.Fatalf("menus=%d", len(replier.menus))
	}
	for _, row := range replier.menus[0].InlineKeyboard {
		for _, button := range row {
			if button.WebApp != nil {
				t.Fatalf("a group menu offered the Mini App button %+v", button)
			}
		}
	}
	if _, err := d.miniAppButton(group); !errors.Is(err, ErrMiniAppPrivateOnly) {
		t.Fatalf("miniAppButton in a group = %v, want ErrMiniAppPrivateOnly", err)
	}
	if issuer.chatID != 0 {
		t.Fatalf("a ticket was issued for chat %d of a group", issuer.chatID)
	}
}
