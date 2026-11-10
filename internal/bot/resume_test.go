// start of the process asks the database instead. These cases pin what that scan is allowed and not allowed to
// do: answer the question exactly once, answer only the newest one, and never answer anything the chat has
// already been given.
package bot

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"bot/internal/model"
	"bot/internal/store"
	"bot/internal/testutil"
)

// interruptDispatch starts Dispatch, waits until it is inside the model call, and cancels it there: the
// question is stored, the answer is not, and the offset is not acknowledged (persistOffset runs after the
// handler in Dispatch).
func interruptDispatch(t *testing.T, d *Dispatcher, m *fakeModel, ev Event) {
	t.Helper()
	var stopCtx, cancel = context.WithCancel(context.Background())
	defer cancel()
	var before = m.callCount()
	var done = make(chan error, 1)
	go func() { done <- d.Dispatch(stopCtx, ev) }()
	testutil.Eventually(t, 2*time.Second, func() bool { return m.callCount() > before },
		func() string { return "the interrupted delivery never reached the model" })
	cancel()
	<-done // an interrupted Dispatch is allowed to report the cancellation: the process is leaving
}

// database what Telegram will not ask it again, and the user gets the answer they never received.
func TestStartupAnswersTheQuestionCutMidGeneration(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "ответ, которого не дождался пользователь"}, delay: 2 * time.Second}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)
	var ev = testEvent(150, 11, 960, "/ask вопрос, оборванный сигналом")

	interruptDispatch(t, d, m, ev)

	var stats, err = store.Stats(context.Background(), db, 150)
	if err != nil {
		t.Fatalf("store.Stats: %v", err)
	}
	if stats.Messages != 1 {
		t.Fatalf("the interrupted chat must hold the question and nothing else, got %d rows", stats.Messages)
	}
	if _, found, offErr := store.LastUpdateID(context.Background(), db); offErr != nil || found {
		t.Fatalf("an interrupted update must not be acknowledged, found=%v err=%v ", found, offErr)
	}
	var commands, cmdErr = store.CountEvents(context.Background(), db, store.EventCommand)
	if cmdErr != nil {
		t.Fatalf("store.CountEvents: %v", cmdErr)
	}

	var restarted = newTestDispatcher(t, cfg, m, r, db)
	var answered, pending, resumeErr = restarted.ResumeInterruptedQuestions(context.Background())
	if resumeErr != nil {
		t.Fatalf("ResumeInterruptedQuestions: %v", resumeErr)
	}
	if answered != 1 || pending != 0 {
		t.Fatalf("the waiting question must be resumed once, got answered=%d pending=%d", answered, pending)
	}
	if m.callCount() != 2 {
		t.Fatalf("one interrupted call plus one resumed call, got %d", m.callCount())
	}
	if r.count() != 1 {
		t.Fatalf("the user must get exactly one answer at the start of the new process, got %d replies", r.count())
	}
	if joined := r.joined(); !strings.Contains(joined, "ответ, которого не дождался пользователь") {
		t.Fatalf("the resumed answer is not what the model returned, got %q", joined)
	}
	// user actually asked, not a copy of it typed again.
	var last = m.lastRequest()
	if len(last.Messages) == 0 || !strings.Contains(last.Messages[len(last.Messages)-1].Content, "вопрос, оборванный сигналом") {
		t.Fatalf("the resumed request must end with the stored question, got %+v", last.Messages)
	}
	if stats, err = store.Stats(context.Background(), db, 150); err != nil {
		t.Fatalf("store.Stats after the resume: %v", err)
	}
	if stats.Messages != 2 {
		t.Fatalf("one question and one answer, no second question row, got %d rows ", stats.Messages)
	}
	// command. The replay goes through the chain, not through Dispatch, so it must not count the same question
	if after, afterErr := store.CountEvents(context.Background(), db, store.EventCommand); afterErr != nil || after != commands {
		t.Fatalf("the replay must not add a second command event, got %d after %d err=%v", after, commands, afterErr)
	}
	if _, found, offErr := store.LastUpdateID(context.Background(), db); offErr != nil || found {
		t.Fatalf("the replay must not move the offset of, found=%v err=%v", found, offErr)
	}
}

// TestStartupStaysSilentWhenTheChatIsCaughtUp — the other half of the same guard: a chat that ends with an
// second copy of an answer the user already has.
func TestStartupStaysSilentWhenTheChatIsCaughtUp(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "обычный ответ"}}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(151, 11, 965, "/ask обычный вопрос")); err != nil {
		t.Fatalf("the ordinary Dispatch must not fail: %v", err)
	}
	var answered, pending, err = d.ResumeInterruptedQuestions(context.Background())
	if err != nil {
		t.Fatalf("ResumeInterruptedQuestions: %v", err)
	}
	if answered != 0 || pending != 0 {
		t.Fatalf("an answered chat must not be resumed, got answered=%d pending=%d", answered, pending)
	}
	if m.callCount() != 1 || r.count() != 1 {
		t.Fatalf("the answered chat must stay exactly as it was, got %d model calls and %d replies", m.callCount(), r.count())
	}
}

// 2026-10-31), now on the scan side: the user asked twice, the first question was answered, the second was cut
// mid-generation. The chat does end with a question — but only the second one is waiting, and answering the
func TestStartupAnswersOnlyTheNewestQuestionOfAChat(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "ответ"}, delay: 2 * time.Second}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(152, 11, 970, "/ask первый вопрос, уже отвечен")); err != nil {
		t.Fatalf("the first Dispatch must not fail: %v", err)
	}
	interruptDispatch(t, d, m, testEvent(152, 11, 971, "/ask второй вопрос, оборванный"))

	var answered, pending, err = d.ResumeInterruptedQuestions(context.Background())
	if err != nil {
		t.Fatalf("ResumeInterruptedQuestions: %v", err)
	}
	if answered != 1 || pending != 0 {
		t.Fatalf("only the second question is waiting, got answered=%d pending=%d", answered, pending)
	}
	var last = m.lastRequest()
	if len(last.Messages) == 0 || !strings.Contains(last.Messages[len(last.Messages)-1].Content, "второй вопрос, оборванный") {
		t.Fatalf("the resumed request must ask the SECOND question, got %+v", last.Messages)
	}
	var stats, statsErr = store.Stats(context.Background(), db, 152)
	if statsErr != nil {
		t.Fatalf("store.Stats: %v", statsErr)
	}
	if stats.Messages != 4 {
		t.Fatalf("two questions and two answers, got %d rows", stats.Messages)
	}
}

// TestStartupReplayAndRedeliveryAnswerOnce — the two paths that can chase the same question at once: the scan of
func TestStartupReplayAndRedeliveryAnswerOnce(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "один-единственный ответ"}, delay: 400 * time.Millisecond}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)
	var ev = testEvent(153, 11, 980, "/ask вопрос, за которым пришли дважды")

	interruptDispatch(t, d, m, ev)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, _, err := d.ResumeInterruptedQuestions(context.Background()); err != nil {
			t.Errorf("the replay of must not fail: %v", err)
		}
	}()
	// the replay takes the lock of first; the redelivery queues behind it
	testutil.Eventually(t, 2*time.Second, func() bool { return m.callCount() == 2 },
		func() string { return "the startup replay never reached the model" })
	go func() {
		defer wg.Done()
		if err := d.Dispatch(context.Background(), ev); err != nil {
			t.Errorf("the redelivered update must not fail, got %v", err)
		}
	}()
	wg.Wait()

	if m.callCount() != 2 {
		t.Fatalf("the question must reach the model once more in total, got %d calls (one interrupted + one answered)", m.callCount())
	}
	if r.count() != 1 {
		t.Fatalf("the user must get one answer, got %d replies ", r.count())
	}
	var stats, err = store.Stats(context.Background(), db, 153)
	if err != nil {
		t.Fatalf("store.Stats: %v", err)
	}
	if stats.Messages != 2 {
		t.Fatalf("one question and one answer after two competing paths, got %d rows", stats.Messages)
	}
}

// 15:55:28 and the first version of resume.go reported wrongly. The scan named the waiting question, the guard of
// notice to the user) and returns nil to the chain. What the handler returns therefore says nothing about the
func TestStartupCountsAQuestionTheModelCouldNotAnswer(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "ответ, которого не будет"}, delay: 2 * time.Second}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)
	var ev = testEvent(154, 11, 985, "/ask вопрос, на который бюджет модели не кончился")

	interruptDispatch(t, d, m, ev)

	var spent = &fakeModel{err: context.DeadlineExceeded}
	var restarted = newTestDispatcher(t, cfg, spent, r, db)
	var answered, pending, err = restarted.ResumeInterruptedQuestions(context.Background())
	if err != nil {
		t.Fatalf("ResumeInterruptedQuestions: %v", err)
	}
	if answered != 0 {
		t.Fatalf("a question with no row of must not be counted as answered, got %d", answered)
	}
	if pending != 1 {
		t.Fatalf("the unanswered question must be counted as pending, got %d", pending)
	}
	if spent.callCount() != 1 {
		t.Fatalf("the replay must have asked the model exactly once, got %d calls", spent.callCount())
	}
	var left, leftErr = store.UnansweredQuestions(context.Background(), db, ResumeScanLimit)
	if leftErr != nil {
		t.Fatalf("store.UnansweredQuestions: %v", leftErr)
	}
	if len(left) != 1 || left[0].ChatID != 154 {
		t.Fatalf("the question must stay waiting for the next start, got %+v", left)
	}
	var stats, statsErr = store.Stats(context.Background(), db, 154)
	if statsErr != nil {
		t.Fatalf("store.Stats: %v", statsErr)
	}
	if stats.Messages != 1 {
		t.Fatalf("no row of may appear for an answer that never happened, got %d rows ", stats.Messages)
	}
	if r.count() != 1 {
		t.Fatalf("the user must get exactly one failure notice, got %d replies", r.count())
	}
	if joined := r.joined(); !strings.Contains(joined, "не ответила") {
		t.Fatalf("the notice of must name the failure, got %q", joined)
	}
	var timeouts, evErr = store.CountEvents(context.Background(), db, store.EventTimeout)
	if evErr != nil || timeouts != 1 {
		t.Fatalf("events(kind=%q) must be 1 after the failed replay, got %d err=%v", store.EventTimeout, timeouts, evErr)
	}
}

func TestStartupDoesNotReplayAnAnswerWhoseDeliveryFailed(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var interruptedModel = &fakeModel{response: model.Response{Content: "прерванный ответ"}, delay: 2 * time.Second}
	var db = testStore(t)
	var interrupted = newTestDispatcher(t, cfg, interruptedModel, &fakeReplier{}, db)
	interruptDispatch(t, interrupted, interruptedModel, testEvent(155, 11, 990, "/ask вопрос"))

	var deliveryFailure = &fakeReplier{err: context.DeadlineExceeded}
	var restarted = newTestDispatcher(t, cfg, &fakeModel{response: model.Response{Content: "ответ сохранён"}}, deliveryFailure, db)
	var answered, pending, err = restarted.ResumeInterruptedQuestions(context.Background())
	if err != nil {
		t.Fatalf("ResumeInterruptedQuestions: %v", err)
	}
	if answered != 1 || pending != 0 {
		t.Fatalf("a stored assistant row must not be called unanswered after a send failure: answered=%d pending=%d", answered, pending)
	}
	var left, leftErr = store.UnansweredQuestions(context.Background(), db, ResumeScanLimit)
	if leftErr != nil || len(left) != 0 {
		t.Fatalf("the stored answer must close the replay window, left=%v err=%v", left, leftErr)
	}
	var rows = assistantRows(t, db, 155)
	if len(rows) != 1 || rows[0].Content != "ответ сохранён" {
		t.Fatalf("want exactly one stored answer and no model replay, got %v", rows)
	}
}
