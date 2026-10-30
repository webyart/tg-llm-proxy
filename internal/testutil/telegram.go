// telegram.go — the fake Telegram Bot API of §11 («Фейк Telegram обязателен для интеграционного теста
// без токена и без сети (и именно он проверяет разбиение/экранирование ответов)») and of the §10 День 4
// DoD («интеграционный тест с фейковым getUpdates»).
//
// The fake speaks the wire shape of the library, because §10 День 4 points the real client at it with
// `bot.WithServerURL`: the library POSTs a multipart form to `{url}/bot{token}/{method}` and reads the
// `{"ok": …, "result": …, "error_code": …}` envelope back (raw_request.go of v1.27.0). Serving anything
// else would test a bot that does not exist. Four methods are enough for §6: getMe (New calls it and
// fails without an answer — §10 День 4), getUpdates (long polling with the offset semantics of §5),
// sendMessage (the chunked answer of §12 UX) and setMyCommands (the command menu of §10 День 4).
//
// The offset semantics are the point of the restart test (§11 уровень «Integration»): Telegram treats
// `offset` as a confirmation, so every update below it is gone forever. The fake does the same — that
// is what makes `bot.WithInitialOffset` + `bot_state` of §5 provable, and `Replay` is the second line
// of defence of §2 п.7: an update that comes back must be swallowed by the UNIQUE guard of §5, not
// answered twice.
package testutil

import (
	"encoding/json"
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

// SentMessage — one sendMessage the fake received (§11: the fake is what checks the chunking and the
// parse mode of §7, because nothing else in the test knows how the answer left the process).
type SentMessage struct {
	ChatID    int64
	Text      string
	ParseMode string
}

// TelegramFailure — one-shot error answer in the envelope of the Bot API. Code 429 with RetryAfter is
// the flood case of §10 День 4 («на исходящем sendMessage библиотека возвращает TooManyRequestsError
// → спать ровно RetryAfter секунд, записать events(kind='flood')»).
type TelegramFailure struct {
	Method      string // "" means the NEXT call of any method
	Code        int    // error_code of the envelope, 0 means 400
	Description string
	RetryAfter  int // parameters.retry_after, only meaningful for 429
}

// Telegram — the running fake. Every field is behind a mutex: the library polls and answers from
// several goroutines, and §10 День 6 pushes parallel messages of one chat through it.
type Telegram struct {
	Server *httptest.Server
	// Token is the token the path must carry: `{url}/bot{token}/{method}`. A different token in the
	// path is a wiring mistake, and the fake answers 401 instead of pretending success (§12 security).
	Token string
	// BotUsername is what getMe reports (§10 День 4: run.go reads the name from getMe, because §6
	// normalization has to strip the `@BotName` suffix Telegram appends in groups).
	BotUsername string
	// BotID is the numeric half of the token, echoed by getMe.
	BotID int64
	// MaxBlock caps the long-poll wait: the library asks for `timeout = pollTimeout - 1s` (§10 День 4),
	// and a test must not sit for a minute waiting for an empty batch.
	MaxBlock time.Duration

	mu          sync.Mutex
	pending     []models.Update
	delivered   []models.Update // confirmed by an offset — Replay puts them back
	sent        []SentMessage
	calls       map[string]int
	commands    []models.BotCommand
	failures    []TelegramFailure
	wrongTokens int
	offsets     []string // the offset params the library sent, for the offset assertions of §5
	wake        chan struct{}
	nextSendID  int
}

// NewTelegram — start the fake. The token is mandatory: the path check is what proves run.go passed
// TELEGRAM_BOT_TOKEN of §7 to the library rather than to the fake's imagination.
func NewTelegram(t *testing.T, token string) *Telegram {
	t.Helper()
	if strings.TrimSpace(token) == "" {
		t.Fatalf("the fake Telegram needs the token to check the request path (§12 security)")
	}
	var s = &Telegram{
		Token:       token,
		BotUsername: "FakeBot",
		BotID:       777,
		MaxBlock:    150 * time.Millisecond,
		calls:       map[string]int{},
		wake:        make(chan struct{}, 1),
		nextSendID:  1,
	}
	var mux = http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

// URL — the value for `bot.WithServerURL` (§11, §16 change 16). The library appends `/bot<token>/`.
func (s *Telegram) URL() string { return s.Server.URL }

// Push — one scripted update. The update is delivered until an offset confirms it (§5).
func (s *Telegram) Push(upd models.Update) {
	s.mu.Lock()
	s.pending = append(s.pending, upd)
	s.mu.Unlock()
	s.notify()
}

// PushText — an incoming text message of §6, the shape Telegram sends for a command or plain text.
// Update id and message id are the same number here on purpose: nothing in §5 depends on it, and the
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

// Replay — the redelivery case of §2 п.7: the updates the offset had already confirmed come back. A
// correct dispatcher must answer none of them (§13 риск «повторные ответы после рестарта»).
func (s *Telegram) Replay() {
	s.mu.Lock()
	s.pending = append(s.pending, s.delivered...)
	s.delivered = nil
	sort.Slice(s.pending, func(i, j int) bool { return s.pending[i].ID < s.pending[j].ID })
	s.mu.Unlock()
	s.notify()
}

// FailNext — the next call of `method` answers the error envelope instead of a result (the flood case
// of §10 День 4, the reliability of §12). One failure per call: the retry has to succeed.
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

// Sent — the messages the bot sent, in the order it sent them (§11: the chunk order of §12 UX).
func (s *Telegram) Sent() []SentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out = make([]SentMessage, len(s.sent))
	copy(out, s.sent)
	return out
}

// Texts — only the texts of Sent, for the content assertions of §6.
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

// Joined — every sent text glued with "\n": the chunks of one answer (§16 change 7 cuts at a word,
// the user still reads one message).
func (s *Telegram) Joined() string { return strings.Join(s.Texts(), "\n") }

// Commands — what setMyCommands published (§10 День 4: «setMyCommands при старте»).
func (s *Telegram) Commands() []models.BotCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out = make([]models.BotCommand, len(s.commands))
	copy(out, s.commands)
	return out
}

// Offsets — the `offset` values of the getUpdates requests, in order (§5 «Offset long polling»).
func (s *Telegram) Offsets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out = make([]string, len(s.offsets))
	copy(out, s.offsets)
	return out
}

// WrongTokens — requests whose path carried another token (§12 security: a mixed-up token shows up
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
// (raw_request.go of v1.27.0, §16 change 16).
func (s *Telegram) handle(w http.ResponseWriter, r *http.Request) {
	var path = strings.TrimPrefix(r.URL.Path, "/")
	var token, method, ok = splitBotPath(path)
	if !ok {
		http.Error(w, "the fake serves only {url}/bot{token}/{method} (§16 change 16)", http.StatusNotFound)
		return
	}
	if token != s.Token {
		s.mu.Lock()
		s.wrongTokens++
		s.mu.Unlock()
		writeError(w, http.StatusUnauthorized, "the token of the path is not the token of the fake (§12 security)", 0)
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
	case "sendMessage":
		s.handleSendMessage(w, r)
	case "setMyCommands":
		s.handleSetMyCommands(w, r)
	default:
		// §6 v1 needs no other method; answering ok would hide a wrong call (§12 reliability).
		writeError(w, http.StatusNotFound, "the fake does not serve "+method+" — v1 of §6 uses getMe, getUpdates, sendMessage, setMyCommands", 0)
	}
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

// handleGetUpdates — the offset of §5: `offset` confirms everything below it (Telegram removes those
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
		// The long poll of §10 День 4: the library asks for `pollTimeout - 1s` and keeps the connection
		// open; the fake waits, but never longer than MaxBlock — otherwise every empty poll costs a
		// minute of test time (§11: tests must stay fast enough for §10 День 6).
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
// offset are confirmed and leave the queue forever (§5 «Offset long polling», §10 День 4
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
		keep = append(keep, upd) // the limit of §10 День 4 (the library sends 100)
		i++
	}
	s.pending = keep
	return batch
}

// handleSendMessage — record the answer of §6 exactly as the Bot API sees it: chat_id, text,
// parse_mode. That is what §11 checks about the chunking and the escaping of §12 UX.
func (s *Telegram) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var chatID, err = strconv.ParseInt(strings.TrimSpace(r.FormValue("chat_id")), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "chat_id is required (§6: the answer goes to one chat)", 0)
		return
	}
	var text = r.FormValue("text")
	if text == "" {
		writeError(w, http.StatusBadRequest, "text is required (§12 UX: an empty answer is not an answer)", 0)
		return
	}
	s.mu.Lock()
	var id = s.nextSendID
	s.nextSendID++
	s.sent = append(s.sent, SentMessage{ChatID: chatID, Text: text, ParseMode: r.FormValue("parse_mode")})
	s.mu.Unlock()

	// models.Message has a custom UnmarshalJSON that decides "accessible" by the `date` field, so the
	// fake answers a real date: a zero date would make the library hand back an InaccessibleMessage.
	writeOK(w, models.Message{
		ID:   id,
		Date: int(time.Now().Unix()),
		Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
	})
}

// handleSetMyCommands — the menu of §10 День 4; the commands arrive as a JSON string inside the form.
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
// TooManyRequestsError.RetryAfter of §10 День 4 reads. The HTTP status itself is ignored by the
// library, but the fake sets it anyway so a real client would see the same thing.
func writeError(w http.ResponseWriter, code int, description string, retryAfter int) {
	if code == 0 {
		code = http.StatusBadRequest
	}
	if description == "" {
		description = "the fake was told to fail (§10 День 4)"
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
