package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"bot/internal/store"
)

func appendWebappAssistant(t *testing.T, app *App, chatID int64, userID int64, content string, model string, tokensIn int64, tokensOut int64) {
	t.Helper()
	if _, err := store.Append(context.Background(), app.db, store.Message{
		ChatID: chatID, UserID: userID, Role: store.RoleAssistant, Content: content, Model: model,
		TokensIn: &tokensIn, TokensOut: &tokensOut,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUsageReturnsContentFreeAggregatesLimitsAndRemaining(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	app.cfg.UserDailyModelLimit = 3
	app.cfg.UserDailyNetworkToolLimit = 2
	ctx := context.Background()
	privateQuery := "private query text must not leave the store"
	privateAnswer := "private answer text must not leave the store"
	if _, err := store.Append(ctx, app.db, store.Message{ChatID: testChatID, UserID: testUserID, Role: store.RoleUser, Content: privateQuery}); err != nil {
		t.Fatal(err)
	}
	appendWebappAssistant(t, app, testChatID, testUserID, privateAnswer, "model-a", 10, 20)
	appendWebappAssistant(t, app, testChatID+1, testUserID+1, "other user private answer", "model-a", 1000, 2000)
	for index := 0; index < 2; index++ {
		if err := store.LogEvent(ctx, app.db, testUserID, store.EventModelCall, "model-a"); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"read_url", "web_search", "calculator", "rag", privateQuery} {
		if err := store.LogEvent(ctx, app.db, testUserID, store.EventUsage, tool); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.LogEvent(ctx, app.db, testUserID+1, store.EventModelCall, "model-a"); err != nil {
		t.Fatal(err)
	}
	var messagesBefore, eventsBefore int
	if err := app.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM messages), (SELECT COUNT(*) FROM events)`).Scan(&messagesBefore, &eventsBefore); err != nil {
		t.Fatal(err)
	}

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/usage", bearer)
	if status != http.StatusOK {
		t.Fatalf("usage status=%d body=%s", status, body)
	}
	var decoded usageResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Date == "" || len(decoded.Models) != 1 {
		t.Fatalf("usage response=%+v", decoded)
	}
	model := decoded.Models[0]
	if model.Model != "model-a" || model.Calls != 2 || model.TokensIn != 10 || model.TokensOut != 20 {
		t.Fatalf("model usage=%+v", model)
	}
	if decoded.Tools.ReadURL != 1 || decoded.Tools.WebSearch != 1 || decoded.Tools.Calculator != 1 || decoded.Tools.RAG != 1 {
		t.Fatalf("tool usage=%+v", decoded.Tools)
	}
	if decoded.Limits.Model.Used != 2 || decoded.Limits.Model.Limit != 3 || decoded.Limits.Model.Remaining == nil || *decoded.Limits.Model.Remaining != 1 {
		t.Fatalf("model limit=%+v", decoded.Limits.Model)
	}
	if decoded.Limits.NetworkTools.Used != 2 || decoded.Limits.NetworkTools.Limit != 2 || decoded.Limits.NetworkTools.Remaining == nil || *decoded.Limits.NetworkTools.Remaining != 0 {
		t.Fatalf("network limit=%+v", decoded.Limits.NetworkTools)
	}
	for _, forbidden := range []string{privateQuery, privateAnswer, "other user private answer", `"content"`, `"goal"`, `"detail"`} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("usage response contains %q: %s", forbidden, body)
		}
	}

	app.cfg.UserDailyModelLimit = 0
	app.cfg.UserDailyNetworkToolLimit = 0
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/usage", bearer)
	if status != http.StatusOK || !strings.Contains(string(body), `"remaining":null`) {
		t.Fatalf("unlimited usage status=%d body=%s", status, body)
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Limits.Model.Remaining != nil || decoded.Limits.NetworkTools.Remaining != nil {
		t.Fatalf("unlimited limits=%+v", decoded.Limits)
	}
	status, body, _ = apiRequest(t, server, http.MethodPost, "/api/usage", bearer)
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("POST usage status=%d body=%s", status, body)
	}
	var messagesAfter, eventsAfter int
	if err := app.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM messages), (SELECT COUNT(*) FROM events)`).Scan(&messagesAfter, &eventsAfter); err != nil {
		t.Fatal(err)
	}
	if messagesAfter != messagesBefore || eventsAfter != eventsBefore {
		t.Fatalf("read-only usage wrote rows: messages %d/%d events %d/%d", messagesBefore, messagesAfter, eventsBefore, eventsAfter)
	}
}
