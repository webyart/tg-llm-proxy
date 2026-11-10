package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// newChatLimitsDB opens an in-memory database whose cache name is the test name, so two sub-cases of
// one test never share rows.
func newChatLimitsDB(t *testing.T) *sql.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_", "-", "_").Replace(t.Name())
	db, err := OpenMemory(context.Background(), name)
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestChatLimitsCRUDAndValidation(t *testing.T) {
	ctx := context.Background()
	db := newChatLimitsDB(t)
	if err := EnsureChat(ctx, db, 11, 11, "private"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureChat(ctx, db, -100200, 11, "group"); err != nil {
		t.Fatal(err)
	}

	if limits, found, err := GetChatLimits(ctx, db, 11); err != nil || found || !limits.IsUnlimited() {
		t.Fatalf("fresh chat limits=%+v found=%v err=%v, want no row and unlimited", limits, found, err)
	}
	want := ChatLimits{
		ChatID: 11, DailyModelLimit: 10, DailyNetworkLimit: 5, PerMinuteLimit: 3,
		DisabledTools: []string{ChatToolWebSearch, ChatToolReadURL},
	}
	if err := SetChatLimits(ctx, db, want); err != nil {
		t.Fatalf("SetChatLimits: %v", err)
	}
	got, found, err := GetChatLimits(ctx, db, 11)
	if err != nil || !found {
		t.Fatalf("GetChatLimits found=%v err=%v", found, err)
	}
	if got.DailyModelLimit != 10 || got.DailyNetworkLimit != 5 || got.PerMinuteLimit != 3 {
		t.Fatalf("numbers=%+v", got)
	}
	// The order the caller sent is not the order the store keeps: the canonical order is stable.
	if len(got.DisabledTools) != 2 || got.DisabledTools[0] != ChatToolReadURL || got.DisabledTools[1] != ChatToolWebSearch {
		t.Fatalf("disabled tools=%v, want canonical read_url,web_search", got.DisabledTools)
	}
	if !got.ToolDisabled(ChatToolReadURL) || got.ToolDisabled(ChatToolCalculator) {
		t.Fatalf("ToolDisabled is wrong for %+v", got.DisabledTools)
	}

	// Replacing keeps one row and rewrites every field.
	if err := SetChatLimits(ctx, db, ChatLimits{ChatID: 11, DailyModelLimit: 7}); err != nil {
		t.Fatalf("second SetChatLimits: %v", err)
	}
	got, found, _ = GetChatLimits(ctx, db, 11)
	if !found || got.DailyModelLimit != 7 || got.DailyNetworkLimit != 0 || got.PerMinuteLimit != 0 || len(got.DisabledTools) != 0 {
		t.Fatalf("replaced limits=%+v found=%v", got, found)
	}

	for _, invalid := range []ChatLimits{
		{ChatID: 11, DailyModelLimit: -1},
		{ChatID: 11, DailyNetworkLimit: ChatLimitMax + 1},
		{ChatID: 11, PerMinuteLimit: 1, DisabledTools: []string{"shell"}},
		{ChatID: 0, DailyModelLimit: 1},
		{ChatID: 999999, DailyModelLimit: 1}, // no chats row
	} {
		if err := SetChatLimits(ctx, db, invalid); err == nil {
			t.Errorf("SetChatLimits(%+v) was accepted", invalid)
		}
	}
	if err := SetChatLimits(ctx, db, ChatLimits{ChatID: 999999, DailyModelLimit: 1}); !errors.Is(err, ErrChatNotFound) {
		t.Errorf("unknown chat error=%v, want ErrChatNotFound", err)
	}

	reset, err := ResetChatLimits(ctx, db, 11)
	if err != nil || !reset {
		t.Fatalf("ResetChatLimits=%v err=%v", reset, err)
	}
	if reset, err := ResetChatLimits(ctx, db, 11); err != nil || reset {
		t.Fatalf("second reset=%v err=%v, want false", reset, err)
	}
	if _, found, _ := GetChatLimits(ctx, db, 11); found {
		t.Fatal("a reset chat still has a limits row")
	}
}

func TestChatLimitsForeignKeyCascadesWithChat(t *testing.T) {
	ctx := context.Background()
	db := newChatLimitsDB(t)
	if err := EnsureChat(ctx, db, -100300, 11, "group"); err != nil {
		t.Fatal(err)
	}
	if err := SetChatLimits(ctx, db, ChatLimits{ChatID: -100300, PerMinuteLimit: 2}); err != nil {
		t.Fatal(err)
	}
	// ON DELETE CASCADE of chat_limits: deleting the chat removes its limits in the same statement.
	if _, err := db.ExecContext(ctx, `DELETE FROM chats WHERE chat_id = ?`, int64(-100300)); err != nil {
		t.Fatalf("delete chat: %v", err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_limits WHERE chat_id = ?`, int64(-100300)).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("chat_limits survived its chat: rows=%d", rows)
	}
}

func TestChatDailyUsageCountsOnlyThisChatAndUTCDay(t *testing.T) {
	ctx := context.Background()
	db := newChatLimitsDB(t)
	if err := EnsureChat(ctx, db, 11, 11, ""); err != nil {
		t.Fatal(err)
	}
	if err := EnsureChat(ctx, db, -100200, 11, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	insert := func(chatID int64, kind, detail, at string) {
		t.Helper()
		if err := LogChatEvent(ctx, db, chatID, 11, kind, detail); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE events SET created_at = ? WHERE id = last_insert_rowid()`, at); err != nil {
			t.Fatal(err)
		}
	}
	insert(11, EventModelCall, "model-a", "2026-05-04 00:00:00")
	insert(11, EventModelCall, "model-b", "2026-05-04 11:59:59")
	insert(11, EventUsage, ChatToolReadURL, "2026-05-04 09:00:00")
	insert(11, EventUsage, ChatToolWebSearch, "2026-05-04 10:00:00")
	insert(11, EventUsage, ChatToolCalculator, "2026-05-04 10:00:00")
	insert(11, EventUsage, "rag", "2026-05-04 10:00:00") // not one of the three restricted tools
	insert(11, EventModelCall, "model-a", "2026-05-03 23:59:59")
	insert(-100200, EventModelCall, "model-a", "2026-05-04 10:00:00")
	insert(-100200, EventUsage, ChatToolWebSearch, "2026-05-04 10:00:00")

	usage, err := ChatDailyUsage(ctx, db, 11, now)
	if err != nil {
		t.Fatalf("ChatDailyUsage: %v", err)
	}
	if usage.ModelCalls != 2 || usage.ReadURL != 1 || usage.WebSearch != 1 || usage.Calculator != 1 {
		t.Fatalf("usage=%+v, want 2 model calls, 1/1/1 tools", usage)
	}
	if usage.NetworkToolCalls() != 2 {
		t.Fatalf("network calls=%d, want read_url+web_search=2", usage.NetworkToolCalls())
	}
	group, err := ChatDailyUsage(ctx, db, -100200, now)
	if err != nil {
		t.Fatalf("group ChatDailyUsage: %v", err)
	}
	if group.ModelCalls != 1 || group.NetworkToolCalls() != 1 || group.Calculator != 0 {
		t.Fatalf("group usage=%+v, want its own 1 model call and 1 network call", group)
	}
}

func TestCountChatUserMessagesSinceCountsOnlyUserRowsOfThisChat(t *testing.T) {
	ctx := context.Background()
	db := newChatLimitsDB(t)
	if err := EnsureChat(ctx, db, 11, 11, ""); err != nil {
		t.Fatal(err)
	}
	if err := EnsureChat(ctx, db, 12, 12, ""); err != nil {
		t.Fatal(err)
	}
	seed := func(chatID int64, role, at string) {
		t.Helper()
		if _, err := Append(ctx, db, Message{ChatID: chatID, UserID: chatID, Role: role, Content: "x"}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE messages SET created_at = ? WHERE id = last_insert_rowid()`, at); err != nil {
			t.Fatal(err)
		}
	}
	seed(11, RoleUser, "2026-05-04 11:59:00")
	seed(11, RoleUser, "2026-05-04 11:59:30")
	seed(11, RoleAssistant, "2026-05-04 11:59:40")
	seed(11, RoleUser, "2026-05-04 11:58:00") // outside the window
	seed(12, RoleUser, "2026-05-04 11:59:10")
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)

	count, err := CountChatUserMessagesSince(ctx, db, 11, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("CountChatUserMessagesSince: %v", err)
	}
	if count != 2 {
		t.Fatalf("count=%d, want 2 user questions in the trailing minute", count)
	}
}

func TestListActiveChatsShowsTypeUsageAndLimits(t *testing.T) {
	ctx := context.Background()
	db := newChatLimitsDB(t)
	if err := EnsureChat(ctx, db, 11, 11, "private chat"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureChat(ctx, db, -100200, 11, "group chat"); err != nil {
		t.Fatal(err)
	}
	// The group is the more active chat: its message is newer than the private one's.
	if _, err := Append(ctx, db, Message{ChatID: 11, UserID: 11, Role: RoleUser, Content: "old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE messages SET created_at = '2026-05-04 08:00:00' WHERE chat_id = 11`); err != nil {
		t.Fatal(err)
	}
	if _, err := Append(ctx, db, Message{ChatID: -100200, UserID: 11, Role: RoleUser, Content: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE messages SET created_at = '2026-05-04 11:00:00' WHERE chat_id = -100200`); err != nil {
		t.Fatal(err)
	}
	if err := SetChatLimits(ctx, db, ChatLimits{ChatID: -100200, DailyModelLimit: 4, DailyNetworkLimit: 2,
		PerMinuteLimit: 1, DisabledTools: []string{ChatToolCalculator}}); err != nil {
		t.Fatal(err)
	}
	insertEvent := func(chatID int64, kind, detail, at string) {
		t.Helper()
		if err := LogChatEvent(ctx, db, chatID, 11, kind, detail); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE events SET created_at = ? WHERE id = last_insert_rowid()`, at); err != nil {
			t.Fatal(err)
		}
	}
	insertEvent(-100200, EventModelCall, "model-a", "2026-05-04 09:00:00")
	insertEvent(-100200, EventModelCall, "model-a", "2026-05-04 10:00:00")
	insertEvent(-100200, EventUsage, ChatToolReadURL, "2026-05-04 10:00:00")
	insertEvent(-100200, EventUsage, ChatToolWebSearch, "2026-05-04 10:00:00")
	insertEvent(-100200, EventUsage, ChatToolCalculator, "2026-05-04 10:00:00")
	insertEvent(-100200, EventModelCall, "model-a", "2026-05-03 10:00:00") // yesterday
	insertEvent(11, EventModelCall, "model-a", "2026-05-04 10:00:00")

	chats, err := ListActiveChatsAt(ctx, db, time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC), 50)
	if err != nil {
		t.Fatalf("ListActiveChatsAt: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("chats=%+v, want two", chats)
	}
	if chats[0].ID != -100200 || chats[0].Type != chatTypeGroupName || chats[0].Title != "group chat" {
		t.Fatalf("newest chat=%+v, want the group first", chats[0])
	}
	if chats[0].Today.ModelCalls != 2 || chats[0].Today.NetworkToolCalls() != 2 || chats[0].Today.Calculator != 1 {
		t.Fatalf("group today=%+v", chats[0].Today)
	}
	if !chats[0].HasLimits || chats[0].Limits.DailyModelLimit != 4 || chats[0].Limits.PerMinuteLimit != 1 {
		t.Fatalf("group limits=%+v has=%v", chats[0].Limits, chats[0].HasLimits)
	}
	if !chats[0].Limits.ToolDisabled(ChatToolCalculator) {
		t.Fatalf("group disabled tools=%v, want calculator", chats[0].Limits.DisabledTools)
	}
	if chats[0].LastActivity != "2026-05-04 11:00:00" {
		t.Fatalf("group last activity=%q", chats[0].LastActivity)
	}
	if chats[1].ID != 11 || chats[1].Type != "private" || chats[1].HasLimits {
		t.Fatalf("private chat=%+v, want no limits row", chats[1])
	}
	if chats[1].Today.ModelCalls != 1 || chats[1].Today.NetworkToolCalls() != 0 {
		t.Fatalf("private today=%+v", chats[1].Today)
	}

	limited, err := ListActiveChatsAt(ctx, db, time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC), 1)
	if err != nil {
		t.Fatalf("limited ListActiveChatsAt: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != -100200 {
		t.Fatalf("limit=1 chats=%+v", limited)
	}
}

func TestMigration020AddsChatLimitsAndEventAttribution(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 19)
	if err := EnsureChat(ctx, db, 11, 11, "before upgrade"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO events (user_id, kind, detail) VALUES (11, 'usage', 'read_url')`); err != nil {
		t.Fatalf("seed a pre-020 event: %v", err)
	}
	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("apply migration 020: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != 20 {
		t.Fatalf("schema version=%d err=%v, want 20", version, err)
	}
	var chatID any
	if err := db.QueryRowContext(ctx, `SELECT chat_id FROM events ORDER BY id LIMIT 1`).Scan(&chatID); err != nil {
		t.Fatalf("read the historical event: %v", err)
	}
	if chatID != nil {
		t.Fatalf("historical event chat_id=%v, want NULL (unattributed)", chatID)
	}
	if _, found, err := GetChatLimits(ctx, db, 11); err != nil || found {
		t.Fatalf("fresh chat_limits found=%v err=%v", found, err)
	}
	if err := SetChatLimits(ctx, db, ChatLimits{ChatID: 11, DailyModelLimit: 3}); err != nil {
		t.Fatalf("write after the upgrade: %v", err)
	}
	if err := LogChatEvent(ctx, db, 11, 11, EventModelCall, "model-a"); err != nil {
		t.Fatalf("LogChatEvent after the upgrade: %v", err)
	}
	var attributed int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE chat_id = 11`).Scan(&attributed); err != nil || attributed != 1 {
		t.Fatalf("attributed events=%d err=%v", attributed, err)
	}
}
