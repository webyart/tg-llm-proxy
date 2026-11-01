// stream_test.go — the §15 «стриминг ответов» seen from the Telegram side (§16 change 34). The measurement of
// §14 sets what these tests insist on: ~48 SSE events per second, so the cadence of §7 is what protects the
// chat; an answer of 6034 runes does not fit one message, so the tail of §12 UX has to arrive as new messages;
// and the answer of the model that thinks for a minute before its first token must not leave the chat silent.
//
// The fake of the streaming model repeats the shape of model.OpenAI.Stream and nothing more: a PARTIAL Response
// travels WITH the error of a broken stream, because part of that text is already on the user's screen and
// §10 День 3 still forbids the assistant row of §5 for a call that failed.
package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
)

// streamStep — one SSE event of the fake: the reasoning the model produces while it thinks, the answer text it
// produces after that, and the pause before the event (the pause is what makes the cadence of §7 observable).
type streamStep struct {
	reasoning string
	content   string
	wait      time.Duration
}

// fakeStreamModel — fakeModel of §11 plus model.Streamer of §15. It is a separate type because Streamer is an
// OPTIONAL capability: a test that wants the plain path passes a plain fakeModel, and a test that wants to prove
// the dispatcher did not stream counts streamCalls.
type fakeStreamModel struct {
	fakeModel
	steps       []streamStep
	fail        error // arrives after the steps, with the partial answer beside it (§15)
	progresses  int
	streamCalls int
}

func (m *fakeStreamModel) Stream(ctx context.Context, req model.Request, h model.StreamHandler) (model.Response, error) {
	m.mu.Lock()
	m.calls++
	m.streamCalls++
	m.requests = append(m.requests, req)
	m.mu.Unlock()

	var start = time.Now()
	var answer strings.Builder
	var reasoning = 0

	var partial = func(err error) (model.Response, error) {
		var resp = m.response
		resp.Content = answer.String()
		resp.Model = "fake-model"
		resp.Latency = time.Since(start)
		resp.Attempts = 1
		return resp, err
	}

	var i = 0
	for i < len(m.steps) {
		var step = m.steps[i]
		if step.wait > 0 {
			select {
			case <-time.After(step.wait):
			case <-ctx.Done():
				return partial(ctx.Err())
			}
		}
		if step.reasoning != "" && h.Progress != nil {
			reasoning += Runes(step.reasoning)
			m.mu.Lock()
			m.progresses++
			m.mu.Unlock()
			h.Progress(model.StreamInfo{
				Model:          "fake-model",
				ReasoningRunes: reasoning,
				ContentRunes:   Runes(answer.String()),
				Elapsed:        time.Since(start),
			})
		}
		if step.content != "" {
			if h.Delta == nil {
				return partial(errors.New("the fake streamed text into a handler with no Delta callback (§15)"))
			}
			if err := h.Delta(step.content); err != nil {
				// The session of §15 stopped reading the stream: exactly what two failed posts are for.
				return partial(err)
			}
			answer.WriteString(step.content)
		}
		i++
	}
	if m.fail != nil {
		return partial(m.fail)
	}
	var resp, _ = partial(nil)
	return resp, nil
}

func (m *fakeStreamModel) streamed() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streamCalls
}

// progressed — how many times this fake pushed a StreamInfo of §15 through h.Progress. The placeholder of the
// session is driven by exactly that callback: an answer that thinks without emitting one content token (§14:
// ~200 events before the first token) still has to move the message of the chat.
func (m *fakeStreamModel) progressed() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.progresses
}

// Content — the answer this fake would have streamed, as one string.
func (m *fakeStreamModel) Content() string {
	var b strings.Builder
	var i = 0
	for i < len(m.steps) {
		b.WriteString(m.steps[i].content)
		i++
	}
	return b.String()
}

// Complete keeps the plain path of §10 runnable on the same fake — which matters for the test that proves the
// dispatcher did NOT stream: the fake can stream, the wiring decided not to, and the answer must still be there.
func (m *fakeStreamModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	var resp = m.response
	resp.Content = m.Content()
	resp.Model = "fake-model"
	resp.Latency = 5 * time.Millisecond
	resp.Attempts = 1
	if resp.Content == "" {
		return m.fakeModel.Complete(ctx, req)
	}
	if m.fail != nil {
		return resp, m.fail
	}
	return resp, nil
}

// streamReplier — the recording Replier of §11 plus the StreamSender of §15. It records the placeholder and
// every rewrite SEPARATELY from the ordinary Send, because the whole point of §15 is the difference between
// «one message that grew» and «a wall of messages» (§12 UX «не спамит»).
type streamReplier struct {
	fakeReplier
	mu           sync.Mutex
	posts        []string
	edits        []string
	parsed       []bool // the parse flag of every rewrite: the interim ones must be plain (§12 UX)
	messageID    int64
	postAttempts int
	editAttempts int
	postErr      error
	editErr      error
}

func (r *streamReplier) SendStreamMessage(ctx context.Context, chatID int64, text string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.postAttempts++
	if r.postErr != nil {
		return 0, r.postErr
	}
	r.posts = append(r.posts, text)
	r.messageID++
	return r.messageID, nil
}

func (r *streamReplier) EditStreamMessage(ctx context.Context, chatID int64, messageID int64, text string, parse bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.editAttempts++
	if r.editErr != nil {
		return r.editErr
	}
	if messageID != r.messageID || r.messageID == 0 {
		// §15 rewrites the message IT posted: an edit addressed to an id that was never posted is a session
		// that lost its own message id, and every rewrite of the chat would land nowhere.
		return fmt.Errorf("the edit went to message %d while the placeholder of this session is %d (§15)", messageID, r.messageID)
	}
	r.edits = append(r.edits, text)
	r.parsed = append(r.parsed, parse)
	return nil
}

func (r *streamReplier) postCount() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.posts) }
func (r *streamReplier) editCount() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.edits) }
func (r *streamReplier) postTexts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.posts...)
}
func (r *streamReplier) editTexts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.edits...)
}
func (r *streamReplier) parseFlags() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool{}, r.parsed...)
}
func (r *streamReplier) lastEdit() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.edits) == 0 {
		return ""
	}
	return r.edits[len(r.edits)-1]
}

// streamConfig — the §7 rows of §15 as the loader produces them, with the allowlist of §7 opened for the test
// user (the deny-by-default of §16 change 13 otherwise refuses /ask before the model is ever asked).
// STREAM_EDIT_INTERVAL_MS stays at the §7 default (5000 ms) unless a test passes an override: with the default,
// a stream whose events all arrive inside one second gets exactly the treatment §15 promises — one placeholder,
// no rewrite storm.
func streamConfig(t *testing.T, overrides map[string]string) config.Config {
	t.Helper()
	var raw = merge(map[string]string{
		"STREAMING_ENABLED":      "true",
		"TELEGRAM_ALLOWED_USERS": "11",
	}, overrides)
	return testConfig(t, raw)
}

// assistantRows — the assistant rows of §5 for one chat, oldest first. §15 rewrites one Telegram message over
// and over, and the invariant underneath it is the one of §10 День 3: exactly ONE row per answered question.
func assistantRows(t *testing.T, db *sql.DB, chatID int64) []store.Message {
	t.Helper()
	var rows, err = store.HistoryOfChat(context.Background(), db, chatID, 50)
	if err != nil {
		t.Fatalf("store.HistoryOfChat: %v", err)
	}
	var out = []store.Message{}
	var i = 0
	for i < len(rows) {
		if rows[i].Role == store.RoleAssistant {
			out = append(out, rows[i])
		}
		i++
	}
	return out
}

// TestStreamIsOneMessageNotAWall — the sentence of §15 «правка одного сообщения вместо одного sendMessage»,
// measured against the §14 fact that a model produces ~48 events per second. Every event of this stream arrives
// inside one second of the §7 default cadence, so the chat must see exactly one posted message and the rewrites
// that did not fit the interval must be counted as skipped, not performed (§10 День 4 flood).
func TestStreamIsOneMessageNotAWall(t *testing.T) {
	var cfg = streamConfig(t, nil)
	var m = &fakeStreamModel{steps: []streamStep{
		{reasoning: "Анализирую запрос."},
		{content: "Раз."},
		{content: " Два."},
		{content: " Три."},
		{content: " Четыре."},
	}}
	var r = &streamReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(101, 11, 2001, "/ask посчитай")); err != nil {
		t.Fatalf("/ask: %v", err)
	}

	if m.streamed() != 1 {
		t.Fatalf("STREAMING_ENABLED=true must take the stream of §15 once, got %d calls", m.streamed())
	}
	if r.postCount() != 1 {
		t.Fatalf("§15 posts ONE placeholder, got %d: %q", r.postCount(), r.postTexts())
	}
	if r.postTexts()[0] != StreamPlaceholder {
		t.Errorf("the first thing the chat sees must be the placeholder of §15, got %q", r.postTexts()[0])
	}
	// Four deltas inside one second of the §7 interval: none of them may rewrite the message. The single edit
	// that exists is the final one deliver() makes to turn the placeholder into the answer.
	var edits = r.editTexts()
	if len(edits) != 1 {
		t.Fatalf("the cadence of §7 must have refused every interim rewrite, got %d: %q", len(edits), edits)
	}
	var flags = r.parseFlags()
	if len(flags) != 1 || !flags[0] {
		t.Errorf("the final rewrite is the one that carries the parse mode of §7, got %v", flags)
	}
	if !strings.Contains(edits[0], "Раз. Два. Три. Четыре.") {
		t.Errorf("the finished answer must replace the placeholder, got %q", edits[0])
	}
	if !strings.Contains(edits[0], "⏱") {
		t.Errorf("the footer of §6 travels with the answer, got %q", edits[0])
	}
	if r.count() != 0 {
		t.Fatalf("§15 must not send extra messages of §12 UX for an answer that fits one message, Replier.Send got %d pieces", r.count())
	}

	// The invariant under the stream: §10 День 3 writes exactly one assistant row, after the final answer, and
	// its text is the answer alone — no placeholder, no footer, no reasoning of §15.
	var rows = assistantRows(t, db, 101)
	if len(rows) != 1 {
		t.Fatalf("§10 День 3: one assistant row per answer, got %d", len(rows))
	}
	if rows[0].Content != "Раз. Два. Три. Четыре." {
		t.Errorf("the assistant row of §5 must hold the answer text alone, got %q", rows[0].Content)
	}
}

// TestStreamThinkingKeepsChatAliveAndPlain — §12 UX «сообщение не зависает» and §15 in one test: the answer of
// §14 spent ~200 events thinking before its first content token, so the chat must be told the model is thinking
// BEFORE any answer text exists, and every interim rewrite must be plain text. MarkdownV2 over half an answer is
// a half-open entity, which is the 400 Telegram answers «can't parse entities» (§12 UX); the final rewrite is the
// one that puts the formatting back over complete, escaped text (reply.go, §16 change 7).
func TestStreamThinkingKeepsChatAliveAndPlain(t *testing.T) {
	var cfg = streamConfig(t, map[string]string{"STREAM_EDIT_INTERVAL_MS": "250"})
	var m = &fakeStreamModel{steps: []streamStep{
		{reasoning: "Сначала анализирую, что спрашивает пользователь."},
		{reasoning: "Потом подбираю формулировку ответа.", wait: 300 * time.Millisecond},
		{content: "Первая часть ответа.", wait: 300 * time.Millisecond},
		{content: " И вторая.", wait: 300 * time.Millisecond},
	}}
	var r = &streamReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(102, 11, 2002, "/ask расскажи")); err != nil {
		t.Fatalf("/ask: %v", err)
	}

	if r.postCount() != 1 {
		t.Fatalf("one placeholder per streamed answer, got %d", r.postCount())
	}
	var edits = r.editTexts()
	if len(edits) < 2 {
		t.Fatalf("a stream that thinks for a second of §7 must rewrite the placeholder, got %d edits: %q", len(edits), edits)
	}
	if !strings.Contains(edits[0], "Модель думает") || !strings.Contains(edits[0], "символов размышлений") {
		t.Errorf("the chat must see that the model is thinking while no answer text exists, got %q", edits[0])
	}
	// The only thing that can move the message before the first content token is the StreamInfo callback of §15:
	// reasoning is not text of the answer, and the message of a thinking model must not freeze (§12 UX).
	if m.progressed() < 2 {
		t.Fatalf("two reasoning events must arrive as StreamInfo of §15, Progress fired %d times", m.progressed())
	}
	var sawPartial = false
	var i = 0
	for i < len(edits) {
		if strings.Contains(edits[i], "ответ пишется") {
			sawPartial = true
		}
		i++
	}
	if !sawPartial {
		t.Errorf("a growing answer must say so with the counter of §15, got %q", edits)
	}

	// Reasoning stays out of the chat AND out of §5 (§15: the model streams delta.reasoning_content as a field of
	// its own, and the assistant row of §5 is made of `content` in both paths).
	var all = strings.Join(append(append([]string{}, r.postTexts()...), edits...), "\n")
	if strings.Contains(all, "анализирую") {
		t.Fatalf("the reasoning of §15 must never reach the chat: %q", all)
	}
	var rows = assistantRows(t, db, 102)
	if len(rows) != 1 {
		t.Fatalf("one assistant row per streamed answer, got %d", len(rows))
	}
	if strings.Contains(rows[0].Content, "анализирую") {
		t.Errorf("the reasoning must never reach the assistant row of §5: %q", rows[0].Content)
	}

	// Every rewrite but the last is plain: the interim ones cannot be parsed as markup they do not close.
	var flags = r.parseFlags()
	if flags[len(flags)-1] != true {
		t.Errorf("the final rewrite must carry the parse mode of §7, got %v", flags)
	}
	var k = 0
	for k < len(flags)-1 {
		if flags[k] {
			t.Errorf("interim rewrite %d must be plain text (§12 UX half-open entities), got %v", k, flags)
		}
		k++
	}
}

func TestStreamTotalBudgetOutlivesTheOrdinaryReplyBudget(t *testing.T) {
	var cfg = streamConfig(t, map[string]string{
		"STRATA_TIMEOUT_SECONDS":        "1",
		"TG_REPLY_DEADLINE_SECONDS":     "2",
		"STREAM_IDLE_TIMEOUT_SECONDS":   "1",
		"STREAM_TOTAL_DEADLINE_SECONDS": "10",
	})
	var m = &fakeStreamModel{steps: []streamStep{{content: "Ответ после двух секунд.", wait: 2200 * time.Millisecond}}}
	var r = &streamReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(108, 11, 2008, "/ask подожди")); err != nil {
		t.Fatalf("/ask: %v", err)
	}
	var rows = assistantRows(t, db, 108)
	if len(rows) != 1 || rows[0].Content != m.Content() {
		t.Fatalf("the stream must outlive TG_REPLY_DEADLINE_SECONDS and store its answer, got %v", rows)
	}
	var last = r.lastEdit()
	if !strings.Contains(last, m.Content()) {
		t.Errorf("the final edit does not contain the delayed answer: %q", last)
	}
}

// TestStreamBudgetsNameThemselves — §15 gave a stream two budgets of its own, and the trap §12 measured for the
// two older ones («one failure described by two different numbers in the two places an operator compares») must
// not come back: the notice in the chat, the events row of §5 and the message the chat is already watching must
// name the SAME row of §7. A stream that went silent and an answer that simply ran long are different failures,
// and neither of them is STRATA_TIMEOUT_SECONDS.
func TestStreamBudgetsNameThemselves(t *testing.T) {
	var cases = []struct {
		name     string
		sentinel error
		row      string
		seconds  func(cfg config.Config) int64
	}{
		{"stalled", model.ErrStreamStalled, "STREAM_IDLE_TIMEOUT_SECONDS", func(cfg config.Config) int64 { return cfg.StreamIdleSeconds }},
		{"too-long", model.ErrStreamTooLong, "STREAM_TOTAL_DEADLINE_SECONDS", func(cfg config.Config) int64 { return cfg.StreamTotalSeconds }},
	}
	var c = 0
	for c < len(cases) {
		var tc = cases[c]
		t.Run(tc.name, func(t *testing.T) {
			var cfg = streamConfig(t, nil)
			var m = &fakeStreamModel{
				steps: []streamStep{{reasoning: "модель думает…"}, {content: "Начало ответа, которое пользователь уже читает."}},
				// The real client of §15 sends the partial answer WITH this error: part of the text is already
				// on the screen, and the error says which budget of §7 gave up.
				fail: fmt.Errorf("%w (the stream already delivered text, a second attempt would repeat it — §15)", tc.sentinel),
			}
			var r = &streamReplier{}
			var db = testStoreNamed(t, tc.name)
			var d = newTestDispatcher(t, cfg, m, r, db)

			if err := dispatch(t, d, testEvent(103, 11, 2003, "/ask долго")); err != nil {
				t.Fatalf("/ask: %v", err)
			}

			// §10 День 3: a call that failed writes NO assistant row — a half answer must not become the
			// context of the next question of §5.
			var rows = assistantRows(t, db, 103)
			if len(rows) != 0 {
				t.Fatalf("a broken stream writes no assistant row of §10 День 3, got %d: %q", len(rows), rows[0].Content)
			}
			var timeouts, evErr = store.CountEvents(context.Background(), db, store.EventTimeout)
			if evErr != nil || timeouts != 1 {
				t.Fatalf("events(kind=%q) must be 1, got %d err=%v", store.EventTimeout, timeouts, evErr)
			}

			// The notice of §12 UX names the budget that expired, with the number of that row of §7.
			var texts = r.texts()
			var want = strconv.FormatInt(tc.seconds(cfg), 10)
			if len(texts) != 1 || !strings.Contains(texts[0], tc.row) || !strings.Contains(texts[0], want+" с") {
				t.Fatalf("the failure notice must name %s and its %s с, got %q", tc.row, want, texts)
			}

			// The message the chat is watching says it is unfinished, fits ONE message of §7, and does not claim
			// to still be writing an answer that already stopped.
			var last = r.lastEdit()
			if !strings.Contains(last, "прервано") {
				t.Fatalf("a half answer must be marked as broken, got %q", last)
			}
			if strings.Contains(last, "ответ пишется") {
				t.Errorf("a stopped stream must not keep promising an answer, got %q", last)
			}
			if Runes(last) > int(cfg.MaxMessageChunk) {
				t.Fatalf("the notice must fit one message of §7 (%d symbols), got %d: %q", cfg.MaxMessageChunk, Runes(last), last)
			}
		})
		c++
	}
}

// TestLongStreamedAnswerArrivesComplete — the 6034-rune answer measured on §14 fits no single message of §7. The
// message the chat watched grow keeps the first chunk and the rest arrives as the ordinary new messages of
// §12 UX; no rewrite may pass the limit of one message, because Telegram answers «too long» and the user is left
// with the placeholder. MAX_MESSAGE_CHUNK is set to its §7 floor of 64 symbols to make the shape obvious.
func TestLongStreamedAnswerArrivesComplete(t *testing.T) {
	var cfg = streamConfig(t, map[string]string{"MAX_MESSAGE_CHUNK": "64"})
	var long = "Начало " + strings.Repeat("символ ", 40) + " Конец."
	var m = &fakeStreamModel{steps: []streamStep{{content: long}}}
	var r = &streamReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(104, 11, 2004, "/ask длинно")); err != nil {
		t.Fatalf("/ask: %v", err)
	}

	var everything = append(r.postTexts(), r.editTexts()...)
	var i = 0
	for i < len(everything) {
		if Runes(everything[i]) > int(cfg.MaxMessageChunk) {
			t.Fatalf("rewrite %d passes the §7 limit of one message (%d symbols): %d — %q",
				i, cfg.MaxMessageChunk, Runes(everything[i]), everything[i])
		}
		i++
	}
	if r.editCount() == 0 {
		t.Fatalf("the placeholder must become the answer, no rewrite happened")
	}
	var last = r.lastEdit()
	var tail = r.texts()
	if len(tail) < 1 {
		t.Fatalf("the part of the answer past MAX_MESSAGE_CHUNK must arrive as new messages of §12 UX, got %d", len(tail))
	}
	var j = 0
	for j < len(tail) {
		if Runes(tail[j]) > int(cfg.MaxMessageChunk) {
			t.Fatalf("a tail message of §12 UX passes the §7 limit: %d symbols", Runes(tail[j]))
		}
		j++
	}
	var joined = last + "\n" + strings.Join(tail, "\n")
	if !strings.Contains(joined, "Начало") || !strings.Contains(joined, "Конец.") {
		t.Fatalf("a streamed answer longer than one message must still reach the chat complete, got %q", joined)
	}
	// The footer of §6 travels with the answer; which piece the chunking of §16 change 4 put it into matters
	// less than that the user gets it at all.
	if !strings.Contains(joined, "⏱") {
		t.Errorf("the streamed answer must keep the footer of §6, got %q", joined)
	}
	// The chat saw several messages; §5 saw one answer — the whole of it, not the head the placeholder carried.
	var rows = assistantRows(t, db, 104)
	if len(rows) != 1 {
		t.Fatalf("one assistant row per streamed answer however many messages the chat saw, got %d", len(rows))
	}
	if rows[0].Content != long {
		t.Errorf("the assistant row of §5 must hold the WHOLE answer (%d symbols), got %d", Runes(long), Runes(rows[0].Content))
	}
}

// TestStreamNeedsEveryCapabilityOrNoneAtAll — §15 is optional in three places: the row of §7, a model client that
// can stream, a sender that can edit. A missing one is not an error and not a half-measure — the answer goes the
// way §10 День 3 always sent it, with the same one assistant row of §5. This is what makes the feature shippable
// behind a row of §7: nothing in the bot has to be ready for it all at once.
func TestStreamNeedsEveryCapabilityOrNoneAtAll(t *testing.T) {
	var cases = []struct {
		name        string
		env         map[string]string
		useStreamer bool
		useSender   bool
	}{
		{"the row of §7 is off", map[string]string{"STREAMING_ENABLED": "false"}, true, true},
		{"the model cannot stream", nil, false, true},
		{"the sender cannot edit", nil, true, false},
	}
	var c = 0
	for c < len(cases) {
		var tc = cases[c]
		t.Run(tc.name, func(t *testing.T) {
			var cfg = streamConfig(t, tc.env)
			var streamModel = &fakeStreamModel{steps: []streamStep{{content: "ответ"}}}
			var plainModel = &fakeModel{response: model.Response{Content: "ответ"}}
			var m model.Client = plainModel
			if tc.useStreamer {
				m = streamModel
			}
			var streamSender = &streamReplier{}
			var plainSender = &fakeReplier{}
			var replier Replier = plainSender
			if tc.useSender {
				replier = streamSender
			}
			var db = testStore(t)
			var d = newTestDispatcher(t, cfg, m, replier, db)

			if err := dispatch(t, d, testEvent(107, 11, 2007, "/ask вопрос")); err != nil {
				t.Fatalf("/ask: %v", err)
			}
			if streamModel.streamed() != 0 {
				t.Fatalf("the plain path must not call Stream (§15), got %d stream calls", streamModel.streamed())
			}
			if streamSender.postCount() != 0 {
				t.Fatalf("the plain path must not post a placeholder, got %d", streamSender.postCount())
			}
			var got = append(plainSender.texts(), streamSender.texts()...)
			if len(got) != 1 || !strings.Contains(got[0], "ответ") {
				t.Fatalf("the answer of §10 День 3 must reach the chat as it always did, got %q", got)
			}
			if len(assistantRows(t, db, 107)) != 1 {
				t.Fatalf("the plain path writes its one assistant row of §10 День 3, got otherwise")
			}
		})
		c++
	}
}

// TestStreamThatCannotPostStillDeliversTheAnswer — a chat that refuses the placeholder (the bot was blocked, a
// flood of §10 День 4, a 400) must not cost the answer the GPU already wrote. The session tries a bounded number
// of times, goes silent about the growing message — the ~48 events per second of §14 must not become 48 failing
// Bot API calls per second — and the finished text leaves through the ordinary path of §10 День 3.
func TestStreamThatCannotPostStillDeliversTheAnswer(t *testing.T) {
	var cfg = streamConfig(t, map[string]string{"STREAM_EDIT_INTERVAL_MS": "250"})
	var m = &fakeStreamModel{steps: []streamStep{
		{reasoning: "модель думает…"},
		{content: "Раз."},
		{content: " Два."},
		{content: " Три."},
		{content: " Четыре."},
		{content: " Пять."},
	}}
	var r = &streamReplier{postErr: errors.New("bot was blocked by the user (§12: 403)")}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(105, 11, 2005, "/ask вопрос")); err != nil {
		t.Fatalf("/ask: %v", err)
	}
	if r.postAttempts > deliveryFailureBudget {
		t.Fatalf("a chat that refuses the answer was asked %d times, the budget of §15 is %d (§10 День 4 flood)",
			r.postAttempts, deliveryFailureBudget)
	}
	if r.postAttempts != deliveryFailureBudget {
		t.Fatalf("the session must use the whole budget of §15 before going silent, got %d attempts", r.postAttempts)
	}
	if r.editCount() != 0 {
		t.Fatalf("nothing was posted, so nothing could be rewritten, got %d edits", r.editCount())
	}
	var texts = r.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "Раз. Два. Три. Четыре. Пять.") {
		t.Fatalf("the answer of a stream nothing could rewrite must still reach the chat the way §10 День 3 does, got %q", texts)
	}
	// The model call itself succeeded — the failure was only in how it arrives — so §10 День 3 writes its row.
	var rows = assistantRows(t, db, 105)
	if len(rows) != 1 {
		t.Fatalf("a delivered answer writes exactly one assistant row of §5, got %d", len(rows))
	}
}

// TestStreamViewsFitOneMessage — an interim rewrite must never pass the limit of one message (§12 UX: over the
// limit Telegram answers «too long», and the chat is left with the placeholder forever). §16 change 7: the cut
// goes by RUNES, so a Cyrillic answer is measured the way Telegram measures it. Two shapes of MAX_MESSAGE_CHUNK:
// the §7 default, where both notices and a head fit, and the §7 floor of 64 symbols, where they do not and the
// session has to choose what to keep — the counter of §15, not the note about where the rest goes.
func TestStreamViewsFitOneMessage(t *testing.T) {
	var cases = []struct {
		name    string
		chunk   string
		markers []string
	}{
		{"the marker fits at 600 symbols", "600", []string{msgStreamCutMarker, "ответ пишется"}},
		{"the floor of §7", "64", []string{"ответ пишется"}},
	}
	var c = 0
	for c < len(cases) {
		var tc = cases[c]
		t.Run(tc.name, func(t *testing.T) {
			var cfg = streamConfig(t, map[string]string{"MAX_MESSAGE_CHUNK": tc.chunk})
			var d = newTestDispatcher(t, cfg, nil, nil, nil)
			var s = d.newStreamSession(context.Background(), 106, &streamReplier{})
			s.answer.WriteString(strings.Repeat("Кириллица и пробелы. ", 30))

			var view = s.partialView()
			if Runes(view) > int(cfg.MaxMessageChunk) {
				t.Fatalf("an interim rewrite must fit one message of §7 (%d symbols), got %d: %q",
					cfg.MaxMessageChunk, Runes(view), view)
			}
			var m = 0
			for m < len(tc.markers) {
				if !strings.Contains(view, tc.markers[m]) {
					t.Fatalf("a long answer must keep %q (it is still being written, the rest follows), got %q", tc.markers[m], view)
				}
				m++
			}

			var broken = s.brokenView("STRATA не ответил: dial tcp 127.0.0.1:8080: connectex failed (STREAM_IDLE_TIMEOUT_SECONDS)")
			if Runes(broken) > int(cfg.MaxMessageChunk) {
				t.Fatalf("the notice of a broken stream must fit one message of §7 (%d symbols), got %d: %q",
					cfg.MaxMessageChunk, Runes(broken), broken)
			}
			// What gets cut is the reason of §12, never the sentence that tells the user this is not an answer.
			if !strings.Contains(broken, "прервано") || !strings.Contains(broken, "в истории этого ответа нет") {
				t.Fatalf("the template of §15 must survive the cut, got %q", broken)
			}
		})
		c++
	}
}
