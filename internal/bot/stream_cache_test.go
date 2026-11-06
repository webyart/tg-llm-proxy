package bot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func newCachedStreamSession(t *testing.T) (*streamSession, *streamReplier, string) {
	t.Helper()
	cfg := streamConfig(t, map[string]string{"MAX_MESSAGE_CHUNK": "128", "PARSE_MODE": "MarkdownV2"})
	r := &streamReplier{}
	d := newTestDispatcher(t, cfg, nil, r, testStore(t))
	s := d.newStreamSession(context.Background(), 100, r)
	s.interval = time.Hour
	answer := strings.Repeat("**жирный** и `код` со [ссылкой](https://example.com). ", 8)
	if err := s.handler().Delta(answer); err != nil {
		t.Fatal(err)
	}
	s.lastEdit = time.Now().Add(-s.interval - time.Millisecond)
	if err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	return s, r, answer
}

func TestStreamRefreshReusesChunksForUnchangedAnswer(t *testing.T) {
	s, r, answer := newCachedStreamSession(t)
	cached := s.renderedChunks
	if len(cached) == 0 {
		t.Fatal("expected rendered chunks")
	}
	firstView := r.lastEdit()

	s.lastEdit = time.Now().Add(-s.interval - time.Millisecond)
	if err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	if !s.rendered || s.renderedAnswer != answer {
		t.Fatalf("cache no longer matches the answer: rendered=%v", s.rendered)
	}
	if &cached[0] != &s.renderedChunks[0] {
		t.Fatal("unchanged answer rebuilt its rendered chunks")
	}
	if got := r.lastEdit(); got != firstView {
		t.Fatalf("unchanged refresh output differs:\nfirst %q\nsecond %q", firstView, got)
	}
}

func TestStreamRefreshRebuildsChunksWhenAnswerChanges(t *testing.T) {
	s, r, answer := newCachedStreamSession(t)
	previous := s.renderedAnswer
	addition := " **Новая часть.**"
	if err := s.handler().Delta(addition); err != nil {
		t.Fatal(err)
	}
	s.lastEdit = time.Now().Add(-s.interval - time.Millisecond)
	if err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	changed := answer + addition
	if s.answer.String() != changed || s.answerRunes != Runes(changed) {
		t.Fatalf("answer or rune count differs: runes=%d want=%d", s.answerRunes, Runes(changed))
	}
	if s.renderedAnswer == previous || s.renderedAnswer != changed {
		t.Fatal("changed answer did not replace the rendered-answer cache")
	}
	wantChunks := s.dispatcher.chunksOf(changed)
	if len(s.renderedChunks) != len(wantChunks) {
		t.Fatalf("cached chunks=%d, want %d", len(s.renderedChunks), len(wantChunks))
	}
	for index := range wantChunks {
		if s.renderedChunks[index] != wantChunks[index] {
			t.Errorf("cached chunk %d=%q, want %q", index, s.renderedChunks[index], wantChunks[index])
		}
	}
	wantView := s.partialViewChunks(wantChunks)
	if got := r.lastEdit(); got != wantView {
		t.Fatalf("changed refresh output=%q, want %q", got, wantView)
	}
	if !strings.Contains(wantView, fmt.Sprintf(msgStreamSuffix, s.answerRunes)) {
		t.Fatalf("refresh did not use the tracked answer rune count: %q", wantView)
	}
}
