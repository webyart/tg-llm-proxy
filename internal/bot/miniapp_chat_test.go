package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

// miniAppEventLog collects the events of one answer. Delta events (and the delta carried by done)
// are kept apart by deltas(), because done repeats the final text for reconciliation.
type miniAppEventLog struct {
	events []MiniAppStreamEvent
}

func (l *miniAppEventLog) emit(event MiniAppStreamEvent) error {
	l.events = append(l.events, event)
	return nil
}

func (l *miniAppEventLog) deltas() []string {
	out := []string{}
	for _, event := range l.events {
		if event.Delta != "" && !event.Done {
			out = append(out, event.Delta)
		}
	}
	return out
}

func (l *miniAppEventLog) done() (MiniAppStreamEvent, bool) {
	for index := len(l.events) - 1; index >= 0; index-- {
		if l.events[index].Done {
			return l.events[index], true
		}
	}
	return MiniAppStreamEvent{}, false
}

func TestMiniAppAnswerStreamsDeltasAndStoresHistoryInOrder(t *testing.T) {
	cfg := streamConfig(t, nil)
	streamed := &fakeStreamModel{steps: []streamStep{
		{content: "При"},
		{content: "вет"},
		{content: ", "},
		{content: "мир"},
	}}
	replier := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, streamed, replier, db)

	log := &miniAppEventLog{}
	err := d.AnswerForMiniApp(context.Background(), 11, 11, "  привет  ", log.emit)
	if err != nil {
		t.Fatalf("AnswerForMiniApp: %v", err)
	}
	if streamed.streamed() != 1 {
		t.Fatalf("stream calls = %d, want 1 with STREAMING_ENABLED=true", streamed.streamed())
	}
	if replier.count() != 0 {
		t.Fatalf("Mini App answer sent %d Telegram messages: %q", replier.count(), replier.texts())
	}

	deltas := log.deltas()
	if got := strings.Join(deltas, ""); got != "Привет, мир" {
		t.Fatalf("forwarded deltas = %q, want %q (in order)", got, "Привет, мир")
	}
	done, ok := log.done()
	if !ok {
		t.Fatal("the answer ended without a done event")
	}
	if last := log.events[len(log.events)-1]; !last.Done {
		t.Fatalf("done must be the last event, got %+v", log.events)
	}

	rows, err := store.HistoryOfChat(context.Background(), db, 11, 10)
	if err != nil {
		t.Fatalf("HistoryOfChat: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("history rows = %+v, want user then assistant", rows)
	}
	if rows[0].Role != store.RoleUser || rows[1].Role != store.RoleAssistant {
		t.Fatalf("history roles = %q, %q, want user, assistant", rows[0].Role, rows[1].Role)
	}
	if rows[0].Content != "привет" {
		t.Errorf("stored question = %q, want the trimmed text", rows[0].Content)
	}
	if rows[1].Content != done.Delta || done.Delta != "Привет, мир" {
		t.Errorf("done text = %q, stored assistant = %q, streamed = %q", done.Delta, rows[1].Content, streamed.Content())
	}
	if rows[1].Model != "fake-model" {
		t.Errorf("stored assistant model = %q", rows[1].Model)
	}
}

func TestMiniAppAnswerNonStreamingFallbackEmitsWholeAnswer(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "false"})
	m := &fakeModel{response: model.Response{Content: "ответ модели"}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)

	log := &miniAppEventLog{}
	if err := d.AnswerForMiniApp(context.Background(), 11, 11, "вопрос", log.emit); err != nil {
		t.Fatalf("AnswerForMiniApp: %v", err)
	}
	if m.callCount() != 1 {
		t.Fatalf("model calls = %d, want 1", m.callCount())
	}
	deltas := log.deltas()
	if len(deltas) != 1 || deltas[0] != "ответ модели" {
		t.Fatalf("fallback deltas = %q, want one full answer", deltas)
	}
	done, ok := log.done()
	if !ok || done.Delta != "ответ модели" {
		t.Fatalf("done = %+v ok=%v", done, ok)
	}
	if r.count() != 0 {
		t.Fatalf("fallback sent Telegram messages: %q", r.texts())
	}
}

func TestMiniAppAnswerStreamingDisabledUsesCompleteEvenIfModelCanStream(t *testing.T) {
	cfg := streamConfig(t, map[string]string{"STREAMING_ENABLED": "false"})
	m := &fakeStreamModel{steps: []streamStep{{content: "раз"}, {content: " два"}}}
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, testStore(t))

	log := &miniAppEventLog{}
	if err := d.AnswerForMiniApp(context.Background(), 11, 11, "вопрос", log.emit); err != nil {
		t.Fatalf("AnswerForMiniApp: %v", err)
	}
	if m.streamed() != 0 {
		t.Fatalf("stream calls = %d, want 0 when STREAMING_ENABLED=false", m.streamed())
	}
	if got := strings.Join(log.deltas(), ""); got != "раз два" {
		t.Fatalf("fallback deltas = %q", got)
	}
}

func TestMiniAppAnswerSecondCallIsRateLimited(t *testing.T) {
	cfg := streamConfig(t, map[string]string{"RATE_LIMIT_PER_MINUTE": "1"})
	m := &fakeModel{response: model.Response{Content: "ответ"}}
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, testStore(t))

	if err := d.AnswerForMiniApp(context.Background(), 11, 11, "первый", (&miniAppEventLog{}).emit); err != nil {
		t.Fatalf("first AnswerForMiniApp: %v", err)
	}
	log := &miniAppEventLog{}
	err := d.AnswerForMiniApp(context.Background(), 11, 11, "второй", log.emit)
	var limited *MiniAppRateLimitError
	if !errors.As(err, &limited) {
		t.Fatalf("second AnswerForMiniApp = %v, want *MiniAppRateLimitError", err)
	}
	if limited.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %v, want a positive window", limited.RetryAfter)
	}
	if len(log.events) != 0 {
		t.Fatalf("rate-limited call emitted events: %+v", log.events)
	}
	if m.callCount() != 1 {
		t.Fatalf("model calls = %d, want the second call rejected before the model", m.callCount())
	}
	rows, err := store.HistoryOfChat(context.Background(), d.deps.DB, 11, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Content != "первый" {
		t.Fatalf("rate-limited call changed history: %+v", rows)
	}
}

func TestMiniAppAnswerDenyByDefaultRefusesWithoutModelCall(t *testing.T) {
	cfg := testConfig(t, nil) // TELEGRAM_ALLOWED_USERS empty => deny by default
	m := &fakeModel{response: model.Response{Content: "не должно случиться"}}
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, testStore(t))

	log := &miniAppEventLog{}
	err := d.AnswerForMiniApp(context.Background(), 11, 11, "вопрос", log.emit)
	if !errors.Is(err, ErrMiniAppDenied) {
		t.Fatalf("AnswerForMiniApp = %v, want ErrMiniAppDenied", err)
	}
	if len(log.events) != 0 {
		t.Fatalf("denied call emitted events: %+v", log.events)
	}
	if m.callCount() != 0 {
		t.Fatalf("denied user reached the model %d times", m.callCount())
	}
	rows, err := store.HistoryOfChat(context.Background(), d.deps.DB, 11, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("denied user stored history: %+v", rows)
	}
}

func TestMiniAppResetHidesContextButKeepsHistory(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "false"})
	m := &fakeModel{response: model.Response{Content: "ответ"}}
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, testStore(t))
	ctx := context.Background()

	if err := d.AnswerForMiniApp(ctx, 11, 11, "первый вопрос", (&miniAppEventLog{}).emit); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	if err := d.ResetContextForMiniApp(ctx, 11, 11); err != nil {
		t.Fatalf("ResetContextForMiniApp: %v", err)
	}
	visible, err := store.RecentByChat(ctx, d.deps.DB, 11, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 0 {
		t.Fatalf("reset left %d messages visible to the model: %+v", len(visible), visible)
	}
	history, err := store.HistoryOfChat(ctx, d.deps.DB, 11, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("reset deleted history: %+v", history)
	}

	if err := d.AnswerForMiniApp(ctx, 11, 11, "второй вопрос", (&miniAppEventLog{}).emit); err != nil {
		t.Fatalf("second answer: %v", err)
	}
	request := m.lastRequest()
	for _, message := range request.Messages {
		if message.Content == "первый вопрос" || message.Content == "ответ" {
			t.Fatalf("context after reset still contains %q: %+v", message.Content, request.Messages)
		}
	}
	found := false
	for _, message := range request.Messages {
		if message.Role == model.RoleUser && message.Content == "второй вопрос" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the new question is missing from the model request: %+v", request.Messages)
	}
}

func TestMiniAppSkipAndUnskipMirrorSlashCommands(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	d := newTestDispatcher(t, cfg, &fakeModel{}, &fakeReplier{}, testStore(t))
	ctx := context.Background()

	if changed, err := d.SkipPendingForMiniApp(ctx, 11, 11, true); err != nil || changed {
		t.Fatalf("skip without a pending question = %v/%v, want false/nil", changed, err)
	}
	if _, err := store.Append(ctx, d.deps.DB, store.Message{ChatID: 11, UserID: 11, Role: store.RoleUser, Content: "ожидает ответа"}); err != nil {
		t.Fatal(err)
	}
	changed, err := d.SkipPendingForMiniApp(ctx, 11, 11, true)
	if err != nil || !changed {
		t.Fatalf("skip = %v/%v, want true/nil", changed, err)
	}
	questionID, found, err := store.ResumeSkip(ctx, d.deps.DB, 11)
	if err != nil || !found || questionID <= 0 {
		t.Fatalf("ResumeSkip = %d/%v/%v", questionID, found, err)
	}
	if changed, err := d.SkipPendingForMiniApp(ctx, 11, 11, false); err != nil || !changed {
		t.Fatalf("unskip = %v/%v, want true/nil", changed, err)
	}
	if _, found, err := store.ResumeSkip(ctx, d.deps.DB, 11); err != nil || found {
		t.Fatalf("suppression survived unskip: found=%v err=%v", found, err)
	}
	if changed, err := d.SkipPendingForMiniApp(ctx, 11, 11, false); err != nil || changed {
		t.Fatalf("repeated unskip = %v/%v, want false/nil", changed, err)
	}
	rows, err := store.HistoryOfChat(ctx, d.deps.DB, 11, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("skip/unskip changed history: rows=%+v err=%v", rows, err)
	}
	if _, err := d.SkipPendingForMiniApp(ctx, 11, 12, true); !errors.Is(err, ErrMiniAppDenied) {
		t.Fatalf("skip for a non-allowlisted user = %v, want ErrMiniAppDenied", err)
	}
}

func TestMiniAppModelFailureKeepsTheQuestionUnansweredAndEmitsError(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "false"})
	m := &fakeModel{err: errors.New("provider is down")}
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, testStore(t))

	log := &miniAppEventLog{}
	err := d.AnswerForMiniApp(context.Background(), 11, 11, "вопрос без ответа", log.emit)
	if err == nil {
		t.Fatal("AnswerForMiniApp returned nil for a failed model call")
	}
	if len(log.events) == 0 || log.events[len(log.events)-1].Error == "" {
		t.Fatalf("failure produced no error event: %+v", log.events)
	}
	if _, ok := log.done(); ok {
		t.Fatal("a failed answer must not emit done")
	}
	rows, err := store.HistoryOfChat(context.Background(), d.deps.DB, 11, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Role != store.RoleUser {
		t.Fatalf("failed answer stored rows = %+v, want the unanswered question only", rows)
	}
	lastID, lastRole, err := store.LastVisibleMessage(context.Background(), d.deps.DB, 11)
	if err != nil || lastID == 0 || lastRole != store.RoleUser {
		t.Fatalf("the chat must end with the unanswered question: id=%d role=%q err=%v", lastID, lastRole, err)
	}
}
