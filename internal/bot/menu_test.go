package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestMenuMarkupAndStartSendInlineKeyboard(t *testing.T) {
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, testConfig(t, map[string]string{"PARSE_MODE": "MarkdownV2"}), nil, r, testStore(t))
	if err := dispatch(t, d, testEvent(100, 11, 180, "/start")); err != nil {
		t.Fatalf("/start: %v", err)
	}
	if !strings.Contains(r.texts()[0], `Привет\!`) {
		t.Errorf("menu text must escape '!' for MarkdownV2, got %q", r.texts()[0])
	}
	if err := d.sendMenu(context.Background(), 100, "a+b"); err != nil {
		t.Fatalf("sendMenu with '+': %v", err)
	}
	if got := r.texts()[len(r.texts())-1]; got != "a\\+b" {
		t.Errorf("menu text must escape '+', got %q", got)
	}
	if len(r.menus) != 2 {
		t.Fatalf("/start must send one inline keyboard, got %d", len(r.menus))
	}
	seen := map[string]bool{}
	for _, row := range r.menus[0].InlineKeyboard {
		for _, button := range row {
			seen[button.CallbackData] = true
		}
	}
	for _, name := range []string{CommandAsk, CommandModel, CommandStatus, CommandHistory, CommandReset, CommandSkip, CommandUnskip, CommandStats, CommandWhoAmI, CommandPing, CommandHelp, CommandStart} {
		if !seen["cmd:"+name] {
			t.Errorf("menu misses cmd:%s", name)
		}
	}
}

func TestCallbackEventUsesOnlyRegisteredCommands(t *testing.T) {
	var d = newTestDispatcher(t, testConfig(t, nil), nil, &fakeReplier{}, testStore(t))
	update := &models.Update{
		ID: 181,
		CallbackQuery: &models.CallbackQuery{
			ID:   "cb1",
			Data: "cmd:ping",
			From: models.User{ID: 11, Username: "tester"},
			Message: models.MaybeInaccessibleMessage{Message: &models.Message{
				ID:   181,
				Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
			}},
		},
	}
	ev, ok := d.callbackEventOf(update)
	if !ok || ev.Text != "/ping" || ev.ChatID != 100 || ev.UserID != 11 || ev.UpdateID != 181 {
		t.Fatalf("callbackEventOf = %+v ok=%v", ev, ok)
	}
	update.CallbackQuery.Data = "cmd:unknown"
	if _, ok := d.callbackEventOf(update); ok {
		t.Fatal("an unregistered callback must be rejected")
	}
	update.CallbackQuery.Data = "not-a-command"
	if _, ok := d.callbackEventOf(update); ok {
		t.Fatal("arbitrary callback data must be rejected")
	}
}
