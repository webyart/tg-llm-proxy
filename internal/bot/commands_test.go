// both the listing and the Telegram command menu from the same slice.
package bot

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"bot/internal/model"
	"bot/internal/store"
)

func TestRegistryCoversTheTableOfSection6(t *testing.T) {
	var d = newTestDispatcher(t, testConfig(t, nil), nil, nil, testStore(t))
	var byName = map[string]Command{}
	var i = 0
	for i < len(d.Registry()) {
		var cmd = d.Registry()[i]
		if cmd.Name == "" || cmd.Description == "" || cmd.Handler == nil {
			t.Fatalf("a registry entry must have a name, a description and a handler : %+v", cmd)
		}
		if _, exists := byName[cmd.Name]; exists {
			t.Fatalf("the registry has %q twice ", cmd.Name)
		}
		byName[cmd.Name] = cmd
		i++
	}
	var want = []string{CommandStart, CommandHelp, CommandPing, CommandStatus, CommandModel, CommandStream, CommandAsk, CommandReset, CommandSkip, CommandUnskip, CommandHistory, CommandStats, CommandWhoAmI}
	var j = 0
	for j < len(want) {
		if _, ok := byName[want[j]]; !ok {
			t.Fatalf(" lists /%s and the registry does not", want[j])
		}
		j++
	}
	if !byName[CommandAsk].UsesModel {
		t.Fatalf("/ask is the path that reaches STRATA ")
	}
	if !byName[CommandAsk].NeedsArgs {
		t.Fatalf("/ask needs the question ")
	}
	if byName[CommandHelp].UsesModel || byName[CommandPing].UsesModel || byName[CommandStatus].UsesModel || byName[CommandSkip].UsesModel || byName[CommandUnskip].UsesModel {
		t.Fatalf("/help, /ping, /status, /skip and /unskip must not touch the model ")
	}
	if !byName[CommandStart].AlwaysAllowed || !byName[CommandWhoAmI].AlwaysAllowed {
		t.Fatalf(": /start and /whoami answer even a closed bot")
	}

	var help = strings.Join(d.helpLines(), "\n")
	var k = 0
	for k < len(want) {
		if !strings.Contains(help, "/"+want[k]) {
			t.Fatalf("/help must list /%s because derives the list from the registry, got %q", want[k], help)
		}
		k++
	}
}

func TestPingAndWhoami(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, nil, r, testStore(t))

	if err := dispatch(t, d, testEvent(100, 11, 300, "/ping")); err != nil {
		t.Fatalf("/ping: %v", err)
	}
	if !strings.HasPrefix(r.joined(), "pong ") {
		t.Fatalf(" wants `pong <uptime>`, got %q", r.joined())
	}

	var ev = testEvent(100, 11, 301, "/whoami")
	ev.ChatType = "private"
	ev.Username = "tester"
	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("/whoami: %v", err)
	}
	var text = r.joined()
	for _, want := range []string{"user_id: 11", "chat_id: 100", "private", "@tester"} {
		if !strings.Contains(text, want) {
			t.Fatalf(" /whoami must show %q, got %q", want, text)
		}
	}
}

func TestStatusReportsStreamingAndPendingQuestion(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "true"})
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, r, db)
	d.deps.BuildCommit = "test-commit"

	if err := dispatch(t, d, testEvent(100, 11, 302, "/status")); err != nil {
		t.Fatalf("/status: %v", err)
	}
	var first = r.texts()[0]
	for _, want := range []string{"Build: test-commit", "Uptime:", "Модель: " + cfg.StrataModel, "Стриминг: включён", "5000 мс", "900 с", "Последний вопрос чата ждёт ответа: нет"} {
		if !strings.Contains(first, want) {
			t.Errorf("/status must show %q, got %q", want, first)
		}
	}

	if err := store.EnsureChat(context.Background(), db, 100, 11, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "вопрос", TelegramMessageID: intPtr(303)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 304, "/status")); err != nil {
		t.Fatalf("/status with a pending question: %v", err)
	}
	var texts = r.texts()
	if len(texts) != 2 || !strings.Contains(texts[1], "Последний вопрос чата ждёт ответа: да") {
		t.Fatalf("the newest visible user row must be reported as pending, got %q", texts)
	}
}

func TestSkipSuppressesOnlyTheCurrentQuestion(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, r, db)
	var ctx = context.Background()

	if err := dispatch(t, d, testEvent(100, 11, 305, "/skip")); err != nil {
		t.Fatalf("/skip without a pending question: %v", err)
	}
	if !strings.Contains(r.joined(), "пропускать нечего") {
		t.Fatalf("/skip must report the absence of a pending question, got %q", r.joined())
	}
	if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	var questionID, err = store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "оставить в истории", TelegramMessageID: intPtr(306)})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	for _, updateID := range []int64{307, 308} {
		if err := dispatch(t, d, testEvent(100, 11, updateID, "/skip")); err != nil {
			t.Fatalf("/skip: %v", err)
		}
	}
	var left, leftErr = store.UnansweredQuestions(ctx, db, 10)
	if leftErr != nil || len(left) != 0 {
		t.Fatalf("/skip must suppress startup recovery, left=%v err=%v", left, leftErr)
	}
	var history, historyErr = store.HistoryOfChat(ctx, db, 100, 10)
	if historyErr != nil || len(history) != 1 || history[0].ID != questionID {
		t.Fatalf("/skip must not delete history, got %+v err=%v", history, historyErr)
	}
	if err := dispatch(t, d, testEvent(100, 11, 309, "/status")); err != nil {
		t.Fatalf("/status after /skip: %v", err)
	}
	if !strings.Contains(r.texts()[len(r.texts())-1], "автовосстановление отключено (/skip)") {
		t.Fatalf("/status must show the explicit suppression, got %q", r.joined())
	}
	var newerID, newerErr = store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "новый вопрос", TelegramMessageID: intPtr(310)})
	if newerErr != nil {
		t.Fatalf("Append newer question: %v", newerErr)
	}
	left, leftErr = store.UnansweredQuestions(ctx, db, 10)
	if leftErr != nil || len(left) != 1 || left[0].ID != newerID {
		t.Fatalf("the newer question must remain recoverable, left=%+v err=%v", left, leftErr)
	}
}

func TestUnskipRestoresRecoveryWithoutCallingTheModel(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)
	var ctx = context.Background()

	if err := dispatch(t, d, testEvent(100, 11, 311, "/unskip")); err != nil {
		t.Fatalf("/unskip without suppression: %v", err)
	}
	if !strings.Contains(r.joined(), "Нет отключённого автовосстановления") {
		t.Fatalf("/unskip must be idempotent when no suppression exists, got %q", r.joined())
	}
	if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	var questionID, err = store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "повторить при старте", TelegramMessageID: intPtr(312)})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 313, "/skip")); err != nil {
		t.Fatalf("/skip: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 314, "/unskip")); err != nil {
		t.Fatalf("/unskip: %v", err)
	}
	var last = r.texts()[len(r.texts())-1]
	if !strings.Contains(last, "следующем перезапуске") || !strings.Contains(last, "id=") {
		t.Fatalf("/unskip must explain when recovery can happen, got %q", last)
	}
	if err := dispatch(t, d, testEvent(100, 11, 315, "/unskip")); err != nil {
		t.Fatalf("repeated /unskip: %v", err)
	}
	var left, leftErr = store.UnansweredQuestions(ctx, db, 10)
	if leftErr != nil || len(left) != 1 || left[0].ID != questionID {
		t.Fatalf("/unskip must make the same question recoverable, left=%+v err=%v", left, leftErr)
	}
	var history, historyErr = store.HistoryOfChat(ctx, db, 100, 10)
	if historyErr != nil || len(history) != 1 || history[0].ID != questionID {
		t.Fatalf("/unskip must not change history, got %+v err=%v", history, historyErr)
	}
	if err := dispatch(t, d, testEvent(100, 11, 316, "/status")); err != nil {
		t.Fatalf("/status after /unskip: %v", err)
	}
	last = r.texts()[len(r.texts())-1]
	if strings.Contains(last, "автовосстановление отключено") {
		t.Fatalf("/status must no longer report suppression, got %q", last)
	}
	if m.callCount() != 0 {
		t.Fatalf("/skip and /unskip must never call the model, got %d calls", m.callCount())
	}
}

// attributes the events to, so /start has to create it.
func TestStartRegistersTheChat(t *testing.T) {
	var cfg = testConfig(t, nil) // closed bot: /start must still answer
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, r, db)

	var ev = testEvent(200, 11, 310, "/start")
	ev.ChatTitle = "Рабочий чат"
	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("/start: %v", err)
	}
	var stats, err = store.Stats(context.Background(), db, 200)
	if err != nil {
		t.Fatalf("Stats must find the chat registered by /start : %v", err)
	}
	if stats.Messages != 0 {
		t.Fatalf("/start must not write a message row , got %d", stats.Messages)
	}
	var text = r.joined()
	if !strings.Contains(text, "/ask") || !strings.Contains(text, "user_id") {
		t.Fatalf(" /start must give the command list and, for a closed bot, the id: %q", text)
	}
}

func TestModelCommand(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{models: []string{"qwen2.5-7b", "qwen2.5-14b"}}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(100, 11, 320, "/model")); err != nil {
		t.Fatalf("/model: %v", err)
	}
	var first = r.joined()
	if !strings.Contains(first, cfg.StrataModel) || !strings.Contains(first, cfg.StrataBase) {
		t.Fatalf(" /model must name the model and the endpoint, got %q", first)
	}

	if err := dispatch(t, d, testEvent(100, 11, 321, "/model check")); err != nil {
		t.Fatalf("/model check: %v", err)
	}
	var checked = r.texts()[len(r.texts())-1]
	if !strings.Contains(checked, "qwen2.5-7b") || !strings.Contains(checked, "qwen2.5-14b") {
		t.Fatalf(" /model check must list the models of GET /models, got %q", checked)
	}

	var broken = &fakeModel{modelsErr: errors.New("connection refused")}
	var r2 = &fakeReplier{}
	var db2 = testStoreNamed(t, t.Name()+"-broken")
	var d2 = newTestDispatcher(t, cfg, broken, r2, db2)
	if err := dispatch(t, d2, testEvent(101, 11, 322, "/model check")); err != nil {
		t.Fatalf("/model check of an unreachable STRATA must answer, not fail: %v", err)
	}
	if !strings.Contains(r2.joined(), "connection refused") {
		t.Fatalf(" /model check must report the reason, got %q", r2.joined())
	}
	var failures, evErr = store.CountEvents(context.Background(), db2, store.EventModelError)
	if evErr != nil || failures != 1 {
		t.Fatalf("a failed STRATA check must be audited as events(kind='model_error') , got %d err=%v", failures, evErr)
	}
	var rows, histErr = store.HistoryOfChat(context.Background(), db2, 101, 10)
	if histErr != nil {
		t.Fatalf("HistoryOfChat: %v", histErr)
	}
	if len(rows) != 0 {
		t.Fatalf("a service command must not write a message row , got %d", len(rows))
	}
}

func TestModelSelectScopesAndResets(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{models: []string{"qwen-test", "qwen-alt"}, response: model.Response{Content: "ответ"}}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(100, 11, 323, "/model select qwen-alt")); err != nil {
		t.Fatalf("/model select: %v", err)
	}
	selected, found, err := store.ChatModel(context.Background(), db, 100)
	if err != nil || !found || selected != "qwen-alt" {
		t.Fatalf("ChatModel=%q found=%v err=%v", selected, found, err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 324, "/model select missing")); err != nil {
		t.Fatalf("/model select missing: %v", err)
	}
	if !strings.Contains(r.joined(), "отсутствует") {
		t.Fatalf("an unknown model must be refused with the available list, got %q", r.joined())
	}
	if err := dispatch(t, d, testEvent(100, 11, 325, "/ask вопрос")); err != nil {
		t.Fatalf("/ask with override: %v", err)
	}
	if got := m.lastRequest().Model; got != "qwen-alt" {
		t.Fatalf("the request must use the chat override, got %q", got)
	}
	if err := dispatch(t, d, testEvent(100, 11, 326, "/status")); err != nil {
		t.Fatalf("/status with override: %v", err)
	}
	if !strings.Contains(r.texts()[len(r.texts())-1], "Модель: qwen-alt") {
		t.Fatalf("/status must show the override, got %q", r.joined())
	}
	if err := dispatch(t, d, testEvent(100, 11, 327, "/model reset")); err != nil {
		t.Fatalf("/model reset: %v", err)
	}
	if _, found, err := store.ChatModel(context.Background(), db, 100); err != nil || found {
		t.Fatalf("/model reset must clear the override, found=%v err=%v", found, err)
	}
}

// chat_resets, история остаётся»: after /reset the model must not see the old rows, /history must
// still list them, and /stats keeps counting them.
func TestResetHidesTheContextButKeepsHistory(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{contentFor: func(req model.Request) string { return "ответ на " + req.Messages[len(req.Messages)-1].Content }}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)
	var ctx = context.Background()

	var i = 0
	for i < 2 {
		if _, err := store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: "user", Content: "старый вопрос"}); err != nil {
			t.Fatalf("Append user: %v", err)
		}
		if _, err := store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: "assistant", Content: "старый ответ"}); err != nil {
			t.Fatalf("Append assistant: %v", err)
		}
		i++
	}

	if err := dispatch(t, d, testEvent(100, 11, 330, "/reset")); err != nil {
		t.Fatalf("/reset: %v", err)
	}
	var resetText = r.joined()
	if !strings.Contains(resetText, "4") || !strings.Contains(resetText, "Контекст очищен") {
		t.Fatalf(" /reset must report how many rows it hid, got %q", resetText)
	}

	if err := dispatch(t, d, testEvent(100, 11, 331, "/ask новый вопрос")); err != nil {
		t.Fatalf("/ask after /reset: %v", err)
	}
	var req = m.lastRequest()
	if len(req.Messages) != 2 {
		t.Fatalf("after /reset the context must be the system prompt plus the new question , got %d messages: %+v",
			len(req.Messages), req.Messages)
	}
	if req.Messages[0].Role != "system" || req.Messages[1].Content != "новый вопрос" {
		t.Fatalf(" the last message of the context is the new user text, got %+v", req.Messages)
	}

	var rows, err = store.HistoryOfChat(ctx, db, 100, 50)
	if err != nil {
		t.Fatalf("HistoryOfChat: %v", err)
	}
	if len(rows) != 6 { // 4 hidden by /reset + the new question + the answer of «история остаётся»
		t.Fatalf("/reset must not delete anything , got %d rows", len(rows))
	}
	var stats, statsErr = store.Stats(ctx, db, 100)
	if statsErr != nil || stats.Messages != 6 {
		t.Fatalf("/stats must count the whole history , got %d err=%v", stats.Messages, statsErr)
	}
}

func TestHistoryDefaultAndArgument(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, r, db)
	var ctx = context.Background()

	if err := dispatch(t, d, testEvent(100, 11, 340, "/history")); err != nil {
		t.Fatalf("/history of an empty chat: %v", err)
	}
	if !strings.Contains(r.joined(), "История чата пуста") {
		t.Fatalf(" /history of a fresh chat must say so, got %q", r.joined())
	}

	var i = 0
	for i < 60 { // more than HistoryMax of, so the cap of /history is visible
		if _, err := store.Append(ctx, db, store.Message{ChatID: 100, UserID: 11, Role: "user", Content: "вопрос"}); err != nil {
			t.Fatalf("Append: %v", err)
		}
		i++
	}

	var cases = []struct {
		text  string
		lines int
	}{
		{"/history", HistoryDefault},    //: default 5
		{"/history 2", 2},               //: the argument
		{"/history 100000", HistoryMax}, // the budget of UX caps the argument
	}
	var j = 0
	for j < len(cases) {
		var before = r.count()
		if err := dispatch(t, d, testEvent(100, 11, int64(341+j), cases[j].text)); err != nil {
			t.Fatalf("%s: %v", cases[j].text, err)
		}
		var text = r.texts()[before]
		if got := strings.Count(text, "\n"); got != cases[j].lines {
			t.Fatalf("%s must show %d rows, got %d: %q", cases[j].text, cases[j].lines, got, text)
		}
		j++
	}

	var k = 0
	for _, bad := range []string{"/history abc", "/history 0", "/history -3"} {
		var before = r.count()
		if err := dispatch(t, d, testEvent(100, 11, int64(350+k), bad)); err != nil {
			t.Fatalf("%s: %v", bad, err)
		}
		if !strings.Contains(r.texts()[before], "ждёт число") {
			t.Fatalf("%s must be answered with a hint, not with an empty listing: %q", bad, r.texts()[before])
		}
		k++
	}
}

// the user the chat belongs to, because events are keyed by user_id and not by chat_id.
func TestStatsCounters(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, r, db)
	var ctx = context.Background()

	if err := store.EnsureChat(ctx, db, 100, 11, "Рабочий чат"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if _, err := store.Append(ctx, db, store.Message{
		ChatID: 100, UserID: 11, Role: "user", Content: "вопрос", TokensIn: intPtr(10),
	}); err != nil {
		t.Fatalf("Append user: %v", err)
	}
	if _, err := store.Append(ctx, db, store.Message{
		ChatID: 100, UserID: 11, Role: "assistant", Content: "ответ", Model: "qwen2.5-7b", TokensOut: intPtr(5),
	}); err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	if err := store.LogEvent(ctx, db, 11, store.EventModelError, "upstream 502"); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}
	// numbers — a chat that saw a timeout must not read as a chat where nothing failed (measured 2026-10-31).
	if err := store.LogEvent(ctx, db, 11, store.EventTimeout, "STRATA_TIMEOUT_SECONDS 15s spent"); err != nil {
		t.Fatalf("LogEvent timeout: %v", err)
	}

	if err := dispatch(t, d, testEvent(100, 11, 360, "/stats")); err != nil {
		t.Fatalf("/stats: %v", err)
	}
	var text = r.joined()
	for _, want := range []string{"Сообщений: 2", "10 входящих", "5 исходящих", "Ошибок модели: 1", "Таймаутов модели: 1", "qwen2.5-7b"} {
		if !strings.Contains(text, want) {
			t.Fatalf(" /stats must show %q, got %q", want, text)
		}
	}
}

func TestAskWithoutTextAndFreeText(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{contentFor: func(req model.Request) string {
		return "модель видит: " + req.Messages[len(req.Messages)-1].Content
	}}
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))

	if err := dispatch(t, d, testEvent(100, 11, 370, "/ask")); err != nil {
		t.Fatalf("/ask without text: %v", err)
	}
	if m.callCount() != 0 {
		t.Fatalf(": /ask without an argument must not reach the model, calls=%d", m.callCount())
	}
	if !strings.Contains(r.joined(), "/ask") {
		t.Fatalf(": the hint must name the command, got %q", r.joined())
	}

	if err := dispatch(t, d, testEvent(100, 11, 371, "привет, модель")); err != nil {
		t.Fatalf("free text: %v", err)
	}
	if m.callCount() != 1 {
		t.Fatalf(" PROXY_FREE_TEXT=true: free text is /ask, calls=%d", m.callCount())
	}
	if !strings.Contains(m.lastRequest().Messages[len(m.lastRequest().Messages)-1].Content, "привет, модель") {
		t.Fatalf(": the free text must become the question, got %+v", m.lastRequest().Messages)
	}
	if !strings.Contains(r.joined(), "⏱") {
		t.Fatalf(": the reply carries the latency footer, got %q", r.joined())
	}

	var closed = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "PROXY_FREE_TEXT": "false"})
	var m2 = &fakeModel{}
	var r2 = &fakeReplier{}
	var d2 = newTestDispatcher(t, closed, m2, r2, testStoreNamed(t, t.Name()+"-nofree"))
	if err := dispatch(t, d2, testEvent(100, 11, 372, "/nope")); err != nil {
		t.Fatalf("an unknown command with PROXY_FREE_TEXT=false: %v", err)
	}
	if m2.callCount() != 0 {
		t.Fatalf(": with the flag off an unknown command must not reach the model, calls=%d", m2.callCount())
	}
	if !strings.Contains(r2.joined(), "/help") {
		t.Fatalf(": the unknown command must be answered with the list of commands, got %q", r2.joined())
	}
}

func TestAskLogsTheFieldsOfDay7(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{
		Content:   "текст ответа модели, который в лог попасть не должен",
		Model:     "qwen-test-7b",
		TokensIn:  ptr(12),
		TokensOut: ptr(5),
		Latency:   250 * time.Millisecond,
		Attempts:  2, //: the second attempt succeeded — the log has to say so
	}}
	var r = &fakeReplier{}
	// concurrent writer that forced syncLog in the Integration harness does not exist here.
	var logs = &bytes.Buffer{}
	var d = newTestDispatcher(t, cfg, m, r, testStoreNamed(t, t.Name()), slog.New(slog.NewJSONHandler(logs, nil)))

	if err := dispatch(t, d, testEvent(100, 11, 380, "/ask сколько будет два плюс два")); err != nil {
		t.Fatalf("/ask: %v", err)
	}

	var line = logs.String()
	for _, want := range []string{`"chat_id":100`, `"latency_ms":250`, `"model":"qwen-test-7b"`, `"tokens_in":12`, `"tokens_out":5`, `"attempts":2`} {
		if !strings.Contains(line, want) {
			t.Errorf("the JSON log of lacks %s: %s", want, line)
		}
	}
	if strings.Contains(line, "текст ответа модели") || strings.Contains(line, "сколько будет") {
		t.Errorf(" security: the log carries the text of the question or of the answer: %s", line)
	}
}

func TestAskLogsUnknownTokensAsNull(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "ответ без токенов", Model: "qwen-test-7b", Latency: 20 * time.Millisecond, Attempts: 1}}
	var logs = &bytes.Buffer{}
	var d = newTestDispatcher(t, cfg, m, &fakeReplier{}, testStoreNamed(t, t.Name()), slog.New(slog.NewJSONHandler(logs, nil)))

	if err := dispatch(t, d, testEvent(101, 11, 381, "/ask без токенов")); err != nil {
		t.Fatalf("/ask: %v", err)
	}
	var line = logs.String()
	if !strings.Contains(line, `"tokens_in":null`) || !strings.Contains(line, `"tokens_out":null`) {
		t.Errorf("an unknown usage must stay null in the JSON log, got: %s", line)
	}
	if !strings.Contains(line, `"latency_ms":20`) {
		t.Errorf("the latency of latency_ms is missing: %s", line)
	}
}

// not answer» meant opening the database. The failed call must therefore leave an ERROR line naming the chat
func TestModelFailureIsLoggedWithItsReason(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{err: errors.New(`Post "http://127.0.0.1:8080/v1/chat/completions": dial tcp 127.0.0.1:8080: ` +
		`connectex: No connection could be made because the target machine actively refused it. `)}
	var logs = &bytes.Buffer{}
	var db = testStoreNamed(t, t.Name())
	var d = newTestDispatcher(t, cfg, m, &fakeReplier{}, db, slog.New(slog.NewJSONHandler(logs, nil)))

	if err := dispatch(t, d, testEvent(102, 11, 382, "/ask это я")); err != nil {
		t.Fatalf("/ask: %v", err)
	}

	var line = logs.String()
	if !strings.Contains(line, `"level":"ERROR"`) || !strings.Contains(line, "the model did not answer") {
		t.Fatalf("a failed model call must be visible in the log, got: %s", line)
	}
	if !strings.Contains(line, `"chat_id":102`) || !strings.Contains(line, "connectex") {
		t.Fatalf("the log line must name the chat of and the reason of the failure, got: %s", line)
	}
	if strings.Contains(line, cfg.BotToken) {
		t.Fatalf(" security: the token reached the log: %s", line)
	}
	var events, evErr = store.CountEvents(context.Background(), db, store.EventModelError)
	if evErr != nil || events != 1 {
		t.Fatalf("the audit row of must stay next to the log line: events(kind='model_error') = %d err=%v", events, evErr)
	}
}
