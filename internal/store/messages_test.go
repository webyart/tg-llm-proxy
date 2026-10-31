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

// TestStatsModelIsTheLastAnswer — the «Модель: …» line of §6 /stats has to name the model that answered
// LAST, not the largest name ever stored. Measured 2026-10-31 in a real chat: its older rows were answered
// by qwen3.8-flash-next-nvfp4-nvfp4-nvidia and the newer ones by qwen3.8-flash-next-iq3_s, while the
// aggregate MAX(model) kept printing the older name — MAX over TEXT is lexicographic, not chronological.
// The second answer below deliberately uses a name that sorts BEFORE the first one, so a lexicographic
// reading and a chronological one cannot agree.
func TestStatsModelIsTheLastAnswer(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	if _, err := Append(ctx, db, Message{ChatID: 43, UserID: 9, Role: RoleUser, Content: "первый вопрос"}); err != nil {
		t.Fatalf("Append the first user row: %v", err)
	}
	if _, err := Append(ctx, db, Message{ChatID: 43, UserID: 9, Role: RoleAssistant, Content: "старый ответ", Model: "zzz-model-older"}); err != nil {
		t.Fatalf("Append the older answer: %v", err)
	}
	if _, err := Append(ctx, db, Message{ChatID: 43, UserID: 9, Role: RoleUser, Content: "второй вопрос"}); err != nil {
		t.Fatalf("Append the second user row: %v", err)
	}
	if _, err := Append(ctx, db, Message{ChatID: 43, UserID: 9, Role: RoleAssistant, Content: "новый ответ", Model: "aaa-model-latest"}); err != nil {
		t.Fatalf("Append the newest answer: %v", err)
	}

	var stats, err = Stats(ctx, db, 43)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Model != "aaa-model-latest" {
		t.Errorf("Stats.Model = %q, want the model of the LAST answer (§6 /stats: the line reads as «which model serves me now»)", stats.Model)
	}
	if stats.Messages != 4 {
		t.Errorf("Stats.Messages = %d, want 4 rows of the chat (§5 messages)", stats.Messages)
	}
}

// TestStatsModelEmptyBeforeAnyAnswer — a chat with only user rows has no model to name: §6 /stats prints
// the line only when it is not empty, so an empty value must come back without an error (§5 messages.model
// is NULL for a user row and for a failed model call, §10 День 3).
func TestStatsModelEmptyBeforeAnyAnswer(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	if _, err := Append(ctx, db, Message{ChatID: 44, UserID: 10, Role: RoleUser, Content: "вопрос без ответа"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	var stats, err = Stats(ctx, db, 44)
	if err != nil {
		t.Fatalf("Stats on a chat nothing has answered yet: %v", err)
	}
	if stats.Model != "" {
		t.Errorf("Stats.Model = %q, want an empty string (§10 День 3: a failed call stores no assistant row)", stats.Model)
	}
	if stats.Messages != 1 {
		t.Errorf("Stats.Messages = %d, want 1 (the unanswered question IS stored, the answer is not)", stats.Messages)
	}
}

// TestStatsCountsTimeoutsApartFromErrors — §5 keeps kind='model_error' and kind='timeout' as different service
// events, and §6 /stats has to show both numbers: measured 2026-10-31 in a live chat, one timeout left the
// error counter reading «Ошибок модели: 3», i.e. the counter stayed at the value from before the failure.
// A kind /stats does not count must also not break the reading (§5 keeps kind free TEXT on purpose).
func TestStatsCountsTimeoutsApartFromErrors(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	if _, err := Append(ctx, db, Message{ChatID: 45, UserID: 12, Role: RoleUser, Content: "вопрос"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	for _, row := range []struct{ kind, detail string }{
		{EventModelError, "upstream 502"},
		{EventModelError, "empty_answer"},
		{EventTimeout, "STRATA_TIMEOUT_SECONDS 15s spent"},
		{EventTimeout, "STRATA_TIMEOUT_SECONDS 15s spent"},
		{EventTimeout, "TG_REPLY_DEADLINE_SECONDS 30s spent"},
		{EventFlood, "retry_after 5 s"},
	} {
		if err := LogEvent(ctx, db, 12, row.kind, row.detail); err != nil {
			t.Fatalf("LogEvent %s: %v", row.kind, err)
		}
	}

	var stats, err = Stats(ctx, db, 45)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Errors != 2 {
		t.Errorf("Stats.Errors = %d, want 2 rows of kind='model_error' (§5)", stats.Errors)
	}
	if stats.Timeouts != 3 {
		t.Errorf("Stats.Timeouts = %d, want 3 rows of kind='timeout' (§7 budgets, §5 events)", stats.Timeouts)
	}

	// The errors of another user must not leak into this chat: events are keyed by user_id of §5, and the chat
	// row of §5 is what ties the two together.
	if err := LogEvent(ctx, db, 99, EventTimeout, "another user"); err != nil {
		t.Fatalf("LogEvent for another user: %v", err)
	}
	var again, againErr = Stats(ctx, db, 45)
	if againErr != nil {
		t.Fatalf("Stats the second time: %v", againErr)
	}
	if again.Timeouts != 3 {
		t.Errorf("Stats.Timeouts = %d after an event of another user, want 3 (events belong to the chat's user_id)", again.Timeouts)
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

// TestLastVisibleMessageAndRowID — the two reads the dedup guard of §2 п.7 needs, written after the §11 item
// «Ctrl-C / docker compose down при длинном /ask» showed that «a row of this update exists» and «this question
// was answered» are not the same fact: stored of §10 День 4 writes role='user' BEFORE the model call, while the
// offset of §5 is acknowledged only after the handler, so a process stopped mid-generation leaves the question
// in the database with no answer. The pair answers the question update by update: RowIDByTelegramMessageID
// locates the row of THIS update, LastVisibleMessage names the newest row the context of §5 sees, and an
// unanswered question is the case where those two are the same row.
func TestLastVisibleMessageAndRowID(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	// A chat with no rows: no id, no role, no error — and the row of an update that never came is 0.
	if id, role, err := LastVisibleMessage(ctx, db, 40); err != nil || id != 0 || role != "" {
		t.Fatalf("a chat with no rows must report no row at all, got id=%d role=%q err=%v", id, role, err)
	}
	if id, err := RowIDByTelegramMessageID(ctx, db, 40, 940); err != nil || id != 0 {
		t.Fatalf("a chat that never saw the update must report id 0, got %d err=%v", id, err)
	}

	// The state §11 is about: the question is stored, nothing answered it.
	var questionID, err = Append(ctx, db, Message{ChatID: 40, UserID: 9, Role: RoleUser, Content: "вопрос без ответа", TelegramMessageID: intPtr(940)})
	if err != nil {
		t.Fatalf("Append user: %v", err)
	}
	var lastID int64
	var lastRole string
	if lastID, lastRole, err = LastVisibleMessage(ctx, db, 40); err != nil || lastID != questionID || lastRole != RoleUser {
		t.Fatalf("the chat that ends with an unanswered question must report its own row, got id=%d role=%q err=%v", lastID, lastRole, err)
	}
	if rowID, rowErr := RowIDByTelegramMessageID(ctx, db, 40, 940); rowErr != nil || rowID != questionID {
		t.Fatalf("RowIDByTelegramMessageID must find the row of the update, got %d err=%v (§5 uq_messages_chat_tg_id)", rowID, rowErr)
	}

	// The answer of §6 is the next row of the same chat (§10 День 3 «ровно одна запись assistant»), and from now
	// on the row of the update is NOT the newest one — which is precisely what lets the guard stay silent.
	var answerID, appendErr = Append(ctx, db, Message{ChatID: 40, UserID: 9, Role: RoleAssistant, Content: "ответ", Model: "qwen-test"})
	if appendErr != nil {
		t.Fatalf("Append assistant: %v", appendErr)
	}
	if lastID, lastRole, err = LastVisibleMessage(ctx, db, 40); err != nil || lastID != answerID || lastRole != RoleAssistant {
		t.Fatalf("the chat that ends with an answer must report that answer, got id=%d role=%q err=%v", lastID, lastRole, err)
	}

	// The case a decision made on the ROLE alone would get wrong: the user asked twice, and the chat now ends
	// with the row of the second question. Replaying the FIRST update must not produce a second answer to a
	// question that was answered — the row of the first update is no longer the newest row of §5.
	var secondQuestionID, secondErr = Append(ctx, db, Message{ChatID: 40, UserID: 9, Role: RoleUser, Content: "второй вопрос", TelegramMessageID: intPtr(941)})
	if secondErr != nil {
		t.Fatalf("Append the second question: %v", secondErr)
	}
	if lastID, lastRole, err = LastVisibleMessage(ctx, db, 40); err != nil || lastID != secondQuestionID || lastRole != RoleUser {
		t.Fatalf("the newest row must be the second question, got id=%d role=%q err=%v", lastID, lastRole, err)
	}
	var firstRowID, firstErr = RowIDByTelegramMessageID(ctx, db, 40, 940)
	if firstErr != nil {
		t.Fatalf("RowIDByTelegramMessageID for the first update: %v", firstErr)
	}
	if firstRowID != questionID || firstRowID == lastID {
		t.Fatalf("the first update must own an older row than the newest one: row=%d newest=%d (the guard has to stay silent on its redelivery)", firstRowID, lastID)
	}
	var secondRowID, secondRowErr = RowIDByTelegramMessageID(ctx, db, 40, 941)
	if secondRowErr != nil || secondRowID != secondQuestionID || secondRowID != lastID {
		t.Fatalf("the second update owns the newest row (§11: its answer is what the restart still owes), got row=%d newest=%d err=%v", secondRowID, lastID, secondRowErr)
	}

	// A /reset of §6 hides the history from the context of §5 — and LastVisibleMessage follows that window,
	// because the rows under the watermark of ClearChat are not the turns the next model request would end with.
	// RowIDByTelegramMessageID deliberately ignores the window: the row of an update is a fact about the incoming
	// side, so the guard sees «this row exists but is not the newest visible row» and stays silent.
	if _, clearErr := ClearChat(ctx, db, 40); clearErr != nil {
		t.Fatalf("ClearChat: %v", clearErr)
	}
	if lastID, lastRole, err = LastVisibleMessage(ctx, db, 40); err != nil || lastID != 0 || lastRole != "" {
		t.Fatalf("after /reset the hidden rows must not be reported as visible, got id=%d role=%q err=%v", lastID, lastRole, err)
	}
	if rowID, rowErr := RowIDByTelegramMessageID(ctx, db, 40, 941); rowErr != nil || rowID != secondQuestionID {
		t.Fatalf("the row of an update survives a /reset (it is not a fact about the context of §5), got %d err=%v", rowID, rowErr)
	}
	if rows, histErr := HistoryOfChat(ctx, db, 40, 10); histErr != nil || len(rows) != 3 {
		t.Fatalf("/history still sees the hidden rows (§6), got %d rows err=%v", len(rows), histErr)
	}

	// chat_id of §5 keeps the chats apart: neither read leaks a row of another chat.
	if _, otherErr := Append(ctx, db, Message{ChatID: 41, UserID: 9, Role: RoleAssistant, Content: "чужой ответ"}); otherErr != nil {
		t.Fatalf("Append of the other chat: %v", otherErr)
	}
	if otherID, otherRole, otherReadErr := LastVisibleMessage(ctx, db, 41); otherReadErr != nil || otherID == 0 || otherRole != RoleAssistant {
		t.Fatalf("the other chat keeps its own newest row, got id=%d role=%q err=%v", otherID, otherRole, otherReadErr)
	}
	if lastID, lastRole, err = LastVisibleMessage(ctx, db, 40); err != nil || lastID != 0 || lastRole != "" {
		t.Fatalf("the other chat must not leak into chat 40, got id=%d role=%q err=%v", lastID, lastRole, err)
	}
	if rowID, rowErr := RowIDByTelegramMessageID(ctx, db, 41, 941); rowErr != nil || rowID != 0 {
		t.Fatalf("the telegram id of a row belonging to another chat must not be found here, got %d err=%v (§5 uq_messages_chat_tg_id is per chat)", rowID, rowErr)
	}
}

// TestUnansweredQuestions — the scan the §15 item «догонять вопрос, оборванный внутри обработчика» runs at the
// start of the process. §11 measured why it has to exist: the stop left the row of the question in §5 and the
// offset behind it, and Telegram did not hand the update back, so no incoming update ever asked the guard of
// §2 п.7 about that question again. Here the database is asked per chat, with no update in hand.
func TestUnansweredQuestions(t *testing.T) {
	var db = memDB(t)
	var ctx = context.Background()

	// Nothing stored at all: nothing to chase, and no error.
	if rows, err := UnansweredQuestions(ctx, db, 10); err != nil || len(rows) != 0 {
		t.Fatalf("an empty database must return no question, got %d rows err=%v", len(rows), err)
	}

	// The state §15 is about: the chat ends with a question that has no answer of §6 after it.
	var waitingQuestion, err = Append(ctx, db, Message{ChatID: 51, UserID: 7, Role: RoleUser,
		Content: "вопрос, до которого не дошёл процесс", TelegramMessageID: intPtr(950)})
	if err != nil {
		t.Fatalf("Append of the waiting question: %v", err)
	}

	// A chat that was answered: the newest visible row is the assistant row, so the chat is caught up
	// (§10 День 3 «ровно одна запись assistant» writes it before sending).
	if _, err = Append(ctx, db, Message{ChatID: 52, UserID: 7, Role: RoleUser, Content: "отвеченный вопрос", TelegramMessageID: intPtr(951)}); err != nil {
		t.Fatalf("Append of the answered question: %v", err)
	}
	if _, err = Append(ctx, db, Message{ChatID: 52, UserID: 7, Role: RoleAssistant, Content: "ответ", Model: "qwen-test"}); err != nil {
		t.Fatalf("Append of the answer: %v", err)
	}

	// A question row without a telegram_message_id has no uq_messages_chat_tg_id of §5 to make a replay
	// idempotent, so the scan refuses to chase it (§13 риск «повторные ответы»).
	if _, err = Append(ctx, db, Message{ChatID: 53, UserID: 7, Role: RoleUser, Content: "строка без telegram_message_id"}); err != nil {
		t.Fatalf("Append of the row without a telegram id: %v", err)
	}

	// `/reset` of §6 hides the rows rather than deleting them: a question the user chose to forget is not a
	// question the bot should answer on its own.
	if _, err = Append(ctx, db, Message{ChatID: 54, UserID: 7, Role: RoleUser, Content: "вопрос до /reset", TelegramMessageID: intPtr(954)}); err != nil {
		t.Fatalf("Append before /reset: %v", err)
	}
	if _, err = ClearChat(ctx, db, 54); err != nil {
		t.Fatalf("ClearChat: %v", err)
	}

	// The case a decision made on the ROLE alone would get wrong, now on the scan side: the user asked twice
	// and the chat ends with the second question. Only that one is waiting — the first question already has
	// its answer, and answering it again is exactly the §13 risk.
	if _, err = Append(ctx, db, Message{ChatID: 55, UserID: 7, Role: RoleUser, Content: "первый вопрос, отвечен", TelegramMessageID: intPtr(955)}); err != nil {
		t.Fatalf("Append of the first question: %v", err)
	}
	if _, err = Append(ctx, db, Message{ChatID: 55, UserID: 7, Role: RoleAssistant, Content: "ответ на первый", Model: "qwen-test"}); err != nil {
		t.Fatalf("Append of the first answer: %v", err)
	}
	var secondQuestionID, secondErr = Append(ctx, db, Message{ChatID: 55, UserID: 7, Role: RoleUser,
		Content: "второй вопрос, оборванный", TelegramMessageID: intPtr(956)})
	if secondErr != nil {
		t.Fatalf("Append of the second question: %v", secondErr)
	}

	var rows, readErr = UnansweredQuestions(ctx, db, 10)
	if readErr != nil {
		t.Fatalf("UnansweredQuestions: %v", readErr)
	}
	// Chats 52 (answered), 53 (no telegram id) and 54 (hidden by /reset) must all stay out; 51 and 55 must be
	// there, newest activity first — the last_active_at of every row here lands in the same second, so the
	// chat_id tie-break of the ORDER BY decides and 55 comes first.
	if len(rows) != 2 {
		t.Fatalf("exactly the two chats that end with an unanswered question must be reported, got %d: %+v", len(rows), rows)
	}
	if rows[0].ChatID != 55 || rows[0].ID != secondQuestionID {
		t.Fatalf("the second question of a twice-asked chat must be the one that is waiting, got chat=%d row=%d (%d expected)",
			rows[0].ChatID, rows[0].ID, secondQuestionID)
	}
	if rows[1].ChatID != 51 || rows[1].ID != waitingQuestion {
		t.Fatalf("the first chat must keep its own waiting question, got chat=%d row=%d (%d expected)",
			rows[1].ChatID, rows[1].ID, waitingQuestion)
	}
	// The replay of §6 is built from these two fields: the text goes to the model, the telegram_message_id is
	// what makes uq_messages_chat_tg_id of §5 reject the second question row.
	if rows[1].Content != "вопрос, до которого не дошёл процесс" || rows[1].TelegramMessageID == nil || *rows[1].TelegramMessageID != 950 {
		t.Fatalf("the row must carry its text and its telegram id, got %q / %v", rows[1].Content, rows[1].TelegramMessageID)
	}
	// messages of §5 has no user_id column — the chats row of §5 supplies the one the allowlist of §7 needs.
	if rows[1].UserID != 7 {
		t.Fatalf("the user_id of the chats row must come along, got %d", rows[1].UserID)
	}
	if rows[1].CreatedAt == "" {
		t.Fatalf("the log of the replay names the age of the row, so created_at must be read, got %q", rows[1].CreatedAt)
	}

	// §9: the scan is bounded. The chats are looked at newest-first, so the limit costs the OLDEST activity —
	// not the question the user is waiting for.
	if limited, limitErr := UnansweredQuestions(ctx, db, 1); limitErr != nil || len(limited) != 1 || limited[0].ChatID != 55 {
		t.Fatalf("limit=1 must return the newest chat only, got %+v err=%v", limited, limitErr)
	}
	if zero, zeroErr := UnansweredQuestions(ctx, db, 0); zeroErr != nil || len(zero) != 0 {
		t.Fatalf("limit=0 must ask for nothing, got %d rows err=%v", len(zero), zeroErr)
	}
}
