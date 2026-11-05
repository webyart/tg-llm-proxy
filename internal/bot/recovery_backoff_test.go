package bot

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"bot/internal/model"
	"bot/internal/store"
)

func pendingRecoveryQuestion(t *testing.T, db *sql.DB, chatID int64, telegramMessageID int64, now time.Time) int64 {
	t.Helper()
	rowID, err := store.Append(context.Background(), db, store.Message{
		ChatID: chatID, UserID: 11, Role: store.RoleUser, Content: "вопрос", TelegramMessageID: intPtr(telegramMessageID),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	createdAt := now.Add(-time.Hour).Format(time.DateTime)
	if _, err := db.ExecContext(context.Background(), `UPDATE messages SET created_at = ? WHERE id = ?`, createdAt, rowID); err != nil {
		t.Fatalf("recovery fixture timestamp: %v", err)
	}
	return rowID
}

func runRecoveryAt(t *testing.T, d *Dispatcher, now time.Time, wantAnswered int, wantPending int) {
	t.Helper()
	answered, pending, err := d.resumeInterruptedQuestionsAt(context.Background(), now)
	if err != nil || answered != wantAnswered || pending != wantPending {
		t.Fatalf("recovery at %s: answered=%d pending=%d err=%v, want %d/%d/nil", now, answered, pending, err, wantAnswered, wantPending)
	}
}

func TestRecoveryBackoffSurvivesRestartLoop(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{err: context.DeadlineExceeded}
	r := &fakeReplier{}
	db := testStore(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	rowID := pendingRecoveryQuestion(t, db, 180, 1400, now)
	d := newTestDispatcher(t, cfg, m, r, db)

	runRecoveryAt(t, d, now, 0, 1)
	state, found, err := store.ResumeRecovery(context.Background(), db, 180, rowID)
	if err != nil || !found || state.Attempts != 1 || !state.NextRecoveryAt.Equal(now.Add(ResumeInitialBackoff)) {
		t.Fatalf("first recovery state = %+v found=%v err=%v", state, found, err)
	}
	for restart := 1; restart <= 3; restart++ {
		restarted := newTestDispatcher(t, cfg, m, r, db)
		runRecoveryAt(t, restarted, now.Add(time.Duration(restart)*time.Second), 0, 1)
	}
	if m.callCount() != 1 || r.count() != 1 {
		t.Fatalf("restart loop made %d model calls and sent %d notices, want 1/1", m.callCount(), r.count())
	}
	rows, err := store.UnansweredQuestions(context.Background(), db, ResumeScanLimit)
	if err != nil || len(rows) != 1 || rows[0].ID != rowID {
		t.Fatalf("restart loop deleted or hid pending history: rows=%+v err=%v", rows, err)
	}
}

func TestRecoveryBackoffDefersUntilElapsed(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{err: context.DeadlineExceeded}
	r := &fakeReplier{}
	db := testStore(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	rowID := pendingRecoveryQuestion(t, db, 181, 1410, now)
	d := newTestDispatcher(t, cfg, m, r, db)

	runRecoveryAt(t, d, now, 0, 1)
	state, _, _ := store.ResumeRecovery(context.Background(), db, 181, rowID)
	runRecoveryAt(t, newTestDispatcher(t, cfg, m, r, db), state.NextRecoveryAt.Add(-time.Nanosecond), 0, 1)
	if m.callCount() != 1 || r.count() != 1 {
		t.Fatalf("backoff elapsed early: calls=%d replies=%d", m.callCount(), r.count())
	}
	runRecoveryAt(t, newTestDispatcher(t, cfg, m, r, db), state.NextRecoveryAt, 0, 1)
	if m.callCount() != 2 {
		t.Fatalf("elapsed backoff made %d total model calls, want 2", m.callCount())
	}
	second, found, err := store.ResumeRecovery(context.Background(), db, 181, rowID)
	wantNext := state.NextRecoveryAt.Add(2 * ResumeInitialBackoff)
	if err != nil || !found || second.Attempts != 2 || !second.NextRecoveryAt.Equal(wantNext) {
		t.Fatalf("second recovery state = %+v found=%v err=%v, want next %s", second, found, err, wantNext)
	}
}

func TestRecoveryMaxAttemptsSuppressesWithoutDeletingHistory(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{err: context.DeadlineExceeded}
	r := &fakeReplier{}
	db := testStore(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	rowID := pendingRecoveryQuestion(t, db, 182, 1420, now)

	for attempt := 1; attempt <= ResumeMaxAttempts; attempt++ {
		d := newTestDispatcher(t, cfg, m, r, db)
		runRecoveryAt(t, d, now, 0, 1)
		state, found, err := store.ResumeRecovery(context.Background(), db, 182, rowID)
		if err != nil || !found || state.Attempts != attempt {
			t.Fatalf("attempt %d state = %+v found=%v err=%v", attempt, state, found, err)
		}
		now = state.NextRecoveryAt
	}
	if m.callCount() != ResumeMaxAttempts || r.count() != ResumeMaxAttempts {
		t.Fatalf("max-attempt calls=%d replies=%d", m.callCount(), r.count())
	}
	skipped, found, err := store.ResumeSkip(context.Background(), db, 182)
	if err != nil || !found || skipped != rowID {
		t.Fatalf("max-attempt skip=%d found=%v err=%v, want row %d", skipped, found, err, rowID)
	}
	runRecoveryAt(t, newTestDispatcher(t, cfg, m, r, db), now, 0, 0)
	if m.callCount() != ResumeMaxAttempts || r.count() != ResumeMaxAttempts {
		t.Fatalf("suppressed question ran again: calls=%d replies=%d", m.callCount(), r.count())
	}
	stats, err := store.Stats(context.Background(), db, 182)
	if err != nil || stats.Messages != 1 {
		t.Fatalf("suppression changed history: stats=%+v err=%v", stats, err)
	}
	d := newTestDispatcher(t, cfg, m, r, db)
	if err := dispatch(t, d, testEvent(182, 11, 1421, "/status")); err != nil {
		t.Fatalf("/status: %v", err)
	}
	if !strings.Contains(r.texts()[len(r.texts())-1], "автовосстановление отключено (/skip)") {
		t.Fatalf("/status does not expose max-attempt suppression: %q", r.joined())
	}
}

func TestRecoverySuccessResetsAttemptsAndBackoff(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	failing := &fakeModel{err: context.DeadlineExceeded}
	r := &fakeReplier{}
	db := testStore(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	rowID := pendingRecoveryQuestion(t, db, 183, 1430, now)
	d := newTestDispatcher(t, cfg, failing, r, db)
	runRecoveryAt(t, d, now, 0, 1)
	state, _, _ := store.ResumeRecovery(context.Background(), db, 183, rowID)

	succeeding := &fakeModel{response: model.Response{Content: "ответ после паузы"}}
	runRecoveryAt(t, newTestDispatcher(t, cfg, succeeding, r, db), state.NextRecoveryAt, 1, 0)
	if _, found, err := store.ResumeRecovery(context.Background(), db, 183, rowID); err != nil || found {
		t.Fatalf("successful recovery left backoff: found=%v err=%v", found, err)
	}
	if _, found, err := store.ResumeSkip(context.Background(), db, 183); err != nil || found {
		t.Fatalf("successful recovery left suppression: found=%v err=%v", found, err)
	}
	runRecoveryAt(t, newTestDispatcher(t, cfg, succeeding, r, db), state.NextRecoveryAt.Add(time.Hour), 0, 0)
	if succeeding.callCount() != 1 {
		t.Fatalf("answered question was replayed: calls=%d", succeeding.callCount())
	}
}

func TestRecoveryDeferredRestartDoesNotDuplicateFailureNotice(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{err: context.DeadlineExceeded}
	r := &fakeReplier{}
	db := testStore(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	rowID := pendingRecoveryQuestion(t, db, 184, 1440, now)
	runRecoveryAt(t, newTestDispatcher(t, cfg, m, r, db), now, 0, 1)
	state, _, _ := store.ResumeRecovery(context.Background(), db, 184, rowID)
	runRecoveryAt(t, newTestDispatcher(t, cfg, m, r, db), state.NextRecoveryAt.Add(-time.Second), 0, 1)

	if r.count() != 1 || !strings.Contains(r.joined(), "не ответила") {
		t.Fatalf("deferred restart duplicated the failure notice: %q", r.joined())
	}
}

func TestUnskipResetsRecoveryAttemptsAndBackoff(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	failing := &fakeModel{err: context.DeadlineExceeded}
	r := &fakeReplier{}
	db := testStore(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	rowID := pendingRecoveryQuestion(t, db, 185, 1450, now)
	for attempt := 1; attempt <= ResumeMaxAttempts; attempt++ {
		d := newTestDispatcher(t, cfg, failing, r, db)
		runRecoveryAt(t, d, now, 0, 1)
		state, _, _ := store.ResumeRecovery(context.Background(), db, 185, rowID)
		now = state.NextRecoveryAt
	}
	d := newTestDispatcher(t, cfg, failing, r, db)
	if err := dispatch(t, d, testEvent(185, 11, 1451, "/unskip")); err != nil {
		t.Fatalf("/unskip: %v", err)
	}
	if failing.callCount() != ResumeMaxAttempts {
		t.Fatalf("/unskip called the model: calls=%d", failing.callCount())
	}
	if _, found, err := store.ResumeRecovery(context.Background(), db, 185, rowID); err != nil || found {
		t.Fatalf("/unskip left recovery state: found=%v err=%v", found, err)
	}

	succeeding := &fakeModel{response: model.Response{Content: "ответ после unskip"}}
	runRecoveryAt(t, newTestDispatcher(t, cfg, succeeding, r, db), now, 1, 0)
	if succeeding.callCount() != 1 {
		t.Fatalf("reset backoff did not permit immediate recovery: calls=%d", succeeding.callCount())
	}
}

func TestManualRedeliveryCanAnswerDuringStartupBackoff(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	failing := &fakeModel{err: context.DeadlineExceeded}
	r := &fakeReplier{}
	db := testStore(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	rowID := pendingRecoveryQuestion(t, db, 186, 1460, now)
	runRecoveryAt(t, newTestDispatcher(t, cfg, failing, r, db), now, 0, 1)
	state, _, _ := store.ResumeRecovery(context.Background(), db, 186, rowID)

	succeeding := &fakeModel{response: model.Response{Content: "ручной ответ"}}
	d := newTestDispatcher(t, cfg, succeeding, r, db)
	if err := dispatch(t, d, testEvent(186, 11, 1460, "/ask вопрос")); err != nil {
		t.Fatalf("manual redelivery: %v", err)
	}
	if succeeding.callCount() != 1 {
		t.Fatalf("manual redelivery was blocked by startup backoff: calls=%d", succeeding.callCount())
	}
	runRecoveryAt(t, newTestDispatcher(t, cfg, succeeding, r, db), state.NextRecoveryAt.Add(-time.Second), 0, 0)
	if succeeding.callCount() != 1 {
		t.Fatalf("manual answer was replayed by startup recovery: calls=%d", succeeding.callCount())
	}
}

func TestRecoveryBackoffIsExponentialAndBounded(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: time.Minute},
		{attempt: 2, want: 2 * time.Minute},
		{attempt: 3, want: 4 * time.Minute},
		{attempt: 100, want: ResumeMaxBackoff},
	}
	for _, test := range tests {
		if got := recoveryBackoff(test.attempt); got != test.want {
			t.Fatalf("recoveryBackoff(%d) = %s, want %s", test.attempt, got, test.want)
		}
	}
}
