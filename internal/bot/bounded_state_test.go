package bot

import (
	"testing"
)

// TestChatTurnsReleaseEntriesWhenTheUpdateFinishes — the per-chat counter is dropped when the last admitted
// update of the chat returns, and a newer update that is still in flight keeps the entry alive.
func TestChatTurnsReleaseEntriesWhenTheUpdateFinishes(t *testing.T) {
	var turns chatTurns
	if got := turns.next(700); got != 1 {
		t.Fatalf("first turn = %d, want 1", got)
	}
	if got := turns.next(700); got != 2 {
		t.Fatalf("second turn = %d, want 2", got)
	}
	turns.consume(700)
	if got := turns.current(700); got != 2 {
		t.Fatalf("the newer in-flight turn was dropped: current=%d, want 2", got)
	}
	turns.consume(700)
	if got := turns.current(700); got != 0 {
		t.Fatalf("the consumed chat kept its turn entry: current=%d, want 0", got)
	}
	if len(turns.number) != 0 || len(turns.active) != 0 {
		t.Fatalf("chatTurns still holds consumed chats: number=%d active=%d", len(turns.number), len(turns.active))
	}
}

// TestDispatchDoesNotGrowChatTurns — every dispatched chat releases its turn, so a long-running process does
// not keep one entry per chat forever.
func TestDispatchDoesNotGrowChatTurns(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	d := newTestDispatcher(t, cfg, nil, nil, nil)
	for i := 0; i < 40; i++ {
		var chatID = int64(800 + i)
		if err := dispatch(t, d, testEvent(chatID, 11, int64(8000+i), "/ping")); err != nil {
			t.Fatalf("Dispatch for chat %d: %v", chatID, err)
		}
	}
	d.turns.mu.Lock()
	var entries = len(d.turns.number)
	var active = len(d.turns.active)
	d.turns.mu.Unlock()
	if entries != 0 || active != 0 {
		t.Fatalf("chatTurns grew with the number of chats: number=%d active=%d", entries, active)
	}
}

// TestTelegramReplierUserMapIsBounded — the chat→user map is a flood-audit detail, not history. It is capped
// and evicts the oldest chat instead of growing with every chat the bot has ever seen.
func TestTelegramReplierUserMapIsBounded(t *testing.T) {
	r := &telegramReplier{}
	var total = telegramReplierUsersMax + 10
	for i := 1; i <= total; i++ {
		r.noteChatUser(int64(i), int64(1000+i))
	}
	r.mu.Lock()
	var size = len(r.users)
	var ring = len(r.userRing)
	r.mu.Unlock()
	if size != telegramReplierUsersMax {
		t.Fatalf("user map size = %d, want %d", size, telegramReplierUsersMax)
	}
	if ring != telegramReplierUsersMax {
		t.Fatalf("eviction ring size = %d, want %d", ring, telegramReplierUsersMax)
	}
	if got := r.chatUser(1); got != 0 {
		t.Fatalf("the oldest chat was not evicted: chatUser(1)=%d", got)
	}
	if got := r.chatUser(int64(total)); got != int64(1000+total) {
		t.Fatalf("the newest chat was not remembered: chatUser(%d)=%d", total, got)
	}
}
