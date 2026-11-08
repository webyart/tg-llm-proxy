package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// draftUpdate — one SendDraft call of the fake: the chat, the id shared by every update of one streamed
// answer, and the interim text the user saw.
type draftUpdate struct {
	chatID  int64
	draftID string
	text    string
}

// draftReplier — the ordinary streaming fake plus the optional SendDraft capability. Embedding the pointer
// keeps every edit-path assertion (posts, edits, sends) of streamReplier.
type draftReplier struct {
	*streamReplier
	mu       sync.Mutex
	drafts   []draftUpdate
	attempts int
	draftErr error
}

func (r *draftReplier) SendDraft(ctx context.Context, chatID int64, draftID, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if r.draftErr != nil {
		return r.draftErr
	}
	r.drafts = append(r.drafts, draftUpdate{chatID: chatID, draftID: draftID, text: text})
	return nil
}

func (r *draftReplier) draftAttempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

func (r *draftReplier) draftCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.drafts)
}

func (r *draftReplier) draftTexts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out = make([]string, 0, len(r.drafts))
	var i = 0
	for i < len(r.drafts) {
		out = append(out, r.drafts[i].text)
		i++
	}
	return out
}

func (r *draftReplier) draftIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out = make([]string, 0, len(r.drafts))
	var i = 0
	for i < len(r.drafts) {
		out = append(out, r.drafts[i].draftID)
		i++
	}
	return out
}

// TestStreamPrivateChatUsesDrafts — a private chat whose sender speaks sendMessageDraft sees the growing
// answer as drafts and receives the finished answer as exactly one ordinary message: no placeholder, no
// editMessageText.
func TestStreamPrivateChatUsesDrafts(t *testing.T) {
	var cfg = streamConfig(t, map[string]string{"STREAM_EDIT_INTERVAL_MS": "250", "STREAM_DRAFTS_ENABLED": "true"})
	var m = &fakeStreamModel{steps: []streamStep{
		{reasoning: "Думаю.", wait: 300 * time.Millisecond},
		{content: "Раз.", wait: 300 * time.Millisecond},
		{content: " Два.", wait: 300 * time.Millisecond},
		{content: " Три.", wait: 300 * time.Millisecond},
	}}
	var r = &draftReplier{streamReplier: &streamReplier{}}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))

	if err := dispatch(t, d, testEvent(101, 11, 2101, "/ask посчитай")); err != nil {
		t.Fatalf("/ask: %v", err)
	}

	if got := r.draftCount(); got < 2 {
		t.Fatalf("a private chat with draft support must stream through drafts, got %d", got)
	}
	if got := r.postCount(); got != 0 {
		t.Fatalf("a draft stream must not post a placeholder, got %d: %q", got, r.postTexts())
	}
	if got := r.editCount(); got != 0 {
		t.Fatalf("a draft stream must not edit messages, got %d: %q", got, r.editTexts())
	}
	var sends = r.texts()
	if len(sends) != 1 {
		t.Fatalf("the finished short answer must be exactly one sendMessage, got %d: %q", len(sends), sends)
	}
	if !strings.Contains(sends[0], m.Content()) {
		t.Errorf("the final message must carry the whole model answer %q, got %q", m.Content(), sends[0])
	}
	if !strings.Contains(sends[0], "⏱") {
		t.Errorf("the final message keeps the footer of the ordinary reply, got %q", sends[0])
	}

	var ids = r.draftIDs()
	var i = 0
	for i < len(ids) {
		if ids[i] == "" || ids[i] != ids[0] {
			t.Fatalf("draft %d id = %q: every update of one answer must share one non-empty draft id %q", i, ids[i], ids[0])
		}
		i++
	}
	var drafts = r.draftTexts()
	if strings.Contains(drafts[len(drafts)-1], "⏱") {
		t.Errorf("an interim draft must not carry the final footer, got %q", drafts[len(drafts)-1])
	}
}

// TestStreamDraftFinalAnswerIsCompleteAndChunked — the draft carries one message worth of the growing
// answer; the finished answer is still split by MAX_MESSAGE_CHUNK and arrives whole through Send.
func TestStreamDraftFinalAnswerIsCompleteAndChunked(t *testing.T) {
	var cfg = streamConfig(t, map[string]string{
		"MAX_MESSAGE_CHUNK":       "64",
		"STREAM_EDIT_INTERVAL_MS": "250",
		"STREAM_DRAFTS_ENABLED":   "true",
	})
	var first = strings.Repeat("символ ", 10)
	var answer = first + strings.Repeat("слово ", 30) + "Конец."
	var m = &fakeStreamModel{steps: []streamStep{
		{content: first, wait: 300 * time.Millisecond},
		{content: strings.Repeat("слово ", 30) + "Конец.", wait: 300 * time.Millisecond},
	}}
	var r = &draftReplier{streamReplier: &streamReplier{}}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))

	if err := dispatch(t, d, testEvent(102, 11, 2102, "/ask длинно")); err != nil {
		t.Fatalf("/ask: %v", err)
	}

	if got := r.draftCount(); got < 2 {
		t.Fatalf("a long answer must keep the drafts coming, got %d", got)
	}
	var drafts = r.draftTexts()
	var i = 0
	for i < len(drafts) {
		if Runes(drafts[i]) > int(cfg.MaxMessageChunk) {
			t.Fatalf("draft %d has %d runes, one message holds %d: %q", i, Runes(drafts[i]), cfg.MaxMessageChunk, drafts[i])
		}
		i++
	}
	var sends = r.texts()
	if len(sends) < 2 {
		t.Fatalf("an answer of %d runes must be chunked, got %d sendMessage calls: %q", Runes(answer), len(sends), sends)
	}
	var joined = strings.Join(sends, "")
	if !strings.Contains(joined, answer) {
		t.Fatalf("the chunked final message set dropped part of the answer: got %q, want %q inside", joined, answer)
	}
	var k = 0
	for k < len(sends) {
		if Runes(sends[k]) > int(cfg.MaxMessageChunk) {
			t.Fatalf("final chunk %d has %d runes, limit %d: %q", k, Runes(sends[k]), cfg.MaxMessageChunk, sends[k])
		}
		k++
	}
	if got := len(d.chunksOf(joined)); got != len(sends) {
		t.Errorf("the final set was not split by the ordinary chunking: %d chunks, %d messages", got, len(sends))
	}
}

// TestStreamGroupChatNeverDrafts — groups and channels have negative chat ids, where Bot API drafts do not
// exist: the answer keeps the placeholder-and-edits path.
func TestStreamGroupChatNeverDrafts(t *testing.T) {
	var cfg = streamConfig(t, map[string]string{"STREAM_EDIT_INTERVAL_MS": "250", "STREAM_DRAFTS_ENABLED": "true"})
	var m = &fakeStreamModel{steps: []streamStep{
		{content: "Раз.", wait: 300 * time.Millisecond},
		{content: " Два.", wait: 300 * time.Millisecond},
		{content: " Три.", wait: 300 * time.Millisecond},
	}}
	var r = &draftReplier{streamReplier: &streamReplier{}}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))
	var ev = testEvent(-100500, 11, 2103, "/ask привет")
	ev.ChatType = "supergroup"

	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("/ask: %v", err)
	}

	if got := r.draftAttempts(); got != 0 {
		t.Fatalf("a group chat must never send a draft, got %d attempts", got)
	}
	if got := r.postCount(); got != 1 {
		t.Fatalf("a group chat posts exactly one placeholder, got %d: %q", got, r.postTexts())
	}
	if r.editCount() == 0 {
		t.Fatal("the group placeholder was never edited into the answer")
	}
	var last = r.lastEdit()
	if !strings.Contains(last, m.Content()) || !strings.Contains(last, "⏱") {
		t.Fatalf("the group answer must be finalized in the placeholder, got %q", last)
	}
}

// TestStreamDraftFailureFallsBackToEdits — a Bot API that refuses drafts must lose nothing: the first
// refusal posts a real placeholder and the rest of the stream continues as edits; drafts are not retried.
func TestStreamDraftFailureFallsBackToEdits(t *testing.T) {
	var cfg = streamConfig(t, map[string]string{"STREAM_EDIT_INTERVAL_MS": "250", "STREAM_DRAFTS_ENABLED": "true"})
	var m = &fakeStreamModel{steps: []streamStep{
		{reasoning: "Думаю.", wait: 300 * time.Millisecond},
		{content: "Раз.", wait: 300 * time.Millisecond},
		{content: " Два.", wait: 300 * time.Millisecond},
		{content: " Три.", wait: 300 * time.Millisecond},
	}}
	var r = &draftReplier{
		streamReplier: &streamReplier{},
		draftErr:      errors.New("Bad Request: drafts are not supported"),
	}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))

	if err := dispatch(t, d, testEvent(104, 11, 2104, "/ask вопрос")); err != nil {
		t.Fatalf("/ask: %v", err)
	}

	if got := r.draftAttempts(); got != 1 {
		t.Fatalf("the first refused draft must switch the session to edits exactly once, got %d attempts", got)
	}
	if got := r.draftCount(); got != 0 {
		t.Fatalf("a refused draft must not be recorded as delivered, got %d", got)
	}
	if got := r.postCount(); got != 1 {
		t.Fatalf("the fallback posts exactly one real placeholder, got %d: %q", got, r.postTexts())
	}
	if r.editCount() == 0 {
		t.Fatal("the fallback never edited the placeholder")
	}
	var last = r.lastEdit()
	if !strings.Contains(last, m.Content()) {
		t.Fatalf("the answer must survive the draft failure, got %q", last)
	}
	if !strings.Contains(last, "⏱") {
		t.Errorf("the fallback answer keeps the footer, got %q", last)
	}
	if got := r.count(); got != 0 {
		t.Fatalf("a short answer finalized in the placeholder must not send extra messages, got %q", r.texts())
	}
}

// TestMiniAppStreamSendsNoDrafts — the Mini App path streams over SSE and has no Telegram message to draft:
// its sender does not implement SendDraft, even when the configured replier does.
func TestMiniAppStreamSendsNoDrafts(t *testing.T) {
	var cfg = streamConfig(t, nil)
	var m = &fakeStreamModel{steps: []streamStep{
		{content: "При"},
		{content: "вет"},
	}}
	var r = &draftReplier{streamReplier: &streamReplier{}}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))

	var log = &miniAppEventLog{}
	if err := d.AnswerForMiniApp(context.Background(), 11, 11, "вопрос", log.emit); err != nil {
		t.Fatalf("AnswerForMiniApp: %v", err)
	}
	if got := r.draftAttempts(); got != 0 {
		t.Fatalf("the Mini App path sent %d drafts", got)
	}
	if got := r.postCount(); got != 0 {
		t.Fatalf("the Mini App path posted %d Telegram placeholders", got)
	}
	if got := r.count(); got != 0 {
		t.Fatalf("the Mini App path sent %d Telegram messages: %q", got, r.texts())
	}
	if deltas := log.deltas(); strings.Join(deltas, "") != "Привет" {
		t.Fatalf("the Mini App deltas = %q, want %q", deltas, "Привет")
	}
}
