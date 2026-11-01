// resume_test.go — the §15 item «догонять вопрос, оборванный внутри обработчика» (resume.go). §11 measured the
// real Telegram on 2026-10-31: a stop inside the handler leaves the row of the question in §5 and the offset of
// §5 behind it, and the update is never handed back, so the guard of §2 п.7 never gets a second look at it. The
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
)

// interruptDispatch — the stop of §10 День 1 in the middle of a generation: the row of the question is already
// in §5 (stored of §2 п.7 writes it before the model call), the answer never lands, the offset is never
// acknowledged (persistOffset runs after the handler in Dispatch).
func interruptDispatch(t *testing.T, d *Dispatcher, ev Event) {
	t.Helper()
	var stopCtx, cancel = context.WithCancel(context.Background())
	defer cancel()
	var done = make(chan error, 1)
	go func() { done <- d.Dispatch(stopCtx, ev) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done // an interrupted Dispatch is allowed to report the cancellation: the process is leaving
}

// TestStartupAnswersTheQuestionCutMidGeneration — the whole point of resume.go: the restart of §8.1 asks the
// database what Telegram will not ask it again, and the user gets the answer they never received.
func TestStartupAnswersTheQuestionCutMidGeneration(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "ответ, которого не дождался пользователь"}, delay: 2 * time.Second}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)
	var ev = testEvent(150, 11, 960, "/ask вопрос, оборванный сигналом")

	interruptDispatch(t, d, ev)

	// The state §11 measured in the volume of §8.1: the question is stored, nothing answered it, and the
	// offset of §5 stayed behind the update.
	var stats, err = store.Stats(context.Background(), db, 150)
	if err != nil {
		t.Fatalf("store.Stats: %v", err)
	}
	if stats.Messages != 1 {
		t.Fatalf("the interrupted chat must hold the question and nothing else, got %d rows", stats.Messages)
	}
	if _, found, offErr := store.LastUpdateID(context.Background(), db); offErr != nil || found {
		t.Fatalf("an interrupted update must not be acknowledged, found=%v err=%v (§5: persistOffset runs after the handler)", found, offErr)
	}
	var commands, cmdErr = store.CountEvents(context.Background(), db, store.EventCommand)
	if cmdErr != nil {
		t.Fatalf("store.CountEvents: %v", cmdErr)
	}

	// The restart: a fresh Dispatcher over the same rows, with the scan of §15 instead of a redelivery.
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
	// The glued context of §5 must end with the stored question: the replay asks the model the question the
	// user actually asked, not a copy of it typed again.
	var last = m.lastRequest()
	if len(last.Messages) == 0 || !strings.Contains(last.Messages[len(last.Messages)-1].Content, "вопрос, оборванный сигналом") {
		t.Fatalf("the resumed request must end with the stored question, got %+v", last.Messages)
	}
	if stats, err = store.Stats(context.Background(), db, 150); err != nil {
		t.Fatalf("store.Stats after the resume: %v", err)
	}
	if stats.Messages != 2 {
		t.Fatalf("one question and one answer, no second question row, got %d rows (§5 uq_messages_chat_tg_id is what keeps the question single)", stats.Messages)
	}
	// §5 events («service: commands, errors, limits»): the interrupted run already wrote the audit row of the
	// command. The replay goes through the chain, not through Dispatch, so it must not count the same question
	// as a second command — `dbinspect` of §9 would otherwise read one question as two.
	if after, afterErr := store.CountEvents(context.Background(), db, store.EventCommand); afterErr != nil || after != commands {
		t.Fatalf("the replay must not add a second command event, got %d after %d err=%v", after, commands, afterErr)
	}
	// There is no update here to acknowledge: the Event is rebuilt from a row, so the offset of §5 stays where
	// the interrupted run left it (§13 риск «повторные ответы после рестарта»).
	if _, found, offErr := store.LastUpdateID(context.Background(), db); offErr != nil || found {
		t.Fatalf("the replay must not move the offset of §5, found=%v err=%v", found, offErr)
	}
}

// TestStartupStaysSilentWhenTheChatIsCaughtUp — the other half of the same guard: a chat that ends with an
// answer of §6 gets nothing at the start of the process. The §13 risk this whole path exists to avoid is a
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

// TestStartupAnswersOnlyTheNewestQuestionOfAChat — the trap the naive role comparison fell into (§2 п.7,
// 2026-10-31), now on the scan side: the user asked twice, the first question was answered, the second was cut
// mid-generation. The chat does end with a question — but only the second one is waiting, and answering the
// first again is the duplicate answer §13 names.
func TestStartupAnswersOnlyTheNewestQuestionOfAChat(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "ответ"}, delay: 2 * time.Second}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(152, 11, 970, "/ask первый вопрос, уже отвечен")); err != nil {
		t.Fatalf("the first Dispatch must not fail: %v", err)
	}
	interruptDispatch(t, d, testEvent(152, 11, 971, "/ask второй вопрос, оборванный"))

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
// §15 and a real redelivery of the update (Telegram does re-deliver when the getUpdates response never reached
// the client — §11). Whichever of them gets the per-chat lock of §12 second must find the answer row the first
// one wrote and stay silent: the §13 риск «повторные ответы» is what the dedup guard exists for.
func TestStartupReplayAndRedeliveryAnswerOnce(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "один-единственный ответ"}, delay: 400 * time.Millisecond}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)
	var ev = testEvent(153, 11, 980, "/ask вопрос, за которым пришли дважды")

	interruptDispatch(t, d, ev)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, _, err := d.ResumeInterruptedQuestions(context.Background()); err != nil {
			t.Errorf("the replay of §15 must not fail: %v", err)
		}
	}()
	time.Sleep(50 * time.Millisecond) // the replay takes the lock of §12 first; the redelivery queues behind it
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
		t.Fatalf("the user must get one answer, got %d replies (§12 lock of the chat plus uq_messages_chat_tg_id of §5)", r.count())
	}
	var stats, err = store.Stats(context.Background(), db, 153)
	if err != nil {
		t.Fatalf("store.Stats: %v", err)
	}
	if stats.Messages != 2 {
		t.Fatalf("one question and one answer after two competing paths, got %d rows", stats.Messages)
	}
}

// TestStartupCountsAQuestionTheModelCouldNotAnswer — the case the live container of §8.1 found on 2026-10-31 at
// 15:55:28 and the first version of resume.go reported wrongly. The scan named the waiting question, the guard of
// §2 п.7 let it through, the model spent the whole STRATA_TIMEOUT_SECONDS 60s and wrote nothing — and the process
// printed «answered 1», because the ask handler of §6 handles a model failure itself (the events row of §5, the
// notice to the user) and returns nil to the chain. What the handler returns therefore says nothing about the
// answer of §6; resume.go counts from §5 instead, and this test is the reason.
func TestStartupCountsAQuestionTheModelCouldNotAnswer(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "ответ, которого не будет"}, delay: 2 * time.Second}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)
	var ev = testEvent(154, 11, 985, "/ask вопрос, на который бюджет модели не кончился")

	interruptDispatch(t, d, ev)

	// The restart, with the model of §3 failing exactly the way it failed in the container: the budget of §7 gone.
	var spent = &fakeModel{err: context.DeadlineExceeded}
	var restarted = newTestDispatcher(t, cfg, spent, r, db)
	var answered, pending, err = restarted.ResumeInterruptedQuestions(context.Background())
	if err != nil {
		t.Fatalf("ResumeInterruptedQuestions: %v", err)
	}
	if answered != 0 {
		t.Fatalf("a question with no row of §6 must not be counted as answered, got %d", answered)
	}
	if pending != 1 {
		t.Fatalf("the unanswered question must be counted as pending, got %d", pending)
	}
	if spent.callCount() != 1 {
		t.Fatalf("the replay must have asked the model exactly once, got %d calls", spent.callCount())
	}
	// §5 still ends with the question, which is what makes the next start of the process try it again — and the
	// guard of §2 п.7 plus uq_messages_chat_tg_id of §5 are what keep that retry from answering twice.
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
		t.Fatalf("no row of §6 may appear for an answer that never happened, got %d rows (§10 День 3)", stats.Messages)
	}
	// The user did get something: the failure notice of §12 UX, and it must not read like an answer.
	if r.count() != 1 {
		t.Fatalf("the user must get exactly one failure notice, got %d replies", r.count())
	}
	if joined := r.joined(); !strings.Contains(joined, "не ответила") {
		t.Fatalf("the notice of §6 must name the failure, got %q", joined)
	}
	// events of §5: the timeout budget spent, which is the row `dbinspect` of §9 reads (§10 День 3 — a failed
	// call leaves the event and nothing else in §5).
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
	interruptDispatch(t, interrupted, testEvent(155, 11, 990, "/ask вопрос"))

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
