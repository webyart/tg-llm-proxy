// helpers_test.go — the fakes §11 requires for the §6 scenarios without a Telegram API, a token or a
// GPU: a recording Replier, a scripted model.Client and the in-memory SQLite of §5. The config comes
// from config.LoadFrom, so the tests exercise the REAL defaults and bounds of §7 instead of a copy.
package bot

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	tgmodels "github.com/go-telegram/bot/models"

	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
)

type sentMessage struct {
	chatID int64
	text   string
}

type fakeReplier struct {
	mu    sync.Mutex
	sent  []sentMessage
	menus []tgmodels.InlineKeyboardMarkup
	err   error
}

func (f *fakeReplier) SendMenu(ctx context.Context, chatID int64, text string, markup tgmodels.InlineKeyboardMarkup) error {
	f.mu.Lock()
	f.menus = append(f.menus, markup)
	f.mu.Unlock()
	return f.Send(ctx, chatID, text)
}

func (f *fakeReplier) Send(ctx context.Context, chatID int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, sentMessage{chatID: chatID, text: text})
	return nil
}

func (f *fakeReplier) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out = []string{}
	var i = 0
	for i < len(f.sent) {
		out = append(out, f.sent[i].text)
		i++
	}
	return out
}

func (f *fakeReplier) joined() string { return strings.Join(f.texts(), "\n") }

func (f *fakeReplier) menu() []tgmodels.InlineKeyboardButton {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.menus) == 0 {
		return nil
	}
	return f.menus[len(f.menus)-1].InlineKeyboard[0]
}

func (f *fakeReplier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// fakeModel — model.Client with a script: a Response, an error, a delay (for the per-chat order of
// §12), a panic (for the recovery of §12) and a model list (for /model check of §6).
type fakeModel struct {
	mu         sync.Mutex
	calls      int
	requests   []model.Request
	response   model.Response
	err        error
	panicky    bool
	delay      time.Duration
	contentFor func(req model.Request) string // the answer of §6 per request, when the test needs it
	models     []string
	modelsErr  error
}

func (m *fakeModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	if m.panicky {
		panic("the fake model exploded (§12 надёжность: panic recovery)")
	}
	m.mu.Lock()
	m.calls++
	m.requests = append(m.requests, req)
	m.mu.Unlock()

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
	if m.err != nil {
		return model.Response{}, m.err
	}
	var resp = m.response
	if m.contentFor != nil {
		resp.Content = m.contentFor(req)
	}
	if resp.Model == "" {
		resp.Model = "fake-model"
	}
	if resp.Latency == 0 {
		resp.Latency = 5 * time.Millisecond
	}
	if resp.Attempts == 0 {
		resp.Attempts = 1
	}
	return resp, nil
}

func (m *fakeModel) Models(ctx context.Context) ([]string, error) {
	if m.modelsErr != nil {
		return nil, m.modelsErr
	}
	return m.models, nil
}

func (m *fakeModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *fakeModel) lastRequest() model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return model.Request{}
	}
	return m.requests[len(m.requests)-1]
}

// testConfig — the §7 loader with the test ENV. PARSE_MODE=plain keeps the assertions about the text
// readable (the escaping itself is tested in reply_test.go).
func testConfig(t *testing.T, overrides map[string]string) config.Config {
	t.Helper()
	var raw = map[string]string{
		"TELEGRAM_BOT_TOKEN": "12345:test-token-not-a-secret",
		"PARSE_MODE":         "plain",
		"DB_PATH":            "./data/test.db",
	}
	var k string
	for k = range overrides {
		raw[k] = overrides[k]
	}
	var cfg, problems = config.LoadFrom(raw)
	if len(problems) > 0 {
		t.Fatalf("config.LoadFrom rejected the test ENV (§7 validation): %v", problems)
	}
	return cfg
}

// testStore — the in-memory SQLite of §5 with a unique name (§5 note: cache=shared is dangerous
// between tests of one package, so the name carries the test name).
func testStore(t *testing.T) *sql.DB {
	t.Helper()
	return testStoreNamed(t, t.Name())
}

// testStoreNamed — a fresh DB for one sub-case of a table-driven test: cache=shared means two cases
// that ask for the same name share the rows, which would make a "one row" assertion pass by accident.
// The name is sanitized because store.MemoryDSN accepts a bare identifier only (§5: the name lands in
// the URI path).
func testStoreNamed(t *testing.T, name string) *sql.DB {
	t.Helper()
	var runes = []rune(name)
	var out = make([]rune, 0, len(runes))
	var i = 0
	for i < len(runes) {
		var r = runes[i]
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
		i++
	}
	var db, err = store.OpenMemory(context.Background(), string(out))
	if err != nil {
		t.Fatalf("store.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestDispatcher — the wiring of §10 День 4 without the library: the offset writer is the real
// store.SetLastUpdateID of §5, so the offset assertions read the same row a restart would read. The
// optional logger is for §10 День 7 («логи slog (JSON) с chat_id, latency_ms, model»): a test that cares
// about the log shape passes a JSON handler, everyone else keeps the silent default of New.
//
// The model and the replier are the INTERFACES of §4/§6 rather than the fakes below, because §15 tests need a
// model that can stream and a replier that can edit — an optional capability is only reachable through the
// interface it is asserted against (streamingPossible of stream.go).
func newTestDispatcher(t *testing.T, cfg config.Config, m model.Client, r Replier, db *sql.DB, logger ...*slog.Logger) *Dispatcher {
	t.Helper()
	if db == nil {
		db = testStore(t)
	}
	if r == nil {
		r = &fakeReplier{}
	}
	if m == nil {
		m = &fakeModel{response: model.Response{Content: "ответ модели"}}
	}
	var offset, _, err = store.LastUpdateID(context.Background(), db)
	if err != nil {
		t.Fatalf("store.LastUpdateID: %v", err)
	}
	var deps = Deps{
		Cfg:         cfg,
		DB:          db,
		Model:       m,
		Replier:     r,
		Token:       cfg.BotToken,
		BotName:     "TestBot",
		StartOffset: offset, // the restart case of §5: the dispatcher continues from bot_state
		Offset: func(ctx context.Context, updateID int64) error {
			return store.SetLastUpdateID(ctx, db, updateID)
		},
	}
	if len(logger) > 0 && logger[0] != nil {
		deps.Logger = logger[0] // §10 День 7: the caller reads the JSON lines the process would write
	}
	return New(deps)
}

// testEvent — one message of §6 as run.go would build it from models.Update.
func testEvent(chatID int64, userID int64, tgMessageID int64, text string) Event {
	return Event{
		UpdateID:          tgMessageID + 1000,
		TelegramMessageID: tgMessageID,
		ChatID:            chatID,
		ChatType:          "private",
		UserID:            userID,
		Username:          "tester",
		Text:              text,
	}
}

// merge — a small helper for the §7 overrides of the tests.
func merge(base map[string]string, extra map[string]string) map[string]string {
	var out = map[string]string{}
	var k string
	for k = range base {
		out[k] = base[k]
	}
	for k = range extra {
		out[k] = extra[k]
	}
	return out
}

// dispatch — Dispatch with a fresh ctx and a fatal error on failure (the tests of §6 never want a
// silent refusal to look like a pass).
func dispatch(t *testing.T, d *Dispatcher, ev Event) error {
	t.Helper()
	return d.Dispatch(context.Background(), ev)
}

// intPtr — the nullable token columns of §5 (tokens_in, tokens_out) need a pointer, and &10 is not
// legal Go.
func intPtr(v int64) *int64 { return &v }
