//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"bot/internal/bot"
	"bot/internal/store"
	"bot/internal/testutil"
)

func miniAppLifecycleSession(t *testing.T, run *botRun) string {
	t.Helper()
	ticket, err := run.miniApp.NewLaunchTicket(chatID, userID)
	if err != nil {
		t.Fatal(err)
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
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session status=%d body=%s", response.StatusCode, body)
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &session); err != nil || session.Token == "" {
		t.Fatalf("session body=%s err=%v", body, err)
	}
	return session.Token
}

func latestTaskApprovalCallback(t *testing.T, tgAPI *testutil.Telegram, prefix string) (string, int) {
	t.Helper()
	callback := ""
	messageID := 0
	for _, message := range tgAPI.Sent() {
		if !strings.Contains(message.Text, "запросила новое сетевое действие") {
			continue
		}
		var markup models.InlineKeyboardMarkup
		if err := json.Unmarshal([]byte(message.ReplyMarkup), &markup); err != nil {
			t.Fatal(err)
		}
		for _, row := range markup.InlineKeyboard {
			for _, button := range row {
				if strings.HasPrefix(button.CallbackData, prefix) {
					callback = button.CallbackData
					messageID = message.MessageID
				}
			}
		}
	}
	return callback, messageID
}

func pushTaskLifecycleCallback(tgAPI *testutil.Telegram, updateID int64, callback string, messageID int) {
	tgAPI.Push(models.Update{ID: updateID, CallbackQuery: &models.CallbackQuery{
		ID: "task-lifecycle", Data: callback, From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: messageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		}},
	}})
}

func TestMiniAppTaskLifecycleRequiresFreshApprovalAndClearsState(t *testing.T) {
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(provider.Close)
	strata := testutil.NewStrata(t, secretStrataKey,
		testutil.StrataReply{Content: `{"steps":[{"description":"Reason about the bounded goal"}]}`, Model: "fake-model"},
		testutil.StrataReply{ToolCallID: "lifecycle-first", ToolName: "web_search", ToolArguments: `{"query":"lifecycle scoped query"}`, Model: "fake-model"},
		testutil.StrataReply{Content: "result discarded by cancellation", Model: "fake-model", Delay: 2 * time.Second},
		testutil.StrataReply{ToolCallID: "lifecycle-after-resume", ToolName: "web_search", ToolArguments: `{"query":"lifecycle scoped query"}`, Model: "fake-model"},
	)
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{
		"WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": provider.URL,
		"MINIAPP_ENABLED": "true", "MINIAPP_PUBLIC_URL": "https://example.test/companion",
	})
	db := openStore(t, path)
	run := startBot(t, context.Background(), cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, 0)
	bearer := miniAppLifecycleSession(t, run)

	tgAPI.PushText(900, chatID, userID, "/task inspect the bounded goal")
	waitFor(t, "initial task network approval", func() bool {
		current, found, err := store.CurrentTask(context.Background(), db, chatID)
		return err == nil && found && current.Status == store.TaskStatusAwaitingApproval
	})
	initial, found, err := store.CurrentTask(context.Background(), db, chatID)
	if err != nil || !found {
		t.Fatalf("initial task found=%v err=%v", found, err)
	}
	scopeCallback, approvalMessageID := latestTaskApprovalCallback(t, tgAPI, "menu:network:scope:")
	if scopeCallback == "" || approvalMessageID == 0 {
		t.Fatalf("initial scope callback=%q message=%d", scopeCallback, approvalMessageID)
	}
	pushTaskLifecycleCallback(tgAPI, 901, scopeCallback, approvalMessageID)
	waitFor(t, "scoped network call and delayed step result", func() bool {
		return providerCalls.Load() == 1 && strata.ChatCalls() == 3
	})

	status, body := miniAppSettingsRequest(t, run.miniAppURL, http.MethodPost, "/api/tasks/cancel", bearer, "")
	if status != http.StatusOK {
		t.Fatalf("Mini App cancel status=%d body=%s", status, body)
	}
	cancelled, found, err := store.CurrentTask(context.Background(), db, chatID)
	if err != nil || !found || cancelled.Status != store.TaskStatusCancelled || cancelled.ContinuationJSON != "" || cancelled.ApprovalToken != "" {
		t.Fatalf("cancelled task=%+v found=%v err=%v", cancelled, found, err)
	}
	status, body = miniAppSettingsRequest(t, run.miniAppURL, http.MethodPost, "/api/tasks/"+strconv.FormatInt(initial.ID, 10)+"/resume", bearer, "")
	if status != http.StatusOK {
		t.Fatalf("Mini App resume status=%d body=%s", status, body)
	}

	waitFor(t, "fresh network approval after Mini App resume", func() bool {
		current, found, err := store.CurrentTask(context.Background(), db, chatID)
		return err == nil && found && current.Status == store.TaskStatusAwaitingApproval && current.ApprovalToken != "" && current.ApprovalToken != initial.ApprovalToken
	})
	fresh, found, err := store.CurrentTask(context.Background(), db, chatID)
	if err != nil || !found {
		t.Fatalf("fresh task found=%v err=%v", found, err)
	}
	if providerCalls.Load() != 1 || strata.ChatCalls() != 4 {
		t.Fatalf("stale grant ran after resume provider=%d model=%d", providerCalls.Load(), strata.ChatCalls())
	}
	freshConfirm, freshMessageID := latestTaskApprovalCallback(t, tgAPI, "menu:network:confirm:")
	if freshConfirm != "menu:network:confirm:"+fresh.ApprovalToken || freshMessageID == 0 {
		t.Fatalf("fresh callback=%q token=%q message=%d", freshConfirm, fresh.ApprovalToken, freshMessageID)
	}

	markupCalls := tgAPI.Calls("editMessageReplyMarkup")
	status, body = miniAppSettingsRequest(t, run.miniAppURL, http.MethodPost, "/api/tasks/clear", bearer, "")
	if status != http.StatusOK || !strings.Contains(string(body), `"deleted":1`) {
		t.Fatalf("Mini App clear status=%d body=%s", status, body)
	}
	if _, found, err := store.CurrentTask(context.Background(), db, chatID); err != nil || found {
		t.Fatalf("cleared task found=%v err=%v", found, err)
	}
	if _, found, err := store.TaskByApprovalToken(context.Background(), db, fresh.ApprovalToken); err != nil || found {
		t.Fatalf("cleared continuation found=%v err=%v", found, err)
	}
	if tgAPI.Calls("editMessageReplyMarkup") <= markupCalls {
		t.Fatal("Mini App clear did not remove the Telegram approval keyboard")
	}

	pushTaskLifecycleCallback(tgAPI, 902, freshConfirm, freshMessageID)
	quiet()
	if providerCalls.Load() != 1 || strata.ChatCalls() != 4 {
		t.Fatalf("cleared approval executed provider=%d model=%d", providerCalls.Load(), strata.ChatCalls())
	}
	secretLeak(t, "Mini App task lifecycle Telegram", tgAPI.Joined())
	secretLeak(t, "Mini App task lifecycle logs", run.logs.String())
	run.stop()
}
