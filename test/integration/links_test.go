//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"bot/internal/bot"
	"bot/internal/store"
	"bot/internal/testutil"
)

func TestLinksFakeTelegramOptInSummaryAndDelete(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "Краткое резюме", Model: "fake-model"})
	var targetRequests atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		targetRequests.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><head><title>Сохранённая страница</title></head><body>Привет мир. Факты для краткого резюме.</body></html>"))
	}))
	t.Cleanup(target.Close)
	_, port, err := net.SplitHostPort(target.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	urlClient := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, target.Listener.Addr().String())
			},
		},
	}
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{
		"TELEGRAM_ALLOWED_USERS": fmt.Sprintf("%d,%d", userID, userID+1),
		// The save path fetches the page, so the reader must be enabled: since v1.40 /links add and its
		// confirmation obey URL_READER_ENABLED, and a bare URL is not stored while the reader is off.
		"URL_READER_ENABLED": "true",
	})
	db := openStore(t, path)
	run := startBotWithURLReader(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, urlClient, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second, Workers: 2}, 0)
	rawURL := "https://example.com:" + port + "/saved#fragment"
	tgAPI.PushText(800, chatID, userID, rawURL)
	waitFor(t, "bare URL save offer", func() bool { return tgAPI.Calls("sendMessage") == 1 })
	quiet()
	if targetRequests.Load() != 0 || strata.ChatCalls() != 0 {
		t.Fatalf("bare URL ran silently: target=%d model=%d", targetRequests.Load(), strata.ChatCalls())
	}
	if rows, err := store.ListSavedLinks(ctx, db, chatID, 20); err != nil || len(rows) != 0 {
		t.Fatalf("bare URL stored rows=%+v err=%v", rows, err)
	}
	offer := tgAPI.Sent()[0]
	var offerMarkup models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(offer.ReplyMarkup), &offerMarkup); err != nil {
		t.Fatal(err)
	}
	confirm := ""
	for _, row := range offerMarkup.InlineKeyboard {
		for _, button := range row {
			if strings.HasPrefix(button.CallbackData, "menu:link:save:confirm:") {
				confirm = button.CallbackData
			}
			if strings.Contains(button.CallbackData, "example.com") {
				t.Fatalf("URL leaked into callback: %q", button.CallbackData)
			}
		}
	}
	if confirm == "" {
		t.Fatalf("save offer markup=%s", offer.ReplyMarkup)
	}
	callback := func(updateID int64, from int64, data string) {
		tgAPI.Push(models.Update{ID: updateID, CallbackQuery: &models.CallbackQuery{
			ID: fmt.Sprintf("links-%d", updateID), Data: data, From: models.User{ID: from, Username: "tester"},
			Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: offer.MessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}}},
		}})
	}
	callback(801, userID+1, confirm)
	waitFor(t, "wrong-user save refusal", func() bool {
		return len(tgAPI.Edits()) == 1 && strings.Contains(tgAPI.Edits()[0].Text, "другому пользователю")
	})
	if targetRequests.Load() != 0 || tgAPI.Calls("sendMessage") != 1 || tgAPI.Edits()[0].MessageID != offer.MessageID {
		t.Fatal("wrong user authorized URL fetch or received a replacement message")
	}
	callback(802, userID, confirm)
	waitFor(t, "confirmed save", func() bool { return len(tgAPI.Edits()) == 2 && targetRequests.Load() == 1 })
	if tgAPI.Calls("sendMessage") != 1 || tgAPI.Edits()[1].MessageID != offer.MessageID {
		t.Fatalf("confirmed save edits=%+v sends=%d", tgAPI.Edits(), tgAPI.Calls("sendMessage"))
	}
	rows, err := store.ListSavedLinks(ctx, db, chatID, 20)
	if err != nil || len(rows) != 1 || rows[0].CanonicalURL != "https://example.com:"+port+"/saved" || rows[0].Title != "Сохранённая страница" {
		t.Fatalf("saved rows=%+v err=%v", rows, err)
	}
	linkID := rows[0].ID
	if strata.ChatCalls() != 0 {
		t.Fatal("add called the model")
	}

	tgAPI.PushText(803, chatID, userID, "/links")
	waitFor(t, "inline links list", func() bool { return tgAPI.Calls("sendMessage") == 2 })
	list := tgAPI.Sent()[1]
	if !strings.Contains(list.Text, fmt.Sprintf("#%d", linkID)) || !strings.Contains(list.Text, "example.com") || !strings.Contains(list.ReplyMarkup, fmt.Sprintf("menu:link:summary:%d", linkID)) || !strings.Contains(list.ReplyMarkup, "menu:docs") {
		t.Fatalf("links list=%+v markup=%s", list, list.ReplyMarkup)
	}
	tgAPI.PushText(804, chatID, userID, "/links search ПРИВЕТ")
	waitFor(t, "Unicode links search", func() bool { return tgAPI.Calls("sendMessage") == 3 })
	if search := tgAPI.Sent()[2].Text; !strings.Contains(search, "Сохранённая страница") || !strings.Contains(search, "Область поиска") {
		t.Fatalf("search=%q", search)
	}

	tgAPI.Push(models.Update{ID: 805, CallbackQuery: &models.CallbackQuery{
		ID: "links-summary", Data: fmt.Sprintf("menu:link:summary:%d", linkID), From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: list.MessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}}},
	}})
	waitFor(t, "stored-link summary", func() bool { return len(tgAPI.Edits()) == 3 && strata.ChatCalls() == 1 })
	if targetRequests.Load() != 1 || !strings.Contains(tgAPI.Edits()[2].Text, "Краткое резюме") || tgAPI.Edits()[2].MessageID != list.MessageID || tgAPI.Calls("sendMessage") != 3 {
		t.Fatalf("summary target=%d edit=%+v sends=%d", targetRequests.Load(), tgAPI.Edits()[2], tgAPI.Calls("sendMessage"))
	}
	if body := strata.LastRequestBody(); strings.Contains(body, `"tools":`) || !strings.Contains(body, "недоверенные данные") || !strings.Contains(body, "Факты для краткого резюме") {
		t.Fatalf("summary request=%s", body)
	}

	tgAPI.PushText(806, chatID, userID, fmt.Sprintf("/links delete %d", linkID))
	waitFor(t, "text link deletion", func() bool { return tgAPI.Calls("sendMessage") == 4 })
	if _, found, err := store.GetSavedLink(ctx, db, chatID, linkID); err != nil || found {
		t.Fatalf("deleted link found=%v err=%v", found, err)
	}
	tgAPI.PushText(807, chatID, userID, fmt.Sprintf("/links summary %d", linkID))
	waitFor(t, "missing-link summary error", func() bool { return tgAPI.Calls("sendMessage") == 5 })
	if strata.ChatCalls() != 1 || targetRequests.Load() != 1 || !strings.Contains(tgAPI.Sent()[4].Text, "больше недоступны") {
		t.Fatalf("missing summary model=%d target=%d reply=%q", strata.ChatCalls(), targetRequests.Load(), tgAPI.Sent()[4].Text)
	}
	secretLeak(t, "links run log", run.logs.String())
	if strings.Contains(run.logs.String(), rawURL) {
		t.Fatal("saved URL leaked into logs")
	}
	run.stop()
}
