//go:build integration

// the fake Telegram is explicitly the thing that «проверяет разбиение/экранирование ответов».
package integration

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"bot/internal/bot"
	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
	"bot/internal/testutil"
)

const (
	chatID = int64(4242)
	userID = int64(1717)
	// получает отказ и попадает в events(kind='denied')»).
	outsider = int64(9999)

	// request URL, token included, into its error texts, so only a memorable secret proves Sanitize.
	secretToken     = "777:SuperSecretTokenValueDoNotLog"
	secretStrataKey = "StrataKeySuperSecretDoNotLog"

	// waitTimeout bounds every polling wait: the fake Telegram caps a long poll at 150 ms and the model
	waitTimeout = 5 * time.Second
	// quietWindow is how long a test watches for silence before it concludes nothing is coming.
	quietWindow = 400 * time.Millisecond
)

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
		"RATE_LIMIT_PER_MINUTE":         "0", // the limiter of is unit-tested; here it must not throttle
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
		t.Fatalf("config.LoadFrom rejected the test ENV : %v", problems)
	}
	return cfg
}

func dbFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "bot.db")
}

// restart case closes the handle itself, and t.Cleanup must not fail on the second Close.
func openStore(t *testing.T, path string) *sql.DB {
	t.Helper()
	var db, err = store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open(%s): %v ", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

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
		// a helper left at the library defaults would test a client the process never builds.
		Stream: model.StreamSettings{
			Idle:     time.Duration(cfg.StreamIdleSeconds) * time.Second,
			Total:    time.Duration(cfg.StreamTotalSeconds) * time.Second,
			Progress: time.Duration(cfg.StreamEditIntervalMS) * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("model.New: %v ", err)
	}
	return client
}

type botRun struct {
	d          *bot.Dispatcher
	tg         *tgbot.Bot
	cancel     context.CancelFunc
	done       chan struct{}
	logs       *syncLog
	offset     int64 // bot_state value the run was seeded with
	webhookURL string
}

// syncLog — the log sink of a run behind a mutex. The library answers every update in its own goroutine
// (ProcessUpdate → `go r(ctx, b, upd)` in v1.27.0), so the handler writes this buffer from several
// goroutines while the test goroutine polls it with waitFor; bytes.Buffer documents itself as NOT safe
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

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// startBot — «cmd/bot/main.go: конфиг → store → model client → bot → healthz → цикл» without the health
// старте») → Start(ctx) in a goroutine. The ctx is a child of the test ctx, so stop() is the SIGTERM of
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
	if cfg.WebhookEnabled && opts.WebhookSecretToken == "" {
		opts.WebhookSecretToken = cfg.WebhookSecretToken
	}
	var tg, err = d.NewTelegram(opts)
	if err != nil {
		t.Fatalf("NewTelegram against the fake Bot API: %v ", err)
	}
	var menuCtx, cancelMenu = context.WithTimeout(ctx, bot.PublishCommandsTimeout)
	defer cancelMenu()
	if menuErr := d.PublishCommands(menuCtx, tg); menuErr != nil {
		t.Fatalf("setMyCommands: %v ", menuErr)
	}
	var webhookServer *bot.WebhookServer
	if cfg.WebhookEnabled {
		webhookServer, err = bot.NewWebhookServer(tg, cfg.WebhookAddress(), cfg.WebhookPath, cfg.WebhookSecretToken)
		if err != nil {
			t.Fatalf("NewWebhookServer: %v", err)
		}
		if err := d.ConfigureWebhook(ctx, tg); err != nil {
			_ = webhookServer.Close()
			t.Fatalf("ConfigureWebhook: %v", err)
		}
	}
	var runCtx, cancel = context.WithCancel(ctx)
	var done = make(chan struct{})
	go func() {
		if webhookServer != nil {
			if err := webhookServer.Run(runCtx); err != nil {
				logger.Error("webhook run stopped", "error", err)
			}
		} else {
			tg.Start(runCtx) //: Run у библиотеки нет, Start blocks until the ctx is cancelled
		}
		close(done)
	}()
	var run = &botRun{d: d, tg: tg, cancel: cancel, done: done, logs: logs, offset: startOffset}
	if webhookServer != nil {
		run.webhookURL = webhookServer.URL()
	}
	t.Cleanup(func() {
		if webhookServer != nil {
			_ = webhookServer.Close()
		}
		run.stop()
	})
	return run
}

// сообщение в этот чат» and the restart case are only provable when no goroutine of the previous run is
func (b *botRun) stop() {
	b.cancel()
	select {
	case <-b.done:
	case <-time.After(3 * time.Second):
	}
}

// waitFor — poll until the fake says the thing happened. The fakes answer from their own goroutines, so
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

// window rather than a poll.
func quiet() { time.Sleep(quietWindow) }

func textOf(tgAPI *testutil.Telegram) string { return tgAPI.Joined() }

// appear in it. The error texts of the library carry `https://…/bot<TOKEN>/sendMessage`, so this is what
// Sanitize of run.go has to pass.
func secretLeak(t *testing.T, where string, text string) {
	t.Helper()
	if strings.Contains(text, secretToken) {
		t.Errorf("%s carries the Telegram token ", where)
	}
	if strings.Contains(text, secretStrataKey) {
		t.Errorf("%s carries the STRATA key ", where)
	}
}

func storedOffset(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var offset, _, err = store.LastUpdateID(context.Background(), db)
	if err != nil {
		t.Fatalf("store.LastUpdateID: %v", err)
	}
	return offset
}

// 'denied' and 'flood' here, because the audit row is the promise the plan makes.
func countEvents(t *testing.T, db *sql.DB, kind string) int64 {
	t.Helper()
	var n, err = store.CountEvents(context.Background(), db, kind)
	if err != nil {
		t.Fatalf("store.CountEvents(%s): %v", kind, err)
	}
	return n
}

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
		ServerURL:   tgAPI.URL(), //: bot.WithServerURL — the library talks to the fake
		PollTimeout: 2 * time.Second,
		Workers:     2, // WithWorkers, parallelism of one chat
	}, storedOffset(t, db))

	waitFor(t, "setMyCommands reached the fake Telegram", func() bool { return len(tgAPI.Commands()) == 19 })
	var menu = tgAPI.Commands()
	var i = 0
	var sawAsk = false
	var sawStatus = false
	var sawStream = false
	var sawSkip = false
	var sawUnskip = false
	for i < len(menu) {
		if menu[i].Command == "ask" {
			sawAsk = true
		}
		if menu[i].Command == "status" {
			sawStatus = true
		}
		if menu[i].Command == "stream" {
			sawStream = true
		}
		if menu[i].Command == "skip" {
			sawSkip = true
		}
		if menu[i].Command == "unskip" {
			sawUnskip = true
		}
		if strings.Contains(menu[i].Command, " ") || strings.Contains(menu[i].Command, "/") {
			t.Errorf("setMyCommands sent %q: the names are lowercase, without the slash and without arguments ", menu[i].Command)
		}
		i++
	}
	if !sawAsk || !sawStatus || !sawStream || !sawSkip || !sawUnskip {
		t.Errorf("the menu of must contain /ask, /status, /stream, /skip and /unskip: %v", menu)
	}
	// not would make bot.New fail and the test would die in startBot rather than here.
	if tgAPI.Calls("getMe") < 1 {
		t.Errorf("getMe was called %d times ", tgAPI.Calls("getMe"))
	}

	tgAPI.PushText(41, chatID, userID, "/ask сколько будет 2+2?")
	waitFor(t, "the answer of /ask arrived in the fake Telegram", func() bool { return tgAPI.Calls("sendMessage") >= 1 })
	quiet() // the footer of rides in the same Send, so a second chunk is not expected here

	var sent = tgAPI.Sent()
	if len(sent) != 1 {
		t.Fatalf("/ask produced %d sendMessage calls, want 1 : %q", len(sent), tgAPI.Texts())
	}
	if sent[0].ChatID != chatID {
		t.Errorf("sendMessage went to chat %d, want %d ", sent[0].ChatID, chatID)
	}
	if sent[0].ParseMode != "" {
		t.Errorf("parse_mode = %q with PARSE_MODE=plain ", sent[0].ParseMode)
	}
	if !strings.Contains(sent[0].Text, "Два и два — четыре (почти)") {
		t.Errorf("the answer text lost the model content: %q", sent[0].Text)
	}
	if !strings.Contains(sent[0].Text, "10→20 токенов") || !strings.Contains(sent[0].Text, "fake-model") {
		t.Errorf("the latency footer of is missing: %q", sent[0].Text)
	}

	if strata.ChatCalls() != 1 {
		t.Errorf("the fake STRATA got %d chat requests, want 1 ", strata.ChatCalls())
	}
	if strata.LastAuthorization() != "Bearer "+secretStrataKey {
		t.Errorf("the Authorization header of is not the Bearer of the key , got %d bytes", len(strata.LastAuthorization()))
	}
	if !strings.Contains(strata.LastRequestBody(), "сколько будет 2+2?") || !strings.Contains(strata.LastRequestBody(), "qwen-test") {
		t.Errorf("the request body of lost the question or the model: %q", strata.LastRequestBody())
	}

	var rows = rowsOf(t, db)
	if len(rows) != 2 {
		t.Fatalf("messages of the chat = %d rows, want 2 : %+v", len(rows), rows)
	}
	if rows[0].Role != store.RoleUser || rows[0].Content != "сколько будет 2+2?" {
		t.Errorf("the user row of is %s/%q ", rows[0].Role, rows[0].Content)
	}
	if rows[0].TelegramMessageID == nil || *rows[0].TelegramMessageID != 41 {
		t.Errorf("the user row carries telegram_message_id %v, want 41 ", rows[0].TelegramMessageID)
	}
	if rows[1].Role != store.RoleAssistant || rows[1].Content != "Два и два — четыре (почти)" {
		t.Errorf("the assistant row of is %s/%q ", rows[1].Role, rows[1].Content)
	}
	if rows[1].TokensIn == nil || *rows[1].TokensIn != 10 || rows[1].TokensOut == nil || *rows[1].TokensOut != 20 {
		t.Errorf("the tokens of on the assistant row are %v/%v, want 10/20", rows[1].TokensIn, rows[1].TokensOut)
	}
	if rows[1].Model != "fake-model" {
		t.Errorf("the assistant row says model %q, want fake-model ", rows[1].Model)
	}
	if rows[1].TelegramMessageID != nil {
		t.Errorf("the assistant row carries telegram_message_id %d, want NULL ", *rows[1].TelegramMessageID)
	}

	if storedOffset(t, db) != 41 {
		t.Errorf("bot_state last_update_id = %d, want 41 ", storedOffset(t, db))
	}
	if countEvents(t, db, store.EventDenied) != 0 {
		t.Errorf("an allowed user produced events(kind='denied') ")
	}
	if tgAPI.WrongTokens() != 0 {
		t.Errorf("%d requests carried a different token in the path ", tgAPI.WrongTokens())
	}
	secretLeak(t, "the texts the bot sent", textOf(tgAPI))
	secretLeak(t, "the log of the run", run.logs.String())

	tgAPI.PushText(42, chatID, userID, "привет ещё раз")
	waitFor(t, "the free text got an answer through the default handler", func() bool { return strata.ChatCalls() == 2 })
	quiet()
	if len(tgAPI.Sent()) != 2 {
		t.Errorf("the free text produced %d messages, want 2 in total ", len(tgAPI.Sent()))
	}
	if storedOffset(t, db) != 42 {
		t.Errorf("bot_state last_update_id = %d, want 42 ", storedOffset(t, db))
	}

	tgAPI.PushText(43, chatID, userID, "/ping")
	waitFor(t, "/ping answered", func() bool { return tgAPI.Calls("sendMessage") >= 3 })
	if !strings.Contains(tgAPI.Texts()[2], "pong") {
		t.Errorf("/ping said %q ", tgAPI.Texts()[2])
	}
	if strata.ChatCalls() != 2 {
		t.Errorf("the fake STRATA got %d chat requests after /ping, want 2 ", strata.ChatCalls())
	}
	if len(rowsOf(t, db)) != 4 {
		t.Errorf("messages of the chat = %d, want 4 ", len(rowsOf(t, db)))
	}
	run.stop()

	// though production runs at LOG_LEVEL=info.
	secretLeak(t, "the whole log of the run", run.logs.String())
}

// kind='denied', no answer»): the update is consumed, the offset moves, nothing reaches the model and no
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
	waitFor(t, "the menu of is published", func() bool { return len(tgAPI.Commands()) == 19 })

	tgAPI.PushText(61, chatID, outsider, "/whoami")
	waitFor(t, "/whoami answered the stranger", func() bool { return tgAPI.Calls("sendMessage") >= 1 })
	if !strings.Contains(tgAPI.Texts()[0], strconv.FormatInt(outsider, 10)) {
		t.Errorf("/whoami of did not identify the stranger: %q", tgAPI.Texts()[0])
	}

	tgAPI.PushText(62, chatID, outsider, "/ask секретный вопрос")
	waitFor(t, "the denied update reached the handler", func() bool { return storedOffset(t, db) == 62 })
	quiet()
	if strata.ChatCalls() != 0 {
		t.Errorf("the fake STRATA got %d requests from a user outside TELEGRAM_ALLOWED_USERS ", strata.ChatCalls())
	}
	// onboarding hint («Твой user_id — N») rather than silence, because a user who cannot read his own id
	// cannot ask to be added. What must not happen is the answer of the model.
	if len(tgAPI.Sent()) != 2 {
		t.Errorf("the stranger got %d messages, want the /whoami answer plus the refusal of", len(tgAPI.Sent()))
	}
	if strings.Contains(tgAPI.Texts()[1], "не должно быть отправлено") {
		t.Errorf("the stranger received the answer of the model : %q", tgAPI.Texts()[1])
	}
	if !strings.Contains(tgAPI.Texts()[1], strconv.FormatInt(outsider, 10)) {
		t.Errorf("the refusal hides the user_id the stranger needs to get into TELEGRAM_ALLOWED_USERS: %q", tgAPI.Texts()[1])
	}
	if countEvents(t, db, store.EventDenied) != 1 {
		t.Errorf("events(kind='denied') = %d, want 1 ", countEvents(t, db, store.EventDenied))
	}
	if len(rowsOf(t, db)) != 0 {
		t.Errorf("a denied update wrote %d messages ", len(rowsOf(t, db)))
	}
	secretLeak(t, "the texts sent to the stranger", textOf(tgAPI))
	secretLeak(t, "the log of the denied run", run.logs.String())
	run.stop()
}

// an update and dies, the second run opens the same file and must not answer the update it already
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
	waitFor(t, "the first run published the menu", func() bool { return len(tgAPI.Commands()) == 19 })

	tgAPI.PushText(71, chatID, userID, "/ask первый")
	waitFor(t, "the first run answered", func() bool { return tgAPI.Calls("sendMessage") >= 1 })
	waitFor(t, "the processed offset was persisted", func() bool { return storedOffset(t, firstDB) == 71 })
	if storedOffset(t, firstDB) != 71 {
		t.Errorf("bot_state last_update_id = %d, want 71 before the restart ", storedOffset(t, firstDB))
	}
	first.stop()
	firstDB.Close()

	var secondDB = openStore(t, path)
	var second = startBot(t, ctx, cfg, secondDB, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second,
	}, storedOffset(t, secondDB))
	if second.offset != 71 {
		t.Errorf("the restart started at offset %d, want 71 ", second.offset)
	}
	if len(rowsOf(t, secondDB)) != 2 {
		t.Errorf("the messages of the first run disappeared after the restart: %d rows", len(rowsOf(t, secondDB)))
	}
	var pollsBefore = tgAPI.Calls("getUpdates")
	waitFor(t, "the restarted run polled the fake", func() bool { return tgAPI.Calls("getUpdates") > pollsBefore })
	if tgAPI.Offsets()[0] != "1" {
		t.Errorf("the first run polled with offset %q, want 1 ", tgAPI.Offsets()[0])
	}
	if tgAPI.Offsets()[len(tgAPI.Offsets())-1] != "72" {
		t.Errorf("the last poll asked offset %q, want 72 ", tgAPI.Offsets()[len(tgAPI.Offsets())-1])
	}

	// The fake already dropped update 71 (the offset was 72), so a new update is the only thing to answer:
	// one extra message and one extra pair of rows is the whole story of the restart.
	tgAPI.PushText(72, chatID, userID, "/ask второй")
	waitFor(t, "the restarted run answered the new update", func() bool { return tgAPI.Calls("sendMessage") >= 2 })
	quiet()
	if len(tgAPI.Sent()) != 2 {
		t.Errorf("the restart produced %d messages in total, want 2 ", len(tgAPI.Sent()))
	}
	if !strings.Contains(tgAPI.Texts()[1], "второй ответ") {
		t.Errorf("the restarted run answered %q, want the scripted «второй ответ» ", tgAPI.Texts()[1])
	}
	if len(rowsOf(t, secondDB)) != 4 {
		t.Errorf("messages after the restart = %d, want 4 ", len(rowsOf(t, secondDB)))
	}
	if storedOffset(t, secondDB) != 72 {
		t.Errorf("bot_state last_update_id = %d, want 72 ", storedOffset(t, secondDB))
	}
	second.stop()
	secondDB.Close()
}

// /reset hid are still there, and the order is oldest → newest).
func rowsOf(t *testing.T, db *sql.DB) []store.Message {
	t.Helper()
	var rows, err = store.HistoryOfChat(context.Background(), db, chatID, 50)
	if err != nil {
		t.Fatalf("store.HistoryOfChat: %v ", err)
	}
	return rows
}

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
	waitFor(t, "the menu of is published", func() bool { return len(tgAPI.Commands()) == 19 })

	tgAPI.PushText(81, chatID, userID, "/ask длинный ответ")
	waitFor(t, "the long answer left the bot in pieces", func() bool { return tgAPI.Calls("sendMessage") >= 2 })
	quiet()

	var texts = tgAPI.Texts()
	if len(texts) < 2 {
		t.Fatalf("a %d-rune answer arrived as %d message(s), UX asks for chunks of MAX_MESSAGE_CHUNK", bot.Runes(body), len(texts))
	}
	var i = 0
	for i < len(texts) {
		var runes = bot.Runes(texts[i])
		if runes > 4096 {
			t.Errorf("chunk %d is %d characters, Telegram's limit of is 4096", i, runes)
		}
		if runes > int(cfg.MaxMessageChunk) {
			t.Errorf("chunk %d is %d characters, MAX_MESSAGE_CHUNK of is %d", i, runes, cfg.MaxMessageChunk)
		}
		if trailingBackslashes(texts[i])%2 == 1 {
			t.Errorf("chunk %d ends in a dangling escape : %q", i, tail(texts[i], 12))
		}
		i++
	}
	// The escape happened once, over the whole text: a raw «(важно)» could only mean the text went out
	var joined = tgAPI.Joined()
	if strings.Contains(joined, "(важно)") {
		t.Errorf("the parentheses of reached Telegram unescaped: %q", tail(joined, 60))
	}
	if strings.Contains(joined, `\\(`) {
		t.Errorf("the escape is doubled : %q", tail(joined, 60))
	}
	if !strings.Contains(joined, `\(`) {
		t.Errorf("no MarkdownV2 escape at all: %q", tail(joined, 60))
	}
	var stripped = strings.ReplaceAll(joined, "\\", "")
	if !strings.Contains(stripped, "слово") || !strings.Contains(stripped, "финал") {
		t.Errorf("the chunking of UX lost the head or the tail of the answer")
	}
	if !strings.Contains(texts[len(texts)-1], "3→7 токенов") {
		t.Errorf("the last chunk has no latency footer of: %q", tail(texts[len(texts)-1], 60))
	}
	for i = 1; i < len(texts)-1; i++ {
		if strings.Contains(texts[i], "токенов") {
			t.Errorf("chunk %d repeats the footer of: %q", i, tail(texts[i], 60))
		}
	}
	for i = 0; i < len(tgAPI.Sent()); i++ {
		if tgAPI.Sent()[i].ParseMode != "MarkdownV2" {
			t.Errorf("chunk %d carries parse_mode %q, PARSE_MODE of is MarkdownV2", i, tgAPI.Sent()[i].ParseMode)
		}
	}
	var rows = rowsOf(t, db)
	if len(rows) != 2 || rows[1].Content != body {
		t.Errorf("the assistant row of must hold the unescaped answer (%d rows)", len(rows))
	}
	secretLeak(t, "the chunked answer", joined)
	secretLeak(t, "the log of the chunked run", run.logs.String())
	run.stop()
}

// prints what Telegram got, and a 5000-rune quote is not readable).
func tail(text string, n int) string {
	var runes = []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[len(runes)-n:])
}

// trailingBackslashes — how many backslashes end the text; odd means the last escape has no character
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

// kind='flood', не слать следующее сообщение в этот чат». The sleep happens inside Send, so the chat is
// quiet for RetryAfter seconds instead of hammering Telegram: the second sendMessage of the same chat is
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
	waitFor(t, "the menu of is published", func() bool { return len(tgAPI.Commands()) == 19 })

	tgAPI.FailNext("sendMessage", 429, "Too Many Requests: retry after 1", 1)
	var began = time.Now()
	tgAPI.PushText(91, chatID, userID, "/ask flood меня?")
	waitFor(t, "the answer arrived after the pause", func() bool { return len(tgAPI.Sent()) >= 1 })
	var waited = time.Since(began)

	if tgAPI.Calls("sendMessage") != 2 {
		t.Errorf("sendMessage was attempted %d times, want the rejected one plus the retry ", tgAPI.Calls("sendMessage"))
	}
	if waited < time.Second {
		t.Errorf("the chat waited %s, sleeps ровно RetryAfter = 1s", waited)
	}
	if waited > 8*time.Second {
		t.Errorf("the chat waited %s, the sleep of is RetryAfter, capped by FloodSleepMax of %s", waited, bot.FloodSleepMax)
	}
	if !strings.Contains(tgAPI.Texts()[0], "ответ после flood") {
		t.Errorf("the retry sent %q, want the answer of the model", tgAPI.Texts()[0])
	}
	if countEvents(t, db, store.EventFlood) != 1 {
		t.Errorf("events(kind='flood') = %d, want 1 ", countEvents(t, db, store.EventFlood))
	}
	if !strings.Contains(run.logs.String(), "retry_after") {
		t.Errorf("the log of надёжность does not mention retry_after: %s", run.logs.String())
	}
	secretLeak(t, "the log of the flood run", run.logs.String())
	run.stop()
}

// closest to the log: the library puts the request URL — token included — into its error texts, and the
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
	waitFor(t, "the menu of is published", func() bool { return len(tgAPI.Commands()) == 19 })

	tgAPI.FailNext("sendMessage", 403, "Forbidden: bot was blocked by the user", 0)
	tgAPI.PushText(101, chatID, userID, "/ask после блокировки")
	waitFor(t, "the failed send reached the log", func() bool { return strings.Contains(run.logs.String(), "Forbidden") })

	if tgAPI.Calls("sendMessage") != 1 {
		t.Errorf("sendMessage was attempted %d times, want 1: only a 429 gets the retry of", tgAPI.Calls("sendMessage"))
	}
	if len(tgAPI.Sent()) != 0 {
		t.Errorf("a blocked chat got %d messages ", len(tgAPI.Sent()))
	}
	if storedOffset(t, db) != 101 {
		t.Errorf("bot_state last_update_id = %d, want 101 ", storedOffset(t, db))
	}
	if countEvents(t, db, store.EventCommand) != 1 {
		t.Errorf("events(kind='command') = %d, want 1 ", countEvents(t, db, store.EventCommand))
	}
	if !strings.Contains(run.logs.String(), "bot was blocked") {
		t.Errorf("the log lost the class of the Telegram failure : %s", run.logs.String())
	}
	secretLeak(t, "the log of the failed send", run.logs.String())
	run.stop()
}

// getMe of NewTelegram the foreign suffix would be stripped as well, and the bot would answer commands
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
	waitFor(t, "the menu of is published", func() bool { return len(tgAPI.Commands()) == 19 })

	// п.2 still lowercases the name.
	tgAPI.PushText(111, chatID, userID, "/Ping@FakeBot")
	waitFor(t, "/Ping@FakeBot was recognized", func() bool { return tgAPI.Calls("sendMessage") >= 1 })
	if !strings.HasPrefix(tgAPI.Texts()[0], "pong ") {
		t.Errorf("the suffixed command of said %q, want the /ping answer ", tgAPI.Texts()[0])
	}

	tgAPI.PushText(112, chatID, userID, "/ping@AnotherBot")
	waitFor(t, "the foreign suffix fell to the hint of", func() bool { return tgAPI.Calls("sendMessage") >= 2 })
	quiet()
	if strings.HasPrefix(tgAPI.Texts()[1], "pong ") {
		t.Errorf("a command addressed to another bot was answered as ours : %q", tgAPI.Texts()[1])
	}
	if !strings.Contains(tgAPI.Texts()[1], "/ask <текст>") {
		t.Errorf("the foreign suffix did not fall to the /help hint of: %q", tgAPI.Texts()[1])
	}
	if strata.ChatCalls() != 0 {
		t.Errorf("the foreign suffix reached the model %d times ", strata.ChatCalls())
	}
	if storedOffset(t, db) != 112 {
		t.Errorf("bot_state last_update_id = %d, want 112 ", storedOffset(t, db))
	}
	secretLeak(t, "the suffix test", textOf(tgAPI))
	secretLeak(t, "the log of the suffix run", run.logs.String())
	run.stop()
}

// «повторные ответы после рестарта»), so the restarted poller starts behind the update and Telegram
// delivers it a second time.
func TestRedeliveredUpdateIsAnsweredOnce(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	// Two replies are scripted on purpose: a leak past the guard would consume the second one and put its
	var strata = testutil.NewStrata(t, secretStrataKey,
		testutil.StrataReply{Content: "первый ответ"},
		testutil.StrataReply{Content: "второй ответ — он выдал бы, что повтор дошёл до модели"})
	var cfg = integrationEnv(t, path, strata.BaseURL(), nil)
	var db = openStore(t, path)

	var first = startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second, Workers: 2,
	}, storedOffset(t, db))
	waitFor(t, "the first run published the menu", func() bool { return len(tgAPI.Commands()) == 19 })

	tgAPI.PushText(91, chatID, userID, "/ask один")
	waitFor(t, "the question was answered", func() bool { return tgAPI.Calls("sendMessage") >= 1 })
	quiet()
	if got := strata.ChatCalls(); got != 1 {
		t.Fatalf("the model was called %d times for one question, want 1 ", got)
	}
	first.stop()

	// next poller starts from a bot_state that was never written and Telegram hands the same update over
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
		t.Errorf("the chat received %d messages after the redelivery, want 1 ", got)
	}
	if got := strata.ChatCalls(); got != 1 {
		t.Errorf("the model was called %d times, want 1 ", got)
	}
	if got := len(rowsOf(t, db)); got != 2 {
		t.Errorf("messages = %d, want 2 ", got)
	}
	if got := storedOffset(t, db); got != 91 {
		t.Errorf("bot_state last_update_id = %d, want 91 ", got)
	}
	if !strings.Contains(second.logs.String(), "already answered") {
		t.Errorf("the log does not name the reason of the silence : %s", second.logs.String())
	}
	secretLeak(t, "the log of the redelivery", second.logs.String())
	secretLeak(t, "the chat of the redelivery", textOf(tgAPI))
	second.stop()
}

// rewrites of that same message id, then the footer on the last rewrite.
func TestStreamedAnswerEditsOneMessage(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	var strata = testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{
		Model: "gpt-4o-mini", TokensIn: 3, TokensOut: 7,
		StreamReasoning: []string{"думаю", " ещё думаю"},
		StreamGap:       300 * time.Millisecond, // six events at the cadence floor: the placeholder gets rewritten several times
		StreamContent:   []string{"Один ", "два ", "три.", " (важно)"},
		StreamUsage:     true,
	})
	// PARSE_MODE=MarkdownV2 because a streamed answer goes through the same escaping as any other one
	// table: a faster cadence is refused by config, and the progress gate of the client follows it. The idle
	var cfg = integrationEnv(t, path, strata.BaseURL(), map[string]string{
		"PARSE_MODE": "MarkdownV2", "STREAMING_ENABLED": "true", "STREAM_EDIT_INTERVAL_MS": "250",
		"STREAM_IDLE_TIMEOUT_SECONDS": "4",
	})
	var db = openStore(t, path)

	var run = startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second, Workers: 2,
	}, storedOffset(t, db))
	waitFor(t, "the menu of", func() bool { return len(tgAPI.Commands()) == 19 })

	tgAPI.PushText(140, chatID, userID, "/ask один")
	// The last rewrite is what the user keeps on screen, so that is the moment the test waits for — the
	waitFor(t, "the streamed answer was rewritten with the footer of", func() bool {
		var edits = tgAPI.Edits()
		return len(edits) > 0 && strings.Contains(edits[len(edits)-1].Text, "токенов")
	})
	quiet()
	var sent = tgAPI.Sent()
	var edits = tgAPI.Edits()
	if len(sent) != 1 {
		t.Fatalf("the chat received %d messages for one streamed question, want 1 : %s",
			len(sent), strings.Join(tgAPI.Texts(), " | "))
	}
	if sent[0].Text != bot.StreamPlaceholder {
		t.Errorf("the message posted first was %q, want the placeholder %q ",
			sent[0].Text, bot.StreamPlaceholder)
	}
	if sent[0].ParseMode != "" {
		t.Errorf("the placeholder was sent with parse_mode %q, want none (: the same message later holds «(3 847 символов)», which no parser accepts)", sent[0].ParseMode)
	}
	if len(edits) < 2 {
		t.Fatalf("the placeholder was rewritten %d times, want at least 2 : %s",
			len(edits), strings.Join(tgAPI.EditStrings(), " | "))
	}

	// Every rewrite addresses the id the placeholder was posted with. The fake answers 400 for an id it never
	// handed out, so a bot editing nothing would already have failed above; this names the id the process held.
	var sawThinking = false
	var i = 0
	for i < len(edits) {
		if edits[i].MessageID != sent[0].MessageID {
			t.Errorf("rewrite %d addressed message %d, want the placeholder %d ",
				i+1, edits[i].MessageID, sent[0].MessageID)
		}
		if strings.Contains(edits[i].Text, "Модель думает") {
			sawThinking = true
		}
		i++
	}
	if !sawThinking {
		t.Errorf("no rewrite told the user the model is thinking : %s",
			strings.Join(tgAPI.EditStrings(), " | "))
	}
	if edits[0].ParseMode != "" {
		t.Errorf("rewrite 1 carried parse_mode %q, want none ", edits[0].ParseMode)
	}
	// only rewrite that asks for formatting at all.
	var last = edits[len(edits)-1]
	if last.ParseMode != "MarkdownV2" {
		t.Errorf("the final rewrite carried parse_mode %q, want MarkdownV2 ", last.ParseMode)
	}
	if !strings.Contains(last.Text, `\(важно\)`) {
		t.Errorf("the final rewrite is not escaped for MarkdownV2 : %q", last.Text)
	}
	if !strings.Contains(last.Text, "3→7 токенов") {
		t.Errorf("the final rewrite lost the footer of : %q", last.Text)
	}
	if strings.Contains(strings.Join(tgAPI.EditStrings(), " "), "ещё думаю") {
		t.Errorf("the model's reasoning reached the chat ")
	}

	var stored = rowsOf(t, db)
	if stored[0].Content != "один" {
		t.Errorf("the stored question = %q, want «один» ", stored[0].Content)
	}
	if stored[1].Content != "Один два три. (важно)" {
		t.Errorf("the stored answer = %q, want «Один два три. (важно)» ", stored[1].Content)
	}
	if stored[1].Model == "" {
		t.Errorf("the stored answer names no model : %+v", stored[1])
	}
	if tgAPI.Calls("sendMessage") != 1 {
		t.Errorf("sendMessage was called %d times for one streamed question, want 1 ", tgAPI.Calls("sendMessage"))
	}
	secretLeak(t, "the log of the streamed run", run.logs.String())
	secretLeak(t, "the chat of the streamed run", textOf(tgAPI))
	run.stop()
}

func TestWebhookAcceptsOnlySecretUpdatesWithoutPolling(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	var strata = testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "ответ по webhook", Model: "fake-model", TokensIn: 4, TokensOut: 6})
	var cfg = integrationEnv(t, path, strata.BaseURL(), map[string]string{
		"WEBHOOK_ENABLED":      "true",
		"WEBHOOK_PUBLIC_URL":   "https://example.test/telegram/webhook",
		"WEBHOOK_SECRET_TOKEN": "1234567890abcdef",
	})
	cfg.WebhookPort = 0
	var db = openStore(t, path)
	var run = startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, 0)

	registration, found := tgAPI.Webhook()
	if !found || registration.URL != cfg.WebhookPublicURL || registration.SecretToken != cfg.WebhookSecretToken {
		t.Fatalf("setWebhook registration = %+v found=%v", registration, found)
	}
	if registration.DropPendingUpdates || len(registration.AllowedUpdates) != 2 || registration.AllowedUpdates[0] != "message" || registration.AllowedUpdates[1] != "callback_query" {
		t.Fatalf("webhook must keep pending updates and allow messages and menu callbacks: %+v", registration)
	}
	if run.webhookURL == "" {
		t.Fatal("the local webhook listener did not start")
	}
	if tgAPI.Calls("getUpdates") != 0 {
		t.Fatalf("webhook mode must not start long polling, getUpdates calls=%d", tgAPI.Calls("getUpdates"))
	}

	wrong := postWebhook(t, run.webhookURL, "wrong-secret")
	if wrong.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong-secret response = %d, want 403", wrong.StatusCode)
	}
	missing := postWebhook(t, run.webhookURL, "")
	if missing.StatusCode != http.StatusForbidden {
		t.Fatalf("missing-secret response = %d, want 403", missing.StatusCode)
	}
	quiet()
	if tgAPI.Calls("sendMessage") != 0 {
		t.Fatal("an update with a wrong or missing secret reached the model")
	}
	if strings.Contains(run.logs.String(), "invalid webhook secret") || strings.Contains(run.logs.String(), "error decode request body") {
		t.Fatalf("rejected webhook reached the library logging path: %s", run.logs.String())
	}
	resp, err := http.Get(run.webhookURL)
	if err != nil {
		t.Fatalf("GET webhook: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET webhook status = %d, want 405", resp.StatusCode)
	}

	accepted := postWebhook(t, run.webhookURL, cfg.WebhookSecretToken)
	if accepted.StatusCode != http.StatusOK {
		t.Fatalf("valid webhook status = %d, want 200", accepted.StatusCode)
	}
	waitFor(t, "the webhook answer reached fake Telegram", func() bool { return tgAPI.Calls("sendMessage") == 1 })
	if !strings.Contains(tgAPI.Joined(), "ответ по webhook") {
		t.Fatalf("webhook answer = %q", tgAPI.Joined())
	}
	waitFor(t, "the webhook offset was stored", func() bool {
		offset, found, err := store.LastUpdateID(ctx, db)
		return err == nil && found && offset == 160
	})
	secretLeak(t, "webhook run log", run.logs.String())
	run.stop()
}

func postWebhook(t *testing.T, url string, secret string) *http.Response {
	t.Helper()
	body := `{"update_id":160,"message":{"message_id":160,"date":1700000000,"chat":{"id":4242,"type":"private"},"from":{"id":1717,"is_bot":false,"first_name":"Tester","username":"tester"},"text":"/ask webhook"}}`
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST webhook: %v", err)
	}
	_ = resp.Body.Close()
	return resp
}

func TestModelSelectPersistsPerChat(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	var strata = testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "ответ", Model: "qwen-alt", Models: []string{"qwen-test", "qwen-alt"}})
	var cfg = integrationEnv(t, path, strata.BaseURL(), nil)
	var db = openStore(t, path)
	var run = startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, 0)

	tgAPI.PushText(170, chatID, userID, "/model select qwen-alt")
	waitFor(t, "model selection was confirmed", func() bool { return tgAPI.Calls("sendMessage") == 1 })
	selected, found, err := store.ChatModel(ctx, db, chatID)
	if err != nil || !found || selected != "qwen-alt" {
		t.Fatalf("ChatModel=%q found=%v err=%v", selected, found, err)
	}

	tgAPI.PushText(171, chatID, userID, "/ask вопрос")
	waitFor(t, "the answer used the selected model", func() bool { return tgAPI.Calls("sendMessage") == 2 })
	if !strings.Contains(strata.LastRequestBody(), "qwen-alt") {
		t.Fatalf("the completion request must carry qwen-alt: %s", strata.LastRequestBody())
	}

	tgAPI.PushText(172, chatID, userID, "/model reset")
	waitFor(t, "model reset was confirmed", func() bool { return tgAPI.Calls("sendMessage") == 3 })
	if _, found, err := store.ChatModel(ctx, db, chatID); err != nil || found {
		t.Fatalf("reset must clear the override, found=%v err=%v", found, err)
	}
	run.stop()
}

func TestInlineMenuAndCallback(t *testing.T) {
	var ctx = context.Background()
	var path = dbFile(t)
	var tgAPI = testutil.NewTelegram(t, secretToken)
	var strata = testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "ответ"})
	var cfg = integrationEnv(t, path, strata.BaseURL(), map[string]string{"PARSE_MODE": "MarkdownV2"})
	var db = openStore(t, path)
	var run = startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, 0)

	tgAPI.PushText(180, chatID, userID, "/start")
	waitFor(t, "the inline menu was sent", func() bool { return tgAPI.Calls("sendMessage") == 1 })
	if !strings.Contains(tgAPI.Sent()[0].Text, `Привет\!`) {
		t.Errorf("the actual /start text must escape '!', got %q", tgAPI.Sent()[0].Text)
	}
	var menu = tgAPI.Sent()[0].ReplyMarkup
	for _, want := range []string{"inline_keyboard", "menu:ask", "menu:reset", "menu:history", "menu:docs", "menu:settings", "menu:status", "menu:help", "menu:more"} {
		if !strings.Contains(menu, want) {
			t.Errorf("inline menu misses %q: %s", want, menu)
		}
	}

	menuMessageID := tgAPI.Sent()[0].MessageID
	tgAPI.Push(models.Update{ID: 181, CallbackQuery: &models.CallbackQuery{
		ID: "cb-history", Data: "menu:history", From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: menuMessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		}},
	}})
	waitFor(t, "history navigation edited the original menu", func() bool {
		return tgAPI.Calls("answerCallbackQuery") == 1 && len(tgAPI.Edits()) == 1
	})
	if edit := tgAPI.Edits()[0]; edit.MessageID != menuMessageID || !strings.Contains(edit.Text, "История") {
		t.Fatalf("history navigation edit = %+v, want message %d", edit, menuMessageID)
	}
	if tgAPI.Calls("sendMessage") != 1 {
		t.Fatalf("navigation sent a new full menu instead of editing: %d sends", tgAPI.Calls("sendMessage"))
	}
	tgAPI.Push(models.Update{ID: 182, CallbackQuery: &models.CallbackQuery{
		ID: "cb1", Data: "cmd:ping", From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: menuMessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		}},
	}})
	waitFor(t, "callback was answered and /ping ran", func() bool {
		return tgAPI.Calls("answerCallbackQuery") == 2 && tgAPI.Calls("sendMessage") == 2
	})
	if !strings.Contains(tgAPI.Joined(), "pong") {
		t.Fatalf("callback /ping must answer pong, got %q", tgAPI.Joined())
	}
	tgAPI.PushText(183, chatID, userID, "/persona")
	waitFor(t, "persona menu was sent", func() bool { return tgAPI.Calls("sendMessage") == 3 })
	if !strings.Contains(tgAPI.Sent()[2].ReplyMarkup, "persona:coder") {
		t.Fatalf("persona response has no coder button: %q", tgAPI.Sent()[2].ReplyMarkup)
	}
	tgAPI.Push(models.Update{ID: 184, CallbackQuery: &models.CallbackQuery{
		ID: "cb-persona", Data: "persona:coder", From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: menuMessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		}},
	}})
	waitFor(t, "persona callback selected coder", func() bool {
		return tgAPI.Calls("answerCallbackQuery") == 3 && tgAPI.Calls("sendMessage") == 4
	})
	prompt, found, err := store.ChatPersona(context.Background(), db, chatID)
	if err != nil || !found || !strings.Contains(prompt, "senior software engineer") {
		t.Fatalf("persona callback did not persist coder: prompt=%q found=%v err=%v", prompt, found, err)
	}
	run.stop()
}

func TestInlineAskPendingInputEndToEnd(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "ответ", Model: "fake-model"})
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{"PROXY_FREE_TEXT": "false"})
	db := openStore(t, path)
	run := startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, 0)

	tgAPI.PushText(200, chatID, userID, "/start")
	waitFor(t, "root menu for pending Ask", func() bool { return tgAPI.Calls("sendMessage") == 1 })
	menuMessageID := tgAPI.Sent()[0].MessageID
	tgAPI.Push(models.Update{ID: 201, CallbackQuery: &models.CallbackQuery{
		ID: "ask-pending", Data: "menu:ask", From: models.User{ID: userID, Username: "tester"},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: menuMessageID, Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		}},
	}})
	waitFor(t, "Ask callback installed pending input", func() bool {
		return tgAPI.Calls("answerCallbackQuery") == 1 && len(tgAPI.Edits()) == 1
	})
	if !strings.Contains(tgAPI.Edits()[0].Text, "2 минуты") {
		t.Fatalf("Ask pending prompt = %q", tgAPI.Edits()[0].Text)
	}
	tgAPI.PushText(202, chatID, userID, "вопрос из inline-меню")
	waitFor(t, "pending Ask reached the model", func() bool { return strata.ChatCalls() == 1 && tgAPI.Calls("sendMessage") == 2 })
	if !strings.Contains(strata.LastRequestBody(), "вопрос из inline-меню") {
		t.Fatalf("model request = %s", strata.LastRequestBody())
	}
	rows := rowsOf(t, db)
	if len(rows) != 2 || rows[0].Content != "вопрос из inline-меню" {
		t.Fatalf("pending Ask rows = %+v", rows)
	}
	tgAPI.PushText(203, chatID, userID, "сообщение вне pending-состояния")
	waitFor(t, "ordinary message outside pending state was handled", func() bool { return tgAPI.Calls("sendMessage") == 3 })
	if strata.ChatCalls() != 1 {
		t.Fatalf("message outside pending state reached model %d times", strata.ChatCalls())
	}
	tgAPI.Push(models.Update{ID: 204, CallbackQuery: &models.CallbackQuery{ID: "stale-menu", Data: "menu:model:set:99", From: models.User{ID: userID}}})
	waitFor(t, "stale callback was answered", func() bool { return tgAPI.Calls("answerCallbackQuery") == 2 })
	run.stop()
}

func TestParseErrorFallsBackToPlainMessage(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "Точка. Восклицание! Плюс +"})
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{"PARSE_MODE": "MarkdownV2"})
	db := openStore(t, path)
	run := startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, 0)
	waitFor(t, "menu published", func() bool { return len(tgAPI.Commands()) == 19 })

	tgAPI.FailNext("sendMessage", 400, "Bad Request: can't parse entities: Character '.' is reserved", 0)
	tgAPI.PushText(190, chatID, userID, "/ask проверка пунктуации")
	waitFor(t, "plain fallback delivered", func() bool { return len(tgAPI.Sent()) == 1 })
	if tgAPI.Calls("sendMessage") != 2 {
		t.Fatalf("sendMessage calls=%d, want one rejected formatted attempt and one plain fallback", tgAPI.Calls("sendMessage"))
	}
	sent := tgAPI.Sent()[0]
	if sent.ParseMode != "" {
		t.Fatalf("fallback parse_mode=%q, want plain", sent.ParseMode)
	}
	if strings.Contains(sent.Text, `\.`) || !strings.Contains(sent.Text, "Точка.") {
		t.Fatalf("fallback text is not readable plain content: %q", sent.Text)
	}
	if len(rowsOf(t, db)) != 2 {
		t.Fatal("fallback must keep exactly one user and one assistant row")
	}
	run.stop()
}

func TestStreamFinalEditParseErrorFallsBackOnSameMessage(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{
		StreamContent: []string{"Длинный ответ. ", "Продолжение! ", "Финал + конец."},
		StreamUsage:   true,
	})
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{
		"PARSE_MODE": "MarkdownV2", "STREAMING_ENABLED": "true", "STREAM_IDLE_TIMEOUT_SECONDS": "4",
	})
	db := openStore(t, path)
	run := startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, 0)
	waitFor(t, "menu published", func() bool { return len(tgAPI.Commands()) == 19 })

	tgAPI.FailNext("editMessageText", 400, "Bad Request: can't parse entities: Character '.' is reserved", 0)
	tgAPI.PushText(191, chatID, userID, "/ask длинный ответ")
	waitFor(t, "plain final edit delivered", func() bool { return len(tgAPI.Edits()) == 1 })
	if tgAPI.Calls("editMessageText") != 2 {
		t.Fatalf("editMessageText calls=%d, want formatted failure and plain fallback", tgAPI.Calls("editMessageText"))
	}
	if len(tgAPI.Sent()) != 1 {
		t.Fatalf("stream must keep one placeholder message, got %d sends", len(tgAPI.Sent()))
	}
	edit := tgAPI.Edits()[0]
	if edit.MessageID != tgAPI.Sent()[0].MessageID {
		t.Fatalf("fallback edited id=%d, want original placeholder id=%d", edit.MessageID, tgAPI.Sent()[0].MessageID)
	}
	if edit.ParseMode != "" || strings.Contains(edit.Text, `\.`) || !strings.Contains(edit.Text, "Финал + конец.") {
		t.Fatalf("fallback final edit is not complete plain content: %+v", edit)
	}
	if len(rowsOf(t, db)) != 2 {
		t.Fatal("stream fallback must keep exactly one user and one assistant row")
	}
	run.stop()
}
