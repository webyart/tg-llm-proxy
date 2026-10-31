//go:build integration

// run_test.go — the Integration level of §11 («бот ↔ фейковый STRATA ↔ фейковый Telegram API
// (bot.WithServerURL)», «Go-тесты //go:build integration») for §10 День 4. These tests are the DoD of
// День 4 («интеграционный тест с фейковым getUpdates», «рестарт не повторяет ответы») and of §11, where
// the fake Telegram is explicitly the thing that «проверяет разбиение/экранирование ответов».
//
// Nothing here mocks the dispatcher: run.go is attached to the real Dispatcher, the real SQLite of §5
// lives in t.TempDir() (the WAL of §10 День 2 survives a restart, which is what the restart case needs),
// and the fake STRATA of §11 answers the model calls. The tag keeps `go test ./...` of §10 День 1 free
// of the HTTP wiring (§11: «Unit — go test (хост, без -race)»).
package integration

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"

	"bot/internal/bot"
	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
	"bot/internal/testutil"
)

const (
	// chatID / userID — the private chat of §6 that every scripted update belongs to.
	chatID = int64(4242)
	userID = int64(1717)
	// outsider — a user_id that is NOT in TELEGRAM_ALLOWED_USERS (§6 allowlist, §10 День 4 DoD «чужой user id
	// получает отказ и попадает в events(kind='denied')»).
	outsider = int64(9999)

	// secretToken is the TELEGRAM_BOT_TOKEN of §7 in the shape BotFather issues (§14.1 item 1). The
	// secret half is distinctive because §12 requires «логи не содержат токен»: the library puts the
	// request URL, token included, into its error texts, so only a memorable secret proves Sanitize.
	secretToken = "777:SuperSecretTokenValueDoNotLog"
	// secretStrataKey — STRATA_API_KEY of §7, asserted the same way (§12 security).
	secretStrataKey = "StrataKeySuperSecretDoNotLog"

	// waitTimeout bounds every polling wait: the fake Telegram caps a long poll at 150 ms and the model
	// client gets 3 s per attempt (§7), so five seconds covers a healthy answer even with one retry.
	waitTimeout = 5 * time.Second
	// quietWindow is how long a test watches for silence before it concludes nothing is coming.
	quietWindow = 400 * time.Millisecond
)

// integrationEnv — the §7 loader with the ENV of the test, so the real bounds and defaults of §7 are
// what the dispatcher sees (§11: the tests never copy the config table). The STRATA base is the fake of
// §11; PARSE_MODE stays plain unless the test wants the escaping of §12 UX.
func integrationEnv(t *testing.T, dbPath string, strataBase string, extra map[string]string) config.Config {
	t.Helper()
	var raw = map[string]string{
		"TELEGRAM_BOT_TOKEN":            secretToken,
		"STRATA_API_KEY":                secretStrataKey,
		"STRATA_BASE_URL":               strataBase,
		"STRATA_MODEL":                  "qwen-test",
		"STRATA_TIMEOUT_SECONDS":        "3",
		"TG_REPLY_DEADLINE_SECONDS":     "10",
		"DB_PATH":                       dbPath,
		"TELEGRAM_ALLOWED_USERS":        strconv.FormatInt(userID, 10),
		"RATE_LIMIT_PER_MINUTE":         "0", // the limiter of §12 is unit-tested; here it must not throttle
		"MAX_CONCURRENT_MODEL_REQUESTS": "2",
		"MAX_MESSAGE_CHUNK":             "1200",
		"PARSE_MODE":                    "plain",
		"PROXY_FREE_TEXT":               "true",
		"LOG_LEVEL":                     "debug",
	}
	var key string
	for key = range extra {
		raw[key] = extra[key]
	}
	var cfg, problems = config.LoadFrom(raw)
	if len(problems) > 0 {
		t.Fatalf("config.LoadFrom rejected the test ENV (§7 validation): %v", problems)
	}
	return cfg
}

// dbFile — the SQLite file of §5 inside t.TempDir(). A FILE and not a memory database, because the
// restart case of §10 День 4 has to survive a closed connection: the WAL of §10 День 2 keeps the rows
// and the offset after db.Close (§11 «Store (WAL): файловая SQLite во временном каталоге»).
func dbFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "bot.db")
}

// openStore — store.Open of §5 (DSN whitelist, Ping, pragmas, schema). Closing twice is fine: the
// restart case closes the handle itself, and t.Cleanup must not fail on the second Close.
func openStore(t *testing.T, path string) *sql.DB {
	t.Helper()
	var db, err = store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open(%s): %v (§5: WAL + schema must be ready for the bot)", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newClient — the real model client of §4 against the fake STRATA of §11. A short attempt timeout and a
// short backoff keep the §10 День 3 cases fast; the reply budget of §7 comes from the config.
func newClient(t *testing.T, cfg config.Config, strata *testutil.Strata) model.Client {
	t.Helper()
	var client, err = model.New(model.Options{
		ChatURL:       cfg.ChatURL,
		ModelsURL:     cfg.ModelsURL,
		APIKey:        cfg.StrataKey,
		Model:         cfg.StrataModel,
		Timeout:       3 * time.Second,
		Attempts:      2,
		Backoff:       100 * time.Millisecond,
		MaxConcurrent: int(cfg.MaxConcurrentRequests),
	})
	if err != nil {
		t.Fatalf("model.New: %v (§4: the client is built from the §7 URLs)", err)
	}
	return client
}

// botRun — the process of §4 in miniature: the dispatcher of §6, the adapter of §10 День 4, the
// goroutine of Start(ctx) and the log buffer §12 inspects.
type botRun struct {
	d      *bot.Dispatcher
	tg     *tgbot.Bot
	cancel context.CancelFunc
	done   chan struct{}
	logs   *syncLog
	offset int64 // §5 bot_state value the run was seeded with (§10 День 4 WithInitialOffset)
}

// syncLog — the log sink of a run behind a mutex. The library answers every update in its own goroutine
// (ProcessUpdate → `go r(ctx, b, upd)` in v1.27.0), so the handler writes this buffer from several
// goroutines while the test goroutine polls it with waitFor; bytes.Buffer documents itself as NOT safe
// for concurrent use, and `go test -race` of §11 Concurrency is right to call that a data race (§16
// change 27). Production is unaffected: main.go hands slog an os.Stdout, which is written by the handler
// under its own internal mutex and is never read back.
type syncLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// String — the snapshot the §11 assertions read (§12 security: secretLeak over «the whole log of the run»).
func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// startBot — «cmd/bot/main.go: конфиг → store → model client → bot → healthz → цикл» without the health
// server: bot.New → NewTelegram (WithServerURL of §11) → PublishCommands (§10 День 4 «setMyCommands при
// старте») → Start(ctx) in a goroutine. The ctx is a child of the test ctx, so stop() is the SIGTERM of
// the signal ctx of §10 День 1 (`signal.NotifyContext`) plays, and the next start can open the same file
// without racing an old poller.
func startBot(t *testing.T, ctx context.Context, cfg config.Config, db *sql.DB, client model.Client, tgAPI *testutil.Telegram, opts bot.Options, startOffset int64) *botRun {
	t.Helper()
	var logs = &syncLog{}
	var logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var d = bot.New(bot.Deps{
		Cfg:         cfg,
		DB:          db,
		Model:       client,
		Logger:      logger,
		Token:       cfg.BotToken,
		StartOffset: startOffset,
		Offset:      func(ctx context.Context, updateID int64) error { return store.SetLastUpdateID(ctx, db, updateID) },
	})
	var tg, err = d.NewTelegram(opts)
	if err != nil {
		t.Fatalf("NewTelegram against the fake Bot API: %v (§10 День 4: New calls getMe and must be answered)", err)
	}
	var menuCtx, cancelMenu = context.WithTimeout(ctx, bot.PublishCommandsTimeout)
	defer cancelMenu()
	if menuErr := d.PublishCommands(menuCtx, tg); menuErr != nil {
		t.Fatalf("setMyCommands: %v (§10 День 4 publishes the menu of §6 at start)", menuErr)
	}
	var runCtx, cancel = context.WithCancel(ctx)
	var done = make(chan struct{})
	go func() {
		tg.Start(runCtx) // §10 День 4: Run у библиотеки нет, Start blocks until the ctx is cancelled
		close(done)
	}()
	var run = &botRun{d: d, tg: tg, cancel: cancel, done: done, logs: logs, offset: startOffset}
	t.Cleanup(run.stop)
	return run
}

// stop — cancel + wait for Start to return. Waiting is the point: §10 День 4 «не слать следующее
// сообщение в этот чат» and the restart case are only provable when no goroutine of the previous run is
// still answering. The library has no Stop method (§16 change 27), so the ctx is the only lever.
func (b *botRun) stop() {
	b.cancel()
	select {
	case <-b.done:
	case <-time.After(3 * time.Second):
	}
}

// waitFor — poll until the fake says the thing happened. The fakes answer from their own goroutines, so
// a fixed sleep would make the test flaky on a loaded host (§11).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	var deadline = time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, waitTimeout)
}

// quiet — hold the process still for a moment; the caller then asserts that nothing changed. §13 риск
// «повторные ответы» and §10 День 4 «не дублировать отправку» are negative assertions, so they need a
// window rather than a poll.
func quiet() { time.Sleep(quietWindow) }

// textOf — every text the fake Telegram received, glued (§12 UX: the chunks of one answer still read as
// one message, and §11 asserts the content through the fake, because nothing else knows how it left).
func textOf(tgAPI *testutil.Telegram) string { return tgAPI.Joined() }

// secretLeak — the §12 security check over a text the bot produced or logged: neither secret of §7 may
// appear in it. The error texts of the library carry `https://…/bot<TOKEN>/sendMessage`, so this is what
// Sanitize of run.go has to pass.
func secretLeak(t *testing.T, where string, text string) {
	t.Helper()
	if strings.Contains(text, secretToken) {
		t.Errorf("%s carries the Telegram token (§12 security: логи не содержат токен)", where)
	}
	if strings.Contains(text, secretStrataKey) {
		t.Errorf("%s carries the STRATA key (§12 security: печатается только длина)", where)
	}
}

// storedOffset — bot_state last_update_id of §5, read the way a restart reads it.
func storedOffset(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var offset, _, err = store.LastUpdateID(context.Background(), db)
	if err != nil {
		t.Fatalf("store.LastUpdateID: %v", err)
	}
	return offset
}

// countEvents — the events table of §5 («service: commands, errors, limits»): §10 День 4 DoD counts
// 'denied' and 'flood' here, because the audit row is the promise the plan makes.
func countEvents(t *testing.T, db *sql.DB, kind string) int64 {
	t.Helper()
	var n, err = store.CountEvents(context.Background(), db, kind)
	if err != nil {
		t.Fatalf("store.CountEvents(%s): %v", kind, err)
	}
	return n
}

// TestAskOverFakeTelegram — the main path of §6 through the real adapter: getMe of §10 День 4, the menu
// of setMyCommands, /ask → the fake STRATA → sendMessage, the two rows of §10 День 3, bot_state of §5 and
// the free text of §7 PROXY_FREE_TEXT. It is the integration DoD of День 4 without a token and without a
// GPU (§11: «Фейк Telegram обязателен для интеграционного теста без токена и без сети»).
func TestAskOverFakeTelegram(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	var strata = testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{
		Content:   "Два и два — четыре (почти)",
		Model:     "fake-model",
		TokensIn:  10,
		TokensOut: 20,
	})
	var cfg = integrationEnv(t, path, strata.BaseURL(), nil)
	var db = openStore(t, path)
	var client = newClient(t, cfg, strata)
	var run = startBot(t, ctx, cfg, db, client, tgAPI, bot.Options{
		ServerURL:   tgAPI.URL(), // §11: bot.WithServerURL — the library talks to the fake
		PollTimeout: 2 * time.Second,
		Workers:     2, // §10 День 4 WithWorkers, §10 День 6 parallelism of one chat
	}, storedOffset(t, db))

	// §10 День 4 «setMyCommands при старте»: the menu Telegram shows for «/» comes from the registry of
	// §6, so the count is the registry length and a new v2 command appears here without editing run.go.
	waitFor(t, "setMyCommands reached the fake Telegram", func() bool { return len(tgAPI.Commands()) == 9 })
	var menu = tgAPI.Commands()
	var i = 0
	var sawAsk = false
	for i < len(menu) {
		if menu[i].Command == "ask" {
			sawAsk = true
		}
		if strings.Contains(menu[i].Command, " ") || strings.Contains(menu[i].Command, "/") {
			t.Errorf("setMyCommands sent %q: the names are lowercase, without the slash and without arguments (§6)", menu[i].Command)
		}
		i++
	}
	if !sawAsk {
		t.Errorf("the menu of §6 has no /ask: %v", menu)
	}
	// §10 День 4: New calls getMe (WithCheckInitTimeout), so the fake must answer it — a fake that did
	// not would make bot.New fail and the test would die in startBot rather than here.
	if tgAPI.Calls("getMe") < 1 {
		t.Errorf("getMe was called %d times (§10 День 4: New checks the token with getMe)", tgAPI.Calls("getMe"))
	}

	tgAPI.PushText(41, chatID, userID, "/ask сколько будет 2+2?")
	waitFor(t, "the answer of /ask arrived in the fake Telegram", func() bool { return tgAPI.Calls("sendMessage") >= 1 })
	quiet() // the footer of §6 rides in the same Send, so a second chunk is not expected here

	var sent = tgAPI.Sent()
	if len(sent) != 1 {
		t.Fatalf("/ask produced %d sendMessage calls, want 1 (§12 UX: one short answer is one chunk): %q", len(sent), tgAPI.Texts())
	}
	if sent[0].ChatID != chatID {
		t.Errorf("sendMessage went to chat %d, want %d (§6: the answer goes to the chat that asked)", sent[0].ChatID, chatID)
	}
	if sent[0].ParseMode != "" {
		t.Errorf("parse_mode = %q with PARSE_MODE=plain (§7: plain sends no parse_mode)", sent[0].ParseMode)
	}
	if !strings.Contains(sent[0].Text, "Два и два — четыре (почти)") {
		t.Errorf("the answer text lost the model content: %q", sent[0].Text)
	}
	// §6 «ответ модели + ⏱ latency, tokens» — the footer is part of the same outgoing text.
	if !strings.Contains(sent[0].Text, "10→20 токенов") || !strings.Contains(sent[0].Text, "fake-model") {
		t.Errorf("the latency footer of §6 is missing: %q", sent[0].Text)
	}

	// The model request: exactly one call, the model of §7, the key of §7 in the header, the question of
	// §6 in the body (§16 change 26: the fake is reached through /v1).
	if strata.ChatCalls() != 1 {
		t.Errorf("the fake STRATA got %d chat requests, want 1 (§10 День 3: one answer per question)", strata.ChatCalls())
	}
	if strata.LastAuthorization() != "Bearer "+secretStrataKey {
		t.Errorf("the Authorization header of §7 is not the Bearer of the key (§12 security: the key is never printed), got %d bytes", len(strata.LastAuthorization()))
	}
	if !strings.Contains(strata.LastRequestBody(), "сколько будет 2+2?") || !strings.Contains(strata.LastRequestBody(), "qwen-test") {
		t.Errorf("the request body of §4 lost the question or the model: %q", strata.LastRequestBody())
	}

	// §10 День 3 «ровно одна запись assistant»: the user row holds the question, the assistant row the
	// answer with the tokens of §5; the assistant has no telegram_message_id (§5 nullable by design).
	var rows = rowsOf(t, db)
	if len(rows) != 2 {
		t.Fatalf("messages of the chat = %d rows, want 2 (§10 День 3): %+v", len(rows), rows)
	}
	if rows[0].Role != store.RoleUser || rows[0].Content != "сколько будет 2+2?" {
		t.Errorf("the user row of §5 is %s/%q (§6 п.3 keeps the whole text after the command)", rows[0].Role, rows[0].Content)
	}
	if rows[0].TelegramMessageID == nil || *rows[0].TelegramMessageID != 41 {
		t.Errorf("the user row carries telegram_message_id %v, want 41 (§5 UNIQUE guard of §10 День 4)", rows[0].TelegramMessageID)
	}
	if rows[1].Role != store.RoleAssistant || rows[1].Content != "Два и два — четыре (почти)" {
		t.Errorf("the assistant row of §5 is %s/%q (§10 День 3: exactly one assistant row)", rows[1].Role, rows[1].Content)
	}
	if rows[1].TokensIn == nil || *rows[1].TokensIn != 10 || rows[1].TokensOut == nil || *rows[1].TokensOut != 20 {
		t.Errorf("the tokens of §5 on the assistant row are %v/%v, want 10/20", rows[1].TokensIn, rows[1].TokensOut)
	}
	if rows[1].Model != "fake-model" {
		t.Errorf("the assistant row says model %q, want fake-model (§6 /stats shows it)", rows[1].Model)
	}
	if rows[1].TelegramMessageID != nil {
		t.Errorf("the assistant row carries telegram_message_id %d, want NULL (§5: NULL for answers of the model)", *rows[1].TelegramMessageID)
	}

	// §5 «Offset long polling»: the offset of the processed update is in bot_state before any restart.
	if storedOffset(t, db) != 41 {
		t.Errorf("bot_state last_update_id = %d, want 41 (§5: the offset survives the restart of §10 День 5)", storedOffset(t, db))
	}
	if countEvents(t, db, store.EventDenied) != 0 {
		t.Errorf("an allowed user produced events(kind='denied') (§7 allowlist of §6)")
	}
	if tgAPI.WrongTokens() != 0 {
		t.Errorf("%d requests carried a different token in the path (§12 security: run.go passed the token of §7)", tgAPI.WrongTokens())
	}
	secretLeak(t, "the texts the bot sent", textOf(tgAPI))
	secretLeak(t, "the log of the run", run.logs.String())

	// §7 PROXY_FREE_TEXT: a plain message becomes /ask of §6 — the WithDefaultHandler branch of run.go,
	// which no unit test of §11 reaches because there is no library there.
	tgAPI.PushText(42, chatID, userID, "привет ещё раз")
	waitFor(t, "the free text got an answer through the default handler", func() bool { return strata.ChatCalls() == 2 })
	quiet()
	if len(tgAPI.Sent()) != 2 {
		t.Errorf("the free text produced %d messages, want 2 in total (§6 «обычный текст → /ask»)", len(tgAPI.Sent()))
	}
	if storedOffset(t, db) != 42 {
		t.Errorf("bot_state last_update_id = %d, want 42 (§5: the offset moves on every processed update)", storedOffset(t, db))
	}

	// A command that never touches the model (§6 /ping) still answers over the real transport.
	tgAPI.PushText(43, chatID, userID, "/ping")
	waitFor(t, "/ping answered", func() bool { return tgAPI.Calls("sendMessage") >= 3 })
	if !strings.Contains(tgAPI.Texts()[2], "pong") {
		t.Errorf("/ping said %q (§6: pong + uptime)", tgAPI.Texts()[2])
	}
	if strata.ChatCalls() != 2 {
		t.Errorf("the fake STRATA got %d chat requests after /ping, want 2 (§6: /ping does not use the model)", strata.ChatCalls())
	}
	if len(rowsOf(t, db)) != 4 {
		t.Errorf("messages of the chat = %d, want 4 (§5: /ping writes no conversation rows)", len(rowsOf(t, db)))
	}
	run.stop()

	// §12 security over everything the run wrote: the debug buffer is checked for the secrets of §7 even
	// though production runs at LOG_LEVEL=info.
	secretLeak(t, "the whole log of the run", run.logs.String())
}

// TestDeniedUser — §7 TELEGRAM_ALLOWED_USERS over the real transport (§10 День 4 DoD: «неallowlist → events
// kind='denied', no answer»): the update is consumed, the offset moves, nothing reaches the model and no
// message leaves (§12 security: the rejection must not tell the stranger that the bot exists).
func TestDeniedUser(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	var strata = testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "не должно быть отправлено"})
	var cfg = integrationEnv(t, path, strata.BaseURL(), nil)
	var db = openStore(t, path)
	var run = startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second,
	}, 0)
	waitFor(t, "the menu of §6 is published", func() bool { return len(tgAPI.Commands()) == 9 })

	// /whoami is AlwaysAllowed in §6, so the same user is answered for that command and refused for /ask:
	// the difference between the two proves that the gate is the allowlist of §7 and not a dead bot.
	tgAPI.PushText(61, chatID, outsider, "/whoami")
	waitFor(t, "/whoami answered the stranger", func() bool { return tgAPI.Calls("sendMessage") >= 1 })
	if !strings.Contains(tgAPI.Texts()[0], strconv.FormatInt(outsider, 10)) {
		t.Errorf("/whoami of §6 did not identify the stranger: %q", tgAPI.Texts()[0])
	}

	tgAPI.PushText(62, chatID, outsider, "/ask секретный вопрос")
	waitFor(t, "the denied update reached the handler", func() bool { return storedOffset(t, db) == 62 })
	quiet()
	if strata.ChatCalls() != 0 {
		t.Errorf("the fake STRATA got %d requests from a user outside §7 TELEGRAM_ALLOWED_USERS (§10 День 4 DoD)", strata.ChatCalls())
	}
	// §7 line 502 answers only /start and /whoami to a stranger; the middleware of §16 change 13 adds the
	// onboarding hint («Твой user_id — N») rather than silence, because a user who cannot read his own id
	// cannot ask to be added. What must not happen is the answer of the model.
	if len(tgAPI.Sent()) != 2 {
		t.Errorf("the stranger got %d messages, want the /whoami answer plus the refusal of §16 change 13", len(tgAPI.Sent()))
	}
	if strings.Contains(tgAPI.Texts()[1], "не должно быть отправлено") {
		t.Errorf("the stranger received the answer of the model (§7 deny by default): %q", tgAPI.Texts()[1])
	}
	if !strings.Contains(tgAPI.Texts()[1], strconv.FormatInt(outsider, 10)) {
		t.Errorf("the refusal hides the user_id the stranger needs to get into §7 TELEGRAM_ALLOWED_USERS: %q", tgAPI.Texts()[1])
	}
	if countEvents(t, db, store.EventDenied) != 1 {
		t.Errorf("events(kind='denied') = %d, want 1 (§5 service event of §6 п.2)", countEvents(t, db, store.EventDenied))
	}
	if len(rowsOf(t, db)) != 0 {
		t.Errorf("a denied update wrote %d messages (§5: the conversation of a stranger is not kept)", len(rowsOf(t, db)))
	}
	secretLeak(t, "the texts sent to the stranger", textOf(tgAPI))
	secretLeak(t, "the log of the denied run", run.logs.String())
	run.stop()
}

// TestOffsetSurvivesRestart — the DoD of §10 День 5 «offset переживает restart»: the first run consumes
// an update and dies, the second run opens the same file and must not answer the update it already
// handled, because bot_state held the offset (§5 «Offset long polling»).
func TestOffsetSurvivesRestart(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	var strata = testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "первый ответ"}, testutil.StrataReply{Content: "второй ответ"})
	var cfg = integrationEnv(t, path, strata.BaseURL(), nil)

	var firstDB = openStore(t, path)
	var first = startBot(t, ctx, cfg, firstDB, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second,
	}, storedOffset(t, firstDB))
	waitFor(t, "the first run published the menu", func() bool { return len(tgAPI.Commands()) == 9 })

	tgAPI.PushText(71, chatID, userID, "/ask первый")
	waitFor(t, "the first run answered", func() bool { return tgAPI.Calls("sendMessage") >= 1 })
	if storedOffset(t, firstDB) != 71 {
		t.Errorf("bot_state last_update_id = %d, want 71 before the restart (§5)", storedOffset(t, firstDB))
	}
	first.stop()
	firstDB.Close()

	// The restart of §10 День 5: a fresh store.Open over the same file (WAL of §5 keeps the rows next to
	// it) and a fresh Run seeded with the stored offset of §10 День 4 WithInitialOffset.
	var secondDB = openStore(t, path)
	var second = startBot(t, ctx, cfg, secondDB, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second,
	}, storedOffset(t, secondDB))
	if second.offset != 71 {
		t.Errorf("the restart started at offset %d, want 71 (§10 День 4 WithInitialOffset read from bot_state)", second.offset)
	}
	if len(rowsOf(t, secondDB)) != 2 {
		t.Errorf("the messages of the first run disappeared after the restart (§5 WAL: %d rows)", len(rowsOf(t, secondDB)))
	}
	// A quiet getUpdates on the fake is what §10 День 5 DoD means by «не повторяет»: the update of 71 is
	// gone from Telegram's side because bot_state fed the offset of §10 День 4 WithInitialOffset.
	var pollsBefore = tgAPI.Calls("getUpdates")
	waitFor(t, "the restarted run polled the fake", func() bool { return tgAPI.Calls("getUpdates") > pollsBefore })
	if tgAPI.Offsets()[0] != "1" {
		t.Errorf("the first run polled with offset %q, want 1 (§5 long polling starts at the stored offset)", tgAPI.Offsets()[0])
	}
	if tgAPI.Offsets()[len(tgAPI.Offsets())-1] != "72" {
		t.Errorf("the last poll asked offset %q, want 72 (the stored 71 + 1 of §5)", tgAPI.Offsets()[len(tgAPI.Offsets())-1])
	}

	// The fake already dropped update 71 (the offset was 72), so a new update is the only thing to answer:
	// one extra message and one extra pair of rows is the whole story of the restart.
	tgAPI.PushText(72, chatID, userID, "/ask второй")
	waitFor(t, "the restarted run answered the new update", func() bool { return tgAPI.Calls("sendMessage") >= 2 })
	quiet()
	if len(tgAPI.Sent()) != 2 {
		t.Errorf("the restart produced %d messages in total, want 2 (§10 День 5 DoD: no repeated answer)", len(tgAPI.Sent()))
	}
	if !strings.Contains(tgAPI.Texts()[1], "второй ответ") {
		t.Errorf("the restarted run answered %q, want the scripted «второй ответ» (§11: the fake serves the model)", tgAPI.Texts()[1])
	}
	if len(rowsOf(t, secondDB)) != 4 {
		t.Errorf("messages after the restart = %d, want 4 (§10 День 3: one assistant row per answer)", len(rowsOf(t, secondDB)))
	}
	if storedOffset(t, secondDB) != 72 {
		t.Errorf("bot_state last_update_id = %d, want 72 (§5: the second run moved the offset forward)", storedOffset(t, secondDB))
	}
	second.stop()
	secondDB.Close()
}

// rowsOf — the messages of one chat through HistoryOfChat of §5 (the /history semantics: the rows a
// /reset hid are still there, and the order is oldest → newest).
func rowsOf(t *testing.T, db *sql.DB) []store.Message {
	t.Helper()
	var rows, err = store.HistoryOfChat(context.Background(), db, chatID, 50)
	if err != nil {
		t.Fatalf("store.HistoryOfChat: %v (§5)", err)
	}
	return rows
}

// TestLongAnswerIsEscapedThenChunked — §12 UX over the wire (§16 change 7 «экранирование до разбиения»):
// a MarkdownV2 answer longer than MAX_MESSAGE_CHUNK of §7 leaves the bot as several messages, each of
// them within Telegram's 4096 characters of §9, none of them ending in a dangling backslash, the escape
// of §9 п.2 applied to the whole text before it was cut.
func TestLongAnswerIsEscapedThenChunked(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	var body = strings.Repeat("слово ", 900) + "(важно) 1.5 финал"
	var strata = testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{
		Content: body, Model: "fake-model", TokensIn: 3, TokensOut: 7,
	})
	var cfg = integrationEnv(t, path, strata.BaseURL(), map[string]string{"PARSE_MODE": "MarkdownV2"})
	var db = openStore(t, path)
	var run = startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second,
	}, 0)
	waitFor(t, "the menu of §6 is published", func() bool { return len(tgAPI.Commands()) == 9 })

	tgAPI.PushText(81, chatID, userID, "/ask длинный ответ")
	waitFor(t, "the long answer left the bot in pieces", func() bool { return tgAPI.Calls("sendMessage") >= 2 })
	quiet()

	var texts = tgAPI.Texts()
	if len(texts) < 2 {
		t.Fatalf("a %d-rune answer arrived as %d message(s), §12 UX asks for chunks of MAX_MESSAGE_CHUNK", bot.Runes(body), len(texts))
	}
	var i = 0
	for i < len(texts) {
		var runes = bot.Runes(texts[i])
		if runes > 4096 {
			t.Errorf("chunk %d is %d characters, Telegram's limit of §9 is 4096", i, runes)
		}
		if runes > int(cfg.MaxMessageChunk) {
			t.Errorf("chunk %d is %d characters, MAX_MESSAGE_CHUNK of §7 is %d", i, runes, cfg.MaxMessageChunk)
		}
		if trailingBackslashes(texts[i])%2 == 1 {
			t.Errorf("chunk %d ends in a dangling escape (§16 change 7: Telegram would answer «Can't parse entities»): %q", i, tail(texts[i], 12))
		}
		i++
	}
	// The escape happened once, over the whole text: a raw «(важно)» could only mean the text went out
	// unescaped, and a doubled escape would mean reply.go escaped it twice (§9 п.2).
	var joined = tgAPI.Joined()
	if strings.Contains(joined, "(важно)") {
		t.Errorf("the parentheses of §9 п.2 reached Telegram unescaped: %q", tail(joined, 60))
	}
	if strings.Contains(joined, `\\(`) {
		t.Errorf("the escape is doubled (§16 change 7: Escape runs once, before Chunk): %q", tail(joined, 60))
	}
	if !strings.Contains(joined, `\(`) {
		t.Errorf("no MarkdownV2 escape at all: %q", tail(joined, 60))
	}
	// What Telegram received still says what the model said, once the escapes of §9 п.2 are removed.
	var stripped = strings.ReplaceAll(joined, "\\", "")
	if !strings.Contains(stripped, "слово") || !strings.Contains(stripped, "финал") {
		t.Errorf("the chunking of §12 UX lost the head or the tail of the answer")
	}
	// §6: the footer of §6 rides in the last chunk only, so the user sees one measurement per answer.
	if !strings.Contains(texts[len(texts)-1], "3→7 токенов") {
		t.Errorf("the last chunk has no latency footer of §6: %q", tail(texts[len(texts)-1], 60))
	}
	for i = 1; i < len(texts)-1; i++ {
		if strings.Contains(texts[i], "токенов") {
			t.Errorf("chunk %d repeats the footer of §6: %q", i, tail(texts[i], 60))
		}
	}
	for i = 0; i < len(tgAPI.Sent()); i++ {
		if tgAPI.Sent()[i].ParseMode != "MarkdownV2" {
			t.Errorf("chunk %d carries parse_mode %q, PARSE_MODE of §7 is MarkdownV2", i, tgAPI.Sent()[i].ParseMode)
		}
	}
	// §5 stores what the model said, not what Telegram was told to render (§9 п.2 is a transport concern).
	var rows = rowsOf(t, db)
	if len(rows) != 2 || rows[1].Content != body {
		t.Errorf("the assistant row of §5 must hold the unescaped answer (%d rows)", len(rows))
	}
	secretLeak(t, "the chunked answer", joined)
	secretLeak(t, "the log of the chunked run", run.logs.String())
	run.stop()
}

// tail — the last n runes of a text, for error messages that must stay readable (§11: a failed assert
// prints what Telegram got, and a 5000-rune quote is not readable).
func tail(text string, n int) string {
	var runes = []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[len(runes)-n:])
}

// trailingBackslashes — how many backslashes end the text; odd means the last escape has no character
// of its own, which is exactly the bug §16 change 7 exists to prevent.
func trailingBackslashes(text string) int {
	var runes = []rune(text)
	var n = 0
	var i = len(runes) - 1
	for i >= 0 && runes[i] == '\\' {
		n++
		i--
	}
	return n
}

// TestTelegram429SleepsInItsChat — §10 День 4 «429: спать ровно RetryAfter секунд, events
// kind='flood', не слать следующее сообщение в этот чат». The sleep happens inside Send, so the chat is
// quiet for RetryAfter seconds instead of hammering Telegram: the second sendMessage of the same chat is
// the retry, and it comes after the pause (§13 риск «бот словил flood» — the unit Replier of §11 cannot
// show the pause, because there is no library there).
func TestTelegram429SleepsInItsChat(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	var strata = testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "ответ после flood"})
	var cfg = integrationEnv(t, path, strata.BaseURL(), nil)
	var db = openStore(t, path)
	var run = startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second,
	}, 0)
	waitFor(t, "the menu of §6 is published", func() bool { return len(tgAPI.Commands()) == 9 })

	tgAPI.FailNext("sendMessage", 429, "Too Many Requests: retry after 1", 1)
	var began = time.Now()
	tgAPI.PushText(91, chatID, userID, "/ask flood меня?")
	waitFor(t, "the answer arrived after the pause", func() bool { return len(tgAPI.Sent()) >= 1 })
	var waited = time.Since(began)

	if tgAPI.Calls("sendMessage") != 2 {
		t.Errorf("sendMessage was attempted %d times, want the rejected one plus the retry (§10 День 4)", tgAPI.Calls("sendMessage"))
	}
	if waited < time.Second {
		t.Errorf("the chat waited %s, §10 День 4 sleeps ровно RetryAfter = 1s", waited)
	}
	if waited > 8*time.Second {
		t.Errorf("the chat waited %s, the sleep of §10 День 4 is RetryAfter, capped by FloodSleepMax of %s", waited, bot.FloodSleepMax)
	}
	if !strings.Contains(tgAPI.Texts()[0], "ответ после flood") {
		t.Errorf("the retry sent %q, want the answer of the model", tgAPI.Texts()[0])
	}
	if countEvents(t, db, store.EventFlood) != 1 {
		t.Errorf("events(kind='flood') = %d, want 1 (§10 День 4 DoD, §5 audit row)", countEvents(t, db, store.EventFlood))
	}
	if !strings.Contains(run.logs.String(), "retry_after") {
		t.Errorf("the log of §12 надёжность does not mention retry_after: %s", run.logs.String())
	}
	secretLeak(t, "the log of the flood run", run.logs.String())
	run.stop()
}

// TestTelegramErrorIsSanitized — §12 security («логи не содержат токен») at the place where the secret is
// closest to the log: the library puts the request URL — token included — into its error texts, and the
// errorsHandler of §10 День 4 plus Dispatch of §6 both pass those texts through Sanitize. A 403 is the
// realistic case: the user blocked the bot, and Telegram says so with the URL of the failed call.
func TestTelegramErrorIsSanitized(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	var strata = testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "пользователь заблокировал бота"})
	var cfg = integrationEnv(t, path, strata.BaseURL(), nil)
	var db = openStore(t, path)
	var run = startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second,
	}, 0)
	waitFor(t, "the menu of §6 is published", func() bool { return len(tgAPI.Commands()) == 9 })

	tgAPI.FailNext("sendMessage", 403, "Forbidden: bot was blocked by the user", 0)
	tgAPI.PushText(101, chatID, userID, "/ask после блокировки")
	waitFor(t, "the failed send reached the log", func() bool { return strings.Contains(run.logs.String(), "Forbidden") })

	if tgAPI.Calls("sendMessage") != 1 {
		t.Errorf("sendMessage was attempted %d times, want 1: only a 429 gets the retry of §10 День 4", tgAPI.Calls("sendMessage"))
	}
	if len(tgAPI.Sent()) != 0 {
		t.Errorf("a blocked chat got %d messages (§6: there is nobody to answer)", len(tgAPI.Sent()))
	}
	// §6 Dispatch: «offset is written even when the handler failed» — a redelivery would answer a user
	// who already left, and the UNIQUE of §5 would not stop it for an edit (§13 риск «повторные ответы»).
	if storedOffset(t, db) != 101 {
		t.Errorf("bot_state last_update_id = %d, want 101 (§6: the offset moves after a failed update)", storedOffset(t, db))
	}
	if countEvents(t, db, store.EventCommand) != 1 {
		t.Errorf("events(kind='command') = %d, want 1 (§5: the audit row is written before the handler)", countEvents(t, db, store.EventCommand))
	}
	if !strings.Contains(run.logs.String(), "bot was blocked") {
		t.Errorf("the log lost the class of the Telegram failure (§12 надёжность): %s", run.logs.String())
	}
	secretLeak(t, "the log of the failed send", run.logs.String())
	run.stop()
}

// TestBotNameSuffixNeedsGetMe — §6 п.1 over the wire: Telegram sends `/ask@MyBot` in groups and on
// autocomplete, and only getMe of §10 День 4 knows the username that makes the suffix ours. Without the
// getMe of NewTelegram the foreign suffix would be stripped as well, and the bot would answer commands
// that Telegram addressed to a different bot (§10 День 4 DoD «/ask@BotName в группе работает»).
func TestBotNameSuffixNeedsGetMe(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	var strata = testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "не должно пригодиться"})
	// PROXY_FREE_TEXT=false so that a command we do not own goes to the /help hint instead of becoming a
	// question for the model: that is what makes the foreign suffix observable.
	var cfg = integrationEnv(t, path, strata.BaseURL(), map[string]string{"PROXY_FREE_TEXT": "false"})
	var db = openStore(t, path)
	var run = startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second,
	}, 0)
	waitFor(t, "the menu of §6 is published", func() bool { return len(tgAPI.Commands()) == 9 })

	// The fake answers getMe with username «FakeBot» (§11), so the suffix of the first update is ours and
	// п.2 still lowercases the name.
	tgAPI.PushText(111, chatID, userID, "/Ping@FakeBot")
	waitFor(t, "/Ping@FakeBot was recognized", func() bool { return tgAPI.Calls("sendMessage") >= 1 })
	if !strings.HasPrefix(tgAPI.Texts()[0], "pong ") {
		t.Errorf("the suffixed command of §6 п.1 said %q, want the /ping answer (§10 День 4 DoD)", tgAPI.Texts()[0])
	}

	tgAPI.PushText(112, chatID, userID, "/ping@AnotherBot")
	waitFor(t, "the foreign suffix fell to the hint of §6", func() bool { return tgAPI.Calls("sendMessage") >= 2 })
	quiet()
	if strings.HasPrefix(tgAPI.Texts()[1], "pong ") {
		t.Errorf("a command addressed to another bot was answered as ours (§6 п.1: the suffix of getMe decides): %q", tgAPI.Texts()[1])
	}
	if !strings.Contains(tgAPI.Texts()[1], "/ask <текст>") {
		t.Errorf("the foreign suffix did not fall to the /help hint of §6: %q", tgAPI.Texts()[1])
	}
	if strata.ChatCalls() != 0 {
		t.Errorf("the foreign suffix reached the model %d times (PROXY_FREE_TEXT of §7 is false)", strata.ChatCalls())
	}
	if storedOffset(t, db) != 112 {
		t.Errorf("bot_state last_update_id = %d, want 112 (§6: an update we could not serve is still consumed)", storedOffset(t, db))
	}
	secretLeak(t, "the suffix test", textOf(tgAPI))
	secretLeak(t, "the log of the suffix run", run.logs.String())
	run.stop()
}

// TestRedeliveredUpdateIsAnsweredOnce — §10 День 6 DoD «идемпотентность: второй уровень защиты», i.e. the
// UNIQUE index uq_messages_chat_tg_id of §5 behind store.Append in the `stored` middleware. The first
// defence is the offset of §5, and TestOffsetSurvivesRestart proves it; this test covers the case the
// offset cannot cover — the process died between «answered» and «bot_state written» (§2 п.7, §13 риск
// «повторные ответы после рестарта»), so the restarted poller starts behind the update and Telegram
// delivers it a second time.
func TestRedeliveredUpdateIsAnsweredOnce(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	// Two replies are scripted on purpose: a leak past the guard would consume the second one and put its
	// text into the chat, so the assertions read the silence of §13 rather than trust an absence.
	var strata = testutil.NewStrata(t, secretStrataKey,
		testutil.StrataReply{Content: "первый ответ"},
		testutil.StrataReply{Content: "второй ответ — он выдал бы, что повтор дошёл до модели"})
	var cfg = integrationEnv(t, path, strata.BaseURL(), nil)
	var db = openStore(t, path)

	var first = startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second, Workers: 2,
	}, storedOffset(t, db))
	waitFor(t, "the first run published the menu", func() bool { return len(tgAPI.Commands()) == 9 })

	tgAPI.PushText(91, chatID, userID, "/ask один")
	waitFor(t, "the question was answered", func() bool { return tgAPI.Calls("sendMessage") >= 1 })
	quiet()
	if got := strata.ChatCalls(); got != 1 {
		t.Fatalf("the model was called %d times for one question, want 1 (§10 День 3)", got)
	}
	first.stop()

	// The crash of §2 п.7: the offset that already confirmed update 91 is lost with the process, so the
	// next poller starts from a bot_state that was never written and Telegram hands the same update over
	// again — the same update_id and the same message_id, which is the key of uq_messages_chat_tg_id (§5).
	// The fake's Replay() is not usable here: it returns only the updates a poll had confirmed while they
	// were still queued, and this one left the queue as a normal delivery, so the redelivery is pushed.
	var second = startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second, Workers: 2,
	}, 0)
	tgAPI.PushText(91, chatID, userID, "/ask один")

	waitFor(t, "the duplicate was skipped by uq_messages_chat_tg_id (the log says «already answered»)", func() bool {
		return strings.Contains(second.logs.String(), "already answered")
	})
	quiet()

	if got := len(tgAPI.Sent()); got != 1 {
		t.Errorf("the chat received %d messages after the redelivery, want 1 (§10 День 6: повтор не отвечает)", got)
	}
	if got := strata.ChatCalls(); got != 1 {
		t.Errorf("the model was called %d times, want 1 (§13: a duplicate costs no tokens)", got)
	}
	if got := len(rowsOf(t, db)); got != 2 {
		t.Errorf("messages = %d, want 2 (§5 uq_messages_chat_tg_id refused the second Append of telegram_message_id 91)", got)
	}
	if got := storedOffset(t, db); got != 91 {
		t.Errorf("bot_state last_update_id = %d, want 91 (§5: a duplicate is still a processed update)", got)
	}
	if !strings.Contains(second.logs.String(), "already answered") {
		t.Errorf("the log does not name the reason of the silence (§12 надёжность): %s", second.logs.String())
	}
	secretLeak(t, "the log of the redelivery", second.logs.String())
	secretLeak(t, "the chat of the redelivery", textOf(tgAPI))
	second.stop()
}
