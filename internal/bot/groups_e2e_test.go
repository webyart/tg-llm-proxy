package bot

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"bot/internal/metrics"
	"bot/internal/model"
	"bot/internal/store"
)

// TestGroupChatIsStoredWithItsTitleAndGroupType — a group is registered under its own negative id,
// with Telegram's title and a type the admin list can name. The chat id is never used as a user id.
func TestGroupChatIsStoredWithItsTitleAndGroupType(t *testing.T) {
	ctx := context.Background()
	cfg := groupTestConfig(t, nil)
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{response: model.Response{Content: "ответ"}}, &fakeReplier{}, db)

	ev := groupEvent(-100500, 11, 700, "@TestBot привет")
	ev.ChatTitle = "Engineering"
	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("group dispatch: %v", err)
	}
	var title string
	if err := db.QueryRowContext(ctx, `SELECT title FROM chats WHERE chat_id = ?`, int64(-100500)).Scan(&title); err != nil {
		t.Fatalf("group title: %v", err)
	}
	if title != "Engineering" {
		t.Fatalf("group title = %q, want Engineering", title)
	}
	chats, err := store.ListActiveChats(ctx, db, 10)
	if err != nil || len(chats) != 1 {
		t.Fatalf("ListActiveChats = %+v err=%v", chats, err)
	}
	if chats[0].ID != -100500 || chats[0].Type != "group" || chats[0].Title != "Engineering" {
		t.Fatalf("admin chat row = %+v, want id -100500, type group, title Engineering", chats[0])
	}
}

// TestGroupLimitsAreKeyedByTheNegativeChatID — the group's minute limit counts that group only and
// a group limit never lands on a sender's private chat.
func TestGroupLimitsAreKeyedByTheNegativeChatID(t *testing.T) {
	ctx := context.Background()
	cfg := groupTestConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12", "TELEGRAM_ADMIN_USERS": "11"})
	db := testStore(t)
	m := &fakeModel{response: model.Response{Content: "ответ"}}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, groupEvent(-100500, 11, 700, "/limits@TestBot minute 1")); err != nil {
		t.Fatalf("admin /limits minute: %v", err)
	}
	limits, found, err := store.GetChatLimits(ctx, db, -100500)
	if err != nil || !found || limits.PerMinuteLimit != 1 {
		t.Fatalf("group limits = %+v found=%v err=%v", limits, found, err)
	}
	if _, found, _ := store.GetChatLimits(ctx, db, 12); found {
		t.Fatal("a group limit was written to a sender's private chat")
	}

	if err := dispatch(t, d, groupEvent(-100500, 12, 701, "@TestBot первый")); err != nil {
		t.Fatalf("first group question: %v", err)
	}
	if err := dispatch(t, d, groupEvent(-100500, 12, 702, "@TestBot второй")); err != nil {
		t.Fatalf("second group question: %v", err)
	}
	if m.callCount() != 1 {
		t.Fatalf("chat minute limit model calls = %d, want 1", m.callCount())
	}
	if !strings.Contains(r.joined(), "не больше 1 сообщений в минуту") {
		t.Fatalf("chat minute refusal = %q", r.joined())
	}

	// A private chat of the same sender has no such limit.
	if err := dispatch(t, d, testEvent(12, 12, 703, "привет")); err != nil {
		t.Fatalf("private dispatch: %v", err)
	}
	if m.callCount() != 2 {
		t.Fatalf("group minute limit leaked into the private chat: model calls=%d want 2", m.callCount())
	}
}

// TestGroupDailyModelLimitUsesTheChatAndNeverTheSenderAsOwner — the daily counter belongs to the
// negative chat id; the per-user quota still belongs to the sender.
func TestGroupDailyModelLimitUsesTheChatAndNeverTheSenderAsOwner(t *testing.T) {
	ctx := context.Background()
	cfg := groupTestConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12", "TELEGRAM_ADMIN_USERS": "11", "USER_DAILY_MODEL_LIMIT": "5"})
	db := testStore(t)
	m := &fakeModel{response: model.Response{Content: "ответ"}}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, db)
	// The admin path is direct here: this test is about enforcement and attribution, not about the
	// write permission (covered by TestGroupLimitsRequireAdministrator).
	if err := d.limitsControl(ctx, groupEvent(-100500, 11, 700, "/limits model 1"), "model 1"); err != nil {
		t.Fatalf("set group model limit: %v", err)
	}

	if err := dispatch(t, d, groupEvent(-100500, 11, 701, "@TestBot первый")); err != nil {
		t.Fatalf("first group question: %v", err)
	}
	if err := dispatch(t, d, groupEvent(-100500, 12, 702, "@TestBot второй")); err != nil {
		t.Fatalf("second group question: %v", err)
	}
	if m.callCount() != 1 {
		t.Fatalf("group daily limit model calls = %d, want 1", m.callCount())
	}
	if !strings.Contains(r.joined(), "Дневной лимит обращений к модели для этого чата") {
		t.Fatalf("group daily refusal = %q", r.joined())
	}
	now := d.now().UTC()
	usedBySender, err := store.DailyQuotaUsed(ctx, db, 11, now, store.DailyQuotaModel)
	if err != nil || usedBySender != 1 {
		t.Fatalf("sender 11 daily quota = %d err=%v, want 1", usedBySender, err)
	}
	usedByChat, chatErr := store.DailyQuotaUsed(ctx, db, -100500, now, store.DailyQuotaModel)
	if chatErr == nil && usedByChat != 0 {
		t.Fatalf("negative chat id was charged as a user: quota=%d", usedByChat)
	}
	var negativeReservations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_quota_reservations WHERE user_id = ?`, int64(-100500)).Scan(&negativeReservations); err != nil {
		t.Fatal(err)
	}
	if negativeReservations != 0 {
		t.Fatalf("negative chat id became a quota owner: %d rows", negativeReservations)
	}
	usedByOther, err := store.DailyQuotaUsed(ctx, db, 12, now, store.DailyQuotaModel)
	if err != nil || usedByOther != 0 {
		t.Fatalf("refused second sender spent quota: %d err=%v", usedByOther, err)
	}
}

// TestGroupLimitsRequireAdministrator — a group member cannot write or reset the group's limits; an
// administrator can. Reading is open to every allowed member.
func TestGroupLimitsRequireAdministrator(t *testing.T) {
	ctx := context.Background()
	cfg := groupTestConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12", "TELEGRAM_ADMIN_USERS": "11"})
	db := testStore(t)
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)

	if err := dispatch(t, d, groupEvent(-100500, 12, 700, "/limits@TestBot model 5")); err != nil {
		t.Fatalf("member /limits: %v", err)
	}
	if !strings.Contains(r.joined(), "только администратор") {
		t.Fatalf("member /limits reply = %q", r.joined())
	}
	if _, found, _ := store.GetChatLimits(ctx, db, -100500); found {
		t.Fatal("a group member wrote the group limits")
	}
	if err := dispatch(t, d, groupEvent(-100500, 12, 701, "/limits@TestBot")); err != nil {
		t.Fatalf("member /limits read: %v", err)
	}
	if !strings.Contains(r.joined(), "Лимиты чата") {
		t.Fatalf("member /limits read = %q", r.joined())
	}
	if err := dispatch(t, d, groupEvent(-100500, 11, 702, "/limits@TestBot model 5")); err != nil {
		t.Fatalf("admin /limits: %v", err)
	}
	limits, found, err := store.GetChatLimits(ctx, db, -100500)
	if err != nil || !found || limits.DailyModelLimit != 5 {
		t.Fatalf("admin /limits = %+v found=%v err=%v", limits, found, err)
	}
}

// TestGroupModelWritesRequireAdministrator — a group member cannot switch or reset the model of the
// whole group; the private owner still manages the model of their own chat.
func TestGroupModelWritesRequireAdministrator(t *testing.T) {
	ctx := context.Background()
	cfg := groupTestConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12", "TELEGRAM_ADMIN_USERS": "11"})
	db := testStore(t)
	m := &fakeModel{models: []string{"model-a", "model-b"}, response: model.Response{Content: "ответ"}}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, groupEvent(-100500, 12, 700, "/model@TestBot select model-a")); err != nil {
		t.Fatalf("member /model select: %v", err)
	}
	if !strings.Contains(r.joined(), "только администратор") {
		t.Fatalf("member /model select reply = %q", r.joined())
	}
	if model, found, _ := store.ChatModel(ctx, db, -100500); found || model != "" {
		t.Fatalf("a group member selected the group model: %q", model)
	}
	if err := dispatch(t, d, groupEvent(-100500, 12, 701, "/model@TestBot reset")); err != nil {
		t.Fatalf("member /model reset: %v", err)
	}
	if !strings.Contains(r.joined(), "только администратор") {
		t.Fatalf("member /model reset reply = %q", r.joined())
	}

	if err := dispatch(t, d, groupEvent(-100500, 11, 702, "/model@TestBot select model-b")); err != nil {
		t.Fatalf("admin /model select: %v", err)
	}
	if model, found, err := store.ChatModel(ctx, db, -100500); err != nil || !found || model != "model-b" {
		t.Fatalf("admin group model = %q found=%v err=%v", model, found, err)
	}
	if err := dispatch(t, d, groupEvent(-100500, 11, 703, "/model@TestBot reset")); err != nil {
		t.Fatalf("admin /model reset: %v", err)
	}
	if model, found, _ := store.ChatModel(ctx, db, -100500); found || model != "" {
		t.Fatalf("admin reset left model %q", model)
	}

	// The private owner is still allowed to change the model of their own chat.
	if err := dispatch(t, d, testEvent(12, 12, 704, "/model select model-a")); err != nil {
		t.Fatalf("private /model select: %v", err)
	}
	if model, found, err := store.ChatModel(ctx, db, 12); err != nil || !found || model != "model-a" {
		t.Fatalf("private owner model = %q found=%v err=%v", model, found, err)
	}
}

// TestGroupTimezoneCommandRequiresAdministrator — a group member cannot move the group's display
// zone through the Telegram command; the administrator can, and the stored override is read back.
func TestGroupTimezoneCommandRequiresAdministrator(t *testing.T) {
	ctx := context.Background()
	cfg := groupTestConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12", "TELEGRAM_ADMIN_USERS": "11"})
	db := testStore(t)
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)

	if err := dispatch(t, d, groupEvent(-100500, 12, 700, "/timezone@TestBot Europe/Berlin")); err != nil {
		t.Fatalf("member /timezone: %v", err)
	}
	if !strings.Contains(r.joined(), "только администратор") {
		t.Fatalf("member /timezone reply = %q", r.joined())
	}
	if timezone, found, _ := store.GetChatTimezone(ctx, db, -100500); found || timezone != "" {
		t.Fatalf("a group member moved the group zone to %q", timezone)
	}
	if err := dispatch(t, d, groupEvent(-100500, 12, 701, "/timezone@TestBot")); err != nil {
		t.Fatalf("member /timezone read: %v", err)
	}
	if !strings.Contains(r.joined(), "по умолчанию") {
		t.Fatalf("member /timezone read = %q", r.joined())
	}
	if err := dispatch(t, d, groupEvent(-100500, 11, 702, "/timezone@TestBot Europe/Berlin")); err != nil {
		t.Fatalf("admin /timezone: %v", err)
	}
	if timezone, found, err := store.GetChatTimezone(ctx, db, -100500); err != nil || !found || timezone != "Europe/Berlin" {
		t.Fatalf("admin group timezone = %q found=%v err=%v", timezone, found, err)
	}
	if err := dispatch(t, d, groupEvent(-100500, 11, 703, "/timezone@TestBot")); err != nil {
		t.Fatalf("admin /timezone read: %v", err)
	}
	if !strings.Contains(r.joined(), "Europe/Berlin") {
		t.Fatalf("admin /timezone read = %q", r.joined())
	}
}

// TestGroupNetworkApprovalBindsToTheGroupAndRequester — the approval prompt is sent into the group,
// only the requester in that group can confirm it, and the action runs once.
func TestGroupNetworkApprovalBindsToTheGroupAndRequester(t *testing.T) {
	const query = "group approval query"
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Результат","url":"https://example.com","content":"Содержимое"}]}`))
	}))
	defer provider.Close()
	call := model.ToolCall{ID: "group-search", Name: "web_search", Arguments: `{"query":"` + query + `"}`}
	m := &scriptedToolModel{responses: []model.Response{
		{ToolCalls: []model.ToolCall{call}, Attempts: 1},
		{Content: "Финальный ответ после подтверждения", Model: "fake-model", Attempts: 1},
	}}
	r := &networkMenuReplier{}
	cfg := groupTestConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11,12",
		"WEB_SEARCH_ENABLED":     "true",
		"WEB_SEARCH_BASE_URL":    provider.URL,
	})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	if err := dispatch(t, d, groupEvent(-100500, 11, 9000, "@TestBot подготовь обзор проекта")); err != nil {
		t.Fatalf("group approval prompt: %v", err)
	}
	confirm, _ := latestNetworkCallbacks(t, r)
	if providerCalls.Load() != 0 || len(m.requests) != 1 {
		t.Fatal("group search ran before confirmation")
	}
	if last := r.sent[len(r.sent)-1]; last.chatID != -100500 {
		t.Fatalf("approval prompt was sent to chat %d, want the group", last.chatID)
	}

	for _, wrong := range []struct {
		chatID    int64
		userID    int64
		messageID int64
	}{
		{-100500, 12, 700}, // another allowlisted member of the same group
		{-100501, 11, 700}, // the requester in another group
		{-100500, 11, 701}, // a different message id
	} {
		if err := dispatchGroupMenuData(t, d, wrong.chatID, wrong.userID, wrong.messageID, confirm); err != nil {
			t.Fatalf("wrong approval dispatch: %v", err)
		}
	}
	if providerCalls.Load() != 0 || len(m.requests) != 1 {
		t.Fatal("a wrong group, member or message consumed the group approval")
	}
	if err := dispatchGroupMenuData(t, d, -100500, 11, 700, confirm); err != nil {
		t.Fatalf("correct group approval: %v", err)
	}
	if providerCalls.Load() != 1 || len(m.requests) != 2 || !strings.Contains(r.joined(), "Финальный ответ после подтверждения") {
		t.Fatalf("approved group loop did not resume: provider=%d model=%d replies=%q", providerCalls.Load(), len(m.requests), r.joined())
	}
	for _, message := range r.sent {
		if message.chatID != -100500 {
			t.Fatalf("group answer leaked into chat %d", message.chatID)
		}
	}
}

// TestGroupReminderStaysInTheGroupAndUsesTheGroupZone — a reminder created in a group is stored with
// the negative chat id and the creator, is listed there and is delivered there in the group's zone.
func TestGroupReminderStaysInTheGroupAndUsesTheGroupZone(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cfg := groupTestConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12"})
	db := testStore(t)
	r := &fakeReplier{}
	m := &fakeModel{response: model.Response{Content: "не должно быть"}}
	d := newTestDispatcher(t, cfg, m, r, db)
	d.now = func() time.Time { return now }
	if err := store.EnsureChat(ctx, db, -100500, 11, "Group"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatTimezone(ctx, db, -100500, "Europe/Berlin"); err != nil {
		t.Fatal(err)
	}

	if err := dispatch(t, d, groupEvent(-100500, 11, 800, "/remind@TestBot every day 09:00 зарядка")); err != nil {
		t.Fatalf("group /remind: %v", err)
	}
	reminders, err := store.ListReminders(ctx, db, -100500, 10)
	if err != nil || len(reminders) != 1 {
		t.Fatalf("group reminders = %+v err=%v", reminders, err)
	}
	reminder := reminders[0]
	if reminder.ChatID != -100500 || reminder.UserID != 11 {
		t.Fatalf("group reminder = %+v, want chat -100500 user 11", reminder)
	}
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	if got := reminder.NextRunAt.In(location).Format("15:04"); got != "09:00" {
		t.Fatalf("group reminder next run = %s, want 09:00 Europe/Berlin", got)
	}
	before := r.count()
	if _, err := d.ProcessDueReminders(ctx, reminder.NextRunAt, 10); err != nil {
		t.Fatalf("deliver group reminder: %v", err)
	}
	if r.count() != before+1 || r.sent[r.count()-1].chatID != -100500 || !strings.Contains(r.sent[r.count()-1].text, "зарядка") {
		t.Fatalf("group reminder delivery = %+v", r.sent)
	}
	if m.callCount() != 0 {
		t.Fatalf("groups reminder delivered by model calls=%d, want 0", m.callCount())
	}
}

// TestGroupTaskStaysInTheGroup — task planning, status and cancellation use the negative group chat
// id and the creator as the task owner.
func TestGroupTaskStaysInTheGroup(t *testing.T) {
	ctx := context.Background()
	cfg := groupTestConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12"})
	db := testStore(t)
	r := &fakeReplier{}
	m := taskTextModel(validTaskPlanOne)
	d := newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, groupEvent(-100500, 11, 810, "/task@TestBot подготовь план")); err != nil {
		t.Fatalf("group /task: %v", err)
	}
	waitTaskPlanningSettled(t, d)
	task, found, err := store.CurrentTask(ctx, db, -100500)
	if err != nil || !found {
		t.Fatalf("group task = %+v found=%v err=%v", task, found, err)
	}
	if task.ChatID != -100500 || task.UserID != 11 || task.Goal != "подготовь план" {
		t.Fatalf("group task = %+v, want chat -100500 user 11 goal", task)
	}
	for _, message := range r.sent {
		if message.chatID != -100500 {
			t.Fatalf("group task message leaked into chat %d", message.chatID)
		}
	}

	if err := dispatch(t, d, groupEvent(-100500, 11, 811, "/task@TestBot")); err != nil {
		t.Fatalf("group /task status: %v", err)
	}
	if !strings.Contains(r.joined(), "Задача #") {
		t.Fatalf("group task status = %q", r.joined())
	}
	if err := dispatch(t, d, groupEvent(-100500, 11, 812, "/task@TestBot cancel")); err != nil {
		t.Fatalf("group /task cancel: %v", err)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, task.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != store.TaskStatusCancelled {
		t.Fatalf("group task status = %q, want cancelled", status)
	}
}

// TestGroupContentNeverReachesLogsMetricsOrEventDetails — the group title and message bodies stay in
// the chats/messages tables, not in slog, /metrics or events.detail.
func TestGroupContentNeverReachesLogsMetricsOrEventDetails(t *testing.T) {
	const (
		secretQuestion = "group-question-SECRET-4711"
		secretAnswer   = "group-answer-SECRET-4712"
		secretTitle    = "Secret Group Title 4713"
	)
	ctx := context.Background()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	cfg := groupTestConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TELEGRAM_ADMIN_USERS": "11"})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{response: model.Response{Content: secretAnswer}}, &fakeReplier{}, db, logger)
	reg := metrics.NewRegistry()
	d.SetMetrics(reg)

	ev := groupEvent(-100500, 11, 820, "@TestBot "+secretQuestion)
	ev.ChatTitle = secretTitle
	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("privacy dispatch: %v", err)
	}

	metricsBody := metricsBody(t, reg)
	for _, text := range []string{secretQuestion, secretAnswer, secretTitle} {
		if strings.Contains(logs.String(), text) {
			t.Fatalf("group content/title leaked into logs: %s", logs.String())
		}
	}
	for _, text := range []string{secretQuestion, secretAnswer, secretTitle, "-100500"} {
		if strings.Contains(metricsBody, text) {
			t.Fatalf("group content/title/id leaked into metrics:\n%s", metricsBody)
		}
	}
	events, err := store.RecentEvents(ctx, db, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		for _, text := range []string{secretQuestion, secretAnswer, secretTitle} {
			if strings.Contains(event.Detail, text) {
				t.Fatalf("group content leaked into events.detail: %+v", event)
			}
		}
	}
	// The content itself must still be stored in the chat, or the feature would be broken rather than private.
	rows, err := store.HistoryOfChat(ctx, db, -100500, 10)
	if err != nil || len(rows) != 2 || rows[0].Content != secretQuestion || rows[1].Content != secretAnswer {
		t.Fatalf("group history = %+v err=%v", rows, err)
	}
}

func dispatchGroupMenuData(t *testing.T, d *Dispatcher, chatID int64, userID int64, messageID int64, data string) error {
	t.Helper()
	ev, ok := d.callbackEventOf(&models.Update{ID: 9100 + messageID, CallbackQuery: &models.CallbackQuery{
		ID:   "group-menu",
		Data: data,
		From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: int(messageID), Chat: models.Chat{ID: chatID, Type: models.ChatTypeSupergroup, Title: "Group"},
		}},
	}})
	if !ok {
		t.Fatalf("group callback %q was not translated", data)
	}
	if ev.ChatType != chatTypeSupergroup {
		t.Fatalf("group callback chat type = %q", ev.ChatType)
	}
	return dispatch(t, d, ev)
}
