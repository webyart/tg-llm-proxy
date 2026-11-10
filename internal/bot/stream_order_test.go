package bot

import (
	"context"
	"errors"
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
	deleted []int64
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

func (r *orderReplier) DeleteMessage(ctx context.Context, chatID int64, messageID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, messageID)
	return nil
}

// TestAnswerAfterANewerQuestionIsSentAsANewMessage — the incident: a placeholder created for question A stays
// above question B, and editing it at the end puts A's answer above B. When a newer update was admitted during
// the stream, the answer must be a new message (below B) and the placeholder must be DELETED, not merely
// stripped of its keyboard: the truncated interim text would otherwise stay above the answer.
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
	var edits, deleted, sends = len(replier.edits), replier.deleted, replier.sent
	replier.mu.Unlock()

	if edits != 0 {
		t.Fatalf("the answer was edited into a placeholder above the newer question (%d edits)", edits)
	}
	if len(deleted) != 1 || deleted[0] != 77 {
		t.Fatalf("the placeholder was not deleted: %v", deleted)
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
	var edits, deleted, sends = replier.edits, replier.deleted, replier.sent
	replier.mu.Unlock()

	if len(edits) != 1 || edits[0] != "ответ" {
		t.Fatalf("the placeholder was not edited into the answer: %v", edits)
	}
	if len(deleted) != 0 {
		t.Fatalf("a placeholder was deleted without a newer question: %v", deleted)
	}
	for _, sent := range sends {
		if sent.text != StreamPlaceholder {
			t.Fatalf("the answer left as a new message: %+v", sends)
		}
	}
}

// fallbackReplier — a StreamSender WITHOUT DeleteMessage, like a transport (or a fake) that never opted into
// the optional deletion capability. Its edits are recorded so the fallback can be checked.
type fallbackReplier struct {
	*fakeReplier
	mu      sync.Mutex
	stream  int64
	edits   []string
	deleted []int64
}

func (r *fallbackReplier) SendStreamMessage(ctx context.Context, chatID int64, text string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stream++
	return r.stream, nil
}

func (r *fallbackReplier) EditStreamMessage(ctx context.Context, chatID int64, messageID int64, text string, parse bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.edits = append(r.edits, text)
	return nil
}

func (r *fallbackReplier) editTexts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.edits...)
}

func (r *fallbackReplier) deletedIDs() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64{}, r.deleted...)
}

// refusedDeleteReplier implements the optional capability, but Telegram refuses the deletion.
type refusedDeleteReplier struct {
	*fallbackReplier
}

func (r *refusedDeleteReplier) DeleteMessage(ctx context.Context, chatID int64, messageID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, messageID)
	return errors.New("the message cannot be deleted")
}

// TestPlaceholderDropFallsBackToAPointer — when the placeholder cannot be deleted (unsupported capability, or
// the deletion is refused), it must not keep the truncated interim text: it is edited into a short pointer at
// the answer that now arrives below it.
func TestPlaceholderDropFallsBackToAPointer(t *testing.T) {
	type fallbackCase struct {
		name       string
		replier    Replier
		stream     StreamSender
		recorder   *fallbackReplier
		wantDelete bool
	}
	var unsupported = &fallbackReplier{fakeReplier: &fakeReplier{}}
	var base = &fallbackReplier{fakeReplier: &fakeReplier{}}
	var refused = &refusedDeleteReplier{fallbackReplier: base}
	var cases = []fallbackCase{
		{name: "capability is not implemented", replier: unsupported, stream: unsupported, recorder: unsupported},
		{name: "deletion is refused", replier: refused, stream: refused, recorder: base, wantDelete: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "true"})
			var d = newTestDispatcher(t, cfg, &fakeModel{}, tc.replier, nil)
			var ctx = context.Background()

			d.noteChatTurn(502)
			var session = d.newStreamSession(ctx, 502, tc.stream)
			session.posted = true
			session.messageID = 91
			session.messageIDs = []int64{91}

			// The next question is admitted while this answer is being produced.
			d.noteChatTurn(502)

			if err := session.deliver(ctx, d, "ответ ниже"); err != nil {
				t.Fatalf("deliver: %v", err)
			}
			var edits = tc.recorder.editTexts()
			if len(edits) != 1 || edits[0] != msgAnswerBelow {
				t.Fatalf("the placeholder was not pointed at the answer below: %q", edits)
			}
			var sends = tc.recorder.sent
			if len(sends) == 0 || sends[len(sends)-1].text != "ответ ниже" {
				t.Fatalf("the answer did not leave as a new message: %+v", sends)
			}
			var deleted = tc.recorder.deletedIDs()
			if tc.wantDelete && (len(deleted) != 1 || deleted[0] != 91) {
				t.Fatalf("the deletion was not attempted: %v", deleted)
			}
			if !tc.wantDelete && len(deleted) != 0 {
				t.Fatalf("a deletion was attempted without the capability: %v", deleted)
			}
		})
	}
}
