// messages_test.go — the §11 "Store" row (CRUD чатов/сообщений, ClearChat, Stats, UNIQUE guard по
// telegram_message_id, persist bot_state) and the §10 День 2 DoD ("Append/RecentByChat/ClearChat/
// Stats покрыты тестами; рестарт процесса не теряет данные").
//
// The in-memory pools use a unique name per test (§5: `cache=shared` gives ONE database to the
// whole process, so parallel tests of one package would otherwise share rows), and the restart test
// runs on a file database in t.TempDir (§11: in-memory reports journal_mode=memory, never wal).
//
// db.Close() always happens before the temp directory disappears (§5).
package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// memDB — one isolated in-memory database of §5 with the schema of §5 already applied.
func memDB(t *testing.T) *sql.DB {
	t.Helper()
	var db, err = OpenMemory(context.Background(), "mem_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func intPtr(v int64) *int64 { return &v }

// TestAppendRecentAndStats — the happy path of §10 День 2: a user message and the model answer are
// stored, the context comes back oldest → newest, the limit cuts from the oldest side, and Stats
// counts messages and tokens (§6 «/stats»).
func TestAppendRecentAndStats(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	var user, err = Append(ctx, db, Message{ChatID: 11, UserID: 7, Role: RoleUser, Content: "вопрос", TelegramMessageID: intPtr(100)})
	if err != nil {
		t.Fatalf("Append user: %v", err)
	}
	if user <= 0 {
		t.Fatalf("Append returned id %d, want the AUTOINCREMENT id of §5", user)
	}
	if _, err := Append(ctx, db, Message{
		ChatID: 11, UserID: 7, Role: RoleAssistant, Content: "ответ",
		TokensIn: intPtr(12), TokensOut: intPtr(30), Model: "qwen-test", LatencyMs: 250,
	}); err != nil {
		t.Fatalf("Append assistant: %v", err)
	}

	var recent []Message
	recent, err = RecentByChat(ctx, db, 11, 20)
	if err != nil {
		t.Fatalf("RecentByChat: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("RecentByChat returned %d rows, want 2", len(recent))
	}
	if recent[0].Role != RoleUser || recent[1].Role != RoleAssistant {
		t.Errorf("the context is not chronological: %s then %s (§5 glues history oldest → newest)", recent[0].Role, recent[1].Role)
	}
	if recent[1].Model != "qwen-test" || recent[1].LatencyMs != 250 || *recent[1].TokensOut != 30 {
		t.Errorf("the answer row lost its metadata: model=%q latency=%d tokensOut=%v (§5 columns)",
			recent[1].Model, recent[1].LatencyMs, recent[1].TokensOut)
	}
	if recent[1].TelegramMessageID != nil {
		t.Error("a model answer has no telegram_message_id (§5: NULL для ответов модели)")
	}
	if recent[0].TelegramMessageID == nil || *recent[0].TelegramMessageID != 100 {
		t.Errorf("the user row lost telegram_message_id: %v", recent[0].TelegramMessageID)
	}

	var limited []Message
	limited, err = RecentByChat(ctx, db, 11, 1)
	if err != nil {
		t.Fatalf("RecentByChat limit: %v", err)
	}
	if len(limited) != 1 || limited[0].Role != RoleAssistant {
		t.Errorf("LIMIT 1 must keep the NEWEST row (§5 idx_messages_chat_time DESC), got %+v", limited)
	}

	var stats ChatStats
	stats, err = Stats(ctx, db, 11)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Messages != 2 || stats.TokensIn != 12 || stats.TokensOut != 30 {
		t.Errorf("Stats = %+v, want 2 messages / 12 in / 30 out (§6 /stats)", stats)
	}
	if stats.Model != "qwen-test" || stats.LastAt == "" {
		t.Errorf("Stats = %+v, want the model name and the last timestamp", stats)
	}
}

// TestAppendRejectsBadRoleAndEmptyContent — the §5 CHECK('user','assistant','system') and the NOT
// NULL content are enforced in the Go layer, so a caller mistake names the rule instead of
// surfacing as an sqlite constraint code (§12 reliability).
func TestAppendRejectsBadRoleAndEmptyContent(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	if _, err := Append(ctx, db, Message{ChatID: 12, UserID: 7, Role: "tool", Content: "x"}); err == nil {
		t.Error("role 'tool' is outside the §5 CHECK and must be rejected")
	} else if err != ErrRole {
		t.Errorf("Append role error = %v, want ErrRole", err)
	}
	if _, err := Append(ctx, db, Message{ChatID: 12, UserID: 7, Role: RoleUser, Content: "   "}); err == nil {
		t.Error("an empty content must be rejected (§5 messages.content is NOT NULL)")
	}
}

// TestAppendRegistersChat — messages REFERENCES chats(chat_id) with foreign_keys=1 of the DSN (§5),
// so Append of a chat that was never /start-ed must register the row itself instead of failing on
// the foreign key (§6: an incoming text can arrive before /start).
func TestAppendRegistersChat(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	if _, err := Append(ctx, db, Message{ChatID: 4242, UserID: 99, Role: RoleUser, Content: "привет"}); err != nil {
		t.Fatalf("Append into an unregistered chat: %v (§5 foreign_keys=1 would reject it)", err)
	}
	var stats, err = Stats(ctx, db, 4242)
	if err != nil || stats.Messages != 1 {
		t.Fatalf("Stats after the implicit registration = %+v err=%v", stats, err)
	}
	if err := EnsureChat(ctx, db, 4242, 99, "группа"); err != nil {
		t.Fatalf("EnsureChat title update: %v", err)
	}
	var title string
	if err := db.QueryRowContext(ctx, `SELECT title FROM chats WHERE chat_id = 4242`).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "группа" {
		t.Errorf("title = %q, want the title of the later /start (§6)", title)
	}
	// A later Append must not erase the title: EnsureChat keeps the existing one when the new
	// title is empty (private chats give no title).
	if _, err := Append(ctx, db, Message{ChatID: 4242, UserID: 99, Role: RoleUser, Content: "ещё"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT title FROM chats WHERE chat_id = 4242`).Scan(&title); err != nil || title != "группа" {
		t.Errorf("title after Append = %q err=%v, want it kept", title, err)
	}
}

// TestDuplicateTelegramMessageID — §10 День 2: «повторный Append с тем же telegram_message_id →
// UNIQUE constraint». This is the second line of defence against repeated answers after a restart
// (§2 п.7, §13 risk), and IsDuplicate is what lets День 4 answer "already processed" instead of
// treating the failure as a broken database.
func TestDuplicateTelegramMessageID(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	if _, err := Append(ctx, db, Message{ChatID: 21, UserID: 5, Role: RoleUser, Content: "первый", TelegramMessageID: intPtr(777)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	var again, err = Append(ctx, db, Message{ChatID: 21, UserID: 5, Role: RoleUser, Content: "второй", TelegramMessageID: intPtr(777)})
	if err == nil {
		t.Fatalf("the second Append of telegram_message_id 777 passed (id %d): uq_messages_chat_tg_id of §5 is not enforced", again)
	}
	if !IsDuplicate(err) {
		t.Errorf("IsDuplicate(%v) = false, want true (§5 names this constraint for the caller)", err)
	}
	// The same Telegram id in ANOTHER chat is a different message (§5 indexes by chat_id +
	// telegram_message_id, and forwarded messages repeat the id across chats).
	if _, err := Append(ctx, db, Message{ChatID: 22, UserID: 5, Role: RoleUser, Content: "переслан", TelegramMessageID: intPtr(777)}); err != nil {
		t.Errorf("Append of the same telegram id into another chat: %v (§5 index is per chat)", err)
	}
	// A NULL telegram_message_id never collides: two model answers of one chat coexist.
	for i := 0; i < 2; i++ {
		if _, err := Append(ctx, db, Message{ChatID: 21, UserID: 5, Role: RoleAssistant, Content: "ответ"}); err != nil {
			t.Errorf("Append model answer %d: %v (§5: NULL is outside the UNIQUE index)", i, err)
		}
	}
}

// TestClearChatHidesContextKeepsHistory — §5 «/reset ставит context_reset_at (история не
// удаляется — /history её видит)» and §6 «/reset → очистить контекст чата».
//
// The last part of the test is the reason the id watermark exists: created_at counts SECONDS, so a
// message written in the same second as /reset would be hidden forever if the filter compared
// timestamps (§10 День 2: the context must stay exact).
func TestClearChatHidesContextKeepsHistory(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	for _, text := range []string{"раз", "два"} {
		if _, err := Append(ctx, db, Message{ChatID: 31, UserID: 3, Role: RoleUser, Content: text}); err != nil {
			t.Fatalf("Append %s: %v", text, err)
		}
	}
	var hidden, err = ClearChat(ctx, db, 31)
	if err != nil {
		t.Fatalf("ClearChat: %v", err)
	}
	if hidden != 2 {
		t.Errorf("ClearChat reported %d hidden messages, want 2", hidden)
	}

	var recent []Message
	recent, err = RecentByChat(ctx, db, 31, 20)
	if err != nil {
		t.Fatalf("RecentByChat after /reset: %v", err)
	}
	if len(recent) != 0 {
		t.Errorf("the context after /reset still holds %d rows: %+v", len(recent), recent)
	}

	var history []Message
	history, err = HistoryOfChat(ctx, db, 31, 20)
	if err != nil {
		t.Fatalf("HistoryOfChat: %v", err)
	}
	if len(history) != 2 {
		t.Errorf("/history after /reset returned %d rows, want the 2 stored rows (§5: история не удаляется)", len(history))
	}

	// The message of the SAME SECOND as /reset must reach the context — the watermark rule.
	if _, err := Append(ctx, db, Message{ChatID: 31, UserID: 3, Role: RoleUser, Content: "после сброса"}); err != nil {
		t.Fatalf("Append after /reset: %v", err)
	}
	var after []Message
	after, err = RecentByChat(ctx, db, 31, 20)
	if err != nil {
		t.Fatalf("RecentByChat: %v", err)
	}
	if len(after) != 1 || after[0].Content != "после сброса" {
		t.Errorf("the message right after /reset is not in the context: %+v (the second resolution of created_at must not hide it)", after)
	}
}

// TestEventsAndStatsErrors — §6 /stats counts errors, and §10 День 3/День 4 write events of the
// kinds denied / model_error / flood (§5 events table).
func TestEventsAndStatsErrors(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	if _, err := Append(ctx, db, Message{ChatID: 41, UserID: 8, Role: RoleUser, Content: "вопрос"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := LogEvent(ctx, db, 8, EventDenied, "user 8 is not in TELEGRAM_ALLOWED_USERS"); err != nil {
		t.Fatalf("LogEvent denied: %v", err)
	}
	if err := LogEvent(ctx, db, 8, EventModelError, "strata 500 after 2 attempts"); err != nil {
		t.Fatalf("LogEvent model_error: %v", err)
	}
	var denied, err = CountEvents(ctx, db, EventDenied)
	if err != nil || denied != 1 {
		t.Errorf("CountEvents(denied) = %d err=%v, want 1", denied, err)
	}
	var stats ChatStats
	stats, err = Stats(ctx, db, 41)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Errors != 1 {
		t.Errorf("Stats.Errors = %d, want the one model_error of the chat's user (§6 /stats)", stats.Errors)
	}
	if stats.Messages != 1 {
		t.Errorf("Stats.Messages = %d, want 1 (the events table is not the history)", stats.Messages)
	}
}

// TestBotStateOffsetRoundTrip — §5 bot_state: the offset of getUpdates is a string, and the two
// helpers keep the "the key does not exist yet" distinction (§10 День 4: a fresh database must not
// start with bot.WithInitialOffset(0)).
func TestBotStateOffsetRoundTrip(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	var offset, found, err = LastUpdateID(ctx, db)
	if err != nil {
		t.Fatalf("LastUpdateID on a fresh database: %v", err)
	}
	if found {
		t.Errorf("LastUpdateID reported found=true on a fresh database (offset %d): §10 День 4 would set WithInitialOffset(0)", offset)
	}

	if err := SetLastUpdateID(ctx, db, 4242); err != nil {
		t.Fatalf("SetLastUpdateID: %v", err)
	}
	var stored, found2, err2 = LastUpdateID(ctx, db)
	if err2 != nil || !found2 || stored != 4242 {
		t.Errorf("LastUpdateID = %d found=%v err=%v, want 4242/true/nil", stored, found2, err2)
	}
	// A corrupted row is an error, not a silent 0 (§13 risk «повторные ответы»).
	if err := SetState(ctx, db, StateLastUpdateID, "not-a-number"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if _, _, err := LastUpdateID(ctx, db); err == nil {
		t.Error("LastUpdateID accepted a non-integer offset (§5 bot_state.value is the offset of getUpdates)")
	}
}

// TestRestartKeepsHistoryAndOffset — the §10 День 2 DoD «рестарт процесса не теряет данные (тест:
// закрыть store, открыть заново, проверить историю и bot_state)». A FILE database in t.TempDir is
// the only honest way to prove it: in-memory is journal_mode=memory and disappears with the pool
// (§11 Store (WAL), §5).
func TestRestartKeepsHistoryAndOffset(t *testing.T) {
	var ctx = context.Background()
	var path = filepath.Join(t.TempDir(), "restart.db")

	var db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := Append(ctx, db, Message{ChatID: 51, UserID: 6, Role: RoleUser, Content: "до рестарта", TelegramMessageID: intPtr(900)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := Append(ctx, db, Message{ChatID: 51, UserID: 6, Role: RoleAssistant, Content: "ответ до рестарта", Model: "qwen-test", LatencyMs: 10}); err != nil {
		t.Fatalf("Append answer: %v", err)
	}
	if err := SetLastUpdateID(ctx, db, 12345); err != nil {
		t.Fatalf("SetLastUpdateID: %v", err)
	}
	// The UNIQUE guard has to survive the reopen as well: the whole point of §2 п.7 is that the
	// second delivery of the same Telegram update after a restart cannot produce a second answer.
	var duplicate, dupErr = Append(ctx, db, Message{ChatID: 51, UserID: 6, Role: RoleUser, Content: "до рестарта", TelegramMessageID: intPtr(900)})
	if dupErr == nil || !IsDuplicate(dupErr) {
		t.Errorf("duplicate before restart passed (id %d err %v)", duplicate, dupErr)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var reopened, err2 = Open(ctx, path)
	if err2 != nil {
		t.Fatalf("reopen: %v", err2)
	}
	defer reopened.Close()

	var recent, err3 = RecentByChat(ctx, reopened, 51, 20)
	if err3 != nil {
		t.Fatalf("RecentByChat after reopen: %v", err3)
	}
	if len(recent) != 2 {
		t.Errorf("after reopen the history has %d rows, want the 2 stored rows (§10 День 2 DoD)", len(recent))
	}
	var offset, found, err4 = LastUpdateID(ctx, reopened)
	if err4 != nil || !found || offset != 12345 {
		t.Errorf("bot_state after reopen = %d found=%v err=%v, want 12345/true/nil (§5: the offset of getUpdates survives a restart)", offset, found, err4)
	}
	if _, err5 := Append(ctx, reopened, Message{ChatID: 51, UserID: 6, Role: RoleUser, Content: "тот же update повторно", TelegramMessageID: intPtr(900)}); err5 == nil || !IsDuplicate(err5) {
		t.Errorf("the replayed telegram id was accepted after reopen: %v (§13 risk «повторные ответы»)", err5)
	}
}
