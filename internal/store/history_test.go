package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestHistoryExportSearchAndDelete(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "history_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for _, row := range []Message{
		{ChatID: 70, UserID: 11, Role: RoleUser, Content: "Привет МИР"},
		{ChatID: 70, UserID: 11, Role: RoleAssistant, Content: "ответ"},
		{ChatID: 71, UserID: 12, Role: RoleUser, Content: "другой чат"},
	} {
		if _, err := Append(ctx, db, row); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	rows, err := ExportMessages(ctx, db, 70, 10)
	if err != nil || len(rows) != 2 || rows[0].Content != "Привет МИР" {
		t.Fatalf("export=%+v err=%v", rows, err)
	}
	matches, err := SearchMessages(ctx, db, 70, "мир", 20)
	if err != nil || len(matches) != 1 || matches[0].Content != "Привет МИР" {
		t.Fatalf("search=%+v err=%v", matches, err)
	}
	if err := SetResumeSkip(ctx, db, 70, rows[0].ID); err != nil {
		t.Fatalf("SetResumeSkip: %v", err)
	}
	if err := SetState(ctx, db, ContextResetIDKey(70), "1"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	deleted, err := DeleteChatHistory(ctx, db, 70)
	if err != nil || deleted != 2 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	left, err := HistoryOfChat(ctx, db, 70, 10)
	if err != nil || len(left) != 0 {
		t.Fatalf("left=%+v err=%v", left, err)
	}
	other, err := HistoryOfChat(ctx, db, 71, 10)
	if err != nil || len(other) != 1 {
		t.Fatalf("other chat=%+v err=%v", other, err)
	}
	if _, found, err := ResumeSkip(ctx, db, 70); err != nil || found {
		t.Fatalf("resume suppression remains: found=%v err=%v", found, err)
	}
	deleted, err = DeleteChatHistory(ctx, db, 70)
	if err != nil || deleted != 0 {
		t.Fatalf("second delete=%d err=%v", deleted, err)
	}
}

func TestDeleteMessageScopesChatAndVisibleRoles(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "history_delete_message_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	ids := make(map[string]int64)
	for name, row := range map[string]Message{
		"user":         {ChatID: 70, UserID: 11, Role: RoleUser, Content: "user"},
		"assistant":    {ChatID: 70, UserID: 11, Role: RoleAssistant, Content: "assistant"},
		"system":       {ChatID: 70, UserID: 11, Role: RoleSystem, Content: "system"},
		"foreign_user": {ChatID: 71, UserID: 12, Role: RoleUser, Content: "foreign"},
	} {
		id, err := Append(ctx, db, row)
		if err != nil {
			t.Fatalf("Append %s: %v", name, err)
		}
		ids[name] = id
	}

	for _, test := range []struct {
		name   string
		chatID int64
		id     int64
	}{
		{name: "wrong chat", chatID: 71, id: ids["user"]},
		{name: "system role", chatID: 70, id: ids["system"]},
		{name: "missing", chatID: 70, id: ids["assistant"] + 1000},
		{name: "zero chat", chatID: 0, id: ids["user"]},
		{name: "zero id", chatID: 70, id: 0},
		{name: "negative id", chatID: 70, id: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			deleted, err := DeleteMessage(ctx, db, test.chatID, test.id)
			if err != nil || deleted {
				t.Fatalf("DeleteMessage deleted=%v err=%v", deleted, err)
			}
		})
	}

	for _, name := range []string{"user", "assistant"} {
		deleted, err := DeleteMessage(ctx, db, 70, ids[name])
		if err != nil || !deleted {
			t.Fatalf("DeleteMessage %s deleted=%v err=%v", name, deleted, err)
		}
		deleted, err = DeleteMessage(ctx, db, 70, ids[name])
		if err != nil || deleted {
			t.Fatalf("second DeleteMessage %s deleted=%v err=%v", name, deleted, err)
		}
	}
	rows, err := HistoryOfChat(ctx, db, 70, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != ids["system"] {
		t.Fatalf("remaining rows=%+v err=%v", rows, err)
	}
	other, err := HistoryOfChat(ctx, db, 71, 10)
	if err != nil || len(other) != 1 || other[0].ID != ids["foreign_user"] {
		t.Fatalf("foreign rows=%+v err=%v", other, err)
	}
}

func TestStreamExportMessagesStopsImmediatelyAndPreservesWindowOrder(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "history_stream_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for index := 0; index < 25; index++ {
		if _, err := Append(ctx, db, Message{ChatID: 72, UserID: 11, Role: RoleUser, Content: string(rune('a' + index))}); err != nil {
			t.Fatalf("Append %d: %v", index, err)
		}
	}
	stop := errors.New("stop export")
	calls := 0
	window, truncated, err := StreamExportMessages(ctx, db, 72, 10, func(window MessageExportWindow, truncated bool) error {
		if window.Count != 10 || window.OldestID != 16 || window.NewestID != 25 || !truncated {
			t.Fatalf("window=%+v truncated=%v", window, truncated)
		}
		return nil
	}, func(row Message) error {
		calls++
		if row.ID != 16 {
			t.Fatalf("first streamed row=%d, want 16", row.ID)
		}
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("stream calls=%d err=%v", calls, err)
	}
	if window.Count != 10 || !truncated {
		t.Fatalf("early-exit metadata=%+v truncated=%v", window, truncated)
	}

	ids := []int64{}
	_, _, err = StreamExportMessages(ctx, db, 72, 10, func(MessageExportWindow, bool) error { return nil }, func(row Message) error {
		ids = append(ids, row.ID)
		return nil
	})
	if err != nil || len(ids) != 10 {
		t.Fatalf("full stream ids=%v err=%v", ids, err)
	}
	for index, id := range ids {
		if want := int64(16 + index); id != want {
			t.Fatalf("stream ids=%v", ids)
		}
	}
}

func TestSearchMessagesEarlyLateAndNoMatch(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "history_search_positions_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	ids := make([]int64, 0, 4)
	for _, content := range []string{"needle oldest", "filler", "needle middle", "needle newest"} {
		id, err := Append(ctx, db, Message{ChatID: 73, UserID: 11, Role: RoleUser, Content: content})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		ids = append(ids, id)
	}
	if _, err := Append(ctx, db, Message{ChatID: 74, UserID: 12, Role: RoleUser, Content: "needle foreign chat"}); err != nil {
		t.Fatalf("Append foreign chat: %v", err)
	}

	tests := []struct {
		name  string
		query string
		limit int
		want  []int64
	}{
		{name: "early match", query: "needle", limit: 1, want: []int64{ids[3]}},
		{name: "late match", query: "oldest", limit: 10, want: []int64{ids[0]}},
		{name: "chronological at limit", query: "needle", limit: 2, want: []int64{ids[2], ids[3]}},
		{name: "no match", query: "absent", limit: 10},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matches, err := SearchMessages(ctx, db, 73, test.query, test.limit)
			if err != nil {
				t.Fatalf("SearchMessages: %v", err)
			}
			if len(matches) != len(test.want) {
				t.Fatalf("matches=%+v, want IDs %v", matches, test.want)
			}
			for index, id := range test.want {
				if matches[index].ID != id || matches[index].ChatID != 73 {
					t.Fatalf("matches=%+v, want IDs %v", matches, test.want)
				}
			}
		})
	}
}

// TestSearchMessagesCoversTheWholeHistoryAndExactLimit — the FTS index replaced a scan of the newest 10 000
// messages, so a match at the very beginning of a long chat is now found, and `limit` still caps the newest
// matches, returned oldest first.
func TestSearchMessagesCoversTheWholeHistoryAndExactLimit(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "history_search_window_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	seedHistoryMessages(t, ctx, db, 75, 10001, func(index int) string {
		switch {
		case index == 0:
			return "outside newest window"
		case index >= 9998:
			return "needle"
		default:
			return "filler"
		}
	})

	matches, err := SearchMessages(ctx, db, 75, "outside", 10)
	if err != nil || len(matches) != 1 || matches[0].ID != 1 {
		t.Fatalf("the oldest row of a long chat was not found: matches=%+v err=%v", matches, err)
	}
	matches, err = SearchMessages(ctx, db, 75, "needle", 3)
	if err != nil {
		t.Fatalf("SearchMessages exact limit: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("matches=%d, want exactly limit 3", len(matches))
	}
	for index, match := range matches {
		if want := int64(9999 + index); match.ID != want {
			t.Fatalf("matches=%+v, want chronological IDs 9999..10001", matches)
		}
	}
}

func TestSearchMessagesScansAllMessageFields(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "history_search_fields_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	tokensIn, tokensOut, telegramID := int64(12), int64(34), int64(56)
	id, err := Append(ctx, db, Message{
		ChatID: 76, UserID: 11, Role: RoleAssistant, Content: "all fields needle",
		TokensIn: &tokensIn, TokensOut: &tokensOut, Model: "test-model", LatencyMs: 78,
		TelegramMessageID: &telegramID,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	matches, err := SearchMessages(ctx, db, 76, "FIELDS", 1)
	if err != nil || len(matches) != 1 {
		t.Fatalf("matches=%+v err=%v", matches, err)
	}
	row := matches[0]
	if row.ID != id || row.ChatID != 76 || row.UserID != 11 || row.Role != RoleAssistant ||
		row.Content != "all fields needle" || row.TokensIn == nil || *row.TokensIn != tokensIn ||
		row.TokensOut == nil || *row.TokensOut != tokensOut || row.Model != "test-model" ||
		row.LatencyMs != 78 || row.TelegramMessageID == nil || *row.TelegramMessageID != telegramID || row.CreatedAt == "" {
		t.Fatalf("scanned row lost fields: %+v", row)
	}
}

func TestSearchMessagesEmptyInputsAndDatabaseError(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "history_search_errors_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, test := range []struct {
		query string
		limit int
	}{
		{query: " \t\n", limit: 10},
		{query: "needle", limit: 0},
		{query: "needle", limit: -1},
	} {
		matches, err := SearchMessages(ctx, db, 77, test.query, test.limit)
		if err != nil || matches == nil || len(matches) != 0 {
			t.Fatalf("query=%q limit=%d matches=%+v err=%v", test.query, test.limit, matches, err)
		}
	}
	if matches, err := SearchMessages(ctx, db, 77, "needle", 1); err == nil || matches != nil {
		t.Fatalf("closed database matches=%+v err=%v, want query error", matches, err)
	}
}

func seedHistoryMessages(tb testing.TB, ctx context.Context, db *sql.DB, chatID int64, count int, content func(int) string) {
	tb.Helper()
	if err := EnsureChat(ctx, db, chatID, 11, ""); err != nil {
		tb.Fatalf("EnsureChat: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		tb.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO messages (chat_id, user_id, role, content) VALUES (?, ?, ?, ?)`)
	if err != nil {
		tb.Fatalf("PrepareContext: %v", err)
	}
	defer stmt.Close()
	for index := 0; index < count; index++ {
		if _, err := stmt.ExecContext(ctx, chatID, 11, RoleUser, content(index)); err != nil {
			tb.Fatalf("insert message %d: %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("Commit: %v", err)
	}
}
