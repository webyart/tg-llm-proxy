package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"bot/internal/model"
)

type capturedTelegramCall struct {
	method          string
	chatID          int64
	messageThreadID int
	replyMessageID  int
	allowNoReply    bool
}

func newCapturingTelegramReplier(t *testing.T, d *Dispatcher) (*telegramReplier, func() []capturedTelegramCall) {
	t.Helper()
	var mu sync.Mutex
	var calls = []capturedTelegramCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call = capturedTelegramCall{method: strings.TrimPrefix(r.URL.Path, "/bot123:test/")}
		if call.method == "sendMessage" || call.method == "sendChatAction" {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("%s form: %v", call.method, err)
			}
			var chatID, threadID, replyRaw string
			chatID = r.FormValue("chat_id")
			threadID = r.FormValue("message_thread_id")
			replyRaw = r.FormValue("reply_parameters")
			if _, err := fmt.Sscanf(chatID, "%d", &call.chatID); err != nil {
				t.Errorf("%s chat_id %q: %v", call.method, chatID, err)
			}
			if threadID != "" {
				if _, err := fmt.Sscanf(threadID, "%d", &call.messageThreadID); err != nil {
					t.Errorf("%s message_thread_id %q: %v", call.method, threadID, err)
				}
			}
			if replyRaw != "" {
				var reply struct {
					MessageID                int  `json:"message_id"`
					AllowSendingWithoutReply bool `json:"allow_sending_without_reply"`
				}
				if err := json.Unmarshal([]byte(replyRaw), &reply); err != nil {
					t.Errorf("%s reply_parameters %q: %v", call.method, replyRaw, err)
				}
				call.replyMessageID = reply.MessageID
				call.allowNoReply = reply.AllowSendingWithoutReply
			}
		}
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if call.method == "sendMessage" {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":-100500,"type":"supergroup"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	t.Cleanup(server.Close)
	tg, err := tgbot.New("123:test", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe(),
		tgbot.WithHTTPClient(time.Second, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	replier := &telegramReplier{d: d, bot: tg, cfg: d.deps.Cfg, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return replier, func() []capturedTelegramCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedTelegramCall(nil), calls...)
	}
}

func findTelegramCall(calls []capturedTelegramCall, method string) (capturedTelegramCall, bool) {
	for _, call := range calls {
		if call.method == method {
			return call, true
		}
	}
	return capturedTelegramCall{}, false
}

// TestGroupReplyThreadsTheTriggeringMessage — a group answer quotes the update it answers and stays
// in its forum topic; the typing indicator is sent into the same topic.
func TestGroupReplyThreadsTheTriggeringMessage(t *testing.T) {
	cfg := groupTestConfig(t, nil)
	d := newTestDispatcher(t, cfg, &fakeModel{response: modelResponse("ответ")}, &fakeReplier{}, testStore(t))
	replier, captured := newCapturingTelegramReplier(t, d)
	d.deps.Replier = replier

	ev := groupEvent(-100500, 11, 777, "@TestBot привет")
	ev.MessageThreadID = 42
	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("group dispatch: %v", err)
	}

	calls := captured()
	send, ok := findTelegramCall(calls, "sendMessage")
	if !ok {
		t.Fatalf("no sendMessage call: %+v", calls)
	}
	if send.chatID != -100500 || send.replyMessageID != 777 || send.messageThreadID != 42 || !send.allowNoReply {
		t.Fatalf("group send = %+v, want chat -100500, reply 777, topic 42, allow_sending_without_reply", send)
	}
	typing, ok := findTelegramCall(calls, "sendChatAction")
	if !ok || typing.messageThreadID != 42 {
		t.Fatalf("typing call = %+v ok=%v, want topic 42", typing, ok)
	}
}

// TestPrivateAnswerKeepsTheOldWireShape — private chats must not gain a reply anchor or a topic.
func TestPrivateAnswerKeepsTheOldWireShape(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	d := newTestDispatcher(t, cfg, &fakeModel{response: modelResponse("ответ")}, &fakeReplier{}, testStore(t))
	replier, captured := newCapturingTelegramReplier(t, d)
	d.deps.Replier = replier

	if err := dispatch(t, d, testEvent(100, 11, 501, "привет")); err != nil {
		t.Fatalf("private dispatch: %v", err)
	}
	calls := captured()
	send, ok := findTelegramCall(calls, "sendMessage")
	if !ok {
		t.Fatalf("no sendMessage call: %+v", calls)
	}
	if send.chatID != 100 || send.replyMessageID != 0 || send.messageThreadID != 0 || send.allowNoReply {
		t.Fatalf("private send = %+v, want chat 100 and no reply/topic", send)
	}
}

// TestEventOfCarriesTheForumTopic — the Telegram translator must expose message_thread_id, otherwise
// the dispatcher cannot keep an answer in the topic of the question.
func TestEventOfCarriesTheForumTopic(t *testing.T) {
	var upd = &models.Update{ID: 9, Message: &models.Message{
		ID: 7, Chat: models.Chat{ID: -100500, Type: models.ChatTypeSupergroup, Title: "Группа"},
		From: &models.User{ID: 11, Username: "tester"}, Text: "@TestBot в теме", MessageThreadID: 42,
	}}
	ev, ok := eventOf(upd)
	if !ok || ev.MessageThreadID != 42 {
		t.Fatalf("eventOf topic = %d ok=%v, want 42", ev.MessageThreadID, ok)
	}
	callback := &models.Update{ID: 9, CallbackQuery: &models.CallbackQuery{
		ID: "cb", Data: "menu:root", From: models.User{ID: 11},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: 5, Chat: models.Chat{ID: -100500, Type: models.ChatTypeSupergroup}, MessageThreadID: 42,
		}},
	}}
	called, ok := (&Dispatcher{}).callbackEventOf(callback)
	if !ok || called.MessageThreadID != 42 {
		t.Fatalf("callbackEventOf topic = %d ok=%v, want 42", called.MessageThreadID, ok)
	}
}

func modelResponse(content string) model.Response {
	return model.Response{Content: content, Model: "fake-model", Attempts: 1}
}
