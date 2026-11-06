//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

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
