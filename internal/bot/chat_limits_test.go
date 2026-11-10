package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"bot/internal/i18n"
	"bot/internal/model"
	"bot/internal/store"
)

// chatLimitsDB registers a chat so chat_limits can reference it. A private chat keeps its positive id;
// the group helper uses a negative one.
func chatLimitsDB(t *testing.T, chats ...int64) (*Dispatcher, *fakeModel, *fakeReplier) {
	t.Helper()
	m := &fakeModel{response: model.Response{Content: "ok"}}
	r := &fakeReplier{}
	db := testStore(t)
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12"})
	d := newTestDispatcher(t, cfg, m, r, db)
	for _, chatID := range chats {
		if err := store.EnsureChat(context.Background(), db, chatID, 11, ""); err != nil {
			t.Fatalf("EnsureChat(%d): %v", chatID, err)
		}
	}
	return d, m, r
}

func TestChatDailyModelLimitRefusesBeforeProvider(t *testing.T) {
	d, m, _ := chatLimitsDB(t, 220, 221)
	ctx := context.Background()
	if err := store.SetChatLimits(ctx, d.deps.DB, store.ChatLimits{ChatID: 220, DailyModelLimit: 1}); err != nil {
		t.Fatal(err)
	}
	chatCtx := withChatScope(ctx, 220)
	if _, err := d.callModel(chatCtx, 11, model.Request{}, nil, nil); err != nil {
		t.Fatalf("below-limit call: %v", err)
	}
	// The chat's language decides the refusal sentence: the default is Russian, an override is English.
	if err := store.SetChatLanguage(ctx, d.deps.DB, 220, i18n.EN); err != nil {
		t.Fatal(err)
	}
	_, err := d.callModel(chatCtx, 11, model.Request{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "daily model call limit of this chat") {
		t.Fatalf("over-limit error=%v, want the localized EN refusal", err)
	}
	if m.callCount() != 1 {
		t.Fatalf("provider calls=%d, want the refused call to never reach the model", m.callCount())
	}
	usage, err := store.ChatDailyUsage(ctx, d.deps.DB, 220, d.now())
	if err != nil || usage.ModelCalls != 1 {
		t.Fatalf("chat usage=%+v err=%v, want one recorded call", usage, err)
	}
	// Another chat of the same user is independent: the limit is per chat, not per user.
	if _, err := d.callModel(withChatScope(ctx, 221), 11, model.Request{}, nil, nil); err != nil || m.callCount() != 2 {
		t.Fatalf("independent chat err=%v calls=%d", err, m.callCount())
	}
	// A refusal is an auditable limit event, not a model error.
	var detail string
	if err := d.deps.DB.QueryRowContext(ctx,
		`SELECT detail FROM events WHERE chat_id = 220 AND kind = ? ORDER BY id DESC LIMIT 1`, store.EventLimit).Scan(&detail); err != nil {
		t.Fatalf("limit event: %v", err)
	}
	if !strings.Contains(detail, "chat_model_daily") {
		t.Fatalf("limit event detail=%q", detail)
	}
}

func TestChatDailyModelLimitRefusalIsDeliveredByDispatch(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "answer"}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	ctx := context.Background()
	if err := store.EnsureChat(ctx, db, 220, 11, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatLimits(ctx, db, store.ChatLimits{ChatID: 220, DailyModelLimit: 1}); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, d, testEvent(220, 11, 710, "первый")); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if err := dispatch(t, d, testEvent(220, 11, 711, "второй")); err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if m.callCount() != 1 {
		t.Fatalf("model calls=%d, want one", m.callCount())
	}
	text := r.joined()
	if !strings.Contains(text, "Дневной лимит обращений к модели для этого чата исчерпан") {
		t.Fatalf("refusal reply=%q", text)
	}
	// The second question is stored (it was asked), but no assistant row was written for it.
	rows, err := store.HistoryOfChat(ctx, db, 220, 10)
	if err != nil || len(rows) != 3 || rows[2].Role != store.RoleUser {
		t.Fatalf("history rows=%+v err=%v, want a stored question without an answer", rows, err)
	}
}

func TestChatDailyNetworkLimitUsesExistingRefusalAndSkipsExecution(t *testing.T) {
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer target.Close()
	cfg := testConfig(t, map[string]string{
		"WEB_SEARCH_ENABLED":            "true",
		"WEB_SEARCH_BASE_URL":           target.URL,
		"USER_DAILY_NETWORK_TOOL_LIMIT": "5",
	})
	db := testStore(t)
	ctx := context.Background()
	for _, chatID := range []int64{220, 221} {
		if err := store.EnsureChat(ctx, db, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetChatLimits(ctx, db, store.ChatLimits{ChatID: 220, DailyNetworkLimit: 1}); err != nil {
		t.Fatal(err)
	}
	search := model.ToolCall{ID: "search", Name: "web_search", Arguments: `{"query":"chat budget"}`}
	m := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{search}, Attempts: 1},
		{Content: "первый ответ", Attempts: 1},
		{ToolCalls: []model.ToolCall{search}, Attempts: 1},
		{Content: "второй ответ", Attempts: 1},
	}}
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, db)
	request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "Найди в интернете chat budget"}}}
	resp, err := d.completeWithTools(withChatScope(ctx, 220), 11, request, nil, nil)
	if err != nil || resp.Content != "первый ответ" || providerCalls.Load() != 1 {
		t.Fatalf("first answer=%q calls=%d err=%v", resp.Content, providerCalls.Load(), err)
	}
	resp, err = d.completeWithTools(withChatScope(ctx, 220), 11, request, nil, nil)
	if err != nil || resp.Content != "второй ответ" || providerCalls.Load() != 1 {
		t.Fatalf("second answer=%q calls=%d err=%v (the refused tool must not reach the network)", resp.Content, providerCalls.Load(), err)
	}
	// The model saw the same JSON refusal the per-user daily budget uses, and the chat counter counted
	// only the executed call: the refused one writes no usage event.
	toolResult := m.requests[3].Messages[len(m.requests[3].Messages)-1]
	if toolResult.Role != model.RoleTool || toolResult.Content != networkDailyLimitErrorJSON || !json.Valid([]byte(toolResult.Content)) {
		t.Fatalf("refused tool result=%+v", toolResult)
	}
	usage, err := store.ChatDailyUsage(ctx, db, 220, d.now())
	if err != nil || usage.NetworkToolCalls() != 1 {
		t.Fatalf("chat network usage=%+v err=%v, want 1 executed call", usage, err)
	}
	// The per-user reservation of the refused call was never taken.
	used, err := store.DailyQuotaUsed(ctx, db, 11, d.now(), store.DailyQuotaNetworkTools)
	if err != nil || used != 1 {
		t.Fatalf("user network reservations=%d err=%v, want 1", used, err)
	}
	// A chat without the restriction executes normally.
	m.responses = append(m.responses,
		model.Response{ToolCalls: []model.ToolCall{search}, Attempts: 1},
		model.Response{Content: "третий ответ", Attempts: 1})
	if resp, err := d.completeWithTools(withChatScope(ctx, 221), 11, request, nil, nil); err != nil || resp.Content != "третий ответ" || providerCalls.Load() != 2 {
		t.Fatalf("independent chat answer=%q calls=%d err=%v", resp.Content, providerCalls.Load(), err)
	}
	var detail string
	if err := db.QueryRowContext(ctx,
		`SELECT detail FROM events WHERE chat_id = 220 AND kind = ? ORDER BY id DESC LIMIT 1`, store.EventLimit).Scan(&detail); err != nil {
		t.Fatalf("network limit event: %v", err)
	}
	if !strings.Contains(detail, "chat_network_daily") {
		t.Fatalf("network limit event detail=%q", detail)
	}
}

func TestChatPerMinuteLimitRefusesAndExemptsAlwaysAllowed(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "answer"}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	ctx := context.Background()
	if err := store.EnsureChat(ctx, db, 220, 11, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatLimits(ctx, db, store.ChatLimits{ChatID: 220, PerMinuteLimit: 1}); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, d, testEvent(220, 11, 720, "первый")); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if err := dispatch(t, d, testEvent(220, 11, 721, "второй")); err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if m.callCount() != 1 {
		t.Fatalf("model calls=%d, want the refused update to stay away from the model", m.callCount())
	}
	if text := r.joined(); !strings.Contains(text, "не больше 1 сообщений в минуту") {
		t.Fatalf("per-minute refusal reply=%q", text)
	}
	// /whoami is AlwaysAllowed: a user locked by the chat limit must still be able to read the ids that
	// diagnose the configuration.
	r.mu.Lock()
	r.sent = nil
	r.mu.Unlock()
	if err := dispatch(t, d, testEvent(220, 11, 722, "/whoami")); err != nil {
		t.Fatalf("/whoami under the chat limit: %v", err)
	}
	if text := r.joined(); strings.Contains(text, "сообщений в минуту") || !strings.Contains(text, "user_id") {
		t.Fatalf("/whoami reply=%q", text)
	}
	var detail string
	if err := db.QueryRowContext(ctx,
		`SELECT detail FROM events WHERE chat_id = 220 AND kind = ? ORDER BY id DESC LIMIT 1`, store.EventLimit).Scan(&detail); err != nil {
		t.Fatalf("per-minute event: %v", err)
	}
	if !strings.Contains(detail, "chat_per_minute 1/1") {
		t.Fatalf("per-minute event detail=%q", detail)
	}
}

func TestChatDisabledToolsAreHiddenAndRefused(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TOOLS_ENABLED": "true"})
	db := testStore(t)
	ctx := context.Background()
	if err := store.EnsureChat(ctx, db, 220, 11, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatLimits(ctx, db, store.ChatLimits{ChatID: 220, DisabledTools: []string{store.ChatToolCalculator}}); err != nil {
		t.Fatal(err)
	}
	call := model.ToolCall{ID: "call-1", Name: "calculator", Arguments: `{"expression":"2+2"}`}
	m := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{call}, Attempts: 1},
		{Content: "Итог", Attempts: 1},
	}}
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, db)
	resp, err := d.completeWithTools(withChatScope(ctx, 220), 11, model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: "посчитай"}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("completeWithTools: %v", err)
	}
	if resp.Content != "Итог" {
		t.Fatalf("answer=%q", resp.Content)
	}
	// The calculator schema is hidden from the request, the refused call becomes a JSON tool result and
	// no usage event is written for it.
	if len(m.requests) != 2 || len(m.requests[0].Tools) != 0 {
		t.Fatalf("model requests=%d first tools=%+v, want no calculator schema", len(m.requests), m.requests[0].Tools)
	}
	last := m.requests[1].Messages[len(m.requests[1].Messages)-1]
	if last.Role != model.RoleTool || last.Content != toolDisabledErrorJSON {
		t.Fatalf("disabled tool result=%+v", last)
	}
	uses, err := store.CountEvents(ctx, db, store.EventUsage)
	if err != nil || uses != 0 {
		t.Fatalf("usage events=%d err=%v, want none for a disabled tool", uses, err)
	}
	// The same answer path for a chat without the restriction executes the calculator.
	other := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{call}, Attempts: 1},
		{Content: "4", Attempts: 1},
	}}
	otherDispatcher := newTestDispatcher(t, cfg, other, &fakeReplier{}, db)
	if _, err := otherDispatcher.completeWithTools(withChatScope(ctx, 221), 11, model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: "посчитай"}},
	}, nil, nil); err != nil {
		t.Fatalf("unrestricted chat: %v", err)
	}
	if len(other.requests) != 2 || len(other.requests[0].Tools) != 1 {
		t.Fatalf("unrestricted tools=%+v", other.requests[0].Tools)
	}
	if last := other.requests[1].Messages[len(other.requests[1].Messages)-1]; !strings.Contains(last.Content, `"result":"4"`) {
		t.Fatalf("unrestricted calculator result=%+v", last)
	}
}

func TestChatLimitsCommandShowSetAndReset(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{response: model.Response{Content: "ok"}}, r, db)
	ctx := context.Background()
	for _, chatID := range []int64{11, 300} {
		if err := store.EnsureChat(ctx, db, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
	}
	// A private chat without a row reports the default source and no limits.
	if err := dispatch(t, d, testEvent(11, 11, 730, "/limits")); err != nil {
		t.Fatalf("/limits: %v", err)
	}
	text := r.texts()[len(r.texts())-1]
	for _, want := range []string{"Лимиты чата 11 (private)", "Источник: по умолчанию", "без ограничений", "read_url 0"} {
		if !strings.Contains(text, want) {
			t.Errorf("/limits misses %q: %q", want, text)
		}
	}
	// The owner of a private chat may set each counter and the disabled-tools list.
	for _, command := range []string{"/limits model 5", "/limits network 2", "/limits minute 3", "/limits tools read_url,web_search"} {
		if err := dispatch(t, d, testEvent(11, 11, 731, command)); err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if got := r.texts()[len(r.texts())-1]; !strings.Contains(got, "Лимиты чата сохранены") {
			t.Errorf("%s reply=%q", command, got)
		}
	}
	limits, found, err := store.GetChatLimits(ctx, db, 11)
	if err != nil || !found {
		t.Fatalf("stored limits found=%v err=%v", found, err)
	}
	if limits.DailyModelLimit != 5 || limits.DailyNetworkLimit != 2 || limits.PerMinuteLimit != 3 {
		t.Fatalf("stored limits=%+v", limits)
	}
	if len(limits.DisabledTools) != 2 || limits.DisabledTools[0] != store.ChatToolReadURL || limits.DisabledTools[1] != store.ChatToolWebSearch {
		t.Fatalf("stored disabled tools=%v", limits.DisabledTools)
	}
	// The report now names the chat source and the numbers.
	if err := dispatch(t, d, testEvent(11, 11, 732, "/limits")); err != nil {
		t.Fatal(err)
	}
	text = r.texts()[len(r.texts())-1]
	for _, want := range []string{"Источник: настройки этого чата", "модели 0 (лимит 5)", "сетевых вызовов 0 (лимит 2)", "read_url, web_search"} {
		if !strings.Contains(text, want) {
			t.Errorf("updated /limits misses %q: %q", want, text)
		}
	}
	// Invalid input changes nothing.
	if err := dispatch(t, d, testEvent(11, 11, 733, "/limits model abc")); err != nil {
		t.Fatal(err)
	}
	if got := r.texts()[len(r.texts())-1]; !strings.Contains(got, "Укажите целое число") {
		t.Errorf("invalid number reply=%q", got)
	}
	if err := dispatch(t, d, testEvent(11, 11, 734, "/limits tools shell")); err != nil {
		t.Fatal(err)
	}
	if got := r.texts()[len(r.texts())-1]; !strings.Contains(got, "Неизвестный инструмент") {
		t.Errorf("invalid tool reply=%q", got)
	}
	if limits, _, _ := store.GetChatLimits(ctx, db, 11); limits.DailyModelLimit != 5 || len(limits.DisabledTools) != 2 {
		t.Fatalf("invalid input changed limits=%+v", limits)
	}
	// Reset clears the row.
	if err := dispatch(t, d, testEvent(11, 11, 735, "/limits reset")); err != nil {
		t.Fatal(err)
	}
	if got := r.texts()[len(r.texts())-1]; !strings.Contains(got, "Лимиты чата сброшены") {
		t.Errorf("reset reply=%q", got)
	}
	if _, found, _ := store.GetChatLimits(ctx, db, 11); found {
		t.Fatal("reset left a limits row")
	}
}

func TestChatLimitsCommandPermissions(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	ctx := context.Background()
	if err := store.EnsureChat(ctx, db, 300, 12, ""); err != nil { // owned by user 12
		t.Fatal(err)
	}
	if err := store.EnsureChat(ctx, db, -100500, 12, "group"); err != nil {
		t.Fatal(err)
	}
	// A private chat that is not ours: user 11 may read but not write.
	if err := d.limitsControl(ctx, testEvent(300, 11, 740, "/limits model 5"), "model 5"); err != nil {
		t.Fatalf("non-owner: %v", err)
	}
	if got := r.texts()[len(r.texts())-1]; !strings.Contains(got, "Управлять лимитами может только администратор") {
		t.Fatalf("non-owner reply=%q", got)
	}
	if _, found, _ := store.GetChatLimits(ctx, db, 300); found {
		t.Fatal("a non-owner wrote limits to a private chat")
	}
	// A group without an administrator: no member can lift its limit, even though the id could be
	// mistaken for a user id. The negative chat id must never fall back to the user check.
	if err := d.limitsControl(ctx, testEvent(-100500, 12, 741, "/limits model 5"), "model 5"); err != nil {
		t.Fatalf("group non-admin: %v", err)
	}
	if got := r.texts()[len(r.texts())-1]; !strings.Contains(got, "Управлять лимитами может только администратор") {
		t.Fatalf("group non-admin reply=%q", got)
	}
	// The same group with an administrator is writable.
	adminCfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12", "TELEGRAM_ADMIN_USERS": "11"})
	adminDispatcher := newTestDispatcher(t, adminCfg, &fakeModel{}, r, db)
	if err := adminDispatcher.limitsControl(ctx, testEvent(-100500, 11, 742, "/limits model 5"), "model 5"); err != nil {
		t.Fatalf("group admin: %v", err)
	}
	if got := r.texts()[len(r.texts())-1]; !strings.Contains(got, "Лимиты чата сохранены") {
		t.Fatalf("group admin reply=%q", got)
	}
	limits, found, _ := store.GetChatLimits(ctx, db, -100500)
	if !found || limits.DailyModelLimit != 5 {
		t.Fatalf("group admin limits=%+v found=%v", limits, found)
	}
}

func TestLimitsCommandIsRegisteredAndListedInHelp(t *testing.T) {
	d := &Dispatcher{}
	var registered bool
	for _, command := range d.registry() {
		if command.Name == CommandLimits {
			registered = true
			if !strings.Contains(command.Description, "лимиты чата") {
				t.Errorf("registry description=%q", command.Description)
			}
		}
	}
	if !registered {
		t.Fatal("registry() does not serve /limits")
	}
	dispatcher := newTestDispatcher(t, testConfig(t, nil), nil, nil, nil)
	help := strings.Join(dispatcher.helpLinesFor(i18n.RU), "\n")
	if !strings.Contains(help, "/limits") || !strings.Contains(help, "лимиты чата") {
		t.Fatalf("/help does not list /limits: %s", help)
	}
}
