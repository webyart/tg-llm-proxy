//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"bot/internal/bot"
	"bot/internal/store"
	"bot/internal/testutil"
)

func TestRemindersFakeTelegramDeliveryRestartAndCancellation(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey)
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{"PARSE_MODE": "MarkdownV2"})
	db := openStore(t, path)
	first := startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second, Workers: 2}, 0)

	tgAPI.PushText(600, chatID, userID, "/remind через 2 секунды напомни *без* форматирования")
	waitFor(t, "reminder creation", func() bool { return tgAPI.Calls("sendMessage") == 1 })
	rows, err := store.ListReminders(ctx, db, chatID, 20)
	if err != nil || len(rows) != 1 {
		t.Fatalf("created rows=%+v err=%v", rows, err)
	}
	firstID := rows[0].ID
	tgAPI.PushText(601, chatID, userID, "/reminders")
	waitFor(t, "reminder inline list", func() bool { return tgAPI.Calls("sendMessage") == 2 })
	list := tgAPI.Sent()[1]
	if !strings.Contains(list.Text, fmt.Sprintf("#%d", firstID)) || !strings.Contains(list.ReplyMarkup, fmt.Sprintf("menu:reminder:cancel:%d", firstID)) || !strings.Contains(list.ReplyMarkup, "menu:more") {
		t.Fatalf("list=%+v markup=%s", list, list.ReplyMarkup)
	}
	waitFor(t, "due reminder delivery", func() bool { return tgAPI.Calls("sendMessage") == 3 })
	delivered := tgAPI.Sent()[2]
	if delivered.Text != "Напоминание: напомни *без* форматирования" || delivered.ParseMode != "" {
		t.Fatalf("plain delivery=%+v", delivered)
	}
	quiet()
	quiet()
	if tgAPI.Calls("sendMessage") != 3 {
		t.Fatalf("one-shot delivered more than once: %d sends", tgAPI.Calls("sendMessage"))
	}
	if strata.ChatCalls() != 0 {
		t.Fatalf("reminders made %d model calls", strata.ChatCalls())
	}
	first.stop()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	secondDB := openStore(t, path)
	second := startBot(t, ctx, cfg, secondDB, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second, Workers: 2}, storedOffset(t, secondDB))
	quiet()
	quiet()
	if tgAPI.Calls("sendMessage") != 3 {
		t.Fatalf("restart duplicated sent reminder: %d sends", tgAPI.Calls("sendMessage"))
	}
	tgAPI.PushText(602, chatID, userID, "/remind через 1 час отменить")
	waitFor(t, "second reminder creation", func() bool { return tgAPI.Calls("sendMessage") == 4 })
	rows, err = store.ListReminders(ctx, secondDB, chatID, 20)
	if err != nil || len(rows) != 1 {
		t.Fatalf("second rows=%+v err=%v", rows, err)
	}
	tgAPI.PushText(603, chatID, userID, fmt.Sprintf("/reminders delete %d", rows[0].ID))
	waitFor(t, "text cancellation", func() bool { return tgAPI.Calls("sendMessage") == 5 })
	rows, err = store.ListReminders(ctx, secondDB, chatID, 20)
	if err != nil || len(rows) != 0 {
		t.Fatalf("cancelled rows=%+v err=%v", rows, err)
	}
	if strata.ChatCalls() != 0 {
		t.Fatal("reminder flow reached STRATA")
	}
	second.stop()
}
