//go:build integration

package integration

import (
	"context"
	"encoding/json"
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

func TestTaskFakeTelegramSequentialAnswerStatusAndRestart(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey,
		testutil.StrataReply{Content: `{"steps":[{"description":"Reason about the supplied goal"}]}`, Model: "fake-model"},
		testutil.StrataReply{Content: "bounded step result", Model: "fake-model"},
		testutil.StrataReply{Content: "consolidated fake-Telegram answer", Model: "fake-model", TokensIn: 8, TokensOut: 13},
	)
	cfg := integrationEnv(t, path, strata.BaseURL(), nil)
	db := openStore(t, path)
	first := startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second, Workers: 2}, storedOffset(t, db))
	tgAPI.PushText(801, chatID, userID, "/task answer one bounded question")
	waitFor(t, "task final answer", func() bool { return strings.Contains(tgAPI.Joined(), "consolidated fake-Telegram answer") })
	joined := tgAPI.Joined()
	if !strings.Contains(joined, "1/1") || !strings.Contains(joined, "Задача #1 завершена (1/1)") {
		t.Fatalf("task progress=%q", joined)
	}
	if strata.ChatCalls() != 3 {
		t.Fatalf("task model calls=%d, want planner + step + synthesis", strata.ChatCalls())
	}
	rows := rowsOf(t, db)
	if len(rows) != 2 || rows[0].Role != store.RoleUser || rows[1].Role != store.RoleAssistant || rows[1].Content != "consolidated fake-Telegram answer" {
		t.Fatalf("task rows=%+v", rows)
	}
	tgAPI.PushText(802, chatID, userID, "/task")
	waitFor(t, "task status menu", func() bool { return strings.Contains(tgAPI.Joined(), "Прогресс: 1/1") })
	if strata.ChatCalls() != 3 {
		t.Fatal("status made an unexpected model call")
	}
	first.stop()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	secondDB := openStore(t, path)
	second := startBot(t, ctx, cfg, secondDB, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second, Workers: 2}, storedOffset(t, secondDB))
	quiet()
	quiet()
	if strata.ChatCalls() != 3 || strings.Count(tgAPI.Joined(), "consolidated fake-Telegram answer") != 1 || len(rowsOf(t, secondDB)) != 2 {
		t.Fatalf("restart duplicated task calls=%d messages=%q rows=%+v", strata.ChatCalls(), tgAPI.Joined(), rowsOf(t, secondDB))
	}
	secretLeak(t, "task fake Telegram", tgAPI.Joined())
	secretLeak(t, "task run log", second.logs.String())
	second.stop()
}

func TestTaskScopedApprovalThroughFakeTelegram(t *testing.T) {
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Scoped","url":"https://example.com","content":"Content"}]}`))
	}))
	t.Cleanup(provider.Close)
	strata := testutil.NewStrata(t, secretStrataKey,
		testutil.StrataReply{Content: `{"steps":[{"description":"Gather bounded evidence"}]}`, Model: "fake-model"},
		testutil.StrataReply{ToolCallID: "scope-one", ToolName: "web_search", ToolArguments: `{"query":"first scoped query"}`, Model: "fake-model"},
		testutil.StrataReply{ToolCallID: "scope-two", ToolName: "web_search", ToolArguments: `{"query":"second scoped query"}`, Model: "fake-model"},
		testutil.StrataReply{Content: "two scoped results", Model: "fake-model"},
		testutil.StrataReply{Content: "scoped Telegram final", Model: "fake-model"},
	)
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{"WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": provider.URL})
	db := openStore(t, path)
	run := startBot(t, t.Context(), cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, storedOffset(t, db))
	tgAPI.PushText(810, chatID, userID, "/task prepare a scoped overview")
	waitFor(t, "task scoped approval prompt", func() bool { return strings.Contains(tgAPI.Joined(), "Разрешить для задачи") })
	scopeCallback := ""
	menuMessageID := 0
	for _, message := range tgAPI.Sent() {
		if !strings.Contains(message.Text, "Разрешить для задачи") {
			continue
		}
		var markup models.InlineKeyboardMarkup
		if err := json.Unmarshal([]byte(message.ReplyMarkup), &markup); err != nil {
			t.Fatal(err)
		}
		for _, row := range markup.InlineKeyboard {
			for _, button := range row {
				if strings.HasPrefix(button.CallbackData, "menu:network:scope:") {
					scopeCallback = button.CallbackData
					menuMessageID = message.MessageID
				}
			}
		}
	}
	if scopeCallback == "" || menuMessageID == 0 || providerCalls.Load() != 0 {
		t.Fatalf("scope callback=%q message=%d provider=%d", scopeCallback, menuMessageID, providerCalls.Load())
	}
	tgAPI.Push(models.Update{ID: 811, CallbackQuery: &models.CallbackQuery{
		ID: "task-scope", Data: scopeCallback, From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: menuMessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}}},
	}})
	waitFor(t, "scoped task final", func() bool { return strings.Contains(tgAPI.Joined(), "scoped Telegram final") })
	quiet()
	if providerCalls.Load() != 2 || strata.ChatCalls() != 5 {
		t.Fatalf("scoped flow provider=%d model=%d", providerCalls.Load(), strata.ChatCalls())
	}
	prompts := 0
	for _, text := range tgAPI.Texts() {
		if strings.Contains(text, "Разрешить для задачи") {
			prompts++
		}
	}
	if prompts != 1 {
		t.Fatalf("second same-tool action required another prompt: %q", tgAPI.Joined())
	}
	rows := rowsOf(t, db)
	if len(rows) != 2 || rows[1].Content != "scoped Telegram final" {
		t.Fatalf("scoped task rows=%+v", rows)
	}
	secretLeak(t, "scoped task Telegram", tgAPI.Joined())
	secretLeak(t, "scoped task log", run.logs.String())
	run.stop()
}
