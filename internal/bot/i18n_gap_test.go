package bot

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"bot/internal/i18n"
	"bot/internal/model"
	"bot/internal/store"
)

// setTestChatLanguage stores the /language override for a chat that may not have a row yet: the
// chat_language table has a foreign key to chats, so the row must exist first.
func setTestChatLanguage(t *testing.T, ctx context.Context, db *sql.DB, chatID int64, userID int64, language string) {
	t.Helper()
	if err := store.EnsureChat(ctx, db, chatID, userID, "test"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if err := store.SetChatLanguage(ctx, db, chatID, language); err != nil {
		t.Fatalf("SetChatLanguage: %v", err)
	}
}

// TestDispatchNoticesFollowChatLanguage covers the three dispatcher notices that used to be RU-only:
// an overloaded queue, a busy chat and a question that could not be stored. The RU branch also keeps the
// historical constants and the catalog in sync, because existing tests assert those strings verbatim.
func TestDispatchNoticesFollowChatLanguage(t *testing.T) {
	var ctx = context.Background()
	var d, r, _ = languageTestDispatcher(t, nil)
	setTestChatLanguage(t, ctx, d.deps.DB, 100, 11, i18n.EN)
	var ev = testEvent(100, 11, 700, "/ask вопрос")
	d.dispatchOverloaded(ctx, ev)
	d.dispatchChatBusy(ctx, ev)
	d.notifyQuestionNotStored(ctx, ev)

	var english = r.texts()
	if len(english) != 3 {
		t.Fatalf("EN notices = %q, want three", english)
	}
	for index, want := range []string{
		"The bot is overloaded: the queue is full.",
		"The previous answer in this chat is not ready yet.",
		"Could not save the question, try again",
	} {
		if !strings.Contains(english[index], want) {
			t.Errorf("EN notice %d = %q, want %q", index, english[index], want)
		}
	}
	for _, russian := range []string{"Бот перегружен", "Предыдущий ответ", "Не удалось сохранить вопрос"} {
		for _, text := range english {
			if strings.Contains(text, russian) {
				t.Errorf("EN notices still contain Russian %q: %q", russian, text)
			}
		}
	}

	var ruDispatcher, ruReplier, _ = languageTestDispatcher(t, nil)
	var ruEvent = testEvent(200, 11, 701, "/ask вопрос")
	ruDispatcher.dispatchOverloaded(ctx, ruEvent)
	ruDispatcher.dispatchChatBusy(ctx, ruEvent)
	ruDispatcher.notifyQuestionNotStored(ctx, ruEvent)
	var russian = ruReplier.texts()
	if len(russian) != 3 {
		t.Fatalf("RU notices = %q, want three", russian)
	}
	if russian[0] != msgOverloaded || russian[1] != msgChatBusy || russian[2] != msgQuestionNotStored {
		t.Fatalf("RU notices = %q, want the historical constants %q / %q / %q",
			russian, msgOverloaded, msgChatBusy, msgQuestionNotStored)
	}
}

// TestAccessRefusalsFollowChatLanguage checks both refusal paths that run before the handler: an
// allowlisted bot with a user outside the list, and a closed bot. The chat language is read from the
// same override /language writes.
func TestAccessRefusalsFollowChatLanguage(t *testing.T) {
	var ctx = context.Background()

	var d, r, _ = languageTestDispatcher(t, nil)
	setTestChatLanguage(t, ctx, d.deps.DB, 100, 11, i18n.EN)
	if err := dispatch(t, d, testEvent(100, 99, 700, "/ask вопрос")); err != nil {
		t.Fatalf("dispatch of a non-listed user: %v", err)
	}
	var refusal = r.joined()
	if !strings.Contains(refusal, "You are not in TELEGRAM_ALLOWED_USERS") || !strings.Contains(refusal, "user_id is 99") {
		t.Fatalf("EN allowlist refusal = %q", refusal)
	}
	if strings.Contains(refusal, "Тебя нет в TELEGRAM_ALLOWED_USERS") {
		t.Fatalf("EN allowlist refusal still contains Russian: %q", refusal)
	}

	var closedDispatcher, closedReplier, _ = languageTestDispatcher(t, nil)
	closedDispatcher.deps.Cfg.DenyByDefault = true
	closedDispatcher.deps.Cfg.UserIDs = nil
	setTestChatLanguage(t, ctx, closedDispatcher.deps.DB, 100, 11, i18n.EN)
	if err := dispatch(t, closedDispatcher, testEvent(100, 11, 701, "/ask вопрос")); err != nil {
		t.Fatalf("dispatch to a closed bot: %v", err)
	}
	var closed = closedReplier.joined()
	if !strings.Contains(closed, "The bot is closed: TELEGRAM_ALLOWED_USERS is empty.") || !strings.Contains(closed, "user_id is 11") {
		t.Fatalf("EN closed-bot refusal = %q", closed)
	}

	if got := refuseClosed(11); got != refuseClosed(11, i18n.RU) || !strings.Contains(got, "Бот закрыт") {
		t.Fatalf("the legacy RU refusal fallback changed: %q", got)
	}
	if got := refuseNotListed(11); got != refuseNotListed(11, i18n.RU) || !strings.Contains(got, "Тебя нет в TELEGRAM_ALLOWED_USERS") {
		t.Fatalf("the legacy RU refusal fallback changed: %q", got)
	}
}

// TestNetworkApprovalPromptFollowsChatLanguage renders the /ask (non-task) approval prompt and its
// buttons in both languages. Callback data and the tool name must not be translated: Telegram and the
// tool loop identify an action by them.
func TestNetworkApprovalPromptFollowsChatLanguage(t *testing.T) {
	var cases = []struct {
		name        string
		language    string
		prompt      string
		buttons     []string
		cancelReply string
	}{
		{
			name:        "russian",
			language:    i18n.RU,
			prompt:      "Модель запросила новое сетевое действие.",
			buttons:     []string{"Разрешить один раз", "Разрешить для задачи", "Отмена"},
			cancelReply: "Сетевое действие отменено. Запрос не выполнен.",
		},
		{
			name:        "english",
			language:    i18n.EN,
			prompt:      "The model requested a new network action.",
			buttons:     []string{"Allow once", "Allow for the task", "Cancel"},
			cancelReply: "The network action was cancelled. The request was not executed.",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var ctx = context.Background()
			var db = testStore(t)
			setTestChatLanguage(t, ctx, db, 100, 11, test.language)
			var call = model.ToolCall{ID: "approval", Name: "web_search", Arguments: `{"query":"approval query"}`}
			var m = &scriptedToolModel{responses: []model.Response{
				{ToolCalls: []model.ToolCall{call}},
				{Content: "done"},
			}}
			var r = &networkMenuReplier{}
			var cfg = testConfig(t, map[string]string{
				"TELEGRAM_ALLOWED_USERS": "11",
				"TOOLS_ENABLED":          "true",
				"WEB_SEARCH_ENABLED":     "true",
				"WEB_SEARCH_BASE_URL":    "https://searx.internal.example",
			})
			var d = newTestDispatcher(t, cfg, m, r, db)
			if err := dispatch(t, d, testEvent(100, 11, 700, "Подготовь обзор")); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			var prompt = r.joined()
			if !strings.Contains(prompt, test.prompt) || !strings.Contains(prompt, "web_search: approval query") {
				t.Fatalf("%s approval prompt = %q", test.name, prompt)
			}
			if len(r.menus) != 1 {
				t.Fatalf("%s approval menus = %d, want one", test.name, len(r.menus))
			}
			var buttons = menuButtons(r.menus[0])
			if len(buttons) != 3 {
				t.Fatalf("%s approval buttons = %+v", test.name, buttons)
			}
			for index, want := range test.buttons {
				if buttons[index].Text != want {
					t.Errorf("%s button %d = %q, want %q", test.name, index, buttons[index].Text, want)
				}
			}
			for index, prefix := range []string{"menu:network:confirm:", "menu:network:scope:", "menu:network:cancel:"} {
				if !strings.HasPrefix(buttons[index].CallbackData, prefix) {
					t.Errorf("%s button %d callback = %q, want prefix %q", test.name, index, buttons[index].CallbackData, prefix)
				}
			}

			var cancel = findMenuCallback(r.menus[0], "menu:network:cancel:")
			if err := dispatchMenuData(t, d, 100, 11, 700, cancel); err != nil {
				t.Fatalf("cancel callback: %v", err)
			}
			if got := r.lastEdit().text; !strings.Contains(got, test.cancelReply) {
				t.Fatalf("%s cancel reply = %q, want %q", test.name, got, test.cancelReply)
			}
		})
	}
}

// TestRecoveryNoticeFollowsChatLanguage covers the superseded-question notice of the startup scan. The
// RU wording is asserted by recovery_notice_test.go; this adds the EN chat.
func TestRecoveryNoticeFollowsChatLanguage(t *testing.T) {
	var ctx = context.Background()
	var db = testStore(t)
	setTestChatLanguage(t, ctx, db, 193, 11, i18n.EN)
	for _, message := range []struct {
		content string
		id      int64
	}{
		{"first question without an answer", 1610},
		{"second question", 1611},
	} {
		if _, err := store.Append(ctx, db, store.Message{
			ChatID: 193, UserID: 11, Role: store.RoleUser, Content: message.content, TelegramMessageID: intPtr(message.id),
		}); err != nil {
			t.Fatalf("storing %q: %v", message.content, err)
		}
	}
	var r = &fakeReplier{}
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var d = newTestDispatcher(t, cfg, &fakeModel{response: model.Response{Content: "answer"}}, r, db)
	if _, _, err := d.ResumeInterruptedQuestions(ctx); err != nil {
		t.Fatalf("ResumeInterruptedQuestions: %v", err)
	}
	var joined = r.joined()
	if !strings.Contains(joined, "The question from ") || !strings.Contains(joined, "/skip") {
		t.Fatalf("EN recovery notice = %q", joined)
	}
	if strings.Contains(joined, "Вопрос от ") {
		t.Fatalf("EN recovery notice still contains Russian: %q", joined)
	}
}

// TestLegacyRussianNoticesMatchCatalog keeps the RU source single: the historical constants that tests and
// the Mini App compare against must stay byte-identical to the catalog entries the Telegram path uses.
func TestLegacyRussianNoticesMatchCatalog(t *testing.T) {
	var cases = map[string]string{
		"dispatch.overloaded":          msgOverloaded,
		"dispatch.chat_busy":           msgChatBusy,
		"dispatch.question_not_stored": msgQuestionNotStored,
		"middleware.panic":             msgPanic,
		"middleware.too_often":         msgTooOftenFormat,
	}
	for key, want := range cases {
		if got := i18n.T(i18n.RU, key); got != want {
			t.Errorf("i18n.T(RU, %q) = %q, want the constant %q", key, got, want)
		}
	}
}
