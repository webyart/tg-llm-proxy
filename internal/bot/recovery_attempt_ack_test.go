package bot

import (
	"context"
	"testing"
	"time"

	"bot/internal/model"
	"bot/internal/ratelimit"
	"bot/internal/store"
)

// TestRateLimitRefusalDoesNotSpendRecoveryAttempts — a limiter refusal happens before the store stage and
// before the model, so it is not an attempt. Three starts under the same exhausted per-user limiter must leave
// the question eligible and the recovery state absent; one real model failure then spends exactly one attempt.
func TestRateLimitRefusalDoesNotSpendRecoveryAttempts(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "не должно вызываться"}}
	r := &fakeReplier{}
	db := testStore(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	rowID := pendingRecoveryQuestion(t, db, 190, 1500, now)

	// The user already spent the per-minute slot: RetryAfter is positive and every replay is refused before
	// the store stage. Sharing the limiter across the three "restarts" keeps the window continuously closed.
	limiter := ratelimit.New(1)
	if !limiter.Allow(11) {
		t.Fatal("the first limiter slot must be available")
	}

	for restart := 0; restart < 3; restart++ {
		d := newTestDispatcher(t, cfg, m, r, db)
		d.deps.Limit = limiter
		runRecoveryAt(t, d, now.Add(time.Duration(restart)*time.Second), 0, 1)
	}
	if m.callCount() != 0 {
		t.Fatalf("a rate-limit refusal reached the model: %d calls", m.callCount())
	}
	if _, found, err := store.ResumeRecovery(context.Background(), db, 190, rowID); err != nil || found {
		t.Fatalf("a rate-limit refusal spent an attempt: found=%v err=%v", found, err)
	}
	if _, found, err := store.ResumeSkip(context.Background(), db, 190); err != nil || found {
		t.Fatalf("a rate-limit refusal suppressed the question: found=%v err=%v", found, err)
	}
	rows, err := store.UnansweredQuestions(context.Background(), db, ResumeScanLimit)
	if err != nil || len(rows) != 1 || rows[0].ID != rowID {
		t.Fatalf("the question must stay eligible after three refusals: rows=%+v err=%v", rows, err)
	}

	failing := &fakeModel{err: context.DeadlineExceeded}
	runRecoveryAt(t, newTestDispatcher(t, cfg, failing, r, db), now.Add(time.Minute), 0, 1)
	state, found, err := store.ResumeRecovery(context.Background(), db, 190, rowID)
	if err != nil || !found || state.Attempts != 1 {
		t.Fatalf("one real model failure must spend exactly one attempt: state=%+v found=%v err=%v", state, found, err)
	}
	if failing.callCount() != 1 {
		t.Fatalf("the failing attempt called the model %d times, want 1", failing.callCount())
	}
	if _, found, err := store.ResumeSkip(context.Background(), db, 190); err != nil || found {
		t.Fatalf("one failure of three must not suppress the question: found=%v err=%v", found, err)
	}
}
