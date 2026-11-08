package bot

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"bot/internal/model"
	"bot/internal/store"

	tgmodels "github.com/go-telegram/bot/models"
)

// menuCallbackEvent — a button of a menu message that runs a model action. Telegram reuses the id of
// that message for every button, which is the whole point of the two tests below.
func menuCallbackEvent(chatID, userID, menuMessageID, updateID int64, text string) Event {
	var ev = testEvent(chatID, userID, menuMessageID, text)
	ev.UpdateID = updateID
	ev.MenuCallback = true
	return ev
}

// menuAwareReplier adds the menu editor capability to the shared fake: a callback event edits the menu
// it came from, and without an editor the dispatcher refuses the callback before the answer is written.
type menuAwareReplier struct {
	*fakeReplier
}

func (menuAwareReplier) EditMenu(context.Context, int64, int64, string, tgmodels.InlineKeyboardMarkup) error {
	return nil
}

// TestTwoModelActionsOfOneMenuBothRun is the regression test for the callback dedup: both actions share
// the menu message id, and writing it into messages.telegram_message_id made the second one a UNIQUE
// conflict that the middleware read as "already answered" — no answer, no error, only a log line.
func TestTwoModelActionsOfOneMenuBothRun(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12,13,14,15,16"})
	var fake = &fakeModel{response: model.Response{Content: "ответ"}}
	var replier = &menuAwareReplier{fakeReplier: &fakeReplier{}}
	var d = newTestDispatcher(t, cfg, fake, replier, nil)

	if err := d.Dispatch(context.Background(), menuCallbackEvent(700, 11, 555, 100, "/ask первый")); err != nil {
		t.Fatalf("first callback: %v", err)
	}
	if err := d.Dispatch(context.Background(), menuCallbackEvent(700, 11, 555, 101, "/ask второй")); err != nil {
		t.Fatalf("second callback: %v", err)
	}

	if got := fake.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2: a second menu action on the same message was dropped", got)
	}
	var rows, err = store.RecentMessages(context.Background(), d.deps.DB, 700, 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("the callbacks wrote no messages")
	}
	for _, row := range rows {
		if row.TelegramMessageID != -1 {
			t.Fatalf("a callback row carries telegram_message_id %d: the menu message id is not a message identity", row.TelegramMessageID)
		}
	}
}

// TestRealMessageStillDeduplicates proves the fix is scoped to callbacks: a redelivered MESSAGE with the
// same telegram_message_id must still be answered once, which is what the UNIQUE index is for.
func TestRealMessageStillDeduplicates(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12,13,14,15,16"})
	var fake = &fakeModel{response: model.Response{Content: "ответ"}}
	var d = newTestDispatcher(t, cfg, fake, &fakeReplier{}, nil)

	var ev = testEvent(701, 12, 900, "/ask вопрос")
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	var again = ev
	again.UpdateID = ev.UpdateID + 1
	if err := d.Dispatch(context.Background(), again); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if got := fake.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want 1: a redelivered message was answered twice", got)
	}
}

// TestChatQueueTimeoutFollowsTheReplyBudget pins the numbers of the busy-chat fix: 90 s reply budget is
// not a 2 s wait any more, and the bounds still hold for absurd configurations.
func TestChatQueueTimeoutFollowsTheReplyBudget(t *testing.T) {
	var cases = []struct {
		deadline int64
		want     time.Duration
	}{
		{0, updateQueueTimeoutMin},
		{1, updateQueueTimeoutMin},
		{90, 90 * time.Second},
		{3600, updateQueueTimeoutMax},
	}
	for _, tc := range cases {
		if got := chatQueueTimeoutFor(tc.deadline); got != tc.want {
			t.Errorf("chatQueueTimeoutFor(%d) = %s, want %s", tc.deadline, got, tc.want)
		}
	}
}

// TestSecondMessageWaitsForTheChatInsteadOfBeingDropped: the old 2 s wait expired while the first answer
// was still being written, the second update got «бот перегружен» and its offset was acknowledged, so
// Telegram would never deliver it again.
func TestSecondMessageWaitsForTheChatInsteadOfBeingDropped(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12,13,14,15,16"})
	var fake = &fakeModel{response: model.Response{Content: "долгий ответ"}, delay: 300 * time.Millisecond}
	var replier = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, fake, replier, nil)

	var firstDone = make(chan error, 1)
	go func() { firstDone <- d.Dispatch(context.Background(), testEvent(702, 13, 1000, "/ask первый")) }()

	// Let the first update take the chat lock before the second one asks for it.
	deadline := time.Now().Add(2 * time.Second)
	for fake.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := d.Dispatch(context.Background(), testEvent(702, 13, 1001, "/ask второй")); err != nil {
		t.Fatalf("second update: %v", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first update: %v", err)
	}

	var joined = replier.joined()
	if strings.Contains(joined, msgOverloaded) || strings.Contains(joined, msgChatBusy) {
		t.Fatalf("the second update was refused instead of queued: %q", joined)
	}
	if got := fake.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2: the queued update was dropped", got)
	}
}

// TestBusyChatIsNotAcknowledged checks the second half of the fix: when the wait DOES expire, the text
// names a busy chat (not a full queue), and bot_state is not advanced, so a restart replays the update.
func TestBusyChatIsNotAcknowledged(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12,13,14,15,16"})
	var fake = &fakeModel{response: model.Response{Content: "долгий ответ"}, delay: 400 * time.Millisecond}
	var replier = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, fake, replier, nil)
	d.chatQueueTimeout = 40 * time.Millisecond

	var firstDone = make(chan error, 1)
	go func() { firstDone <- d.Dispatch(context.Background(), testEvent(703, 14, 2000, "/ask первый")) }()
	var deadline = time.Now().Add(2 * time.Second)
	for fake.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	var second = testEvent(703, 14, 2001, "/ask второй")
	if err := d.Dispatch(context.Background(), second); err != nil {
		t.Fatalf("second update: %v", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first update: %v", err)
	}

	if !strings.Contains(replier.joined(), msgChatBusy) {
		t.Fatalf("the user was not told the chat is busy: %q", replier.joined())
	}
	var storedID, found, err = store.LastUpdateID(context.Background(), d.deps.DB)
	if err != nil {
		t.Fatalf("LastUpdateID: %v", err)
	}
	if found && storedID >= second.UpdateID {
		t.Fatalf("bot_state last_update_id = %d, want < %d: a dropped update must not be acknowledged", storedID, second.UpdateID)
	}
}

// TestChatBusyIsCoalesced keeps the per-chat refusal from flooding a chat that is already waiting.
func TestChatBusyIsCoalesced(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12,13,14,15,16"})
	var fake = &fakeModel{response: model.Response{Content: "долгий ответ"}, delay: 400 * time.Millisecond}
	var replier = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, fake, replier, nil)
	d.chatQueueTimeout = 30 * time.Millisecond

	var firstDone = make(chan error, 1)
	go func() { firstDone <- d.Dispatch(context.Background(), testEvent(704, 15, 3000, "/ask первый")) }()
	var deadline = time.Now().Add(2 * time.Second)
	for fake.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		_ = d.Dispatch(context.Background(), testEvent(704, 15, int64(3001+i), "/ask ещё"))
	}
	<-firstDone

	var busy = 0
	for _, text := range replier.texts() {
		if text == msgChatBusy {
			busy++
		}
	}
	if busy != 1 {
		t.Fatalf("busy notices = %d, want 1: repeated refusals of one chat are not coalesced", busy)
	}
}

// TestChatQueueTimeoutIsLogged asserts the wait is observable: the old code logged «the update queue is
// full» for a chat that was merely busy.
func TestChatQueueTimeoutIsLogged(t *testing.T) {
	var buffer bytes.Buffer
	var logger = slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12,13,14,15,16"})
	var fake = &fakeModel{response: model.Response{Content: "долгий ответ"}, delay: 300 * time.Millisecond}
	var d = newTestDispatcher(t, cfg, fake, &fakeReplier{}, nil, logger)
	d.chatQueueTimeout = 30 * time.Millisecond

	var firstDone = make(chan error, 1)
	go func() { firstDone <- d.Dispatch(context.Background(), testEvent(705, 16, 4000, "/ask первый")) }()
	var deadline = time.Now().Add(2 * time.Second)
	for fake.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	_ = d.Dispatch(context.Background(), testEvent(705, 16, 4001, "/ask второй"))
	<-firstDone

	if !strings.Contains(buffer.String(), "still answering the previous update") {
		t.Fatalf("the busy chat was not logged:\n%s", buffer.String())
	}
	if strings.Contains(buffer.String(), "the update queue is full") {
		t.Fatalf("a busy chat was reported as a full queue:\n%s", buffer.String())
	}
}
