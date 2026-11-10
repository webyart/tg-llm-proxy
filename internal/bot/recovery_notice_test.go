package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

// TestStartupNotifiesOnceAboutASupersededQuestion — the scan only replays the newest visible row. When the
// user asked again and that newer question was answered, the older one had no answer and nobody ever told the
// user. The notice must name it, and the bot_state marker must keep the next start silent.
func TestStartupNotifiesOnceAboutASupersededQuestion(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	db := testStore(t)
	ctx := context.Background()
	if _, err := store.Append(ctx, db, store.Message{
		ChatID: 191, UserID: 11, Role: store.RoleUser,
		Content: "первый вопрос, который остался без ответа", TelegramMessageID: intPtr(1600),
	}); err != nil {
		t.Fatalf("storing the first question: %v", err)
	}
	if _, err := store.Append(ctx, db, store.Message{
		ChatID: 191, UserID: 11, Role: store.RoleUser,
		Content: "второй вопрос", TelegramMessageID: intPtr(1601),
	}); err != nil {
		t.Fatalf("storing the second question: %v", err)
	}

	m := &fakeModel{response: model.Response{Content: "ответ на второй вопрос"}}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, db)
	answered, pending, err := d.ResumeInterruptedQuestions(ctx)
	if err != nil {
		t.Fatalf("ResumeInterruptedQuestions: %v", err)
	}
	if answered != 1 || pending != 0 {
		t.Fatalf("the newest question must be replayed: answered=%d pending=%d", answered, pending)
	}
	notices := countTexts(r.texts(), "остался без ответа")
	if notices != 1 {
		t.Fatalf("the superseded question must produce one notice, got %d in %q", notices, r.joined())
	}
	if joined := r.joined(); !strings.Contains(joined, "Вопрос от ") || !strings.Contains(joined, "/skip") {
		t.Fatalf("the notice does not name the question and the /skip escape: %q", joined)
	}

	// A restart scans the same history: the first question is still superseded, but the marker remembers it.
	r2 := &fakeReplier{}
	d2 := newTestDispatcher(t, cfg, m, r2, db)
	answered, pending, err = d2.ResumeInterruptedQuestions(ctx)
	if err != nil {
		t.Fatalf("second ResumeInterruptedQuestions: %v", err)
	}
	if answered != 0 || pending != 0 {
		t.Fatalf("the answered history must not be replayed: answered=%d pending=%d", answered, pending)
	}
	if got := countTexts(r2.texts(), "остался без ответа"); got != 0 {
		t.Fatalf("the notice was repeated on the next start: %d in %q", got, r2.joined())
	}
}

// TestStartupSendsNoNoticeForAnAnsweredHistory — a normally answered dialogue has no superseded question.
func TestStartupSendsNoNoticeForAnAnsweredHistory(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	db := testStore(t)
	ctx := context.Background()
	for _, message := range []struct {
		role    string
		content string
	}{
		{store.RoleUser, "первый вопрос"},
		{store.RoleAssistant, "первый ответ"},
		{store.RoleUser, "второй вопрос"},
		{store.RoleAssistant, "второй ответ"},
	} {
		if _, err := store.Append(ctx, db, store.Message{
			ChatID: 192, UserID: 11, Role: message.role, Content: message.content,
		}); err != nil {
			t.Fatalf("seeding %s: %v", message.role, err)
		}
	}

	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, &fakeModel{response: model.Response{Content: "не нужен"}}, r, db)
	answered, pending, err := d.ResumeInterruptedQuestions(ctx)
	if err != nil {
		t.Fatalf("ResumeInterruptedQuestions: %v", err)
	}
	if answered != 0 || pending != 0 {
		t.Fatalf("the answered history must stay as it is: answered=%d pending=%d", answered, pending)
	}
	if joined := r.joined(); strings.Contains(joined, "остался без ответа") {
		t.Fatalf("an answered history received a recovery notice: %q", joined)
	}
}

func countTexts(texts []string, fragment string) int {
	count := 0
	for _, text := range texts {
		if strings.Contains(text, fragment) {
			count++
		}
	}
	return count
}
