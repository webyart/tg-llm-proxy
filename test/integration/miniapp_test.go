//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"bot/internal/bot"
	"bot/internal/store"
	"bot/internal/testutil"
)

func miniAppInitData(t *testing.T, token string, id int64, authDate time.Time) string {
	t.Helper()
	user, err := json.Marshal(map[string]any{"id": id, "first_name": "Integration"})
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"auth_date": {strconv.FormatInt(authDate.Unix(), 10)}, "query_id": {"AAEintegration"}, "user": {string(user)}}
	check := "auth_date=" + values.Get("auth_date") + "\nquery_id=" + values.Get("query_id") + "\nuser=" + values.Get("user")
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = mac.Write([]byte(check))
	values.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return values.Encode()
}

func TestMiniAppFakeTelegramLaunchAndReadOnlyAPI(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "must not be called"})
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{
		"MINIAPP_ENABLED": "true", "MINIAPP_PUBLIC_URL": "https://example.test/companion",
	})
	db := openStore(t, path)
	if _, err := store.Append(ctx, db, store.Message{ChatID: chatID, UserID: userID, Role: store.RoleUser, Content: "visible through Mini App"}); err != nil {
		t.Fatal(err)
	}
	run := startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, 0)
	if run.miniAppURL == "" {
		t.Fatal("Mini App test server was not wired")
	}
	defer func() {
		if t.Failed() {
			t.Logf("Telegram calls/send texts: %d %q; logs: %s", tgAPI.Calls("sendMessage"), tgAPI.Texts(), run.logs.String())
		}
	}()

	tgAPI.PushText(700, chatID, userID, "/start")
	waitFor(t, "root menu for Mini App", func() bool { return tgAPI.Calls("sendMessage") == 1 })
	menuMessageID := tgAPI.Sent()[0].MessageID
	tgAPI.FailNext("editMessageText", 400, "Bad Request: message not found", 0)
	tgAPI.Push(models.Update{ID: 701, CallbackQuery: &models.CallbackQuery{
		ID: "miniapp-launch", Data: "menu:more", From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: menuMessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}}},
	}})
	waitFor(t, "More menu with WebApp button", func() bool { return tgAPI.Calls("sendMessage") == 2 })
	var markup models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(tgAPI.Sent()[1].ReplyMarkup), &markup); err != nil {
		t.Fatal(err)
	}
	launchURL := ""
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if button.WebApp != nil {
				launchURL = button.WebApp.URL
			}
		}
	}
	parsed, err := url.Parse(launchURL)
	if err != nil || !strings.HasPrefix(launchURL, run.miniAppURL+"#ticket=") {
		t.Fatalf("WebApp launch URL=%q err=%v", launchURL, err)
	}
	ticket := parsed.Fragment
	if value, err := url.QueryUnescape(strings.TrimPrefix(ticket, "ticket=")); err == nil {
		ticket = value
	}
	raw := miniAppInitData(t, secretToken, userID, time.Now())
	req, err := http.NewRequest(http.MethodPost, run.miniAppURL+"/api/session", strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Launch-Ticket", ticket)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session exchange status=%d body=%s", response.StatusCode, body)
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &session); err != nil || session.Token == "" {
		t.Fatalf("session body=%s err=%v", body, err)
	}

	historyReq, _ := http.NewRequest(http.MethodGet, run.miniAppURL+"/api/history?limit=5", nil)
	historyReq.Header.Set("Authorization", "Bearer "+session.Token)
	historyResponse, err := http.DefaultClient.Do(historyReq)
	if err != nil {
		t.Fatal(err)
	}
	historyBody, _ := io.ReadAll(historyResponse.Body)
	historyResponse.Body.Close()
	if historyResponse.StatusCode != http.StatusOK || !bytes.Contains(historyBody, []byte("visible through Mini App")) {
		t.Fatalf("history status=%d body=%s", historyResponse.StatusCode, historyBody)
	}
	if strata.ChatCalls() != 0 {
		t.Fatalf("Mini App launch made %d model calls", strata.ChatCalls())
	}
	if len(rowsOf(t, db)) != 1 {
		t.Fatal("read-only Mini App changed chat history")
	}

	replayReq, _ := http.NewRequest(http.MethodPost, run.miniAppURL+"/api/session", strings.NewReader(raw))
	replayReq.Header.Set("Content-Type", "text/plain")
	replayReq.Header.Set("X-Launch-Ticket", ticket)
	replayResponse, err := http.DefaultClient.Do(replayReq)
	if err != nil {
		t.Fatal(err)
	}
	replayResponse.Body.Close()
	if replayResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("ticket replay status=%d", replayResponse.StatusCode)
	}
	logText := run.logs.String()
	if strings.Contains(logText, raw) || strings.Contains(logText, session.Token) || strings.Contains(logText, ticket) {
		t.Fatalf("Mini App credentials reached logs: %s", logText)
	}
	secretLeak(t, "Mini App integration logs", logText)
	run.stop()
}
