package bot

import (
	"context"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

func TestStartupSuppressesExpiredQuestionWithoutModelCall(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "не должно быть"}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	rowID, err := store.Append(context.Background(), db, store.Message{
		ChatID: 170, UserID: 11, Role: store.RoleUser, Content: "старый вопрос", TelegramMessageID: intPtr(1300),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE messages SET created_at = '2000-01-01 00:00:00' WHERE id = ?`, rowID); err != nil {
		t.Fatalf("age fixture: %v", err)
	}

	answered, pending, err := d.ResumeInterruptedQuestions(context.Background())
	if err != nil || answered != 0 || pending != 0 {
		t.Fatalf("answered=%d pending=%d err=%v", answered, pending, err)
	}
	if m.callCount() != 0 || r.count() != 0 {
		t.Fatalf("expired recovery called model %d times and sent %d replies", m.callCount(), r.count())
	}
	skipped, found, err := store.ResumeSkip(context.Background(), db, 170)
	if err != nil || !found || skipped != rowID {
		t.Fatalf("resume skip=%d found=%v err=%v, want row %d", skipped, found, err, rowID)
	}
	answered, pending, err = d.ResumeInterruptedQuestions(context.Background())
	if err != nil || answered != 0 || pending != 0 {
		t.Fatalf("second scan: answered=%d pending=%d err=%v", answered, pending, err)
	}
}

func TestStartupAgeCapCanBeDisabled(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "RECOVERY_MAX_AGE_HOURS": "0"})
	m := &fakeModel{response: model.Response{Content: "ответ на старый вопрос"}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	rowID, err := store.Append(context.Background(), db, store.Message{
		ChatID: 171, UserID: 11, Role: store.RoleUser, Content: "старый вопрос", TelegramMessageID: intPtr(1301),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE messages SET created_at = '2000-01-01 00:00:00' WHERE id = ?`, rowID); err != nil {
		t.Fatalf("age fixture: %v", err)
	}
	answered, pending, err := d.ResumeInterruptedQuestions(context.Background())
	if err != nil || answered != 1 || pending != 0 || m.callCount() != 1 {
		t.Fatalf("answered=%d pending=%d calls=%d err=%v", answered, pending, m.callCount(), err)
	}
}

func TestStartupRejectsMalformedRecoveryTimestamp(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "не должно быть"}}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, db)
	rowID, err := store.Append(context.Background(), db, store.Message{
		ChatID: 172, UserID: 11, Role: store.RoleUser, Content: "вопрос", TelegramMessageID: intPtr(1302),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE messages SET created_at = 'not-a-time' WHERE id = ?`, rowID); err != nil {
		t.Fatalf("timestamp fixture: %v", err)
	}
	if _, _, err := d.ResumeInterruptedQuestions(context.Background()); err == nil {
		t.Fatal("malformed timestamp must be reported instead of silently bypassing the age cap")
	}
	if m.callCount() != 0 {
		t.Fatalf("model calls=%d, want 0", m.callCount())
	}
}
