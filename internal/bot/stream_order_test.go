package bot

import (
	"context"
	"sync"
	"testing"
)

// orderReplier is a StreamSender that records the growing placeholder, the edits and deletions, so a test can
// tell "the answer replaced its placeholder" from "the answer arrived as a new message".
type orderReplier struct {
	*fakeReplier
	mu      sync.Mutex
	stream  int64
	edits   []string
	removed []int64
}

func (r *orderReplier) SendStreamMessage(ctx context.Context, chatID int64, text string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stream++
	return r.stream, nil
}

func (r *orderReplier) EditStreamMessage(ctx context.Context, chatID int64, messageID int64, text string, parse bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.edits = append(r.edits, text)
	return nil
}

func (r *orderReplier) RemoveMenu(ctx context.Context, chatID int64, messageID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, messageID)
	return nil
}

// TestAnswerAfterANewerQuestionIsSentAsANewMessage — the incident: a placeholder created for question A stays
// above question B, and editing it at the end puts A's answer above B. When a newer update was admitted during
// the stream, the answer must be a new message (below B) and the placeholder must be dropped.
func TestAnswerAfterANewerQuestionIsSentAsANewMessage(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "true"})
	var replier = &orderReplier{fakeReplier: &fakeReplier{}}
	var d = newTestDispatcher(t, cfg, &fakeModel{}, replier, nil)
	var ctx = context.Background()

	// The first question was admitted (turn 1); the session starts there.
	d.noteChatTurn(500)
	var session = d.newStreamSession(ctx, 500, replier)
	session.posted = true
	session.messageID = 77
	session.messageIDs = []int64{77}

	// The next question is admitted while the first answer is still being produced.
	d.noteChatTurn(500)

	if err := session.deliver(ctx, d, "ответ на первый вопрос"); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	replier.mu.Lock()
	var edits, removed, sends = len(replier.edits), replier.removed, replier.sent
	replier.mu.Unlock()

	if edits != 0 {
		t.Fatalf("the answer was edited into a placeholder above the newer question (%d edits)", edits)
	}
	if len(removed) != 1 || removed[0] != 77 {
		t.Fatalf("the placeholder was not removed: %v", removed)
	}
	if len(sends) == 0 || sends[len(sends)-1].text != "ответ на первый вопрос" {
		t.Fatalf("the answer did not leave as a new message: %+v", sends)
	}
}

// TestAnswerWithoutANewerQuestionStillEditsItsPlaceholder — without a newer update nothing changes: the answer
// replaces the placeholder it grew, which is the classic streaming path.
func TestAnswerWithoutANewerQuestionStillEditsItsPlaceholder(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "true"})
	var replier = &orderReplier{fakeReplier: &fakeReplier{}}
	var d = newTestDispatcher(t, cfg, &fakeModel{}, replier, nil)
	var ctx = context.Background()

	d.noteChatTurn(501)
	var session = d.newStreamSession(ctx, 501, replier)
	session.posted = true
	session.messageID = 88
	session.messageIDs = []int64{88}

	if err := session.deliver(ctx, d, "ответ"); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	replier.mu.Lock()
	var edits, removed, sends = replier.edits, replier.removed, replier.sent
	replier.mu.Unlock()

	if len(edits) != 1 || edits[0] != "ответ" {
		t.Fatalf("the placeholder was not edited into the answer: %v", edits)
	}
	if len(removed) != 0 {
		t.Fatalf("a placeholder was removed without a newer question: %v", removed)
	}
	for _, sent := range sends {
		if sent.text != StreamPlaceholder {
			t.Fatalf("the answer left as a new message: %+v", sends)
		}
	}
}
