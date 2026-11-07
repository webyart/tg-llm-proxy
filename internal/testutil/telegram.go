// DoD («интеграционный тест с фейковым getUpdates»).
//
// `bot.WithServerURL`: the library POSTs a multipart form to `{url}/bot{token}/{method}` and reads the
// `{"ok": …, "result": …, "error_code": …}` envelope back (raw_request.go of v1.27.0). Serving anything
//
// `offset` as a confirmation, so every update below it is gone forever. The fake does the same — that
// answered twice.
package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

type SentMessage struct {
	ChatID      int64
	Text        string
	ParseMode   string
	ReplyMarkup string
	// with no id precisely so that a stream cannot pretend it was rewritten into nothing.
	MessageID int
}

// whether the id and the parse mode came with them.
type SentEdit struct {
	ChatID      int64
	MessageID   int
	Text        string
	ParseMode   string // «» means the request carried no parse_mode at all — what a half-written answer needs
	ReplyMarkup string
}

type SentReplyMarkupEdit struct {
	ChatID      int64
	MessageID   int
	ReplyMarkup string
}

// WebhookRegistration records setWebhook exactly as the fake Bot API received it.
type WebhookRegistration struct {
	URL                string
	SecretToken        string
	AllowedUpdates     []string
	DropPendingUpdates bool
}

// TelegramFailure — one-shot error answer in the envelope of the Bot API. Code 429 with RetryAfter is
// → спать ровно RetryAfter секунд, записать events(kind='flood')»).
type TelegramFailure struct {
	Method      string // "" means the NEXT call of any method
	Code        int    // error_code of the envelope, 0 means 400
	Description string
	RetryAfter  int // parameters.retry_after, only meaningful for 429
}

type FakeFile struct {
	File        models.File
	Data        []byte
	ContentType string
}

// Telegram — the running fake. Every field is behind a mutex: the library polls and answers from
type Telegram struct {
	Server *httptest.Server
	// Token is the token the path must carry: `{url}/bot{token}/{method}`. A different token in the
	Token string
	// normalization has to strip the `@BotName` suffix Telegram appends in groups).
	BotUsername string
	// BotID is the numeric half of the token, echoed by getMe.
	BotID int64
	// and a test must not sit for a minute waiting for an empty batch.
	MaxBlock time.Duration

	mu               sync.Mutex
	pending          []models.Update
	delivered        []models.Update // confirmed by an offset — Replay puts them back
	sent             []SentMessage
	documents        []SentDocument
	edits            []SentEdit // the rewrites of
	replyMarkupEdits []SentReplyMarkupEdit
	sentIDs          map[int]bool // the ids this fake handed out: an edit of an id it never gave is a wiring bug
	calls            map[string]int
	commands         []models.BotCommand
	menuButton       string
	menuButtonSet    bool
	webhook          *WebhookRegistration
	files            map[string]FakeFile
	failures         []TelegramFailure
	wrongTokens      int
	offsets          []string // the offset params the library sent, for the offset assertions of
	wake             chan struct{}
	nextSendID       int
}

// NewTelegram — start the fake. The token is mandatory: the path check is what proves run.go passed
func NewTelegram(t *testing.T, token string) *Telegram {
	t.Helper()
	if strings.TrimSpace(token) == "" {
		t.Fatalf("the fake Telegram needs the token to check the request path ")
	}
	var s = &Telegram{
		Token:       token,
		BotUsername: "FakeBot",
		BotID:       777,
		MaxBlock:    150 * time.Millisecond,
		calls:       map[string]int{},
		files:       map[string]FakeFile{},
		sentIDs:     map[int]bool{},
		wake:        make(chan struct{}, 1),
		nextSendID:  1,
	}
	var mux = http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

func (s *Telegram) URL() string { return s.Server.URL }

func (s *Telegram) Push(upd models.Update) {
	s.mu.Lock()
	s.pending = append(s.pending, upd)
	s.mu.Unlock()
	s.notify()
}

func (s *Telegram) AddFile(fileID string, filePath string, contentType string, data []byte) {
	s.mu.Lock()
	s.files[fileID] = FakeFile{
		File:        models.File{FileID: fileID, FilePath: filePath, FileSize: int64(len(data))},
		Data:        append([]byte(nil), data...),
		ContentType: contentType,
	}
	s.mu.Unlock()
}

func (s *Telegram) PushPhoto(updateID, chatID, userID int64, fileID string, caption string) {
	s.mu.Lock()
	size := int(s.files[fileID].File.FileSize)
	s.mu.Unlock()
	s.Push(models.Update{ID: updateID, Message: &models.Message{
		ID: int(updateID), Date: int(time.Now().Unix()), Caption: caption,
		Chat:  models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		From:  &models.User{ID: userID, IsBot: false, FirstName: "Tester", Username: "tester"},
		Photo: []models.PhotoSize{{FileID: fileID, Width: 2, Height: 2, FileSize: size}},
	}})
}

func (s *Telegram) PushDocument(updateID, chatID, userID int64, fileID string, name string, mime string, caption string) {
	s.mu.Lock()
	size := s.files[fileID].File.FileSize
	s.mu.Unlock()
	s.Push(models.Update{ID: updateID, Message: &models.Message{
		ID: int(updateID), Date: int(time.Now().Unix()), Caption: caption,
		Chat:     models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		From:     &models.User{ID: userID, IsBot: false, FirstName: "Tester", Username: "tester"},
		Document: &models.Document{FileID: fileID, FileName: name, MimeType: mime, FileSize: size},
	}})
}

// offset assertions read better when one number identifies the whole update.
func (s *Telegram) PushText(updateID, chatID, userID int64, text string) models.Update {
	var upd = models.Update{
		ID: updateID,
		Message: &models.Message{
			ID:   int(updateID),
			Date: int(time.Now().Unix()),
			Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
			From: &models.User{ID: userID, IsBot: false, FirstName: "Tester", Username: "tester"},
			Text: text,
		},
	}
	s.Push(upd)
	return upd
}

func (s *Telegram) Replay() {
	s.mu.Lock()
	s.pending = append(s.pending, s.delivered...)
	s.delivered = nil
	sort.Slice(s.pending, func(i, j int) bool { return s.pending[i].ID < s.pending[j].ID })
	s.mu.Unlock()
	s.notify()
}

// FailNext — the next call of `method` answers the error envelope instead of a result (the flood case
func (s *Telegram) FailNext(method string, code int, description string, retryAfter int) {
	s.mu.Lock()
	s.failures = append(s.failures, TelegramFailure{Method: method, Code: code, Description: description, RetryAfter: retryAfter})
	s.mu.Unlock()
}

// Calls — how many requests named `method` arrived (getMe, getUpdates, sendMessage, setMyCommands).
func (s *Telegram) Calls(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

func (s *Telegram) Sent() []SentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out = make([]SentMessage, len(s.sent))
	copy(out, s.sent)
	return out
}

func (s *Telegram) Texts() []string {
	var sent = s.Sent()
	var out = make([]string, 0, len(sent))
	var i = 0
	for i < len(sent) {
		out = append(out, sent[i].Text)
		i++
	}
	return out
}

// the user still reads one message).
func (s *Telegram) Joined() string { return strings.Join(s.Texts(), "\n") }

// them; this says what each one addressed and carried.
func (s *Telegram) Edits() []SentEdit {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out = make([]SentEdit, len(s.edits))
	copy(out, s.edits)
	return out
}

func (s *Telegram) EditStrings() []string {
	var edits = s.Edits()
	var out = make([]string, 0, len(edits))
	var i = 0
	for i < len(edits) {
		out = append(out, edits[i].Text)
		i++
	}
	return out
}

func (s *Telegram) ReplyMarkupEdits() []SentReplyMarkupEdit {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SentReplyMarkupEdit, len(s.replyMarkupEdits))
	copy(out, s.replyMarkupEdits)
	return out
}

func (s *Telegram) MenuButton() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.menuButton, s.menuButtonSet
}

func (s *Telegram) Commands() []models.BotCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out = make([]models.BotCommand, len(s.commands))
	copy(out, s.commands)
	return out
}

// Webhook returns the registered public URL, secret, and update filter.
func (s *Telegram) Webhook() (WebhookRegistration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.webhook == nil {
		return WebhookRegistration{}, false
	}
	out := *s.webhook
	out.AllowedUpdates = append([]string{}, out.AllowedUpdates...)
	return out, true
}

func (s *Telegram) Offsets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out = make([]string, len(s.offsets))
	copy(out, s.offsets)
	return out
}

// here instead of in the log).
func (s *Telegram) WrongTokens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wrongTokens
}

// notify — wake a long-poll that waits for nothing. Non-blocking: one pending wake is enough, because
// take() re-reads the whole queue.
func (s *Telegram) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// handle — the Bot API envelope. The library sends every method as a POST to
// `{url}/bot{token}/{method}`, so the path is the method table and the multipart form is the params
func (s *Telegram) handle(w http.ResponseWriter, r *http.Request) {
	var path = strings.TrimPrefix(r.URL.Path, "/")
	if strings.HasPrefix(path, "file/") {
		s.handleFile(w, r, strings.TrimPrefix(path, "file/"))
		return
	}
	var token, method, ok = splitBotPath(path)
	if !ok {
		http.Error(w, "the fake serves only {url}/bot{token}/{method} ", http.StatusNotFound)
		return
	}
	if token != s.Token {
		s.mu.Lock()
		s.wrongTokens++
		s.mu.Unlock()
		writeError(w, http.StatusUnauthorized, "the token of the path is not the token of the fake ", 0)
		return
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		// getMe arrives with no body at all (raw_request.go sends http.NoBody when the params are empty),
		// and ParseForm still reads a urlencoded or a query-string call.
		_ = r.ParseForm()
	}

	s.mu.Lock()
	s.calls[method]++
	var failure, hasFailure = s.takeFailure(method)
	s.mu.Unlock()
	if hasFailure {
		writeError(w, failure.Code, failure.Description, failure.RetryAfter)
		return
	}

	switch method {
	case "getMe":
		writeOK(w, models.User{ID: s.BotID, IsBot: true, FirstName: "Fake", Username: s.BotUsername})
	case "getUpdates":
		s.handleGetUpdates(w, r)
	case "getFile":
		fileID := r.FormValue("file_id")
		s.mu.Lock()
		file, found := s.files[fileID]
		s.mu.Unlock()
		if !found {
			writeError(w, http.StatusNotFound, "file not found", 0)
			return
		}
		writeOK(w, file.File)
	case "sendMessage":
		s.handleSendMessage(w, r)
	case "sendDocument":
		s.handleSendDocument(w, r)
	case "editMessageText":
		s.handleEditMessageText(w, r)
	case "editMessageReplyMarkup":
		s.handleEditMessageReplyMarkup(w, r)
	case "setMyCommands":
		s.handleSetMyCommands(w, r)
	case "setChatMenuButton":
		s.handleSetChatMenuButton(w, r)
	case "setWebhook":
		s.handleSetWebhook(w, r)
	case "answerCallbackQuery":
		writeOK(w, true)
	default:
		writeError(w, http.StatusNotFound, "the fake does not serve "+method+" — v1 of uses getMe, getUpdates, sendMessage, setMyCommands, and adds editMessageText", 0)
	}
}

func (s *Telegram) handleFile(w http.ResponseWriter, r *http.Request, path string) {
	token, filePath, ok := splitBotPath(path)
	if !ok || token != s.Token {
		s.mu.Lock()
		s.wrongTokens++
		s.mu.Unlock()
		http.Error(w, "invalid file path", http.StatusNotFound)
		return
	}
	s.mu.Lock()
	s.calls["download"]++
	var found FakeFile
	known := false
	for _, file := range s.files {
		if file.File.FilePath == filePath {
			found = file
			known = true
			break
		}
	}
	s.mu.Unlock()
	if !known {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	if found.ContentType != "" {
		w.Header().Set("Content-Type", found.ContentType)
	}
	_, _ = w.Write(found.Data)
}

// takeFailure — consume the one-shot failure queued for this method ("" matches any method).
func (s *Telegram) takeFailure(method string) (TelegramFailure, bool) {
	var i = 0
	for i < len(s.failures) {
		if s.failures[i].Method == "" || s.failures[i].Method == method {
			var f = s.failures[i]
			s.failures = append(s.failures[:i], s.failures[i+1:]...)
			return f, true
		}
		i++
	}
	return TelegramFailure{}, false
}

// updates), so the fake removes them too and only Replay brings them back.
func (s *Telegram) handleGetUpdates(w http.ResponseWriter, r *http.Request) {
	var offsetText = r.FormValue("offset")
	var offset, _ = strconv.ParseInt(offsetText, 10, 64)
	var timeout, _ = strconv.ParseInt(r.FormValue("timeout"), 10, 64)
	var limit, _ = strconv.ParseInt(r.FormValue("limit"), 10, 64)

	s.mu.Lock()
	s.offsets = append(s.offsets, offsetText)
	s.mu.Unlock()

	var batch = s.take(offset, int(limit))
	if len(batch) == 0 && timeout > 0 {
		// open; the fake waits, but never longer than MaxBlock — otherwise every empty poll costs a
		var wait = time.Duration(timeout) * time.Second
		if s.MaxBlock > 0 && wait > s.MaxBlock {
			wait = s.MaxBlock
		}
		select {
		case <-r.Context().Done():
			writeOK(w, []models.Update{})
			return
		case <-s.wake:
		case <-time.After(wait):
		}
		batch = s.take(offset, int(limit))
	}
	writeOK(w, batch)
}

// take — one getUpdates batch: updates with `update_id >= offset` are returned, updates below the
// «сдвигается на processed update_id»).
func (s *Telegram) take(offset int64, limit int) []models.Update {
	s.mu.Lock()
	defer s.mu.Unlock()
	var batch = []models.Update{}
	var keep = make([]models.Update, 0, len(s.pending))
	var i = 0
	for i < len(s.pending) {
		var upd = s.pending[i]
		if upd.ID < offset {
			s.delivered = append(s.delivered, upd) // confirmed by the offset the library sent
			i++
			continue
		}
		if limit <= 0 || len(batch) < limit {
			batch = append(batch, upd)
			i++
			continue
		}
		keep = append(keep, upd) // the limit of (the library sends 100)
		i++
	}
	s.pending = keep
	return batch
}

func (s *Telegram) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var chatID, err = strconv.ParseInt(strings.TrimSpace(r.FormValue("chat_id")), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "chat_id is required ", 0)
		return
	}
	var text = r.FormValue("text")
	if text == "" {
		writeError(w, http.StatusBadRequest, "text is required ", 0)
		return
	}
	s.mu.Lock()
	var id = s.nextSendID
	s.nextSendID++
	s.sentIDs[id] = true
	s.sent = append(s.sent, SentMessage{ChatID: chatID, Text: text, ParseMode: r.FormValue("parse_mode"), ReplyMarkup: r.FormValue("reply_markup"), MessageID: id})
	s.mu.Unlock()

	// models.Message has a custom UnmarshalJSON that decides "accessible" by the `date` field, so the
	// fake answers a real date: a zero date would make the library hand back an InaccessibleMessage.
	writeOK(w, models.Message{
		ID:   id,
		Date: int(time.Now().Unix()),
		Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
	})
}

// being rewritten, the chat it lives in, the text, and whether the request carried a parse_mode. The id is
// checked against the ids this fake handed out, because that is the one thing about a stream that a fake must
// be strict about: a bot that rewrites an id it never received a reply for is editing somebody else's message
// (or nothing at all), and real Telegram answers 400 «message to edit not found».
func (s *Telegram) handleEditMessageText(w http.ResponseWriter, r *http.Request) {
	var chatID, err = strconv.ParseInt(strings.TrimSpace(r.FormValue("chat_id")), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "chat_id is required ", 0)
		return
	}
	var messageID, parseErr = strconv.ParseInt(strings.TrimSpace(r.FormValue("message_id")), 10, 64)
	if parseErr != nil {
		writeError(w, http.StatusBadRequest, "message_id is required ", 0)
		return
	}
	var text = r.FormValue("text")
	if text == "" {
		writeError(w, http.StatusBadRequest, "text is required ", 0)
		return
	}
	s.mu.Lock()
	var known = s.sentIDs[int(messageID)]
	if known {
		s.edits = append(s.edits, SentEdit{
			ChatID:      chatID,
			MessageID:   int(messageID),
			Text:        text,
			ParseMode:   r.FormValue("parse_mode"),
			ReplyMarkup: r.FormValue("reply_markup"),
		})
	}
	s.mu.Unlock()
	if !known {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("the fake never handed out message id %d to edit ", messageID), 0)
		return
	}

	// Telegram answers the edited message, not `true`. The date is what makes models.Message parseable
	// (its UnmarshalJSON decides «accessible» by `date`), same reason as in handleSendMessage; `edit_date`
	// is what the Bot API puts on a rewritten message. models.Message has no parse_mode field, which is why
	writeOK(w, models.Message{
		ID:       int(messageID),
		Date:     int(time.Now().Unix()),
		EditDate: int(time.Now().Unix()),
		Chat:     models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		Text:     text,
		Entities: []models.MessageEntity{},
	})
}

func (s *Telegram) handleEditMessageReplyMarkup(w http.ResponseWriter, r *http.Request) {
	chatID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("chat_id")), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "chat_id is required", 0)
		return
	}
	messageID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("message_id")), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "message_id is required", 0)
		return
	}
	replyMarkup := r.FormValue("reply_markup")
	if replyMarkup != "" {
		var markup models.InlineKeyboardMarkup
		if err := json.Unmarshal([]byte(replyMarkup), &markup); err != nil {
			writeError(w, http.StatusBadRequest, "reply_markup must be valid JSON", 0)
			return
		}
	}
	s.mu.Lock()
	known := s.sentIDs[int(messageID)]
	if known {
		s.replyMarkupEdits = append(s.replyMarkupEdits, SentReplyMarkupEdit{ChatID: chatID, MessageID: int(messageID), ReplyMarkup: replyMarkup})
	}
	s.mu.Unlock()
	if !known {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("the fake never handed out message id %d to edit", messageID), 0)
		return
	}
	writeOK(w, models.Message{ID: int(messageID), Date: int(time.Now().Unix()), EditDate: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}})
}

func (s *Telegram) handleSetChatMenuButton(w http.ResponseWriter, r *http.Request) {
	raw := r.FormValue("menu_button")
	var button models.MenuButton
	if err := json.Unmarshal([]byte(raw), &button); err != nil {
		writeError(w, http.StatusBadRequest, "menu_button must be valid JSON: "+err.Error(), 0)
		return
	}
	s.mu.Lock()
	s.menuButton = raw
	s.menuButtonSet = true
	s.mu.Unlock()
	writeOK(w, true)
}

func (s *Telegram) handleSetWebhook(w http.ResponseWriter, r *http.Request) {
	var allowed []string
	if raw := r.FormValue("allowed_updates"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &allowed); err != nil {
			writeError(w, http.StatusBadRequest, "allowed_updates must be a JSON array: "+err.Error(), 0)
			return
		}
	}
	dropPending, err := strconv.ParseBool(r.FormValue("drop_pending_updates"))
	if err != nil && r.FormValue("drop_pending_updates") != "" {
		writeError(w, http.StatusBadRequest, "drop_pending_updates must be boolean", 0)
		return
	}
	registration := &WebhookRegistration{
		URL:                r.FormValue("url"),
		SecretToken:        r.FormValue("secret_token"),
		AllowedUpdates:     allowed,
		DropPendingUpdates: dropPending,
	}
	if registration.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required", 0)
		return
	}
	s.mu.Lock()
	s.webhook = registration
	s.mu.Unlock()
	writeOK(w, true)
}

func (s *Telegram) handleSetMyCommands(w http.ResponseWriter, r *http.Request) {
	var raw = r.FormValue("commands")
	var cmds = []models.BotCommand{}
	if err := json.Unmarshal([]byte(raw), &cmds); err != nil {
		writeError(w, http.StatusBadRequest, "commands must be a JSON array of {command, description}: "+err.Error(), 0)
		return
	}
	s.mu.Lock()
	s.commands = cmds
	s.mu.Unlock()
	writeOK(w, true)
}

// splitBotPath — `bot<token>/sendMessage` → (token, "sendMessage"). The shape raw_request.go builds
// (`u := b.url + "/bot" + b.token + "/"`, plus "test/" when UseTestEnvironment is on).
func splitBotPath(path string) (token string, method string, ok bool) {
	var rest, found = strings.CutPrefix(path, "bot")
	if !found {
		return "", "", false
	}
	var slash = strings.Index(rest, "/")
	if slash < 0 {
		return "", "", false
	}
	token, rest = rest[:slash], rest[slash+1:]
	rest = strings.TrimPrefix(rest, "test/")
	rest = strings.Trim(rest, "/")
	return token, rest, rest != ""
}

// writeOK — the success envelope of the Bot API.
func writeOK(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// writeError — the failure envelope the library turns into ErrorForbidden / TooManyRequestsError and
// friends (raw_request.go). `retryAfter` lands in parameters.retry_after, which is the field
// library, but the fake sets it anyway so a real client would see the same thing.
func writeError(w http.ResponseWriter, code int, description string, retryAfter int) {
	if code == 0 {
		code = http.StatusBadRequest
	}
	if description == "" {
		description = "the fake was told to fail "
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":          false,
		"error_code":  code,
		"description": description,
		"parameters":  map[string]any{"retry_after": retryAfter},
	})
}
