// dispatch_test.go — the §10 День 4 DoD without a Telegram API: the normalization of §6 п.1–3, the
// allowlist of §7 (with the audit row of §10 День 4), the idempotency of §2 п.7, the write invariant
// of §10 День 3, the middleware of §12 and the offset of §5.
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
)

// ptr — the nullable columns of §5.
func ptr(v int64) *int64 { return &v }

// TestNormalize — the three rules of §6, each with the Telegram shape that motivated it.
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
		{"обычный текст", "", "обычный текст", false},           // the free-text path of §6
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

// TestAskWritesExactlyOneUserRowAndOneAssistantRow — §10 День 3 «одна запись assistant» plus §5: the
// question is stored too, and the tokens/model of §5 come from the Response.
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
		t.Fatalf("one user row + one assistant row (§10 День 3), got %d messages", stats.Messages)
	}
	if stats.TokensIn != 10 || stats.TokensOut != 7 {
		t.Fatalf("the usage of §5 must be stored, got in=%d out=%d", stats.TokensIn, stats.TokensOut)
	}
	if stats.Model != "qwen" {
		t.Fatalf("messages.model of §5 must carry the model, got %q", stats.Model)
	}
	if m.callCount() != 1 {
		t.Fatalf("one /ask is one model call, got %d", m.callCount())
	}
	var answer = r.joined()
	if !strings.Contains(answer, "ответ") || !strings.Contains(answer, "⏱") {
		t.Fatalf("§6 wants the answer plus the ⏱ latency footer, got %q", answer)
	}
	// The request of §5: SYSTEM_PROMPT first, then the user turn the middleware stored.
	var req = m.lastRequest()
	if len(req.Messages) != 2 || req.Messages[0].Role != model.RoleSystem || req.Messages[1].Role != model.RoleUser {
		t.Fatalf("the glued context of §5 must be [system, user], got %+v", req.Messages)
	}
}

// TestDuplicateUpdateAnswersOnce — §2 п.7: the UNIQUE index of §5 is the second line of defence when
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
		t.Fatalf("the second delivery must not reach the model, got %d calls (§13 risk «повторные ответы»)", m.callCount())
	}
	if r.count() != 1 {
		t.Fatalf("the user must be answered once, got %d replies", r.count())
	}
	var stats, err = store.Stats(context.Background(), db, 100)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Messages != 2 {
		t.Fatalf("one question + one answer, got %d rows (§5 uq_messages_chat_tg_id)", stats.Messages)
	}
}

// TestRedeliveredUnansweredQuestionIsAnswered — the case TestDuplicateUpdateAnswersOnce does not cover, and
// the reason the §11 walk «Ctrl-C / docker compose down при длинном /ask» (2026-10-31) is in the README: the
// row of role='user' is written by stored of §2 п.7 BEFORE the model call, while the offset of §5 is
// acknowledged only after the handler (Dispatch → persistOffset). A process stopped in the middle of a
// generation therefore leaves the question stored WITH no answer and the offset behind the update. That is NOT
// a second chance: the §11 walk measured that Telegram does not hand such an update back at all, so the restart
// that this guard used to be written for never brings the update — the real second chance is the scan of
// resume.go (§15), and this test is the fake of the redelivery that §11 could not produce. The old
// guard read «a row of this update exists» as «this update was already answered» and stayed silent: the user
// never got an answer, and no row of events of §5 said why. The sequence here is that exact one — the first
// delivery is cancelled while the model of §7 is still producing, the second delivery of the same update
// arrives as it would after a restart.
func TestRedeliveredUnansweredQuestionIsAnswered(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{response: model.Response{Content: "ответ после ределивери"}, delay: 2 * time.Second}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	var ev = testEvent(120, 11, 900, "/ask вопрос, который оборвался")

	// The stop of §10 День 1 in the middle of the generation: the model call of §7 has begun, the row of the
	// question is already in §5, and the answer never lands.
	var stopCtx, cancel = context.WithCancel(context.Background())
	var firstDone = make(chan error, 1)
	go func() { firstDone <- d.Dispatch(stopCtx, ev) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	_ = <-firstDone // the interrupted Dispatch is allowed to report the cancellation (§10 День 1: the process is leaving)

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
		t.Fatalf("the interrupted chat must hold the question and nothing else, got %d rows (§2 п.7 writes the question before the model call)", stats.Messages)
	}
	var offset, found, offErr = store.LastUpdateID(context.Background(), db)
	if offErr != nil {
		t.Fatalf("store.LastUpdateID: %v", offErr)
	}
	if found && offset >= ev.UpdateID {
		t.Fatalf("an interrupted update must not be acknowledged (§5: persistOffset runs after the handler; §11 measured that Telegram does not hand the update back, which is why §15 asks the database), got offset %d found=%v", offset, found)
	}

	// The restart of §8.1: the same update arrives again, and answering it is the only chance it ever gets.
	if err = d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("the second Dispatch of the same update must not error, got %v", err)
	}
	if m.callCount() != 2 {
		t.Fatalf("the redelivered question must reach the model — it has no answer of §6 to repeat, got %d calls", m.callCount())
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
		t.Fatalf("one question plus one answer and no second question row, got %d rows (§5 uq_messages_chat_tg_id is what keeps the question single)", stats.Messages)
	}
	var recent, recErr = store.RecentByChat(context.Background(), db, 120, 10)
	if recErr != nil {
		t.Fatalf("store.RecentByChat: %v", recErr)
	}
	if len(recent) != 2 || recent[0].Role != store.RoleUser || recent[1].Role != store.RoleAssistant {
		t.Fatalf("the conversation of §5 must be the question once and then one answer, got %+v", recent)
	}
}

// TestRedeliveredAnsweredQuestionStaysSilentWhenTheChatMovedOn — the hazard the §11 item exposed while the fix
// for the interrupted question was being written: a decision made on the ROLE of the newest row of the chat is
// wrong as soon as the user asks twice. The chat then ends with the row of the second question, and the answer
// the first question already received looks missing — so a naive fix would answer the old question a second
// time, which is exactly the §13 risk «повторные ответы» this middleware exists to prevent. The guard compares
// the row of THIS update (store.RowIDByTelegramMessageID) with the newest row of §5
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

	// The second question is the one the process loses in the middle of (§11: the row of the question is stored,
	// its answer is not, and the offset of §5 was never acknowledged).
	var stopCtx, cancel = context.WithCancel(context.Background())
	var secondDone = make(chan error, 1)
	go func() { secondDone <- d.Dispatch(stopCtx, second) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	_ = <-secondDone

	var stats, statsErr = store.Stats(context.Background(), db, 121)
	if statsErr != nil {
		t.Fatalf("store.Stats: %v", statsErr)
	}
	if stats.Messages != 3 {
		t.Fatalf("the chat must hold two questions and one answer, got %d rows (§5)", stats.Messages)
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
		t.Fatalf("the interrupted question must be the newest visible row of §5, got row=%d newest=%d role=%q err=%v", secondRow, newestID, newestRole, newestErr)
	}

	// Telegram hands the FIRST update back after the restart (§5 does not acknowledge updates while the handler
	// of the second one is still running). Its answer exists, so nothing may be sent again.
	if err := d.Dispatch(context.Background(), first); err != nil {
		t.Fatalf("the redelivery of an answered update must not error, got %v", err)
	}
	if m.callCount() != 2 {
		t.Fatalf("the redelivered answered question must not reach the model again, got %d calls (§13 risk «повторные ответы»)", m.callCount())
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
		t.Fatalf("two questions and two answers, got %d rows (§5 uq_messages_chat_tg_id keeps every question single)", stats.Messages)
	}
	var recent, recentErr = store.RecentByChat(context.Background(), db, 121, 10)
	if recentErr != nil {
		t.Fatalf("store.RecentByChat: %v", recentErr)
	}
	if len(recent) != 4 || recent[0].Role != store.RoleUser || recent[1].Role != store.RoleAssistant ||
		recent[2].Role != store.RoleUser || recent[3].Role != store.RoleAssistant {
		t.Fatalf("the conversation of §5 must be question/answer/question/answer, got %+v", recent)
	}
}

// TestClosedBotRefusesAndAudits — §7 deny-by-default + §10 День 4 DoD («чужой user id получает отказ и
// попадает в events(kind='denied')»), with §16 change 13: /start and /whoami still answer, otherwise a
// closed bot could never reveal the id needed to open it.
func TestClosedBotRefusesAndAudits(t *testing.T) {
	var cfg = testConfig(t, nil) // TELEGRAM_ALLOWED_USERS empty → DenyByDefault (§7)
	if !cfg.DenyByDefault {
		t.Fatalf("an empty allowlist must mean a closed bot (§7)")
	}
	var m = &fakeModel{response: model.Response{Content: "ответ"}}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(100, 11, 501, "/ask секрет")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if m.callCount() != 0 {
		t.Fatalf("a denied user must never reach the model (§12 security), got %d calls", m.callCount())
	}
	var refusal = r.joined()
	if !strings.Contains(refusal, "11") {
		t.Fatalf("the refusal must show the user their id (§16 change 13), got %q", refusal)
	}
	var denied, err = store.CountEvents(context.Background(), db, store.EventDenied)
	if err != nil || denied != 1 {
		t.Fatalf("events(kind='denied') must be 1 (§10 День 4 DoD), got %d err=%v", denied, err)
	}

	if err := dispatch(t, d, testEvent(100, 11, 502, "/whoami")); err != nil {
		t.Fatalf("/whoami of a closed bot: %v", err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 503, "/start")); err != nil {
		t.Fatalf("/start of a closed bot: %v", err)
	}
	if r.count() != 3 {
		t.Fatalf("/start and /whoami must be answered even when closed (§16 change 13), got %d replies", r.count())
	}
	var rows, histErr = store.HistoryOfChat(context.Background(), db, 100, 10)
	if histErr != nil {
		t.Fatalf("HistoryOfChat: %v", histErr)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused /ask must not write a message row (§10 День 3), got %d rows", len(rows))
	}
}

// TestAllowlistRowOpensTheBot — §16 change 29: TELEGRAM_ALLOWED_USERS is the row of §7, so the user it
// names must be answered and must NOT land in events(kind='denied'). This is the regression of a real
// .env: the row was there under exactly this name, the id was right, and the bot stayed closed in silence
// because the loader of §10 День 1 knew a different spelling.
func TestAllowlistRowOpensTheBot(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	if cfg.DenyByDefault {
		t.Fatalf("the row of §7 must open the bot (§16 change 29)")
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
		t.Fatalf("events(kind='denied') must stay empty, got %d err=%v (§7)", denied, err)
	}
}

// TestRateLimitRefusesAndAudits — §7 RATE_LIMIT_PER_MINUTE, §12 reliability: the refusal says how long
// to wait and lands in events(kind='limit'), and the model is never reached (§12 protects the GPU).
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
		t.Fatalf("only two of the three may reach the model (§7 limit), got %d calls", m.callCount())
	}
	var last = r.texts()[len(r.texts())-1]
	if !strings.Contains(last, "Слишком часто") || !strings.Contains(last, "2 запросов") {
		t.Fatalf("the refusal must name the limit (§12 UX), got %q", last)
	}
	var limited, err = store.CountEvents(context.Background(), db, store.EventLimit)
	if err != nil || limited != 1 {
		t.Fatalf("events(kind='limit') must be 1, got %d err=%v", limited, err)
	}
}

// TestPanicRecoveryKeepsTheBotAlive — §12 надёжность «panic recovery в обработчике»: the panic becomes
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
	if r.count() != 1 || !strings.Contains(r.joined(), "panic") {
		t.Fatalf("the user must get the notice of §12, got %q", r.joined())
	}

	m.panicky = false
	m.response = model.Response{Content: "после паники"}
	if err := dispatch(t, d, testEvent(100, 11, 601, "ещё")); err != nil {
		t.Fatalf("Dispatch after a panic: %v", err)
	}
	if !strings.Contains(r.joined(), "после паники") {
		t.Fatalf("the bot must keep answering after a panic (§12 надёжность), got %q", r.joined())
	}
}

// TestAnswersOfOneChatKeepTheirOrder — §12 «per-chat lock гарантирует порядок ответов» and §10 День 6
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

	time.Sleep(40 * time.Millisecond) // the second update arrives while the first is with the model
	var err = d.Dispatch(context.Background(), testEvent(100, 11, 801, "второй"))
	if err != nil {
		t.Errorf("second dispatch: %v", err)
	}
	wg.Wait()

	var texts = r.texts()
	if len(texts) < 2 || !strings.Contains(texts[0], "ответ на первый") || !strings.Contains(texts[1], "ответ на второй") {
		t.Fatalf("the answers of one chat must arrive in the order of the questions (§12), got %q", texts)
	}
}

// TestFreeTextAndUnknownCommand — §6 «Обработка прочего текста»: with PROXY_FREE_TEXT=true a plain
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
		t.Fatalf("PROXY_FREE_TEXT=true must send both texts to the model (§6), got %d calls", onModel.callCount())
	}

	var offCfg = testConfig(t, merge(map[string]string{"PROXY_FREE_TEXT": "false"}, allowed))
	var offModel = &fakeModel{response: model.Response{Content: "ответ"}}
	var offReplier = &fakeReplier{}
	var offD = newTestDispatcher(t, offCfg, offModel, offReplier, testStore(t))
	if err := dispatch(t, offD, testEvent(101, 11, 702, "просто текст")); err != nil {
		t.Fatalf("free text off: %v", err)
	}
	if offModel.callCount() != 0 {
		t.Fatalf("PROXY_FREE_TEXT=false must not ask the model (§6), got %d calls", offModel.callCount())
	}
	if !strings.Contains(offReplier.joined(), "/ask") {
		t.Fatalf("PROXY_FREE_TEXT=false must answer with the command list of §6, got %q", offReplier.joined())
	}
}

// TestModelFailureWritesOnlyAnEvent — §10 День 3: a failed call leaves NO assistant row, only the
// event of §5 and the notice of §12 UX.
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
			t.Fatalf("%s: a failed call must leave only the question (§10 День 3), got %+v", c.name, rows)
		}
		var events, evErr = store.CountEvents(context.Background(), db, c.kind)
		if evErr != nil || events != 1 {
			t.Fatalf("%s: events(kind=%q) must be 1, got %d err=%v", c.name, c.kind, events, evErr)
		}
		i++
	}
}

// TestOffsetIsPersistedAndMonotonic — §5 «Offset long polling»: last_update_id is written after the
// update is processed, and a late (out-of-order) update must not downgrade it, because a downgrade
// makes Telegram hand the same update back after a restart (§13 риск «повторные ответы»).
func TestOffsetIsPersistedAndMonotonic(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, nil, nil, db)

	if err := dispatch(t, d, testEvent(100, 11, 41, "текст")); err != nil { // updateID = 1041
		t.Fatalf("Dispatch: %v", err)
	}
	var offset, ok, err = store.LastUpdateID(context.Background(), db)
	if err != nil || !ok || offset != 1041 {
		t.Fatalf("bot_state last_update_id must be 1041 (§5), got %d ok=%v err=%v", offset, ok, err)
	}

	if err := dispatch(t, d, testEvent(100, 11, 12, "старый текст")); err != nil { // updateID = 1012
		t.Fatalf("Dispatch of an older update: %v", err)
	}
	offset, _, err = store.LastUpdateID(context.Background(), db)
	if err != nil || offset != 1041 {
		t.Fatalf("an older update must not move the offset back (§5), got %d err=%v", offset, err)
	}
}

// TestTokenNeverLeaksIntoTheLog — §12 security: a library error carries
// `https://api.telegram.org/bot<TOKEN>/<method>`, so Sanitize is the only path from an error to slog.
func TestTokenNeverLeaksIntoTheLog(t *testing.T) {
	var token = "12345:test-token-not-a-secret"
	var err = errors.New("call sendMessage: 429 https://api.telegram.org/bot" + token + "/sendMessage")
	var text = Sanitize(err, token)
	if strings.Contains(text, token) {
		t.Fatalf("the token must never reach a log line (§12 security), got %q", text)
	}
	if !strings.Contains(text, "***") {
		t.Fatalf("the token must be replaced by a marker, got %q", text)
	}
}

// TestUpdatesWithoutMessageSurviveTheHandler — §12 надёжность, §6 п.7, §10 День 4. The MatchFunc claims
// only a Message with a non-empty Text, so everything else — callback_query, my_chat_member,
// edited_message, a photo, and the nil update that a default handler can be handed — arrives in
// telegramHandler unprotected, and it arrives AFTER getUpdates has already deleted it from the queue of
// Telegram: the library will not offer it again, and §5 last_update_id is written only after Dispatch.
// Measured 2026-10-31 on this machine, where the «carries no text» line read upd.Message.Chat.ID before
// eventOf had looked at Message: the first such update panicked in the goroutine of the library, the
// process died, and the update disappeared with no row in §5 and no log line to explain the silence —
// which is how an answered /ask turned into nothing at all. So: the panic must not escape the handler, the
// handler must not invent a row for an update §5 has no place for, and the offset must not move for an
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
		d.telegramHandler(context.Background(), nil, c.upd) // a panic that escapes here kills the process (§12)
		i++
	}

	var counts, countErr = store.TableCounts(context.Background(), db)
	if countErr != nil {
		t.Fatalf("store.TableCounts: %v", countErr)
	}
	var k = 0
	for k < len(counts) {
		if counts[k].Name != "schema_migrations" && counts[k].Rows != 0 {
			t.Fatalf("an update without a message must write nothing (§5 has no row for it): %s has %d rows", counts[k].Name, counts[k].Rows)
		}
		k++
	}
	var found = false
	var err error
	if _, found, err = store.LastUpdateID(context.Background(), db); err != nil || found {
		t.Fatalf("nothing was processed, so bot_state must stay empty (§5), found=%v err=%v", found, err)
	}
	if len(r.sent) != 0 {
		t.Fatalf("an update without a message must not be answered (§6 п.7), %d sends", len(r.sent))
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
	if rows["chats"] != 1 || rows["events"] != 1 || rows["messages"] != 2 {
		t.Fatalf("/ask through the same handler must behave as before the net (§6, §10 День 3): chats=%d events=%d messages=%d",
			rows["chats"], rows["events"], rows["messages"])
	}
	var offset int64
	if offset, found, err = store.LastUpdateID(context.Background(), db); err != nil || !found || offset != 24 {
		t.Fatalf("bot_state must hold the processed update 24 (§5), got %d found=%v err=%v", offset, found, err)
	}
	if len(r.sent) != 1 {
		t.Fatalf("/ask must answer once (§6), %d sends", len(r.sent))
	}
}
