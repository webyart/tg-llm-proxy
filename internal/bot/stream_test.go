// and the answer of the model that thinks for a minute before its first token must not leave the chat silent.
//
// The fake of the streaming model repeats the shape of model.OpenAI.Stream and nothing more: a PARTIAL Response
// travels WITH the error of a broken stream, because part of that text is already on the user's screen and
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
type streamStep struct {
	reasoning string
	content   string
	wait      time.Duration
}

// OPTIONAL capability: a test that wants the plain path passes a plain fakeModel, and a test that wants to prove
// the dispatcher did not stream counts streamCalls.
type fakeStreamModel struct {
	fakeModel
	steps       []streamStep
	fail        error // arrives after the steps, with the partial answer beside it
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
				return partial(errors.New("the fake streamed text into a handler with no Delta callback "))
			}
			if err := h.Delta(step.content); err != nil {
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

type streamReplier struct {
	fakeReplier
	mu           sync.Mutex
	posts        []string
	edits        []string
	parsed       []bool // the parse flag of every rewrite: the interim ones must be plain
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
	if messageID <= 0 || messageID > r.messageID {
		return fmt.Errorf("the edit went to unknown message %d, latest posted id is %d", messageID, r.messageID)
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

// no rewrite storm.
func streamConfig(t *testing.T, overrides map[string]string) config.Config {
	t.Helper()
	var raw = merge(map[string]string{
		"STREAMING_ENABLED":      "true",
		"TELEGRAM_ALLOWED_USERS": "11",
	}, overrides)
	return testConfig(t, raw)
}

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
		t.Fatalf("STREAMING_ENABLED=true must take the stream of once, got %d calls", m.streamed())
	}
	if r.postCount() != 1 {
		t.Fatalf(" posts ONE placeholder, got %d: %q", r.postCount(), r.postTexts())
	}
	if r.postTexts()[0] != StreamPlaceholder {
		t.Errorf("the first thing the chat sees must be the placeholder of, got %q", r.postTexts()[0])
	}
	// that exists is the final one deliver() makes to turn the placeholder into the answer.
	var edits = r.editTexts()
	if len(edits) != 1 {
		t.Fatalf("the cadence of must have refused every interim rewrite, got %d: %q", len(edits), edits)
	}
	var flags = r.parseFlags()
	if len(flags) != 1 || !flags[0] {
		t.Errorf("the final rewrite is the one that carries the parse mode of, got %v", flags)
	}
	if !strings.Contains(edits[0], "Раз. Два. Три. Четыре.") {
		t.Errorf("the finished answer must replace the placeholder, got %q", edits[0])
	}
	if !strings.Contains(edits[0], "⏱") {
		t.Errorf("the footer of travels with the answer, got %q", edits[0])
	}
	if r.count() != 0 {
		t.Fatalf(" must not send extra messages of UX for an answer that fits one message, Replier.Send got %d pieces", r.count())
	}

	var rows = assistantRows(t, db, 101)
	if len(rows) != 1 {
		t.Fatalf(": one assistant row per answer, got %d", len(rows))
	}
	if rows[0].Content != "Раз. Два. Три. Четыре." {
		t.Errorf("the assistant row of must hold the answer text alone, got %q", rows[0].Content)
	}
}

// BEFORE any answer text exists, and every interim rewrite must be plain text. MarkdownV2 over half an answer is
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
		t.Fatalf("a stream that thinks for a second of must rewrite the placeholder, got %d edits: %q", len(edits), edits)
	}
	if !strings.Contains(edits[0], "Модель думает") || !strings.Contains(edits[0], "символов размышлений") {
		t.Errorf("the chat must see that the model is thinking while no answer text exists, got %q", edits[0])
	}
	if m.progressed() < 2 {
		t.Fatalf("two reasoning events must arrive as StreamInfo of, Progress fired %d times", m.progressed())
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
		t.Errorf("a growing answer must say so with the counter of, got %q", edits)
	}

	var all = strings.Join(append(append([]string{}, r.postTexts()...), edits...), "\n")
	if strings.Contains(all, "анализирую") {
		t.Fatalf("the reasoning of must never reach the chat: %q", all)
	}
	var rows = assistantRows(t, db, 102)
	if len(rows) != 1 {
		t.Fatalf("one assistant row per streamed answer, got %d", len(rows))
	}
	if strings.Contains(rows[0].Content, "анализирую") {
		t.Errorf("the reasoning must never reach the assistant row of: %q", rows[0].Content)
	}

	// Every rewrite but the last is plain: the interim ones cannot be parsed as markup they do not close.
	var flags = r.parseFlags()
	if flags[len(flags)-1] != true {
		t.Errorf("the final rewrite must carry the parse mode of, got %v", flags)
	}
	var k = 0
	for k < len(flags)-1 {
		if flags[k] {
			t.Errorf("interim rewrite %d must be plain text , got %v", k, flags)
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

// two older ones («one failure described by two different numbers in the two places an operator compares») must
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
				fail:  fmt.Errorf("%w ", tc.sentinel),
			}
			var r = &streamReplier{}
			var db = testStoreNamed(t, tc.name)
			var d = newTestDispatcher(t, cfg, m, r, db)

			if err := dispatch(t, d, testEvent(103, 11, 2003, "/ask долго")); err != nil {
				t.Fatalf("/ask: %v", err)
			}

			var rows = assistantRows(t, db, 103)
			if len(rows) != 0 {
				t.Fatalf("a broken stream writes no assistant row of, got %d: %q", len(rows), rows[0].Content)
			}
			var timeouts, evErr = store.CountEvents(context.Background(), db, store.EventTimeout)
			if evErr != nil || timeouts != 1 {
				t.Fatalf("events(kind=%q) must be 1, got %d err=%v", store.EventTimeout, timeouts, evErr)
			}

			var texts = r.texts()
			var want = strconv.FormatInt(tc.seconds(cfg), 10)
			if len(texts) != 1 || !strings.Contains(texts[0], tc.row) || !strings.Contains(texts[0], want+" с") {
				t.Fatalf("the failure notice must name %s and its %s с, got %q", tc.row, want, texts)
			}

			// to still be writing an answer that already stopped.
			var last = r.lastEdit()
			if !strings.Contains(last, "прервано") {
				t.Fatalf("a half answer must be marked as broken, got %q", last)
			}
			if strings.Contains(last, "ответ пишется") {
				t.Errorf("a stopped stream must not keep promising an answer, got %q", last)
			}
			if Runes(last) > int(cfg.MaxMessageChunk) {
				t.Fatalf("the notice must fit one message of (%d symbols), got %d: %q", cfg.MaxMessageChunk, Runes(last), last)
			}
		})
		c++
	}
}

// message the chat watched grow keeps the first chunk and the rest arrives as the ordinary new messages of
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
			t.Fatalf("rewrite %d passes the limit of one message (%d symbols): %d — %q",
				i, cfg.MaxMessageChunk, Runes(everything[i]), everything[i])
		}
		i++
	}
	if r.editCount() == 0 {
		t.Fatalf("the placeholder must become the answer, no rewrite happened")
	}
	everything = append(everything, r.texts()...)
	var joined = strings.Join(everything, "\n")
	if !strings.Contains(joined, "Начало") || !strings.Contains(joined, "Конец.") {
		t.Fatalf("a streamed answer longer than one message must still reach the chat complete, got %q", joined)
	}
	// less than that the user gets it at all.
	if !strings.Contains(joined, "⏱") {
		t.Errorf("the streamed answer must keep the footer of, got %q", joined)
	}
	var rows = assistantRows(t, db, 104)
	if len(rows) != 1 {
		t.Fatalf("one assistant row per streamed answer however many messages the chat saw, got %d", len(rows))
	}
	if rows[0].Content != long {
		t.Errorf("the assistant row of must hold the WHOLE answer (%d symbols), got %d", Runes(long), Runes(rows[0].Content))
	}
}

// can stream, a sender that can edit. A missing one is not an error and not a half-measure — the answer goes the
func TestStreamNeedsEveryCapabilityOrNoneAtAll(t *testing.T) {
	var cases = []struct {
		name        string
		env         map[string]string
		useStreamer bool
		useSender   bool
	}{
		{"the row of is off", map[string]string{"STREAMING_ENABLED": "false"}, true, true},
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
				t.Fatalf("the plain path must not call Stream , got %d stream calls", streamModel.streamed())
			}
			if streamSender.postCount() != 0 {
				t.Fatalf("the plain path must not post a placeholder, got %d", streamSender.postCount())
			}
			var got = append(plainSender.texts(), streamSender.texts()...)
			if len(got) != 1 || !strings.Contains(got[0], "ответ") {
				t.Fatalf("the answer of must reach the chat as it always did, got %q", got)
			}
			if len(assistantRows(t, db, 107)) != 1 {
				t.Fatalf("the plain path writes its one assistant row of, got otherwise")
			}
		})
		c++
	}
}

// TestStreamThatCannotPostStillDeliversTheAnswer — a chat that refuses the placeholder (the bot was blocked, a
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
	var r = &streamReplier{postErr: errors.New("bot was blocked by the user ")}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(105, 11, 2005, "/ask вопрос")); err != nil {
		t.Fatalf("/ask: %v", err)
	}
	if r.postAttempts > deliveryFailureBudget {
		t.Fatalf("a chat that refuses the answer was asked %d times, the budget of is %d ",
			r.postAttempts, deliveryFailureBudget)
	}
	if r.postAttempts != deliveryFailureBudget {
		t.Fatalf("the session must use the whole budget of before going silent, got %d attempts", r.postAttempts)
	}
	if r.editCount() != 0 {
		t.Fatalf("nothing was posted, so nothing could be rewritten, got %d edits", r.editCount())
	}
	var texts = r.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "Раз. Два. Три. Четыре. Пять.") {
		t.Fatalf("the answer of a stream nothing could rewrite must still reach the chat the way does, got %q", texts)
	}
	var rows = assistantRows(t, db, 105)
	if len(rows) != 1 {
		t.Fatalf("a delivered answer writes exactly one assistant row of, got %d", len(rows))
	}
}

// goes by RUNES, so a Cyrillic answer is measured the way Telegram measures it. Two shapes of MAX_MESSAGE_CHUNK:
func TestStreamViewsFitOneMessage(t *testing.T) {
	var cases = []struct {
		name    string
		chunk   string
		markers []string
	}{
		{"the marker fits at 600 symbols", "600", []string{msgStreamCutMarker, "ответ пишется"}},
		{"the floor of", "64", []string{"ответ пишется"}},
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
				t.Fatalf("an interim rewrite must fit one message of (%d symbols), got %d: %q",
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
				t.Fatalf("the notice of a broken stream must fit one message of (%d symbols), got %d: %q",
					cfg.MaxMessageChunk, Runes(broken), broken)
			}
			if !strings.Contains(broken, "прервано") || !strings.Contains(broken, "в истории этого ответа нет") {
				t.Fatalf("the template of must survive the cut, got %q", broken)
			}
		})
		c++
	}
}

func TestStreamRotatesToNextMessageBeforeCompletion(t *testing.T) {
	cfg := streamConfig(t, map[string]string{"MAX_MESSAGE_CHUNK": "64", "PARSE_MODE": "MarkdownV2"})
	r := &streamReplier{}
	d := newTestDispatcher(t, cfg, nil, r, testStore(t))
	s := d.newStreamSession(context.Background(), 106, r)
	s.answer.WriteString(strings.Repeat("слово ", 30))

	s.refresh()
	s.refresh()
	if r.postCount() < 2 {
		t.Fatalf("overflow created %d messages before completion, want at least 2", r.postCount())
	}
	if len(r.texts()) != 0 {
		t.Fatalf("tail was bulk-sent through Replier.Send: %q", r.texts())
	}
	if err := s.deliver(context.Background(), d, s.answer.String()); err != nil {
		t.Fatalf("finalizing rotated messages: %v", err)
	}
	if r.editCount() < 2 {
		t.Fatalf("finalization edited only %d messages, want all existing parts", r.editCount())
	}
}
