package bot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"bot/internal/model"
	"bot/internal/store"
)

type editedMenu struct {
	chatID    int64
	messageID int64
	text      string
	markup    models.InlineKeyboardMarkup
}

type menuEditReplier struct {
	fakeReplier
	edits []editedMenu
}

func (r *menuEditReplier) EditMenu(ctx context.Context, chatID int64, messageID int64, text string, markup models.InlineKeyboardMarkup) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.edits = append(r.edits, editedMenu{chatID: chatID, messageID: messageID, text: text, markup: markup})
	return nil
}

func (r *menuEditReplier) SendMenuWithID(ctx context.Context, chatID int64, text string, markup models.InlineKeyboardMarkup) (int64, error) {
	if err := r.fakeReplier.SendMenu(ctx, chatID, text, markup); err != nil {
		return 0, err
	}
	return 700, nil
}

func (r *menuEditReplier) lastEdit() editedMenu {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.edits) == 0 {
		return editedMenu{}
	}
	return r.edits[len(r.edits)-1]
}

func dispatchMenuData(t *testing.T, d *Dispatcher, chatID int64, userID int64, messageID int64, data string) error {
	t.Helper()
	ev, ok := d.callbackEventOf(&models.Update{ID: 9000 + messageID, CallbackQuery: &models.CallbackQuery{
		ID: fmt.Sprintf("menu-%d-%s", userID, data), Data: data, From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: int(messageID), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}}},
	}})
	if !ok {
		t.Fatalf("callback %q was not translated", data)
	}
	return dispatch(t, d, ev)
}

func menuButtons(markup models.InlineKeyboardMarkup) []models.InlineKeyboardButton {
	var buttons []models.InlineKeyboardButton
	for _, row := range markup.InlineKeyboard {
		buttons = append(buttons, row...)
	}
	return buttons
}

func hasMenuCallback(markup models.InlineKeyboardMarkup, data string) bool {
	for _, button := range menuButtons(markup) {
		if button.CallbackData == data {
			return true
		}
	}
	return false
}

func findMenuCallback(markup models.InlineKeyboardMarkup, prefix string) string {
	for _, button := range menuButtons(markup) {
		if strings.HasPrefix(button.CallbackData, prefix) {
			return button.CallbackData
		}
	}
	return ""
}

func TestStageCRootLeafNavigationAndBack(t *testing.T) {
	r := &menuEditReplier{}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, testStore(t))
	if err := dispatch(t, d, testEvent(100, 11, 180, "/start")); err != nil {
		t.Fatal(err)
	}
	for _, button := range menuButtons(r.menus[0]) {
		if len(button.CallbackData) > 64 {
			t.Fatalf("oversized root callback %q", button.CallbackData)
		}
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:history"); err != nil {
		t.Fatal(err)
	}
	edit := r.lastEdit()
	if edit.messageID != 700 || !strings.Contains(edit.text, "История") || !hasMenuCallback(edit.markup, "menu:history:5") || !hasMenuCallback(edit.markup, "menu:history:20") || !hasMenuCallback(edit.markup, "menu:export:md") || !hasMenuCallback(edit.markup, "menu:export:json") || !hasMenuCallback(edit.markup, "menu:delete") {
		t.Fatalf("history leaf = %+v", edit)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:root"); err != nil {
		t.Fatal(err)
	}
	if !hasMenuCallback(r.lastEdit().markup, "menu:settings") || !hasMenuCallback(r.lastEdit().markup, "menu:docs") {
		t.Fatalf("back did not restore root: %+v", r.lastEdit().markup)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:settings"); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"menu:model", "menu:persona", "menu:stream", "menu:root"} {
		if !hasMenuCallback(r.lastEdit().markup, data) {
			t.Errorf("settings misses %s", data)
		}
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:status"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "Состояние бота") || !hasMenuCallback(r.lastEdit().markup, "menu:status") || !hasMenuCallback(r.lastEdit().markup, "menu:stats") || !hasMenuCallback(r.lastEdit().markup, "menu:ping") {
		t.Fatalf("status leaf = %+v", r.lastEdit())
	}
}

func TestStageCModelControlsUseBoundedFreshChoices(t *testing.T) {
	m := &fakeModel{models: []string{"model-one", "model-two"}}
	r := &menuEditReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:model"); err != nil {
		t.Fatal(err)
	}
	edit := r.lastEdit()
	if !strings.Contains(edit.text, d.deps.Cfg.StrataModel) || !strings.Contains(edit.text, d.deps.Cfg.StrataBase) || !hasMenuCallback(edit.markup, "menu:model:set:0") || !hasMenuCallback(edit.markup, "menu:model:set:1") || !hasMenuCallback(edit.markup, "menu:model:check") || !hasMenuCallback(edit.markup, "menu:model:reset") {
		t.Fatalf("model menu = %+v", edit)
	}
	for _, button := range menuButtons(edit.markup) {
		if strings.Contains(button.CallbackData, "model-one") || strings.Contains(button.CallbackData, "model-two") {
			t.Fatal("model id leaked into callback data")
		}
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:model:set:1"); err != nil {
		t.Fatal(err)
	}
	selected, found, err := store.ChatModel(context.Background(), db, 100)
	if err != nil || !found || selected != "model-two" {
		t.Fatalf("selected=%q found=%v err=%v", selected, found, err)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:model:check"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "Endpoint отвечает; моделей доступно: 2") {
		t.Fatalf("model check = %q", r.lastEdit().text)
	}
	m.models = []string{"model-one"}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:model:set:1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "истекло") {
		t.Fatalf("stale model choice did not get expiry notice: %q", r.lastEdit().text)
	}
	selected, _, _ = store.ChatModel(context.Background(), db, 100)
	if selected != "model-two" {
		t.Fatalf("stale choice changed model to %q", selected)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:model:reset"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.ChatModel(context.Background(), db, 100); err != nil || found {
		t.Fatalf("model reset found=%v err=%v", found, err)
	}
}

func TestStageCStreamAndPersonaControls(t *testing.T) {
	r := &menuEditReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "false"}), nil, r, db)
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:stream"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "выключен") || !strings.Contains(r.lastEdit().text, "общий STREAMING_ENABLED") {
		t.Fatalf("stream state = %q", r.lastEdit().text)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:stream:on"); err != nil {
		t.Fatal(err)
	}
	enabled, found, err := store.ChatStreaming(context.Background(), db, 100)
	if err != nil || !found || !enabled || !strings.Contains(r.lastEdit().text, "настройка этого чата") {
		t.Fatalf("stream on enabled=%v found=%v err=%v text=%q", enabled, found, err, r.lastEdit().text)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:stream:off"); err != nil {
		t.Fatal(err)
	}
	enabled, found, err = store.ChatStreaming(context.Background(), db, 100)
	if err != nil || !found || enabled {
		t.Fatalf("stream off enabled=%v found=%v err=%v", enabled, found, err)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:stream:reset"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.ChatStreaming(context.Background(), db, 100); err != nil || found {
		t.Fatalf("stream reset found=%v err=%v", found, err)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:persona:set:coder"); err != nil {
		t.Fatal(err)
	}
	_, name, override, err := d.effectivePersona(context.Background(), 100)
	if err != nil || !override || name != "coder" {
		t.Fatalf("persona=%q override=%v err=%v", name, override, err)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:persona:reset"); err != nil {
		t.Fatal(err)
	}
	if _, _, override, err := d.effectivePersona(context.Background(), 100); err != nil || override {
		t.Fatalf("persona reset override=%v err=%v", override, err)
	}
}

func TestStageCExportFormatChoices(t *testing.T) {
	r := &menuEditReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, db)
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "экспорт"}); err != nil {
		t.Fatal(err)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:export:md"); err != nil {
		t.Fatal(err)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:export:json"); err != nil {
		t.Fatal(err)
	}
	if len(r.documents) != 2 || r.documents[0].Name != "history-100.md" || r.documents[0].MIME != "text/markdown" || r.documents[1].Name != "history-100.json" || r.documents[1].MIME != "application/json" {
		t.Fatalf("exports = %+v", r.documents)
	}
}

func TestStageCDocumentActionsAndSingleUseConfirmations(t *testing.T) {
	r := &menuEditReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12"}), nil, r, db)
	firstID, _, err := store.PutRAGDocument(context.Background(), db, 100, 11, "private-name", "первый")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutRAGDocument(context.Background(), db, 100, 11, "second", "второй"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutRAGDocument(context.Background(), db, 101, 11, "other-chat", "другой чат"); err != nil {
		t.Fatal(err)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:docs"); err != nil {
		t.Fatal(err)
	}
	edit := r.lastEdit()
	deleteData := fmt.Sprintf("menu:doc:delete:%d", firstID)
	if !hasMenuCallback(edit.markup, deleteData) || !hasMenuCallback(edit.markup, "menu:docs:add") || !hasMenuCallback(edit.markup, "menu:docs:export") || !hasMenuCallback(edit.markup, "menu:docs:clear") {
		t.Fatalf("documents menu = %+v", edit)
	}
	for _, button := range menuButtons(edit.markup) {
		if strings.Contains(button.CallbackData, "private-name") || strings.Contains(button.CallbackData, "second") || len(button.CallbackData) > 64 {
			t.Fatalf("unsafe document callback %q", button.CallbackData)
		}
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:docs:export"); err != nil {
		t.Fatal(err)
	}
	if len(r.documents) != 1 || r.documents[0].Name != "rag-100.json" || r.documents[0].MIME != "application/json" || !strings.Contains(string(r.documents[0].Data), "private-name") || strings.Contains(string(r.documents[0].Data), "other-chat") {
		t.Fatalf("RAG menu export = %+v", r.documents)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, deleteData); err != nil {
		t.Fatal(err)
	}
	confirm := findMenuCallback(r.lastEdit().markup, "menu:doc:confirm:")
	cancel := findMenuCallback(r.lastEdit().markup, "menu:doc:cancel:")
	if confirm == "" || cancel == "" || len(confirm) > 64 {
		t.Fatalf("document confirmation = %+v", r.lastEdit().markup)
	}
	if docs, _ := store.ListRAGDocuments(context.Background(), db, 100); len(docs) != 2 {
		t.Fatal("delete prompt removed a document")
	}
	if err := dispatchMenuData(t, d, 100, 12, 700, confirm); err != nil {
		t.Fatal(err)
	}
	if docs, _ := store.ListRAGDocuments(context.Background(), db, 100); len(docs) != 2 {
		t.Fatal("wrong user consumed document confirmation")
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, confirm); err != nil {
		t.Fatal(err)
	}
	if docs, _ := store.ListRAGDocuments(context.Background(), db, 100); len(docs) != 1 {
		t.Fatal("confirmed document was not deleted")
	}
	if other, _ := store.ListRAGDocuments(context.Background(), db, 101); len(other) != 1 {
		t.Fatal("document delete crossed chat isolation")
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, confirm); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "истекло") {
		t.Fatalf("replayed document confirmation = %q", r.lastEdit().text)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:docs:clear"); err != nil {
		t.Fatal(err)
	}
	clearCancel := findMenuCallback(r.lastEdit().markup, "menu:doc:cancel:")
	if err := dispatchMenuData(t, d, 100, 11, 700, clearCancel); err != nil {
		t.Fatal(err)
	}
	if docs, _ := store.ListRAGDocuments(context.Background(), db, 100); len(docs) != 1 {
		t.Fatal("cancel cleared documents")
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:docs:clear"); err != nil {
		t.Fatal(err)
	}
	clearConfirm := findMenuCallback(r.lastEdit().markup, "menu:doc:confirm:")
	if err := dispatchMenuData(t, d, 100, 11, 700, clearConfirm); err != nil {
		t.Fatal(err)
	}
	if docs, _ := store.ListRAGDocuments(context.Background(), db, 100); len(docs) != 0 {
		t.Fatal("confirmed clear left documents")
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:docs:add"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "/docs add") || !hasMenuCallback(r.lastEdit().markup, "menu:docs") {
		t.Fatalf("add instructions = %+v", r.lastEdit())
	}
}

func TestStageCMoreHidesAdminAndShowsRelevantRecoveryControls(t *testing.T) {
	r := &menuEditReplier{}
	db := testStore(t)
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12", "TELEGRAM_ADMIN_USERS": "12"})
	d := newTestDispatcher(t, cfg, nil, r, db)
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:more"); err != nil {
		t.Fatal(err)
	}
	if hasMenuCallback(r.lastEdit().markup, "menu:admin") || !hasMenuCallback(r.lastEdit().markup, "menu:whoami") || hasMenuCallback(r.lastEdit().markup, "menu:skip") || hasMenuCallback(r.lastEdit().markup, "menu:unskip") {
		t.Fatalf("non-admin more menu = %+v", r.lastEdit().markup)
	}
	if err := dispatchMenuData(t, d, 100, 12, 700, "menu:more"); err != nil {
		t.Fatal(err)
	}
	if !hasMenuCallback(r.lastEdit().markup, "menu:admin") {
		t.Fatal("configured admin cannot see admin button")
	}
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "ждёт"}); err != nil {
		t.Fatal(err)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:more"); err != nil {
		t.Fatal(err)
	}
	if !hasMenuCallback(r.lastEdit().markup, "menu:skip") {
		t.Fatal("pending question did not expose Skip")
	}
	if err := store.SetResumeSkip(context.Background(), db, 100, 1); err != nil {
		t.Fatal(err)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:more"); err != nil {
		t.Fatal(err)
	}
	if hasMenuCallback(r.lastEdit().markup, "menu:skip") || !hasMenuCallback(r.lastEdit().markup, "menu:unskip") {
		t.Fatalf("suppressed recovery controls = %+v", r.lastEdit().markup)
	}
}

func TestStageCPendingInputBindingConsumptionAndExpiry(t *testing.T) {
	r := &menuEditReplier{}
	m := &fakeModel{response: model.Response{Content: "ответ"}}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12", "PROXY_FREE_TEXT": "false"}), m, r, testStore(t))
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:ask"); err != nil {
		t.Fatal(err)
	}
	cancel := findMenuCallback(r.lastEdit().markup, "menu:input:cancel:")
	if cancel == "" || !strings.Contains(r.lastEdit().text, "2 минуты") {
		t.Fatalf("ask prompt = %+v", r.lastEdit())
	}
	wrong := testEvent(100, 12, 701, "чужой вопрос")
	if _, consumed := d.pendingEvent(wrong); consumed {
		t.Fatal("wrong user consumed pending input")
	}
	command := testEvent(100, 11, 702, "/ping")
	if _, consumed := d.pendingEvent(command); consumed {
		t.Fatal("pending input intercepted an existing text command")
	}
	question := testEvent(100, 11, 703, "мой вопрос")
	pending, consumed := d.pendingEvent(question)
	if !consumed || pending.Text != "/ask мой вопрос" {
		t.Fatalf("pending question = %q consumed=%v", pending.Text, consumed)
	}
	if err := dispatch(t, d, pending); err != nil {
		t.Fatal(err)
	}
	if got := m.lastRequest().Messages[len(m.lastRequest().Messages)-1].Content; got != "мой вопрос" {
		t.Fatalf("model received %q", got)
	}
	if _, consumed := d.pendingEvent(testEvent(100, 11, 704, "второй вопрос")); consumed {
		t.Fatal("pending input was consumed twice")
	}
	if err := dispatchMenuData(t, d, 100, 12, 700, cancel); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "истекло") {
		t.Fatalf("consumed cancel should be stale: %q", r.lastEdit().text)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:ask"); err != nil {
		t.Fatal(err)
	}
	cancel = findMenuCallback(r.lastEdit().markup, "menu:input:cancel:")
	if err := dispatchMenuData(t, d, 100, 12, 700, cancel); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.joined(), "другому пользователю") {
		t.Fatalf("wrong-user cancel did not explain binding: %q", r.joined())
	}
	if _, consumed := d.pendingEvent(testEvent(100, 11, 705, "ещё вопрос")); !consumed {
		t.Fatal("wrong-user cancel consumed the owner's pending input")
	}

	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:search"); err != nil {
		t.Fatal(err)
	}
	set := pendingInputsFor(d)
	set.mu.Lock()
	for key, pending := range set.pending {
		pending.ExpiresAt = time.Now().Add(-time.Second)
		set.pending[key] = pending
	}
	set.mu.Unlock()
	expired := testEvent(100, 11, 705, "поиск после expiry")
	if got, consumed := d.pendingEvent(expired); consumed || got.Text != expired.Text {
		t.Fatalf("expired pending input intercepted text: %+v consumed=%v", got, consumed)
	}
}

func TestStageCPendingSearchCustomPersonaAndCancel(t *testing.T) {
	r := &menuEditReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "PROXY_FREE_TEXT": "false"}), nil, r, db)
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "Нужная Строка"}); err != nil {
		t.Fatal(err)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:search"); err != nil {
		t.Fatal(err)
	}
	pending, consumed := d.pendingEvent(testEvent(100, 11, 701, "строка"))
	if !consumed || pending.Text != "/search строка" {
		t.Fatalf("search pending = %q consumed=%v", pending.Text, consumed)
	}
	if err := dispatch(t, d, pending); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.joined(), "Нужная Строка") {
		t.Fatalf("pending search reply = %q", r.joined())
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:persona:custom"); err != nil {
		t.Fatal(err)
	}
	cancel := findMenuCallback(r.lastEdit().markup, "menu:input:cancel:")
	if err := dispatchMenuData(t, d, 100, 11, 700, cancel); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "Ввод отменён") || !hasMenuCallback(r.lastEdit().markup, "menu:persona:custom") {
		t.Fatalf("persona cancel = %+v", r.lastEdit())
	}
	if _, consumed := d.pendingEvent(testEvent(100, 11, 702, "не должно сохраниться")); consumed {
		t.Fatal("cancelled persona input was consumed")
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:persona:custom"); err != nil {
		t.Fatal(err)
	}
	custom, consumed := d.pendingEvent(testEvent(100, 11, 703, "Отвечай коротко"))
	if !consumed || custom.Text != "/persona custom Отвечай коротко" {
		t.Fatalf("custom pending = %q consumed=%v", custom.Text, consumed)
	}
	if err := dispatch(t, d, custom); err != nil {
		t.Fatal(err)
	}
	prompt, name, override, err := d.effectivePersona(context.Background(), 100)
	if err != nil || !override || name != "custom" || prompt != "Отвечай коротко" {
		t.Fatalf("custom persona=%q/%q override=%v err=%v", prompt, name, override, err)
	}
}

func TestStageCMenuDeleteKeepsHistoryConfirmationSingleUse(t *testing.T) {
	r := &menuEditReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, db)
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "вопрос"}); err != nil {
		t.Fatal(err)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:delete"); err != nil {
		t.Fatal(err)
	}
	confirm := ""
	for _, markup := range r.menus {
		confirm = findMenuCallback(markup, "delete:confirm:")
	}
	if confirm == "" || historyLength(t, d, 100) != 1 {
		t.Fatalf("history delete prompt confirm=%q history=%d", confirm, historyLength(t, d, 100))
	}
	for index := 0; index < 2; index++ {
		if err := dispatchMenuData(t, d, 100, 11, 700, confirm); err != nil {
			t.Fatal(err)
		}
	}
	if historyLength(t, d, 100) != 0 || strings.Count(r.joined(), "Удалено сообщений:") != 1 || !strings.Contains(r.joined(), "истекло или уже использовано") {
		t.Fatalf("single-use history confirmation = %q", r.joined())
	}
}

func TestStageCUnknownCallbackGetsExpiredAlert(t *testing.T) {
	var answerText, showAlert string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		answerText = req.FormValue("text")
		showAlert = req.FormValue("show_alert")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	tg, err := tgbot.New("12345:test-token-not-a-secret", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe(), tgbot.WithHTTPClient(time.Second, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	d := newTestDispatcher(t, testConfig(t, nil), nil, &fakeReplier{}, testStore(t))
	d.handleCallback(context.Background(), tg, &models.Update{ID: 1, CallbackQuery: &models.CallbackQuery{ID: "stale", Data: "menu:model:set:99", From: models.User{ID: 11}}})
	if answerText != menuExpiredText || showAlert != "true" {
		t.Fatalf("stale callback answer text=%q show_alert=%q", answerText, showAlert)
	}
	for _, data := range []string{"menu:model:set:model-two", "menu:doc:delete:name", "menu:doc:confirm:bad", "not-a-command", strings.Repeat("x", 65)} {
		if _, ok := d.callbackText(data); ok {
			t.Errorf("unsafe callback accepted: %q", data)
		}
	}
}
