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
	var cfg = testConfig(t, map[string]string{"ALLOWED_USER_IDS": "11"})
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
	var cfg = testConfig(t, map[string]string{"ALLOWED_USER_IDS": "11"})
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

// TestClosedBotRefusesAndAudits — §7 deny-by-default + §10 День 4 DoD («чужой user id получает отказ и
// попадает в events(kind='denied')»), with §16 change 13: /start and /whoami still answer, otherwise a
// closed bot could never reveal the id needed to open it.
func TestClosedBotRefusesAndAudits(t *testing.T) {
	var cfg = testConfig(t, nil) // ALLOWED_USER_IDS empty → DenyByDefault (§7)
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

// TestRateLimitRefusesAndAudits — §7 RATE_LIMIT_PER_MINUTE, §12 reliability: the refusal says how long
// to wait and lands in events(kind='limit'), and the model is never reached (§12 protects the GPU).
func TestRateLimitRefusesAndAudits(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"ALLOWED_USER_IDS": "11", "RATE_LIMIT_PER_MINUTE": "2"})
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
	var cfg = testConfig(t, map[string]string{"ALLOWED_USER_IDS": "11"})
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
	var cfg = testConfig(t, map[string]string{"ALLOWED_USER_IDS": "11"})
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
	var allowed = map[string]string{"ALLOWED_USER_IDS": "11"}

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
	var cfg = testConfig(t, map[string]string{"ALLOWED_USER_IDS": "11"})
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
	var cfg = testConfig(t, map[string]string{"ALLOWED_USER_IDS": "11"})
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
