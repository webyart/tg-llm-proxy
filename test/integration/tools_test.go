//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"bot/internal/bot"
	"bot/internal/store"
	"bot/internal/testutil"
)

func TestCalculatorToolEndToEnd(t *testing.T) {
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey,
		testutil.StrataReply{ToolCallID: "call-1", ToolName: "calculator", ToolArguments: `{"expression":"2+2*3"}`, Model: "fake-model"},
		testutil.StrataReply{Content: "Результат равен 8", Model: "fake-model"},
	)
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{"TOOLS_ENABLED": "true"})
	db := openStore(t, path)
	startBot(t, t.Context(), cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, storedOffset(t, db))
	tgAPI.PushText(701, chatID, userID, "Сколько будет 2+2*3?")
	waitFor(t, "calculator answer", func() bool { return strings.Contains(tgAPI.Joined(), "Результат равен 8") })
	if strata.ChatCalls() != 2 || !strings.Contains(strata.LastRequestBody(), `\"result\":\"8\"`) || !strings.Contains(strata.LastRequestBody(), `"tool_call_id":"call-1"`) {
		t.Fatalf("chat calls=%d final request=%s", strata.ChatCalls(), strata.LastRequestBody())
	}
	rows := rowsOf(t, db)
	if len(rows) != 2 || rows[0].Role != store.RoleUser || rows[1].Content != "Результат равен 8" {
		t.Fatalf("rows=%+v", rows)
	}
}
