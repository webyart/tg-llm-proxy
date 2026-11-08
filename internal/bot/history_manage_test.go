package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"bot/internal/model"
	"bot/internal/store"
)

type removedMenu struct {
	chatID    int64
	messageID int64
}

type deleteMenuReplier struct {
	menuEditReplier
	messageID int64
	removed   []removedMenu
}

func (r *deleteMenuReplier) SendMenuWithID(ctx context.Context, chatID int64, text string, markup models.InlineKeyboardMarkup) (int64, error) {
	if err := r.fakeReplier.SendMenu(ctx, chatID, text, markup); err != nil {
		return 0, err
	}
	if r.messageID == 0 {
		r.messageID = 700
	}
	return r.messageID, nil
}

func (r *deleteMenuReplier) RemoveMenu(ctx context.Context, chatID int64, messageID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, removedMenu{chatID: chatID, messageID: messageID})
	return nil
}

func (r *deleteMenuReplier) joined() string {
	return strings.Join([]string{r.fakeReplier.joined(), r.editsJoined()}, "\n")
}

func deleteCallbacks(t *testing.T, r *deleteMenuReplier) (string, string) {
	t.Helper()
	if len(r.menus) == 0 || len(r.menus[len(r.menus)-1].InlineKeyboard) != 1 {
		t.Fatalf("delete menus=%+v", r.menus)
	}
	buttons := r.menus[len(r.menus)-1].InlineKeyboard[0]
	if len(buttons) != 2 {
		t.Fatalf("delete buttons=%+v", buttons)
	}
	confirm := buttons[0].CallbackData
	cancel := buttons[1].CallbackData
	token := strings.TrimPrefix(confirm, "delete:confirm:")
	if token == confirm || len(token) != 32 || cancel != "delete:cancel:"+token {
		t.Fatalf("delete callbacks=%q, %q", confirm, cancel)
	}
	return confirm, cancel
}

func dispatchDeleteCallback(t *testing.T, d *Dispatcher, chatID int64, userID int64, messageID int64, data string) error {
	t.Helper()
	ev, ok := d.callbackEventOf(&models.Update{ID: messageID + 5000, CallbackQuery: &models.CallbackQuery{
		ID:   fmt.Sprintf("callback-%d-%d", userID, messageID),
		Data: data,
		From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID:   int(messageID),
			Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		}},
	}})
	if !ok {
		t.Fatalf("delete callback %q was not translated", data)
	}
	return dispatch(t, d, ev)
}

func newDeleteDispatcher(t *testing.T, chatID int64) (*Dispatcher, *deleteMenuReplier) {
	t.Helper()
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12"})
	r := &deleteMenuReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: chatID, UserID: 11, Role: store.RoleUser, Content: "вопрос"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	return d, r
}

func historyLength(t *testing.T, d *Dispatcher, chatID int64) int {
	t.Helper()
	rows, err := store.HistoryOfChat(context.Background(), d.deps.DB, chatID, 10)
	if err != nil {
		t.Fatalf("HistoryOfChat: %v", err)
	}
	return len(rows)
}

func TestExportAndSearchHistoryWithoutModel(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "не должно быть"}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	for _, row := range []store.Message{
		{ChatID: 200, UserID: 11, Role: store.RoleUser, Content: "Привет МИР"},
		{ChatID: 200, UserID: 11, Role: store.RoleAssistant, Content: "ответ"},
	} {
		if _, err := store.Append(context.Background(), db, row); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := dispatch(t, d, testEvent(200, 11, 1500, "/export json")); err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(r.documents) != 1 || r.documents[0].MIME != "application/json" || r.documents[0].Caption != "Экспорт истории" {
		t.Fatalf("documents=%+v", r.documents)
	}
	var payload historyExport
	if err := json.Unmarshal(r.documents[0].Data, &payload); err != nil || len(payload.Messages) != 2 || payload.Messages[0].Content != "Привет МИР" {
		t.Fatalf("export payload=%+v err=%v", payload, err)
	}
	if payload.Truncated || payload.Warning != "" || payload.Window.Count != 2 || payload.Window.OldestID != 1 || payload.Window.NewestID != 2 {
		t.Fatalf("unexpected export window=%+v truncated=%v warning=%q", payload.Window, payload.Truncated, payload.Warning)
	}
	if err := dispatch(t, d, testEvent(200, 11, 1501, "/search мир")); err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(r.joined(), "Привет МИР") || !strings.Contains(r.joined(), "вся история чата") || !strings.Contains(r.joined(), "до 20 совпадений") || m.callCount() != 0 {
		t.Fatalf("search reply=%q model calls=%d", r.joined(), m.callCount())
	}
}

func TestExportAndSearchDescribeTruncatedWindow(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	if err := store.EnsureChat(context.Background(), db, 202, 11, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	_, err := db.ExecContext(context.Background(), `
		WITH RECURSIVE seq(n) AS (
			SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?
		)
		INSERT INTO messages (chat_id, role, content, created_at)
		SELECT ?, 'user', CASE WHEN n = 1 THEN 'oldest-only' ELSE 'needle-' || n END,
			strftime('%Y-%m-%dT%H:%M:%SZ', '2026-01-01T00:00:00Z', '+' || n || ' seconds')
		FROM seq`, ExportRowLimit+1, 202)
	if err != nil {
		t.Fatalf("seed history window: %v", err)
	}
	if err := dispatch(t, d, testEvent(202, 11, 1520, "/export json")); err != nil {
		t.Fatalf("export json: %v", err)
	}
	if len(r.documents) != 1 {
		t.Fatalf("documents=%d", len(r.documents))
	}
	var payload historyExport
	if err := json.Unmarshal(r.documents[0].Data, &payload); err != nil {
		t.Fatalf("export json: %v", err)
	}
	if !payload.Truncated || !strings.Contains(payload.Warning, "более старые сообщения не включены") || len(payload.Messages) != ExportRowLimit {
		t.Fatalf("truncated export metadata=%+v messages=%d", payload, len(payload.Messages))
	}
	if payload.Window.Count != ExportRowLimit || payload.Window.Limit != ExportRowLimit || payload.Window.OldestID != 2 || payload.Window.NewestID != ExportRowLimit+1 || payload.Window.OldestCreatedAt == "" || payload.Window.NewestCreatedAt == "" {
		t.Fatalf("export window=%+v", payload.Window)
	}
	if err := dispatch(t, d, testEvent(202, 11, 1521, "/export md")); err != nil {
		t.Fatalf("export md: %v", err)
	}
	markdown := string(r.documents[1].Data)
	if !strings.Contains(markdown, "Внимание") || !strings.Contains(markdown, "ID 2–10001") || !strings.Contains(markdown, "время 2026-01-01T00:00:02Z") {
		t.Fatalf("markdown export warning/range missing: %.500s", markdown)
	}
	if err := dispatch(t, d, testEvent(202, 11, 1522, "/search needle-")); err != nil {
		t.Fatalf("search: %v", err)
	}
	searchReply := r.texts()[len(r.texts())-1]
	// The FTS index answers over the whole history, so the reply no longer warns about an unscanned tail; it
	// does say that the newest matches were shown when more exist.
	if !strings.Contains(searchReply, "Найдено сообщений: 20") || !strings.Contains(searchReply, "вся история чата") ||
		!strings.Contains(searchReply, "до 20 совпадений") || !strings.Contains(searchReply, "Показаны самые новые совпадения") {
		t.Fatalf("search scope=%q", searchReply)
	}
	if err := dispatch(t, d, testEvent(202, 11, 1523, "/search absent-nowhere")); err != nil {
		t.Fatalf("search without matches: %v", err)
	}
	outsideReply := r.texts()[len(r.texts())-1]
	if !strings.Contains(outsideReply, "Совпадений не найдено") || !strings.Contains(outsideReply, "вся история чата") {
		t.Fatalf("empty search reply=%q", outsideReply)
	}
	// A message older than the export window is still findable: that is exactly what the index changed.
	if err := dispatch(t, d, testEvent(202, 11, 1524, "/search oldest-only")); err != nil {
		t.Fatalf("search the oldest row: %v", err)
	}
	if oldestReply := r.texts()[len(r.texts())-1]; !strings.Contains(oldestReply, "Найдено сообщений: 1") {
		t.Fatalf("the oldest message of the chat was not found: %q", oldestReply)
	}
}

func TestIncrementalHistoryExportsRemainValid(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	tokensIn, tokensOut := int64(12), int64(34)
	for _, row := range []store.Message{
		{ChatID: 203, UserID: 11, Role: store.RoleUser, Content: "строка \"json\" <tag>\nновая"},
		{ChatID: 203, UserID: 11, Role: store.RoleAssistant, Content: "ответ", Model: "test-model", TokensIn: &tokensIn, TokensOut: &tokensOut},
	} {
		if _, err := store.Append(context.Background(), db, row); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	for index, format := range []string{"json", "md"} {
		if err := dispatch(t, d, testEvent(203, 11, int64(1530+index), "/export "+format)); err != nil {
			t.Fatalf("export %s: %v", format, err)
		}
		if len(r.documents) != index+1 || len(r.documents[index].Data) == 0 || len(r.documents[index].Data) > ExportMaxBytes {
			t.Fatalf("%s documents=%+v", format, r.documents)
		}
		if format == "json" {
			var payload historyExport
			if err := json.Unmarshal(r.documents[index].Data, &payload); err != nil {
				t.Fatalf("invalid incremental JSON: %v", err)
			}
			if len(payload.Messages) != 2 || payload.Messages[0].Content != "строка \"json\" <tag>\nновая" || payload.Messages[1].Model != "test-model" || payload.Messages[1].TokensIn == nil || *payload.Messages[1].TokensIn != tokensIn || payload.Messages[1].TokensOut == nil || *payload.Messages[1].TokensOut != tokensOut {
				t.Fatalf("JSON messages=%+v", payload.Messages)
			}
			if payload.Window.Count != 2 || payload.Window.OldestID != 1 || payload.Window.NewestID != 2 || payload.ExportedAt == "" {
				t.Fatalf("JSON metadata=%+v exported_at=%q", payload.Window, payload.ExportedAt)
			}
		} else {
			markdown := string(r.documents[index].Data)
			if !strings.Contains(markdown, "включено: 2") || !strings.Contains(markdown, "строка \"json\" <tag>\nновая") || !strings.Contains(markdown, "## assistant") {
				t.Fatalf("invalid incremental Markdown: %.500s", markdown)
			}
		}
	}
}

func TestExportHistoryDataFormatsValidationAndChatIsolation(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	for _, row := range []store.Message{
		{ChatID: 300, UserID: 11, Role: store.RoleUser, Content: "вопрос"},
		{ChatID: 300, UserID: 11, Role: store.RoleAssistant, Content: "ответ"},
		{ChatID: 301, UserID: 11, Role: store.RoleUser, Content: "другой чат"},
	} {
		if _, err := store.Append(ctx, db, row); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	data, err := ExportHistoryData(ctx, db, 300, " JSON ")
	if err != nil {
		t.Fatalf("ExportHistoryData JSON: %v", err)
	}
	var payload historyExport
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("export JSON: %v", err)
	}
	if payload.ChatID != 300 || len(payload.Messages) != 2 || payload.Messages[0].Content != "вопрос" || payload.Messages[1].Content != "ответ" {
		t.Fatalf("export payload = %+v", payload)
	}
	for _, message := range payload.Messages {
		if message.Content == "другой чат" {
			t.Fatal("export included another chat")
		}
	}
	data, err = ExportHistoryData(ctx, db, 300, "")
	if err != nil {
		t.Fatalf("ExportHistoryData Markdown: %v", err)
	}
	markdown := string(data)
	if !strings.Contains(markdown, "# История чата 300") || !strings.Contains(markdown, "вопрос") || !strings.Contains(markdown, "ответ") || strings.Contains(markdown, "другой чат") {
		t.Fatalf("Markdown export = %.500s", markdown)
	}
	data, err = ExportHistoryData(ctx, db, 302, "json")
	if err != nil || len(data) != 0 {
		t.Fatalf("empty export data = %d bytes, err = %v", len(data), err)
	}
	data, err = ExportHistoryData(ctx, db, 300, "csv")
	if len(data) != 0 || !errors.Is(err, ErrHistoryExportFormat) {
		t.Fatalf("invalid format data = %d bytes, err = %v", len(data), err)
	}
}

func TestExportHistoryDataTooLargeSentinel(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	if err := store.EnsureChat(ctx, db, 303, 11, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO messages (chat_id, role, content, created_at)
		VALUES (?, 'user', hex(zeroblob(?)), '2026-01-01T00:00:00Z')`, 303, ExportMaxBytes/2); err != nil {
		t.Fatalf("seed oversized export: %v", err)
	}
	for _, format := range []string{"json", "md"} {
		data, err := ExportHistoryData(ctx, db, 303, format)
		if len(data) != 0 || !errors.Is(err, ErrHistoryExportTooLarge) {
			t.Fatalf("%s oversized export data = %d bytes, err = %v", format, len(data), err)
		}
	}
}

func TestOversizedHistoryTranscriptStopsWithBoundedAllocations(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, &fakeModel{}, r, db)
	ctx := context.Background()
	if err := store.EnsureChat(ctx, db, 204, 11, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	_, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (
			SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 40
		)
		INSERT INTO messages (chat_id, role, content, created_at)
		SELECT ?, 'user', hex(zeroblob(262144)), strftime('%Y-%m-%dT%H:%M:%SZ', '2026-01-01T00:00:00Z', '+' || n || ' seconds')
		FROM seq`, 204)
	if err != nil {
		t.Fatalf("seed oversized transcript: %v", err)
	}
	for index, format := range []string{"json", "md"} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if err := dispatch(t, d, testEvent(204, 11, int64(1540+index), "/export "+format)); err != nil {
			t.Fatalf("export %s: %v", format, err)
		}
		runtime.ReadMemStats(&after)
		allocated := after.TotalAlloc - before.TotalAlloc
		if allocated > 8*ExportMaxBytes {
			t.Fatalf("%s export allocated %d bytes, above eight times the output cap, for a 20 MiB transcript", format, allocated)
		}
		if len(r.documents) != 0 {
			t.Fatalf("%s oversized export produced a document", format)
		}
		texts := r.texts()
		if len(texts) != index+1 || !strings.Contains(texts[len(texts)-1], "превышает 5 МиБ") {
			t.Fatalf("%s overflow replies=%q", format, texts)
		}
	}
}

func TestDeleteHistoryRequiresConfirmationAndPreservesPersona(t *testing.T) {
	d, r := newDeleteDispatcher(t, 201)
	if err := store.SetChatPersona(context.Background(), d.deps.DB, 201, "coder"); err != nil {
		t.Fatalf("SetChatPersona: %v", err)
	}
	if err := dispatch(t, d, testEvent(201, 11, 1510, "/delete")); err != nil {
		t.Fatalf("delete prompt: %v", err)
	}
	confirm, cancel := deleteCallbacks(t, r)
	if historyLength(t, d, 201) != 1 {
		t.Fatal("delete prompt removed history")
	}
	if err := dispatchDeleteCallback(t, d, 201, 11, 700, cancel); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := dispatch(t, d, testEvent(201, 11, 1512, "/delete")); err != nil {
		t.Fatalf("second delete prompt: %v", err)
	}
	confirm, _ = deleteCallbacks(t, r)
	if err := dispatchDeleteCallback(t, d, 201, 11, 700, confirm); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if historyLength(t, d, 201) != 0 {
		t.Fatal("confirmed delete left history")
	}
	if _, found, err := store.ChatPersona(context.Background(), d.deps.DB, 201); err != nil || !found {
		t.Fatalf("persona was removed: found=%v err=%v", found, err)
	}
}

func TestDeleteConfirmationConfirmAfterCancelIsStale(t *testing.T) {
	d, r := newDeleteDispatcher(t, 201)
	if err := dispatch(t, d, testEvent(201, 11, 1510, "/delete")); err != nil {
		t.Fatal(err)
	}
	confirm, cancel := deleteCallbacks(t, r)
	if err := dispatchDeleteCallback(t, d, 201, 11, 700, cancel); err != nil {
		t.Fatal(err)
	}
	if err := dispatchDeleteCallback(t, d, 201, 11, 700, confirm); err != nil {
		t.Fatal(err)
	}
	if historyLength(t, d, 201) != 1 || !strings.Contains(r.joined(), "истекло или уже использовано") {
		t.Fatalf("confirm after cancel deleted history or lacked warning: %q", r.joined())
	}
}

func TestDeleteConfirmationDoubleConfirmRunsOnce(t *testing.T) {
	d, r := newDeleteDispatcher(t, 201)
	if err := dispatch(t, d, testEvent(201, 11, 1510, "/delete")); err != nil {
		t.Fatal(err)
	}
	confirm, _ := deleteCallbacks(t, r)
	for i := 0; i < 2; i++ {
		if err := dispatchDeleteCallback(t, d, 201, 11, 700, confirm); err != nil {
			t.Fatal(err)
		}
	}
	if historyLength(t, d, 201) != 0 || strings.Count(r.joined(), "Удалено сообщений:") != 1 || !strings.Contains(r.joined(), "истекло или уже использовано") {
		t.Fatalf("double confirm result=%q", r.joined())
	}
}

func TestDeleteConfirmationExpires(t *testing.T) {
	d, r := newDeleteDispatcher(t, 201)
	if err := dispatch(t, d, testEvent(201, 11, 1510, "/delete")); err != nil {
		t.Fatal(err)
	}
	confirm, _ := deleteCallbacks(t, r)
	set := deleteConfirmationsFor(d)
	set.mu.Lock()
	for token, pending := range set.pending {
		pending.ExpiresAt = time.Now().Add(-time.Second)
		set.pending[token] = pending
	}
	set.mu.Unlock()
	if err := dispatchDeleteCallback(t, d, 201, 11, 700, confirm); err != nil {
		t.Fatal(err)
	}
	if historyLength(t, d, 201) != 1 || !strings.Contains(r.joined(), "истекло или уже использовано") {
		t.Fatalf("expired confirmation result=%q", r.joined())
	}
}

func TestDeleteConfirmationRejectsWrongUserChatAndMessage(t *testing.T) {
	d, r := newDeleteDispatcher(t, 201)
	if err := dispatch(t, d, testEvent(201, 11, 1510, "/delete")); err != nil {
		t.Fatal(err)
	}
	confirm, _ := deleteCallbacks(t, r)
	for _, ev := range []struct {
		chatID    int64
		userID    int64
		messageID int64
	}{
		{chatID: 201, userID: 12, messageID: 700},
		{chatID: 202, userID: 11, messageID: 700},
		{chatID: 201, userID: 11, messageID: 701},
	} {
		if err := dispatchDeleteCallback(t, d, ev.chatID, ev.userID, ev.messageID, confirm); err != nil {
			t.Fatal(err)
		}
		if historyLength(t, d, 201) != 1 {
			t.Fatalf("mismatched callback deleted history: %+v", ev)
		}
	}
	if !strings.Contains(r.joined(), "другому пользователю") || !strings.Contains(r.joined(), "этого чата или сообщения") {
		t.Fatalf("binding warnings=%q", r.joined())
	}
	if err := dispatchDeleteCallback(t, d, 201, 11, 700, confirm); err != nil {
		t.Fatal(err)
	}
	if historyLength(t, d, 201) != 0 {
		t.Fatal("valid confirmation was consumed by a mismatched callback")
	}
}

func TestDeleteConfirmationUnknownAndLegacyCallbacksCannotDelete(t *testing.T) {
	d, r := newDeleteDispatcher(t, 201)
	if err := dispatch(t, d, testEvent(201, 11, 1510, "/delete")); err != nil {
		t.Fatal(err)
	}
	confirm, _ := deleteCallbacks(t, r)
	for _, data := range []string{"delete:confirm", "delete:confirm:00000000000000000000000000000000"} {
		if err := dispatchDeleteCallback(t, d, 201, 11, 700, data); err != nil {
			t.Fatal(err)
		}
		if historyLength(t, d, 201) != 1 {
			t.Fatalf("callback %q deleted history", data)
		}
	}
	if err := dispatchDeleteCallback(t, d, 201, 11, 700, confirm); err != nil {
		t.Fatal(err)
	}
	if historyLength(t, d, 201) != 0 {
		t.Fatal("unknown callback consumed the valid token")
	}
}

func TestDeleteTextConfirmationAndCancelRemainAvailable(t *testing.T) {
	d, r := newDeleteDispatcher(t, 201)
	if err := dispatch(t, d, testEvent(201, 11, 1510, "/delete cancel")); err != nil {
		t.Fatal(err)
	}
	if historyLength(t, d, 201) != 1 {
		t.Fatal("text cancel deleted history")
	}
	if err := dispatch(t, d, testEvent(201, 11, 1511, "/delete confirm")); err != nil {
		t.Fatal(err)
	}
	if historyLength(t, d, 201) != 0 || !strings.Contains(r.joined(), "Удалено сообщений: 1") {
		t.Fatalf("text confirm reply=%q", r.joined())
	}
}

func TestDeleteConfirmationReplacesKeyboard(t *testing.T) {
	d, r := newDeleteDispatcher(t, 201)
	if err := dispatch(t, d, testEvent(201, 11, 1510, "/delete")); err != nil {
		t.Fatal(err)
	}
	_, cancel := deleteCallbacks(t, r)
	if err := dispatchDeleteCallback(t, d, 201, 11, 700, cancel); err != nil {
		t.Fatal(err)
	}
	edit := r.lastEdit()
	if edit.messageID != 700 || !hasMenuCallback(edit.markup, "menu:history:5") || !hasMenuCallback(edit.markup, "menu:root") || len(r.removed) != 0 {
		t.Fatalf("replacement edit=%+v removed keyboards=%+v", edit, r.removed)
	}
}
