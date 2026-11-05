//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"bot/internal/bot"
	"bot/internal/store"
	"bot/internal/testutil"
)

func TestExportSearchAndDeleteHistoryEndToEnd(t *testing.T) {
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "не должно быть"})
	cfg := integrationEnv(t, path, strata.BaseURL(), nil)
	db := openStore(t, path)
	startBot(t, t.Context(), cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, storedOffset(t, db))
	telegramID := int64(500)
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: chatID, UserID: userID, Role: store.RoleUser, Content: "Привет МИР", TelegramMessageID: &telegramID}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: chatID, UserID: userID, Role: store.RoleAssistant, Content: "ответ"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: chatID + 1, UserID: userID, Role: store.RoleUser, Content: "другой чат"}); err != nil {
		t.Fatalf("Append other chat: %v", err)
	}

	tgAPI.PushText(501, chatID, userID, "/export json")
	waitFor(t, "history document", func() bool { return len(tgAPI.Documents()) == 1 })
	document := tgAPI.Documents()[0]
	if document.ChatID != chatID || document.Name != "history-4242.json" || document.MIME != "application/octet-stream" {
		t.Fatalf("document=%+v", document)
	}
	var exported struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(document.Data, &exported); err != nil || len(exported.Messages) != 2 || exported.Messages[0].Content != "Привет МИР" {
		t.Fatalf("export=%+v err=%v", exported, err)
	}

	tgAPI.PushText(502, chatID, userID, "/search мир")
	waitFor(t, "Unicode history search", func() bool { return strings.Contains(tgAPI.Joined(), "Привет МИР") })
	tgAPI.PushText(503, chatID, userID, "/delete")
	waitFor(t, "delete confirmation", func() bool { return strings.Contains(tgAPI.Joined(), "Удалить всю историю") })
	callbackData := ""
	menuMessageID := 0
	for _, message := range tgAPI.Sent() {
		if !strings.Contains(message.Text, "Удалить всю историю") {
			continue
		}
		var markup models.InlineKeyboardMarkup
		if err := json.Unmarshal([]byte(message.ReplyMarkup), &markup); err != nil || len(markup.InlineKeyboard) == 0 || len(markup.InlineKeyboard[0]) == 0 {
			t.Fatalf("delete markup=%q err=%v", message.ReplyMarkup, err)
		}
		callbackData = markup.InlineKeyboard[0][0].CallbackData
		menuMessageID = message.MessageID
	}
	if callbackData == "" || menuMessageID == 0 {
		t.Fatalf("delete callback or menu message id not found")
	}
	tgAPI.Push(models.Update{ID: 504, CallbackQuery: &models.CallbackQuery{
		ID: "delete-confirm", Data: callbackData, From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: menuMessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}}},
	}})
	waitFor(t, "confirmed delete", func() bool { return strings.Contains(tgAPI.Joined(), "Удалено сообщений: 2") })
	rows, err := store.HistoryOfChat(context.Background(), db, chatID, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("deleted history=%+v err=%v", rows, err)
	}
	other, err := store.HistoryOfChat(context.Background(), db, chatID+1, 10)
	if err != nil || len(other) != 1 {
		t.Fatalf("other chat=%+v err=%v", other, err)
	}
	if strata.ChatCalls() != 0 {
		t.Fatalf("history commands called model %d times", strata.ChatCalls())
	}
}
