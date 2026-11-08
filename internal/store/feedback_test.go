package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func feedbackAssistant(t *testing.T, db *sql.DB, chatID int64, content string) int64 {
	t.Helper()
	id, err := Append(context.Background(), db, Message{ChatID: chatID, UserID: 7, Role: RoleAssistant, Content: content})
	if err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	return id
}

// A second click replaces the first rating instead of adding a row, and the aggregates count users,
// not clicks.
func TestMessageFeedbackReplacesTheRatingOfOneUser(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()
	var messageID = feedbackAssistant(t, db, 11, "ответ для оценки")

	changed, err := SetMessageFeedback(ctx, db, 11, 7, messageID, 1)
	if err != nil || !changed {
		t.Fatalf("first rating changed=%v err=%v, want a new row", changed, err)
	}
	ratings, err := MessageFeedbackFor(ctx, db, 11, 7, []int64{messageID})
	if err != nil || ratings[messageID] != 1 {
		t.Fatalf("rating after 👍 = %v err=%v, want 1", ratings, err)
	}

	changed, err = SetMessageFeedback(ctx, db, 11, 7, messageID, -1)
	if err != nil || !changed {
		t.Fatalf("replacement changed=%v err=%v, want the row to change", changed, err)
	}
	ratings, err = MessageFeedbackFor(ctx, db, 11, 7, []int64{messageID})
	if err != nil || ratings[messageID] != -1 {
		t.Fatalf("rating after 👎 = %v err=%v, want -1", ratings, err)
	}

	changed, err = SetMessageFeedback(ctx, db, 11, 7, messageID, -1)
	if err != nil || changed {
		t.Fatalf("repeated 👎 changed=%v err=%v, want no change", changed, err)
	}

	// Another user rates the same message independently.
	changed, err = SetMessageFeedback(ctx, db, 11, 8, messageID, 1)
	if err != nil || !changed {
		t.Fatalf("second user rating changed=%v err=%v", changed, err)
	}
	up, down, err := MessageFeedbackCounts(ctx, db, 11)
	if err != nil || up != 1 || down != 1 {
		t.Fatalf("counts = %d/%d err=%v, want 1 up and 1 down", up, down, err)
	}
	if ratings, err = MessageFeedbackFor(ctx, db, 11, 7, []int64{messageID}); err != nil || ratings[messageID] != -1 {
		t.Fatalf("first user rating after the second user = %v err=%v", ratings, err)
	}
	if ratings, err = MessageFeedbackFor(ctx, db, 11, 8, []int64{messageID}); err != nil || ratings[messageID] != 1 {
		t.Fatalf("second user rating = %v err=%v", ratings, err)
	}
	if ratings, err = MessageFeedbackFor(ctx, db, 11, 9, []int64{messageID}); err != nil || len(ratings) != 0 {
		t.Fatalf("unrated user map = %v err=%v, want empty", ratings, err)
	}
	// The rating belongs to its chat and does not leak into the view of another one.
	if ratings, err = MessageFeedbackFor(ctx, db, 12, 7, []int64{messageID}); err != nil || len(ratings) != 0 {
		t.Fatalf("another chat sees %v err=%v, want empty", ratings, err)
	}
	if ratings, err = MessageFeedbackFor(ctx, db, 11, 7, nil); err != nil || len(ratings) != 0 {
		t.Fatalf("empty id list = %v err=%v, want empty map", ratings, err)
	}
}

// Only an assistant answer of the rating chat can be rated; a wrong id, a wrong chat, a wrong role and a
// wrong value are named errors, not rows.
func TestMessageFeedbackRejectsWrongRoleChatAndValue(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()
	var assistantID = feedbackAssistant(t, db, 11, "свой ответ")
	var otherChatID = feedbackAssistant(t, db, 12, "чужой ответ")
	var userMessageID, err = Append(ctx, db, Message{ChatID: 11, UserID: 7, Role: RoleUser, Content: "вопрос"})
	if err != nil {
		t.Fatalf("Append user: %v", err)
	}

	for _, test := range []struct {
		name      string
		chatID    int64
		messageID int64
		value     int
		want      error
	}{
		{"user role", 11, userMessageID, 1, ErrFeedbackNotAllowed},
		{"other chat", 11, otherChatID, 1, ErrFeedbackNotAllowed},
		{"zero value", 11, assistantID, 0, ErrFeedbackValue},
		{"value above one", 11, assistantID, 2, ErrFeedbackValue},
		{"missing message", 11, 999999, 1, ErrFeedbackNotFound},
	} {
		changed, err := SetMessageFeedback(ctx, db, test.chatID, 7, test.messageID, test.value)
		if !errors.Is(err, test.want) || changed {
			t.Errorf("%s: changed=%v err=%v, want %v and no change", test.name, changed, err, test.want)
		}
	}
	if up, down, err := MessageFeedbackCounts(ctx, db, 11); err != nil || up != 0 || down != 0 {
		t.Fatalf("rejected ratings left counts %d/%d err=%v", up, down, err)
	}
}

// Deleting the message removes its ratings through the foreign key.
func TestMessageFeedbackCascadesWithTheMessage(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()
	var messageID = feedbackAssistant(t, db, 11, "удаляемый ответ")
	if _, err := SetMessageFeedback(ctx, db, 11, 7, messageID, 1); err != nil {
		t.Fatalf("SetMessageFeedback: %v", err)
	}
	if _, err := SetMessageFeedback(ctx, db, 11, 8, messageID, -1); err != nil {
		t.Fatalf("SetMessageFeedback of the second user: %v", err)
	}

	var stored int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_feedback WHERE message_id = ?`, messageID).Scan(&stored); err != nil || stored != 2 {
		t.Fatalf("stored ratings=%d err=%v, want 2", stored, err)
	}
	deleted, err := DeleteMessage(ctx, db, 11, messageID)
	if err != nil || !deleted {
		t.Fatalf("DeleteMessage deleted=%v err=%v", deleted, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_feedback WHERE message_id = ?`, messageID).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("ratings after the cascade=%d err=%v, want 0", stored, err)
	}
	if up, down, err := MessageFeedbackCounts(ctx, db, 11); err != nil || up != 0 || down != 0 {
		t.Fatalf("counts after the cascade = %d/%d err=%v", up, down, err)
	}
}

// Counts are per chat, and the newest assistant answer is the target of the bot command even when a
// later user question exists.
func TestMessageFeedbackCountsAndLastAssistantMessage(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	if id, err := LastAssistantMessageID(ctx, db, 11); err != nil || id != 0 {
		t.Fatalf("empty chat last assistant = %d err=%v, want 0", id, err)
	}
	if _, err := Append(ctx, db, Message{ChatID: 11, UserID: 7, Role: RoleUser, Content: "вопрос"}); err != nil {
		t.Fatalf("Append user: %v", err)
	}
	if id, err := LastAssistantMessageID(ctx, db, 11); err != nil || id != 0 {
		t.Fatalf("user-only chat last assistant = %d err=%v, want 0", id, err)
	}

	first := feedbackAssistant(t, db, 11, "первый ответ")
	if id, err := LastAssistantMessageID(ctx, db, 11); err != nil || id != first {
		t.Fatalf("last assistant = %d err=%v, want %d", id, err, first)
	}
	if _, err := Append(ctx, db, Message{ChatID: 11, UserID: 7, Role: RoleUser, Content: "ещё вопрос"}); err != nil {
		t.Fatalf("Append second user: %v", err)
	}
	second := feedbackAssistant(t, db, 11, "второй ответ")
	if id, err := LastAssistantMessageID(ctx, db, 11); err != nil || id != second {
		t.Fatalf("last assistant after the newer answer = %d err=%v, want %d", id, err, second)
	}

	if _, err := SetMessageFeedback(ctx, db, 11, 7, first, 1); err != nil {
		t.Fatalf("rate first: %v", err)
	}
	if _, err := SetMessageFeedback(ctx, db, 11, 7, second, 1); err != nil {
		t.Fatalf("rate second 👍: %v", err)
	}
	if _, err := SetMessageFeedback(ctx, db, 11, 8, second, -1); err != nil {
		t.Fatalf("rate second 👎: %v", err)
	}
	up, down, err := MessageFeedbackCounts(ctx, db, 11)
	if err != nil || up != 2 || down != 1 {
		t.Fatalf("counts = %d/%d err=%v, want 2/1", up, down, err)
	}

	// A rating in another chat must not enter these counts.
	var otherID = feedbackAssistant(t, db, 12, "чужой ответ")
	if _, err := SetMessageFeedback(ctx, db, 12, 7, otherID, 1); err != nil {
		t.Fatalf("rate other chat: %v", err)
	}
	up, down, err = MessageFeedbackCounts(ctx, db, 11)
	if err != nil || up != 2 || down != 1 {
		t.Fatalf("counts after another chat = %d/%d err=%v, want 2/1", up, down, err)
	}
}

// Migration 015 must ship the aggregate index, not only the table.
func TestMessageFeedbackIndexExists(t *testing.T) {
	var db = memDB(t)
	var found int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_message_feedback_chat_created'`).Scan(&found); err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	if found != 1 {
		t.Fatal("idx_message_feedback_chat_created is missing: /stats and the chat aggregates would scan the table")
	}
}
