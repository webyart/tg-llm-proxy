package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"bot/internal/model"
	"bot/internal/store"
	"bot/internal/testutil"
)

func ptr(v int64) *int64 { return &v }

func TestNormalize(t *testing.T) {
	var cases = []struct {
		text        string
		wantName    string
		wantArg     string
		wantCommand bool
	}{
		{"/start", "start", "", true},
		{"/Ask@MyBot привет", "ask", "привет", true},            // п.1 the suffix, п.2 the case
		{"/HELP", "help", "", true},                             // п.2
		{"/ask /help", "ask", "/help", true},                    // п.3 the rest is the argument
		{"/ask   много   слов", "ask", "много   слов", true},    // п.3 the text is taken whole
		{"обычный текст", "", "обычный текст", false},           // the free-text path of
		{"/other@OtherBot hi", "", "/other@OtherBot hi", false}, // a suffix of another bot is not ours
		{"   ", "", "", false},
		{"/", "", "/", false},
	}
	var i = 0
	for i < len(cases) {
		var c = cases[i]
		var name, arg, isCommand = Normalize(c.text, "MyBot")
		if name != c.wantName || arg != c.wantArg || isCommand != c.wantCommand {
			t.Errorf("Normalize(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.text, name, arg, isCommand, c.wantName, c.wantArg, c.wantCommand)
		}
		i++
	}
}

func TestAskWritesExactlyOneUserRowAndOneAssistantRow(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "ответ", TokensIn: ptr(10), TokensOut: ptr(7), Model: "qwen"}}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(100, 11, 501, "/ask привет")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	var stats, err = store.Stats(context.Background(), db, 100)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Messages != 2 {
		t.Fatalf("one user row + one assistant row , got %d messages", stats.Messages)
	}
	if stats.TokensIn != 10 || stats.TokensOut != 7 {
		t.Fatalf("the usage of must be stored, got in=%d out=%d", stats.TokensIn, stats.TokensOut)
	}
	if stats.Model != "qwen" {
		t.Fatalf("messages.model of must carry the model, got %q", stats.Model)
	}
	if m.callCount() != 1 {
		t.Fatalf("one /ask is one model call, got %d", m.callCount())
	}
	var answer = r.joined()
	if !strings.Contains(answer, "ответ") || !strings.Contains(answer, "⏱") {
		t.Fatalf(" wants the answer plus the ⏱ latency footer, got %q", answer)
	}
	var req = m.lastRequest()
	if len(req.Messages) != 3 || req.Messages[0].Content != nativeToolInstruction || req.Messages[1].Role != model.RoleSystem || req.Messages[2].Role != model.RoleUser {
		t.Fatalf("the glued context must be [tool instruction, system, user], got %+v", req.Messages)
	}
}

// Telegram (or a restart) hands the same update a second time.
func TestDuplicateUpdateAnswersOnce(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "ответ"}}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	var ev = testEvent(100, 11, 700, "привет")
	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}
	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("the second Dispatch of the same update must not error, got %v", err)
	}
	if m.callCount() != 1 {
		t.Fatalf("the second delivery must not reach the model, got %d calls ", m.callCount())
	}
	if r.count() != 1 {
		t.Fatalf("the user must be answered once, got %d replies", r.count())
	}
	var stats, err = store.Stats(context.Background(), db, 100)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Messages != 2 {
		t.Fatalf("one question + one answer, got %d rows ", stats.Messages)
	}
}

// TestRedeliveredUnansweredQuestionIsAnswered — the case TestDuplicateUpdateAnswersOnce does not cover, and
// acknowledged only after the handler (Dispatch → persistOffset). A process stopped in the middle of a
// generation therefore leaves the question stored WITH no answer and the offset behind the update. That is NOT
// that this guard used to be written for never brings the update — the real second chance is the scan of
// guard read «a row of this update exists» as «this update was already answered» and stayed silent: the user
// arrives as it would after a restart.
func TestRedeliveredUnansweredQuestionIsAnswered(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "ответ после ределивери"}, delay: 2 * time.Second}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	var ev = testEvent(120, 11, 900, "/ask вопрос, который оборвался")

	var stopCtx, cancel = context.WithCancel(context.Background())
	var firstDone = make(chan error, 1)
	go func() { firstDone <- d.Dispatch(stopCtx, ev) }()
	testutil.Eventually(t, 2*time.Second, func() bool { return m.callCount() == 1 },
		func() string { return "the interrupted delivery never reached the model" })
	cancel()
	_ = <-firstDone // the interrupted Dispatch is allowed to report the cancellation

	if m.callCount() != 1 {
		t.Fatalf("the interrupted delivery must have reached the model once, got %d calls", m.callCount())
	}
	if r.count() != 0 {
		t.Fatalf("an interrupted delivery must not send an answer, got %d replies", r.count())
	}
	var stats, err = store.Stats(context.Background(), db, 120)
	if err != nil {
		t.Fatalf("store.Stats: %v", err)
	}
	if stats.Messages != 1 {
		t.Fatalf("the interrupted chat must hold the question and nothing else, got %d rows ", stats.Messages)
	}
	var offset, found, offErr = store.LastUpdateID(context.Background(), db)
	if offErr != nil {
		t.Fatalf("store.LastUpdateID: %v", offErr)
	}
	if found && offset >= ev.UpdateID {
		t.Fatalf("an interrupted update must not be acknowledged , got offset %d found=%v", offset, found)
	}

	if err = d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("the second Dispatch of the same update must not error, got %v", err)
	}
	if m.callCount() != 2 {
		t.Fatalf("the redelivered question must reach the model — it has no answer of to repeat, got %d calls", m.callCount())
	}
	if r.count() != 1 {
		t.Fatalf("the user must get exactly one answer after the restart, got %d replies", r.count())
	}
	if joined := r.joined(); !strings.Contains(joined, "ответ после ределивери") {
		t.Fatalf("the answer produced after the restart is not what the model returned, got %q", joined)
	}
	if stats, err = store.Stats(context.Background(), db, 120); err != nil {
		t.Fatalf("store.Stats after the restart: %v", err)
	}
	if stats.Messages != 2 {
		t.Fatalf("one question plus one answer and no second question row, got %d rows ", stats.Messages)
	}
	var recent, recErr = store.RecentByChat(context.Background(), db, 120, 10)
	if recErr != nil {
		t.Fatalf("store.RecentByChat: %v", recErr)
	}
	if len(recent) != 2 || recent[0].Role != store.RoleUser || recent[1].Role != store.RoleAssistant {
		t.Fatalf("the conversation of must be the question once and then one answer, got %+v", recent)
	}
}

// for the interrupted question was being written: a decision made on the ROLE of the newest row of the chat is
// wrong as soon as the user asks twice. The chat then ends with the row of the second question, and the answer
// the first question already received looks missing — so a naive fix would answer the old question a second
// (store.LastVisibleMessage), so only a question that is still the newest row of the chat is answered again.
func TestRedeliveredAnsweredQuestionStaysSilentWhenTheChatMovedOn(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "ответ первой модели"}, delay: 2 * time.Second}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	var first = testEvent(121, 11, 910, "/ask первый вопрос")
	var second = testEvent(121, 11, 911, "/ask второй вопрос, который оборвался")
	if err := dispatch(t, d, first); err != nil {
		t.Fatalf("the first /ask: %v", err)
	}
	if m.callCount() != 1 || r.count() != 1 {
		t.Fatalf("the first question must be answered once, got %d model calls and %d replies", m.callCount(), r.count())
	}

	var stopCtx, cancel = context.WithCancel(context.Background())
	var secondDone = make(chan error, 1)
	go func() { secondDone <- d.Dispatch(stopCtx, second) }()
	testutil.Eventually(t, 2*time.Second, func() bool { return m.callCount() == 2 },
		func() string { return "the interrupted second question never reached the model" })
	cancel()
	_ = <-secondDone

	var stats, statsErr = store.Stats(context.Background(), db, 121)
	if statsErr != nil {
		t.Fatalf("store.Stats: %v", statsErr)
	}
	if stats.Messages != 3 {
		t.Fatalf("the chat must hold two questions and one answer, got %d rows ", stats.Messages)
	}
	var secondRow, rowErr = store.RowIDByTelegramMessageID(context.Background(), db, 121, 911)
	if rowErr != nil {
		t.Fatalf("store.RowIDByTelegramMessageID: %v", rowErr)
	}
	var newestID, newestRole, newestErr = store.LastVisibleMessage(context.Background(), db, 121)
	if newestErr != nil {
		t.Fatalf("store.LastVisibleMessage: %v", newestErr)
	}
	if secondRow == 0 || newestID != secondRow || newestRole != store.RoleUser {
		t.Fatalf("the interrupted question must be the newest visible row of, got row=%d newest=%d role=%q err=%v", secondRow, newestID, newestRole, newestErr)
	}

	// of the second one is still running). Its answer exists, so nothing may be sent again.
	if err := d.Dispatch(context.Background(), first); err != nil {
		t.Fatalf("the redelivery of an answered update must not error, got %v", err)
	}
	if m.callCount() != 2 {
		t.Fatalf("the redelivered answered question must not reach the model again, got %d calls ", m.callCount())
	}
	if r.count() != 1 {
		t.Fatalf("the user must not receive a second answer to the first question, got %d replies", r.count())
	}

	// The second update — the one that really has no answer — is answered by its own redelivery.
	if err := d.Dispatch(context.Background(), second); err != nil {
		t.Fatalf("the redelivery of the interrupted question must not error, got %v", err)
	}
	if m.callCount() != 3 {
		t.Fatalf("the interrupted question must reach the model once more, got %d calls", m.callCount())
	}
	if r.count() != 2 {
		t.Fatalf("the interrupted question must finally get its answer, got %d replies", r.count())
	}
	if stats, statsErr = store.Stats(context.Background(), db, 121); statsErr != nil {
		t.Fatalf("store.Stats after both redeliveries: %v", statsErr)
	}
	if stats.Messages != 4 {
		t.Fatalf("two questions and two answers, got %d rows ", stats.Messages)
	}
	var recent, recentErr = store.RecentByChat(context.Background(), db, 121, 10)
	if recentErr != nil {
		t.Fatalf("store.RecentByChat: %v", recentErr)
	}
	if len(recent) != 4 || recent[0].Role != store.RoleUser || recent[1].Role != store.RoleAssistant ||
		recent[2].Role != store.RoleUser || recent[3].Role != store.RoleAssistant {
		t.Fatalf("the conversation of must be question/answer/question/answer, got %+v", recent)
	}
}

// closed bot could never reveal the id needed to open it.
func TestClosedBotRefusesAndAudits(t *testing.T) {
	var cfg = testConfig(t, nil) // TELEGRAM_ALLOWED_USERS empty → DenyByDefault
	if !cfg.DenyByDefault {
		t.Fatalf("an empty allowlist must mean a closed bot ")
	}
	var m = &fakeModel{response: model.Response{Content: "ответ"}}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(100, 11, 501, "/ask секрет")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if m.callCount() != 0 {
		t.Fatalf("a denied user must never reach the model , got %d calls", m.callCount())
	}
	var refusal = r.joined()
	if !strings.Contains(refusal, "11") {
		t.Fatalf("the refusal must show the user their id , got %q", refusal)
	}
	var denied, err = store.CountEvents(context.Background(), db, store.EventDenied)
	if err != nil || denied != 1 {
		t.Fatalf("events(kind='denied') must be 1 , got %d err=%v", denied, err)
	}

	if err := dispatch(t, d, testEvent(100, 11, 502, "/whoami")); err != nil {
		t.Fatalf("/whoami of a closed bot: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 503, "/start")); err != nil {
		t.Fatalf("/start of a closed bot: %v", err)
	}
	if r.count() != 3 {
		t.Fatalf("/start and /whoami must be answered even when closed , got %d replies", r.count())
	}
	var rows, histErr = store.HistoryOfChat(context.Background(), db, 100, 10)
	if histErr != nil {
		t.Fatalf("HistoryOfChat: %v", histErr)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused /ask must not write a message row , got %d rows", len(rows))
	}
}

// names must be answered and must NOT land in events(kind='denied'). This is the regression of a real
// .env: the row was there under exactly this name, the id was right, and the bot stayed closed in silence
func TestAllowlistRowOpensTheBot(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	if cfg.DenyByDefault {
		t.Fatalf("the row of must open the bot ")
	}
	var m = &fakeModel{response: model.Response{Content: "ответ"}}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(100, 11, 601, "/ask привет")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if m.callCount() != 1 || r.count() != 1 {
		t.Fatalf("the user the row names must reach the model and be answered, got %d calls / %d replies", m.callCount(), r.count())
	}
	var denied, err = store.CountEvents(context.Background(), db, store.EventDenied)
	if err != nil || denied != 0 {
		t.Fatalf("events(kind='denied') must stay empty, got %d err=%v ", denied, err)
	}
}

func TestModelRateLimitDispatchIsNotStoredAsAssistantAnswer(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "RATE_LIMIT_PER_MINUTE": "2"})
	m := &fakeModel{response: model.Response{Content: "actual answer"}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	now := time.Unix(1700000000, 0)
	d.modelWork.WithClock(func() time.Time { return now })
	for range 5 {
		if !d.modelWork.Allow(11) {
			t.Fatal("failed to pre-consume model work")
		}
	}

	if err := dispatch(t, d, testEvent(100, 11, 650, "first")); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 651, "second")); err != nil {
		t.Fatalf("limited Dispatch: %v", err)
	}
	if m.callCount() != 1 || r.count() != 2 || !strings.Contains(r.texts()[1], modelWorkBudgetErrorText) {
		t.Fatalf("calls=%d replies=%q", m.callCount(), r.texts())
	}
	rows, err := store.HistoryOfChat(context.Background(), db, 100, 10)
	if err != nil {
		t.Fatalf("HistoryOfChat: %v", err)
	}
	if len(rows) != 3 || rows[0].Role != store.RoleUser || rows[1].Role != store.RoleAssistant || rows[1].Content != "actual answer" || rows[2].Role != store.RoleUser {
		t.Fatalf("limited notice changed stored conversation: %+v", rows)
	}
	for _, row := range rows {
		if strings.Contains(row.Content, modelWorkBudgetErrorText) {
			t.Fatalf("limit notice persisted as a message: %+v", row)
		}
	}
}

func TestRateLimitRefusesAndAudits(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "RATE_LIMIT_PER_MINUTE": "2"})
	var m = &fakeModel{response: model.Response{Content: "ответ"}}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	var i = 0
	for i < 3 {
		var err = dispatch(t, d, testEvent(100, 11, int64(900+i), "вопрос"))
		if err != nil {
			t.Fatalf("Dispatch %d: %v", i, err)
		}
		i++
	}
	if m.callCount() != 2 {
		t.Fatalf("only two of the three may reach the model , got %d calls", m.callCount())
	}
	var last = r.texts()[len(r.texts())-1]
	if !strings.Contains(last, "Слишком часто") || !strings.Contains(last, "2 запросов") {
		t.Fatalf("the refusal must name the limit , got %q", last)
	}
	var limited, err = store.CountEvents(context.Background(), db, store.EventLimit)
	if err != nil || limited != 1 {
		t.Fatalf("events(kind='limit') must be 1, got %d err=%v", limited, err)
	}
}

// an error and a notice, and the same dispatcher keeps serving the next update.
func TestPanicRecoveryKeepsTheBotAlive(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{panicky: true}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	var err = d.Dispatch(context.Background(), testEvent(100, 11, 600, "привет"))
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("the panic must surface as an error of the handler, got %v", err)
	}
	if r.count() != 1 || !strings.Contains(r.joined(), "Бот не смог обработать") {
		t.Fatalf("the user must get a readable panic notice, got %q", r.joined())
	}

	m.panicky = false
	m.response = model.Response{Content: "после паники"}
	if err := dispatch(t, d, testEvent(100, 11, 601, "ещё")); err != nil {
		t.Fatalf("Dispatch after a panic: %v", err)
	}
	if !strings.Contains(r.joined(), "после паники") {
		t.Fatalf("the bot must keep answering after a panic , got %q", r.joined())
	}
}

// «нет перепутанных ответов»: the second question waits for the slow first answer.
func TestAnswersOfOneChatKeepTheirOrder(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{contentFor: func(req model.Request) string {
		var last = req.Messages[len(req.Messages)-1]
		return "ответ на " + last.Content
	}}
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))
	m.delay = 150 * time.Millisecond

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.Dispatch(context.Background(), testEvent(100, 11, 800, "первый")); err != nil {
			t.Errorf("first dispatch: %v", err)
		}
	}()

	// the second update arrives while the first is with the model
	testutil.Eventually(t, 2*time.Second, func() bool { return m.callCount() == 1 },
		func() string { return "the first question never reached the model" })
	var err = d.Dispatch(context.Background(), testEvent(100, 11, 801, "второй"))
	if err != nil {
		t.Errorf("second dispatch: %v", err)
	}
	wg.Wait()

	var texts = r.texts()
	if len(texts) < 2 || !strings.Contains(texts[0], "ответ на первый") || !strings.Contains(texts[1], "ответ на второй") {
		t.Fatalf("the answers of one chat must arrive in the order of the questions , got %q", texts)
	}
}

// message and an unknown command both become /ask; with it false they get the command hint.
func TestFreeTextAndUnknownCommand(t *testing.T) {
	var allowed = map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}

	var onCfg = testConfig(t, merge(map[string]string{"PROXY_FREE_TEXT": "true"}, allowed))
	var onModel = &fakeModel{response: model.Response{Content: "ответ"}}
	var onReplier = &fakeReplier{}
	var onD = newTestDispatcher(t, onCfg, onModel, onReplier, testStore(t))
	if err := dispatch(t, onD, testEvent(100, 11, 700, "просто текст")); err != nil {
		t.Fatalf("free text: %v", err)
	}
	if err := dispatch(t, onD, testEvent(100, 11, 701, "/unknown текст")); err != nil {
		t.Fatalf("unknown command: %v", err)
	}
	if onModel.callCount() != 2 {
		t.Fatalf("PROXY_FREE_TEXT=true must send both texts to the model , got %d calls", onModel.callCount())
	}

	var offCfg = testConfig(t, merge(map[string]string{"PROXY_FREE_TEXT": "false"}, allowed))
	var offModel = &fakeModel{response: model.Response{Content: "ответ"}}
	var offReplier = &fakeReplier{}
	var offD = newTestDispatcher(t, offCfg, offModel, offReplier, testStore(t))
	if err := dispatch(t, offD, testEvent(101, 11, 702, "просто текст")); err != nil {
		t.Fatalf("free text off: %v", err)
	}
	if offModel.callCount() != 0 {
		t.Fatalf("PROXY_FREE_TEXT=false must not ask the model , got %d calls", offModel.callCount())
	}
	if !strings.Contains(offReplier.joined(), "/ask") {
		t.Fatalf("PROXY_FREE_TEXT=false must answer with the command list of, got %q", offReplier.joined())
	}
}

func TestModelFailureWritesOnlyAnEvent(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var cases = []struct {
		name     string
		err      error
		wantText string
		kind     string
	}{
		{"5xx", &model.APIError{Status: 503}, "Модель не ответила", store.EventModelError},
		{"timeout", context.DeadlineExceeded, "Модель не ответила за 60 с", store.EventTimeout},
		{"empty", model.ErrEmptyAnswer, "пустой ответ", store.EventModelError},
		{"no room", model.ErrNoRoom, "слоты модели заняты", store.EventModelError},
	}
	var i = 0
	for i < len(cases) {
		var c = cases[i]
		var m = &fakeModel{err: c.err}
		var r = &fakeReplier{}
		var db = testStoreNamed(t, t.Name()+"-"+c.name)
		var d = newTestDispatcher(t, cfg, m, r, db)

		if err := dispatch(t, d, testEvent(100, 11, int64(1200+i), "вопрос")); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !strings.Contains(r.joined(), c.wantText) {
			t.Fatalf("%s: the user must hear %q, got %q", c.name, c.wantText, r.joined())
		}
		var rows, histErr = store.HistoryOfChat(context.Background(), db, 100, 10)
		if histErr != nil {
			t.Fatalf("HistoryOfChat: %v", histErr)
		}
		if len(rows) != 1 || rows[0].Role != store.RoleUser {
			t.Fatalf("%s: a failed call must leave only the question , got %+v", c.name, rows)
		}
		var events, evErr = store.CountEvents(context.Background(), db, c.kind)
		if evErr != nil || events != 1 {
			t.Fatalf("%s: events(kind=%q) must be 1, got %d err=%v", c.name, c.kind, events, evErr)
		}
		i++
	}
}

// update is processed, and a late (out-of-order) update must not downgrade it, because a downgrade
func TestOffsetIsPersistedAndMonotonic(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, nil, db)

	if err := dispatch(t, d, testEvent(100, 11, 41, "текст")); err != nil { // updateID = 1041
		t.Fatalf("Dispatch: %v", err)
	}
	var offset, ok, err = store.LastUpdateID(context.Background(), db)
	if err != nil || !ok || offset != 1041 {
		t.Fatalf("bot_state last_update_id must be 1041 , got %d ok=%v err=%v", offset, ok, err)
	}

	if err := dispatch(t, d, testEvent(100, 11, 12, "старый текст")); err != nil { // updateID = 1012
		t.Fatalf("Dispatch of an older update: %v", err)
	}
	offset, _, err = store.LastUpdateID(context.Background(), db)
	if err != nil || offset != 1041 {
		t.Fatalf("an older update must not move the offset back , got %d err=%v", offset, err)
	}
}

// `https://api.telegram.org/bot<TOKEN>/<method>`, so Sanitize is the only path from an error to slog.
func TestTokenNeverLeaksIntoTheLog(t *testing.T) {
	var token = "12345:test-token-not-a-secret"
	var err = errors.New("call sendMessage: 429 https://api.telegram.org/bot" + token + "/sendMessage")
	var text = Sanitize(err, token)
	if strings.Contains(text, token) {
		t.Fatalf("the token must never reach a log line , got %q", text)
	}
	if !strings.Contains(text, "***") {
		t.Fatalf("the token must be replaced by a marker, got %q", text)
	}
}

// only a Message with a non-empty Text, so everything else — callback_query, my_chat_member,
// edited_message, a photo, and the nil update that a default handler can be handed — arrives in
// telegramHandler unprotected, and it arrives AFTER getUpdates has already deleted it from the queue of
// Measured 2026-10-31 on this machine, where the «carries no text» line read upd.Message.Chat.ID before
// eventOf had looked at Message: the first such update panicked in the goroutine of the library, the
// which is how an answered /ask turned into nothing at all. So: the panic must not escape the handler, the
// update that was not processed. The last part of the test is the reason the net is a net and not a filter:
// the update that DOES carry text still goes all the way through the same function.
func TestUpdatesWithoutMessageSurviveTheHandler(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, r, db)

	var cases = []struct {
		name string
		upd  *models.Update
	}{
		{"callback_query", &models.Update{ID: 20, CallbackQuery: &models.CallbackQuery{ID: "cb-1"}}},
		{"my_chat_member", &models.Update{ID: 21, MyChatMember: &models.ChatMemberUpdated{}}},
		{"edited_message", &models.Update{ID: 22, EditedMessage: &models.Message{ID: 5, Chat: models.Chat{ID: 100}, Text: "правка"}}},
		{"message without text", &models.Update{ID: 23, Message: &models.Message{ID: 6, Chat: models.Chat{ID: 100}}}},
		{"nil update", nil},
	}
	var i = 0
	for i < len(cases) {
		var c = cases[i]
		d.telegramHandler(context.Background(), nil, c.upd) // a panic that escapes here kills the process
		i++
	}

	var counts, countErr = store.TableCounts(context.Background(), db)
	if countErr != nil {
		t.Fatalf("store.TableCounts: %v", countErr)
	}
	var k = 0
	for k < len(counts) {
		// The FTS5 index and its shadow tables (messages_fts*, search_index_meta) are schema, not conversation:
		// opening the database creates them, and their bookkeeping rows are not a reply to an update.
		if strings.HasPrefix(counts[k].Name, "messages_fts") || strings.HasPrefix(counts[k].Name, "rag_chunks_fts") || counts[k].Name == "search_index_meta" {
			k++
			continue
		}
		if counts[k].Name != "schema_migrations" && counts[k].Rows != 0 {
			t.Fatalf("an update without a message must write nothing : %s has %d rows", counts[k].Name, counts[k].Rows)
		}
		k++
	}
	var found = false
	var err error
	if _, found, err = store.LastUpdateID(context.Background(), db); err != nil || found {
		t.Fatalf("nothing was processed, so bot_state must stay empty , found=%v err=%v", found, err)
	}
	if len(r.sent) != 0 {
		t.Fatalf("an update without a message must not be answered , %d sends", len(r.sent))
	}

	d.telegramHandler(context.Background(), nil, &models.Update{ID: 24, Message: &models.Message{
		ID:   7,
		Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
		From: &models.User{ID: 11, Username: "tester"},
		Text: "/ask привет",
	}})

	var after, afterErr = store.TableCounts(context.Background(), db)
	if afterErr != nil {
		t.Fatalf("store.TableCounts after /ask: %v", afterErr)
	}
	var rows = map[string]int64{}
	k = 0
	for k < len(after) {
		rows[after[k].Name] = after[k].Rows
		k++
	}
	if rows["chats"] != 1 || rows["events"] != 2 || rows["messages"] != 2 {
		t.Fatalf("/ask through the same handler must behave as before the net : chats=%d events=%d messages=%d",
			rows["chats"], rows["events"], rows["messages"])
	}
	var offset int64
	if offset, found, err = store.LastUpdateID(context.Background(), db); err != nil || !found || offset != 24 {
		t.Fatalf("bot_state must hold the processed update 24 , got %d found=%v err=%v", offset, found, err)
	}
	if len(r.sent) != 1 {
		t.Fatalf("/ask must answer once , %d sends", len(r.sent))
	}
}

// TestStorageFailureLeavesTheUpdateUnacknowledgedAndTellsTheUser — the P1 of a failed question write: the
// store stage returns before the handler, so there is no model call and no answer. The offset must NOT move
// (Telegram then redelivers the question after a restart) and the user must hear why the chat stayed silent.
func TestStorageFailureLeavesTheUpdateUnacknowledgedAndTellsTheUser(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, r, db)

	var offsets []int64
	d.deps.Offset = func(ctx context.Context, updateID int64) error {
		offsets = append(offsets, updateID)
		return nil
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	var ev = testEvent(160, 11, 700, "/ask вопрос, который не сохранится")
	var err = d.Dispatch(context.Background(), ev)
	if err == nil {
		t.Fatal("a failed question write must surface as an error")
	}
	if len(offsets) != 0 {
		t.Fatalf("the update was acknowledged after a failed write: %v", offsets)
	}
	if joined := r.joined(); !strings.Contains(joined, msgQuestionNotStored) {
		t.Fatalf("the user was not told the question was not stored: %q", joined)
	}
}

// TestAlreadyAnsweredRedeliveryIsAcknowledged — the duplicate branch of the store stage returns without the
// handler. The row it conflicted with proves the question WAS stored, so the update may be acknowledged:
// leaving it unacknowledged would make Telegram redeliver an already answered update on every restart.
func TestAlreadyAnsweredRedeliveryIsAcknowledged(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var db = testStore(t)
	var ctx = context.Background()
	if _, err := store.Append(ctx, db, store.Message{
		ChatID: 161, UserID: 11, Role: store.RoleUser, Content: "вопрос", TelegramMessageID: ptr(4242),
	}); err != nil {
		t.Fatalf("storing the question: %v", err)
	}
	if _, err := store.Append(ctx, db, store.Message{
		ChatID: 161, UserID: 11, Role: store.RoleAssistant, Content: "ответ",
	}); err != nil {
		t.Fatalf("storing the answer: %v", err)
	}
	var m = &fakeModel{response: model.Response{Content: "второй ответ"}}
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, m, r, db)

	var ev = testEvent(161, 11, 4242, "/ask вопрос")
	ev.UpdateID = 5000
	if err := d.Dispatch(ctx, ev); err != nil {
		t.Fatalf("the redelivered answered update must not fail: %v", err)
	}
	if m.callCount() != 0 || r.count() != 0 {
		t.Fatalf("an already answered update was answered again: model=%d replies=%q", m.callCount(), r.texts())
	}
	var offset, found, err = store.LastUpdateID(ctx, db)
	if err != nil || !found || offset != 5000 {
		t.Fatalf("the duplicate was not acknowledged: offset=%d found=%v err=%v", offset, found, err)
	}
}

// TestRateLimitedModelUpdateIsNotAcknowledged — the per-minute refusal happens before the store stage, so the
// question was never written. Acknowledging it would lose it for good, exactly like the failed-write case.
func TestRateLimitedModelUpdateIsNotAcknowledged(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "RATE_LIMIT_PER_MINUTE": "1"})
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, &fakeModel{response: model.Response{Content: "ответ"}}, &fakeReplier{}, db)
	var ctx = context.Background()

	var first = testEvent(162, 11, 800, "/ask первый")
	if err := d.Dispatch(ctx, first); err != nil {
		t.Fatalf("the first update: %v", err)
	}
	var second = testEvent(162, 11, 801, "/ask второй")
	if err := d.Dispatch(ctx, second); err != nil {
		t.Fatalf("the limited update must be refused without an error: %v", err)
	}
	var offset, found, err = store.LastUpdateID(ctx, db)
	if err != nil {
		t.Fatalf("LastUpdateID: %v", err)
	}
	if found && offset >= second.UpdateID {
		t.Fatalf("the rate-limited update was acknowledged: offset=%d second=%d", offset, second.UpdateID)
	}
}
