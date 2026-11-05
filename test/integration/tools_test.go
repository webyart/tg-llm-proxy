//go:build integration

package integration

import (
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

func TestURLReaderGloballyDisabledFailsClosedEndToEnd(t *testing.T) {
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	var requests atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("must not be read"))
	}))
	t.Cleanup(target.Close)
	strata := testutil.NewStrata(t, secretStrataKey,
		testutil.StrataReply{ToolCallID: "forbidden-url", ToolName: "read_url", ToolArguments: `{"url":"` + target.URL + `"}`, Model: "fake-model"},
	)
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{"TOOLS_ENABLED": "false", "URL_READER_ENABLED": "true"})
	db := openStore(t, path)
	startBot(t, t.Context(), cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, storedOffset(t, db))
	tgAPI.PushText(703, chatID, userID, "Прочитай страницу")
	waitFor(t, "disabled tool error", func() bool { return strings.Contains(tgAPI.Joined(), "tool disabled") })
	if strata.ChatCalls() != 1 || requests.Load() != 0 {
		t.Fatalf("chat calls=%d URL requests=%d", strata.ChatCalls(), requests.Load())
	}
	if countEvents(t, db, store.EventUsage) != 0 {
		t.Fatal("disabled read_url recorded usage")
	}
	body := strata.LastRequestBody()
	if strings.Contains(body, `"tools":`) || strings.Contains(body, "native OpenAI tool_calls") {
		t.Fatalf("globally disabled tools were offered: %s", body)
	}
}

func TestURLReaderBlockedAddressEndToEnd(t *testing.T) {
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey,
		testutil.StrataReply{ToolCallID: "call-url", ToolName: "read_url", ToolArguments: `{"url":"https://localhost/secret"}`, Model: "fake-model"},
		testutil.StrataReply{Content: "Такую страницу я читать не буду", Model: "fake-model"},
	)
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{"URL_READER_ENABLED": "true"})
	db := openStore(t, path)
	startBot(t, t.Context(), cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, storedOffset(t, db))
	tgAPI.PushText(702, chatID, userID, "Прочитай https://localhost/secret")
	waitFor(t, "url reader answer", func() bool { return strings.Contains(tgAPI.Joined(), "не буду") })
	body := strata.LastRequestBody()
	if strata.ChatCalls() != 2 || !strings.Contains(body, `"tool_call_id":"call-url"`) || !strings.Contains(body, `\"error\"`) {
		t.Fatalf("chat calls=%d final request=%s", strata.ChatCalls(), body)
	}
	if strings.Contains(body, `\"result\"`) {
		t.Fatalf("a blocked read must not report page content: %s", body)
	}
	rows := rowsOf(t, db)
	if len(rows) != 2 || rows[0].Role != store.RoleUser || rows[1].Content != "Такую страницу я читать не буду" {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestNetworkApprovalResumesThroughTelegramCallbackEndToEnd(t *testing.T) {
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Результат","url":"https://example.com","content":"Содержимое"}]}`))
	}))
	t.Cleanup(provider.Close)
	strata := testutil.NewStrata(t, secretStrataKey,
		testutil.StrataReply{ToolCallID: "approval-search", ToolName: "web_search", ToolArguments: `{"query":"project news"}`, Model: "fake-model"},
		testutil.StrataReply{Content: "Ответ после подтверждения", Model: "fake-model"},
	)
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{"WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": provider.URL})
	db := openStore(t, path)
	startBot(t, t.Context(), cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, storedOffset(t, db))
	tgAPI.PushText(704, chatID, userID, "Подготовь обзор проекта")
	waitFor(t, "network approval prompt", func() bool { return strings.Contains(tgAPI.Joined(), "новое сетевое действие") })
	if providerCalls.Load() != 0 || strata.ChatCalls() != 1 {
		t.Fatalf("network ran before confirmation: provider=%d model=%d", providerCalls.Load(), strata.ChatCalls())
	}
	callbackData := ""
	menuMessageID := 0
	for _, message := range tgAPI.Sent() {
		if !strings.Contains(message.Text, "новое сетевое действие") {
			continue
		}
		var markup models.InlineKeyboardMarkup
		if err := json.Unmarshal([]byte(message.ReplyMarkup), &markup); err != nil || len(markup.InlineKeyboard) == 0 || len(markup.InlineKeyboard[0]) == 0 {
			t.Fatalf("network approval markup=%q err=%v", message.ReplyMarkup, err)
		}
		callbackData = markup.InlineKeyboard[0][0].CallbackData
		menuMessageID = message.MessageID
	}
	if callbackData == "" || menuMessageID == 0 {
		t.Fatal("network callback or menu message id not found")
	}
	tgAPI.Push(models.Update{ID: 705, CallbackQuery: &models.CallbackQuery{
		ID: "network-confirm", Data: callbackData, From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: menuMessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}}},
	}})
	waitFor(t, "approved final synthesis", func() bool {
		return strings.Contains(tgAPI.Joined(), "Ответ после подтверждения")
	})
	if providerCalls.Load() != 1 || strata.ChatCalls() != 2 || tgAPI.Calls("editMessageReplyMarkup") != 1 {
		t.Fatalf("approval did not resume through Telegram capabilities: provider=%d model=%d removals=%d", providerCalls.Load(), strata.ChatCalls(), tgAPI.Calls("editMessageReplyMarkup"))
	}
	rows := rowsOf(t, db)
	if len(rows) != 2 || rows[0].Role != store.RoleUser || rows[1].Content != "Ответ после подтверждения" {
		t.Fatalf("rows=%+v", rows)
	}
	tgAPI.Push(models.Update{ID: 706, CallbackQuery: &models.CallbackQuery{
		ID: "network-replay", Data: callbackData, From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: menuMessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}}},
	}})
	waitFor(t, "replayed approval rejection", func() bool {
		return strings.Contains(tgAPI.Joined(), "истекло или уже использовано")
	})
	if providerCalls.Load() != 1 || strata.ChatCalls() != 2 {
		t.Fatalf("replayed callback executed again: provider=%d model=%d", providerCalls.Load(), strata.ChatCalls())
	}
}
