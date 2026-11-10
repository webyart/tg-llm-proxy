package bot

import (
	"context"
	"strings"
	"testing"

	tgmodels "github.com/go-telegram/bot/models"

	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
)

// groupEvent — an update of a supergroup: a negative chat id, and the sender is a user, never the chat.
func groupEvent(chatID int64, userID int64, telegramMessageID int64, text string) Event {
	var ev = testEvent(chatID, userID, telegramMessageID, text)
	ev.ChatType = chatTypeSupergroup
	return ev
}

func groupTestConfig(t *testing.T, overrides map[string]string) config.Config {
	t.Helper()
	var raw = map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11",
		"GROUPS_ENABLED":         "true",
	}
	var k string
	for k = range overrides {
		raw[k] = overrides[k]
	}
	return testConfig(t, raw)
}

func chatUserOf(t *testing.T, d *Dispatcher, chatID int64) int64 {
	t.Helper()
	var userID int64
	if err := d.deps.DB.QueryRowContext(context.Background(), `SELECT user_id FROM chats WHERE chat_id = ?`, chatID).Scan(&userID); err != nil {
		t.Fatalf("chats row of %d: %v", chatID, err)
	}
	return userID
}

func chatsWithID(t *testing.T, d *Dispatcher, chatID int64) int {
	t.Helper()
	var count int
	if err := d.deps.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM chats WHERE chat_id = ?`, chatID).Scan(&count); err != nil {
		t.Fatalf("count chats %d: %v", chatID, err)
	}
	return count
}

func userMessageContent(t *testing.T, d *Dispatcher, chatID int64) string {
	t.Helper()
	var rows, err = store.HistoryOfChat(context.Background(), d.deps.DB, chatID, 10)
	if err != nil {
		t.Fatalf("HistoryOfChat: %v", err)
	}
	var i = 0
	for i < len(rows) {
		if rows[i].Role == store.RoleUser {
			return rows[i].Content
		}
		i++
	}
	return ""
}

// TestPrivateChatIsUnchangedByGroupMode — the gate must be a no-op for a positive chat id, with group
// mode both off and on.
func TestPrivateChatIsUnchangedByGroupMode(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		t.Run("GROUPS_ENABLED="+enabled, func(t *testing.T) {
			var cfg = groupTestConfig(t, map[string]string{"GROUPS_ENABLED": enabled})
			var m = &fakeModel{response: model.Response{Content: "ответ"}}
			var r = &fakeReplier{}
			var d = newTestDispatcher(t, cfg, m, r, testStore(t))

			if err := dispatch(t, d, testEvent(100, 11, 501, "привет")); err != nil {
				t.Fatalf("private dispatch: %v", err)
			}
			if m.callCount() != 1 {
				t.Fatalf("private model calls = %d, want 1", m.callCount())
			}
			if r.count() != 1 || r.sent[0].chatID != 100 {
				t.Fatalf("private replies = %+v, want one answer to chat 100", r.sent)
			}
			if got := userMessageContent(t, d, 100); got != "привет" {
				t.Fatalf("private stored question = %q, want %q", got, "привет")
			}
		})
	}
}

// TestGroupDisabledIsIgnoredEntirely — no answer, no model call, no daily-quota reservation, no
// message rows; the update is acknowledged so Telegram does not replay it forever.
func TestGroupDisabledIsIgnoredEntirely(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "USER_DAILY_MODEL_LIMIT": "5"})
	var m = &fakeModel{response: model.Response{Content: "не должно быть"}}
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))
	var ev = groupEvent(-100500, 11, 600, "@TestBot привет")

	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("disabled group dispatch: %v", err)
	}
	if m.callCount() != 0 {
		t.Fatalf("disabled group called the model %d times", m.callCount())
	}
	if r.count() != 0 {
		t.Fatalf("disabled group was answered: %+v", r.sent)
	}
	used, err := store.DailyQuotaUsed(context.Background(), d.deps.DB, 11, d.now().UTC(), store.DailyQuotaModel)
	if err != nil || used != 0 {
		t.Fatalf("disabled group spent the daily quota: used=%d err=%v", used, err)
	}
	rows, err := store.HistoryOfChat(context.Background(), d.deps.DB, -100500, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("disabled group stored messages: rows=%+v err=%v", rows, err)
	}
	if got := chatsWithID(t, d, -100500); got != 0 {
		t.Fatalf("disabled group created %d chats rows", got)
	}
	offset, found, err := store.LastUpdateID(context.Background(), d.deps.DB)
	if err != nil || !found || offset != ev.UpdateID {
		t.Fatalf("disabled group offset = %d found=%v err=%v, want %d acknowledged", offset, found, err, ev.UpdateID)
	}
}

// TestGroupMentionAnswersWithTheMentionStripped — the accepted text is stored without the mention and
// the chat is registered under its own id with the sender as its user.
func TestGroupMentionAnswersWithTheMentionStripped(t *testing.T) {
	var cfg = groupTestConfig(t, map[string]string{"USER_DAILY_MODEL_LIMIT": "5"})
	var m = &fakeModel{response: model.Response{Content: "ответ"}}
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))
	var ev = groupEvent(-100500, 11, 601, "  @TestBot   что   такое Go?  ")

	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("mention dispatch: %v", err)
	}
	if m.callCount() != 1 {
		t.Fatalf("mention model calls = %d, want 1", m.callCount())
	}
	if got := m.lastRequest().Messages[len(m.lastRequest().Messages)-1].Content; got != "что   такое Go?" {
		t.Fatalf("model received %q, want the mention stripped", got)
	}
	if r.count() != 1 || r.sent[0].chatID != -100500 {
		t.Fatalf("mention replies = %+v, want one answer to the group", r.sent)
	}
	if got := userMessageContent(t, d, -100500); got != "что   такое Go?" {
		t.Fatalf("stored group question = %q, want the mention stripped", got)
	}
	if got := chatUserOf(t, d, -100500); got != 11 {
		t.Fatalf("chats.user_id of the group = %d, want the sender 11", got)
	}
	used, err := store.DailyQuotaUsed(context.Background(), d.deps.DB, 11, d.now().UTC(), store.DailyQuotaModel)
	if err != nil || used != 1 {
		t.Fatalf("accepted group question spent daily quota %d err=%v, want 1", used, err)
	}
}

// TestGroupReplyToBotAnswers — a reply to the bot's own message needs no mention.
func TestGroupReplyToBotAnswers(t *testing.T) {
	var cfg = groupTestConfig(t, nil)
	var m = &fakeModel{response: model.Response{Content: "ответ"}}
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))
	var ev = groupEvent(-100500, 11, 602, "а что дальше?")
	ev.ReplyToMessageID = 99
	ev.ReplyToUserID = 777
	ev.ReplyToUsername = "TestBot"
	ev.ReplyToIsBot = true

	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("reply dispatch: %v", err)
	}
	if m.callCount() != 1 {
		t.Fatalf("reply model calls = %d, want 1", m.callCount())
	}
	if r.count() != 1 {
		t.Fatalf("reply replies = %+v, want one answer", r.sent)
	}
}

// TestGroupReplyToBotByID — the numeric id from getMe wins even when the replied-to username differs
// from the cached BotName.
func TestGroupReplyToBotByID(t *testing.T) {
	var cfg = groupTestConfig(t, nil)
	var m = &fakeModel{response: model.Response{Content: "ответ"}}
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))
	d.deps.BotID = 777
	var ev = groupEvent(-100500, 11, 603, "продолжай")
	ev.ReplyToMessageID = 99
	ev.ReplyToUserID = 777
	ev.ReplyToUsername = "RenamedBot"
	ev.ReplyToIsBot = true

	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("reply-by-id dispatch: %v", err)
	}
	if m.callCount() != 1 {
		t.Fatalf("reply-by-id model calls = %d, want 1", m.callCount())
	}
}

// TestGroupCommandNeedsTheBotSuffix — `/cmd@Bot` is addressed, a bare `/cmd` or another bot's command
// is not.
func TestGroupCommandNeedsTheBotSuffix(t *testing.T) {
	var cfg = groupTestConfig(t, nil)
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, &fakeModel{}, r, testStore(t))

	for _, text := range []string{"/ping", "/ping@OtherBot"} {
		if err := dispatch(t, d, groupEvent(-100500, 11, 604, text)); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
	if r.count() != 0 {
		t.Fatalf("%q and %q were answered in a group: %+v", "/ping", "/ping@OtherBot", r.sent)
	}
	if err := dispatch(t, d, groupEvent(-100500, 11, 605, "/ping@TestBot")); err != nil {
		t.Fatalf("/ping@TestBot: %v", err)
	}
	if r.count() != 1 {
		t.Fatalf("/ping@TestBot replies = %+v, want one", r.sent)
	}
}

// TestGroupNonAllowlistedSenderIsIgnored — the allowlist names the sender, not the chat, and a refused
// group sender gets no refusal text (unlike a private chat).
func TestGroupNonAllowlistedSenderIsIgnored(t *testing.T) {
	var cfg = groupTestConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "USER_DAILY_MODEL_LIMIT": "5"})
	var m = &fakeModel{response: model.Response{Content: "не должно быть"}}
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))

	if err := dispatch(t, d, groupEvent(-100500, 22, 606, "@TestBot привет")); err != nil {
		t.Fatalf("non-allowlisted group dispatch: %v", err)
	}
	if m.callCount() != 0 || r.count() != 0 {
		t.Fatalf("non-allowlisted group sender got model=%d replies=%+v", m.callCount(), r.sent)
	}
	used, err := store.DailyQuotaUsed(context.Background(), d.deps.DB, 22, d.now().UTC(), store.DailyQuotaModel)
	if err != nil || used != 0 {
		t.Fatalf("non-allowlisted group sender spent quota: used=%d err=%v", used, err)
	}
	if got := chatsWithID(t, d, -100500); got != 0 {
		t.Fatalf("non-allowlisted group sender created %d chats rows", got)
	}
}

// TestAcceptedGroupChatTracksEachSender — EnsureChat keys the row by chat id and stores the sender as
// its user; nothing in the gate assumes chat id == user id.
func TestAcceptedGroupChatTracksEachSender(t *testing.T) {
	var cfg = groupTestConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12"})
	var d = newTestDispatcher(t, cfg, &fakeModel{response: model.Response{Content: "ответ"}}, &fakeReplier{}, testStore(t))

	if err := dispatch(t, d, groupEvent(-100500, 11, 607, "@TestBot первый")); err != nil {
		t.Fatal(err)
	}
	if got := chatUserOf(t, d, -100500); got != 11 {
		t.Fatalf("chats.user_id after sender 11 = %d, want 11", got)
	}
	if err := dispatch(t, d, groupEvent(-100500, 12, 608, "@TestBot второй")); err != nil {
		t.Fatal(err)
	}
	if got := chatUserOf(t, d, -100500); got != 12 {
		t.Fatalf("chats.user_id after sender 12 = %d, want 12", got)
	}
	rows, err := store.HistoryOfChat(context.Background(), d.deps.DB, -100500, 10)
	if err != nil || len(rows) != 4 {
		t.Fatalf("group history=%+v err=%v", rows, err)
	}
	if rows[0].UserID != 11 || rows[2].UserID != 12 {
		t.Fatalf("group questions attributed to %d/%d, want 11/12", rows[0].UserID, rows[2].UserID)
	}
}

// TestPerUserRateLimitAppliesToGroups — the per-user limiter sits after the group gate, so a second
// question of the same sender in the same window is refused by the ordinary text.
func TestPerUserRateLimitAppliesToGroups(t *testing.T) {
	var cfg = groupTestConfig(t, map[string]string{"RATE_LIMIT_PER_MINUTE": "1", "USER_DAILY_MODEL_LIMIT": "5"})
	var m = &fakeModel{response: model.Response{Content: "ответ"}}
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))

	if err := dispatch(t, d, groupEvent(-100500, 11, 609, "@TestBot первый")); err != nil {
		t.Fatalf("first group question: %v", err)
	}
	if err := dispatch(t, d, groupEvent(-100500, 11, 610, "@TestBot второй")); err != nil {
		t.Fatalf("second group question: %v", err)
	}
	if m.callCount() != 1 {
		t.Fatalf("rate-limited group sender called the model %d times, want 1", m.callCount())
	}
	if !strings.Contains(r.joined(), "Слишком часто") {
		t.Fatalf("rate-limited group sender got %q, want the ordinary limit text", r.joined())
	}
	used, err := store.DailyQuotaUsed(context.Background(), d.deps.DB, 11, d.now().UTC(), store.DailyQuotaModel)
	if err != nil || used != 1 {
		t.Fatalf("daily quota after the rate-limited pair = %d err=%v, want 1", used, err)
	}
}

// TestEventOfCarriesTheReplyAuthor — the Telegram translator must expose the replied-to author, which
// is what the reply rule reads.
func TestEventOfCarriesTheReplyAuthor(t *testing.T) {
	var upd = &tgmodels.Update{ID: 9, Message: &tgmodels.Message{
		ID:   10,
		Chat: tgmodels.Chat{ID: -100500, Type: tgmodels.ChatTypeSupergroup, Title: "Группа"},
		From: &tgmodels.User{ID: 11, Username: "tester"},
		Text: "продолжи",
		ReplyToMessage: &tgmodels.Message{
			ID:   7,
			From: &tgmodels.User{ID: 777, Username: "TestBot", IsBot: true},
		},
	}}
	ev, ok := eventOf(upd)
	if !ok {
		t.Fatal("eventOf rejected a group reply")
	}
	if ev.ReplyToMessageID != 7 || ev.ReplyToUserID != 777 || ev.ReplyToUsername != "TestBot" || !ev.ReplyToIsBot {
		t.Fatalf("reply fields = id:%d user:%d name:%q bot:%v", ev.ReplyToMessageID, ev.ReplyToUserID, ev.ReplyToUsername, ev.ReplyToIsBot)
	}
}

// TestBotMentionRecognitionAndStripping — whole-word matching, case-insensitivity and seam cleanup.
func TestBotMentionRecognitionAndStripping(t *testing.T) {
	var mentionCases = []struct {
		name string
		text string
		bot  string
		want bool
	}{
		{"start", "@TestBot hi", "TestBot", true},
		{"middle", "hi @TestBot there", "TestBot", true},
		{"case", "hi @testbot", "TestBot", true},
		{"punctuation", "hi @TestBot, there", "TestBot", true},
		{"longer name", "hi @TestBotExtra", "TestBot", false},
		{"email", "hi email@TestBot", "TestBot", false},
		{"other bot", "hi @OtherBot", "TestBot", false},
		{"unknown username", "hi @TestBot", "", false},
	}
	for _, tc := range mentionCases {
		t.Run("mentions/"+tc.name, func(t *testing.T) {
			if got := mentionsBot(tc.text, tc.bot); got != tc.want {
				t.Fatalf("mentionsBot(%q, %q) = %v, want %v", tc.text, tc.bot, got, tc.want)
			}
		})
	}
	var stripCases = []struct {
		name string
		text string
		want string
	}{
		{"start", "@TestBot привет", "привет"},
		{"middle", "привет @TestBot как дела", "привет как дела"},
		{"end", "привет @TestBot", "привет"},
		{"multiline", "привет\n@TestBot\nкак дела", "привет\nкак дела"},
		{"only mention", "@TestBot", ""},
		{"command suffix is not a mention", "/ask@TestBot привет", "/ask@TestBot привет"},
	}
	for _, tc := range stripCases {
		t.Run("strip/"+tc.name, func(t *testing.T) {
			if got := stripBotMention(tc.text, "TestBot"); got != tc.want {
				t.Fatalf("stripBotMention(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}
