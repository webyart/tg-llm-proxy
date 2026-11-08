//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"bot/internal/bot"
	"bot/internal/store"
	"bot/internal/testutil"
	"bot/internal/webapp"
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

func miniAppSettingsRequest(t *testing.T, baseURL string, method string, path string, bearer string, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, baseURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, responseBody
}

func miniAppSessionExchange(t *testing.T, baseURL string, raw string, ticket string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/session", strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")
	if ticket != "" {
		req.Header.Set("X-Launch-Ticket", ticket)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func miniAppSessionToken(t *testing.T, body []byte) string {
	t.Helper()
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &session); err != nil || session.Token == "" {
		t.Fatalf("session body=%s err=%v", body, err)
	}
	return session.Token
}

func TestMiniAppFakeTelegramLaunchAndSettingsAPI(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "must not be called", Models: []string{"qwen-test", "qwen-alt"}})
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{
		"MINIAPP_ENABLED": "true", "MINIAPP_PUBLIC_URL": "https://example.test/companion",
	})
	db := openStore(t, path)
	if _, err := store.Append(ctx, db, store.Message{ChatID: chatID, UserID: userID, Role: store.RoleUser, Content: "visible through Mini App"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, db, store.Message{ChatID: userID, UserID: userID, Role: store.RoleUser, Content: "private ticketless history"}); err != nil {
		t.Fatal(err)
	}
	run := startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, 0)
	if run.miniAppURL == "" {
		t.Fatal("Mini App test server was not wired")
	}
	rawMenuButton, ok := tgAPI.MenuButton()
	var menuButton models.MenuButton
	if !ok || json.Unmarshal([]byte(rawMenuButton), &menuButton) != nil || menuButton.Type != models.MenuButtonTypeWebApp || menuButton.WebApp == nil || menuButton.WebApp.Text != "Меню" || menuButton.WebApp.WebApp.URL != run.miniAppURL {
		t.Fatalf("Mini App menu button payload=%q present=%v decoded=%+v, want URL %q", rawMenuButton, ok, menuButton, run.miniAppURL)
	}
	defer func() {
		if t.Failed() {
			t.Logf("Telegram calls/send texts: %d %q; logs: %s", tgAPI.Calls("sendMessage"), tgAPI.Texts(), run.logs.String())
		}
	}()

	tgAPI.PushText(700, userID, userID, "/start")
	waitFor(t, "root menu for Mini App", func() bool { return tgAPI.Calls("sendMessage") == 1 })
	menuMessageID := tgAPI.Sent()[0].MessageID
	tgAPI.FailNext("editMessageText", 400, "Bad Request: message not found", 0)
	tgAPI.Push(models.Update{ID: 701, CallbackQuery: &models.CallbackQuery{
		ID: "miniapp-launch-rejected", Data: "menu:more", From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: menuMessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: userID, Type: models.ChatTypePrivate}}},
	}})
	waitFor(t, "rejected More menu edit", func() bool {
		return tgAPI.Calls("answerCallbackQuery") == 1 && tgAPI.Calls("editMessageText") == 1
	})
	quiet()
	if tgAPI.Calls("sendMessage") != 1 || len(tgAPI.Edits()) != 0 {
		t.Fatalf("failed edit produced a duplicate: sends=%d edits=%+v", tgAPI.Calls("sendMessage"), tgAPI.Edits())
	}
	tgAPI.Push(models.Update{ID: 702, CallbackQuery: &models.CallbackQuery{
		ID: "miniapp-launch", Data: "menu:more", From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: menuMessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: userID, Type: models.ChatTypePrivate}}},
	}})
	waitFor(t, "More menu edited with WebApp button", func() bool {
		return tgAPI.Calls("answerCallbackQuery") == 2 && len(tgAPI.Edits()) == 1
	})
	if tgAPI.Calls("sendMessage") != 1 || tgAPI.Edits()[0].MessageID != menuMessageID {
		t.Fatalf("successful More navigation sends=%d edit=%+v", tgAPI.Calls("sendMessage"), tgAPI.Edits()[0])
	}
	var markup models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(tgAPI.Edits()[0].ReplyMarkup), &markup); err != nil {
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
	sessionStatus, body := miniAppSessionExchange(t, run.miniAppURL, raw, ticket)
	if sessionStatus != http.StatusOK {
		t.Fatalf("session exchange status=%d body=%s", sessionStatus, body)
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
	if historyResponse.StatusCode != http.StatusOK || !bytes.Contains(historyBody, []byte("private ticketless history")) || bytes.Contains(historyBody, []byte("visible through Mini App")) {
		t.Fatalf("scoped history status=%d body=%s", historyResponse.StatusCode, historyBody)
	}
	ticketlessStatus, ticketlessBody := miniAppSessionExchange(t, run.miniAppURL, raw, "")
	if ticketlessStatus != http.StatusOK {
		t.Fatalf("ticketless session status=%d body=%s", ticketlessStatus, ticketlessBody)
	}
	ticketlessBearer := miniAppSessionToken(t, ticketlessBody)
	ticketlessReq, _ := http.NewRequest(http.MethodGet, run.miniAppURL+"/api/history?limit=5", nil)
	ticketlessReq.Header.Set("Authorization", "Bearer "+ticketlessBearer)
	ticketlessResponse, err := http.DefaultClient.Do(ticketlessReq)
	if err != nil {
		t.Fatal(err)
	}
	ticketlessHistory, _ := io.ReadAll(ticketlessResponse.Body)
	ticketlessResponse.Body.Close()
	if ticketlessResponse.StatusCode != http.StatusOK || !bytes.Contains(ticketlessHistory, []byte("private ticketless history")) || bytes.Contains(ticketlessHistory, []byte("visible through Mini App")) {
		t.Fatalf("ticketless private history status=%d body=%s", ticketlessResponse.StatusCode, ticketlessHistory)
	}
	if strata.ChatCalls() != 0 {
		t.Fatalf("Mini App launch made %d model calls", strata.ChatCalls())
	}
	if len(rowsOf(t, db)) != 1 {
		t.Fatal("read-only Mini App changed chat history")
	}
	if strata.ModelsCalls() != 0 {
		t.Fatalf("ordinary reads made %d provider calls", strata.ModelsCalls())
	}

	status, settingsBody := miniAppSettingsRequest(t, run.miniAppURL, http.MethodGet, "/api/settings", session.Token, "")
	if status != http.StatusOK || !bytes.Contains(settingsBody, []byte("qwen-alt")) || strata.ModelsCalls() != 1 {
		t.Fatalf("settings read status=%d models calls=%d body=%s", status, strata.ModelsCalls(), settingsBody)
	}
	status, settingsBody = miniAppSettingsRequest(t, run.miniAppURL, http.MethodPut, "/api/settings/model", session.Token, `{"model":"qwen-alt"}`)
	if status != http.StatusOK {
		t.Fatalf("model update status=%d body=%s", status, settingsBody)
	}
	if selected, found, err := store.ChatModel(ctx, db, userID); err != nil || !found || selected != "qwen-alt" {
		t.Fatalf("selected model=%q found=%v err=%v", selected, found, err)
	}
	privatePersona := "integration private persona must not be logged"
	status, settingsBody = miniAppSettingsRequest(t, run.miniAppURL, http.MethodPut, "/api/settings/persona", session.Token, `{"custom":"`+privatePersona+`"}`)
	if status != http.StatusOK {
		t.Fatalf("persona update status=%d body=%s", status, settingsBody)
	}
	if prompt, found, err := store.ChatPersona(ctx, db, userID); err != nil || !found || prompt != privatePersona {
		t.Fatalf("persona found=%v err=%v", found, err)
	}
	status, settingsBody = miniAppSettingsRequest(t, run.miniAppURL, http.MethodPut, "/api/settings/stream", session.Token, `{"enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("stream update status=%d body=%s", status, settingsBody)
	}
	if enabled, found, err := store.ChatStreaming(ctx, db, userID); err != nil || !found || !enabled {
		t.Fatalf("stream enabled=%v found=%v err=%v", enabled, found, err)
	}
	status, settingsBody = miniAppSettingsRequest(t, run.miniAppURL, http.MethodDelete, "/api/settings/stream", session.Token, "")
	if status != http.StatusOK {
		t.Fatalf("stream reset status=%d body=%s", status, settingsBody)
	}
	if _, found, err := store.ChatStreaming(ctx, db, userID); err != nil || found {
		t.Fatalf("stream reset found=%v err=%v", found, err)
	}
	if strata.ChatCalls() != 0 {
		t.Fatalf("settings made %d completion calls", strata.ChatCalls())
	}
	if len(rowsOf(t, db)) != 1 {
		t.Fatal("settings changed chat history")
	}

	replayStatus, _ := miniAppSessionExchange(t, run.miniAppURL, raw, ticket)
	if replayStatus != http.StatusForbidden {
		t.Fatalf("ticket replay status=%d", replayStatus)
	}
	logText := run.logs.String()
	if strings.Contains(logText, raw) || strings.Contains(logText, session.Token) || strings.Contains(logText, ticketlessBearer) || strings.Contains(logText, ticket) || strings.Contains(logText, privatePersona) {
		t.Fatalf("Mini App credentials reached logs: %s", logText)
	}
	secretLeak(t, "Mini App integration logs", logText)
	run.stop()
}

func TestMiniAppSettingsMutationPreservesGroupTokenAttribution(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{
		Content: "group answer", Model: "fake-model", TokensIn: 10, TokensOut: 20,
		Models: []string{"qwen-test", "qwen-alt"},
	})
	const (
		groupID  int64 = -100500
		secondID int64 = 1818
	)
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{
		"TELEGRAM_ALLOWED_USERS": "1717,1818",
		"MINIAPP_ENABLED":        "true",
		"MINIAPP_PUBLIC_URL":     "https://example.test/companion",
	})
	db := openStore(t, path)
	client := newClient(t, cfg, strata)
	startBot(t, ctx, cfg, db, client, tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, 0)
	app := webapp.New(cfg, db, client, slog.Default())
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	pushGroupQuestion := func(updateID int64, senderID int64, text string) {
		tgAPI.Push(models.Update{ID: updateID, Message: &models.Message{
			ID: int(updateID), Date: int(time.Now().Unix()),
			Chat: models.Chat{ID: groupID, Type: models.ChatTypeGroup, Title: "Accounting group"},
			From: &models.User{ID: senderID, FirstName: "Tester"}, Text: text,
		}})
	}
	pushGroupQuestion(800, userID, "first user question")
	waitFor(t, "first group answer", func() bool { return tgAPI.Calls("sendMessage") == 1 && strata.ChatCalls() == 1 })

	// The Mini App is a private-chat companion since v1.40: a group ticket must be refused instead of issued,
	// and a private session must attribute its mutation to that private chat only.
	if _, err := app.NewLaunchTicket(groupID, secondID); !errors.Is(err, webapp.ErrPrivateOnly) {
		t.Fatalf("group ticket err=%v, want ErrPrivateOnly", err)
	}
	ticket, err := app.NewLaunchTicket(secondID, secondID)
	if err != nil {
		t.Fatal(err)
	}
	raw := miniAppInitData(t, secretToken, secondID, time.Now())
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/session", strings.NewReader(raw))
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
		t.Fatalf("group Mini App session status=%d body=%s", response.StatusCode, body)
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &session); err != nil || session.Token == "" {
		t.Fatalf("group Mini App session body=%s err=%v", body, err)
	}
	status, body := miniAppSettingsRequest(t, server.URL, http.MethodPut, "/api/settings/model", session.Token, `{"model":"qwen-alt"}`)
	if status != http.StatusOK {
		t.Fatalf("private Mini App model mutation status=%d body=%s", status, body)
	}
	if selected, found, err := store.ChatModel(ctx, db, secondID); err != nil || !found || selected != "qwen-alt" {
		t.Fatalf("private chat model=%q found=%v err=%v", selected, found, err)
	}
	if _, found, err := store.ChatModel(ctx, db, groupID); err != nil || found {
		t.Fatalf("the group chat received a Mini App setting: found=%v err=%v", found, err)
	}

	pushGroupQuestion(801, secondID, "second user question")
	waitFor(t, "second group answer", func() bool { return tgAPI.Calls("sendMessage") == 2 && strata.ChatCalls() == 2 })
	rows, err := store.HistoryOfChat(ctx, db, groupID, 10)
	if err != nil || len(rows) != 4 {
		t.Fatalf("group history=%+v err=%v", rows, err)
	}
	if rows[1].Role != store.RoleAssistant || rows[1].UserID != userID || rows[3].Role != store.RoleAssistant || rows[3].UserID != secondID {
		t.Fatalf("group assistant attribution=%+v", rows)
	}
	for _, id := range []int64{userID, secondID} {
		usage, err := store.UserDailyUsage(ctx, db, id, time.Now())
		var tokensIn, tokensOut int64
		for _, usageModel := range usage.Models {
			tokensIn += usageModel.TokensIn
			tokensOut += usageModel.TokensOut
		}
		if err != nil || usage.ModelCalls() != 1 || tokensIn != 10 || tokensOut != 20 {
			t.Fatalf("user %d usage=%+v err=%v", id, usage, err)
		}
	}
}
