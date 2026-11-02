// run.go — the adapter of §4 («run.go — единственный файл, который импортирует библиотеку») and the
// whole of §10 День 4: «обёртка над библиотекой: bot.New(c.BotToken, opts) с WithServerURL для тестов,
// WithInitialOffset из bot_state, WithHTTPClient(pollTimeout) и workers; обработка 429
// TooManyRequestsError; middleware allowlist; middleware panic-recovery; middleware rate limit;
// setMyCommands при старте».
//
// Everything §6 answers with is decided by dispatch.go, middleware.go, commands.go, context.go and
// reply.go, which speak the Event/Replier vocabulary. This file only translates: models.Update →
// Event (the reduction of §6 п.7 that drops edited_message), and Replier.Send → sendMessage (with the
// parse mode of §7, the chunks of §12 UX and the sleep of §10 День 4).
//
// Two facts of v1.27.0 shape the file (§16 change 27): `models.MessageTypeText` does not exist, so the
// matcher tests `update.Message.Text`; and `models.ParseModeMarkdown` is the constant whose VALUE is
// "MarkdownV2" — a rename nobody made in the library, so reply.go keeps its own names and maps here.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"bot/internal/config"
	"bot/internal/store"
)

const (
	// DefaultPollTimeout — the long-poll wait of §10 День 4 («WithHTTPClient(pollTimeout)»). The
	// library sends `timeout = pollTimeout - 1s` to getUpdates so the answer arrives before the client
	// gives up (wait_updates.go), which is why the http.Client timeout must be larger (§12 reliability).
	DefaultPollTimeout = 30 * time.Second
	// InitTimeout — getMe at New time (§10 День 4: New calls getMe with WithCheckInitTimeout and
	// returns an error without an answer). Two route attempts may each need a TLS handshake before polling starts.
	InitTimeout = 65 * time.Second
	// TLSHandshakeTimeout tolerates a slow or intermittently filtered Telegram route without changing
	// the long-poll budget. The Go default is 10 s; transient route failures must not force a reconnect loop.
	TLSHandshakeTimeout = 30 * time.Second
	// FloodSleepMax — the cap of the 429 sleep. The live streamed walk measured retry_after=120; a 30-second
	// cap retried while the chat was still throttled and lost the tail. The per-chat lock stays held and the
	// reply context still wins if its budget is shorter than the requested pause.
	FloodSleepMax = 120 * time.Second
	// PublishCommandsTimeout — setMyCommands is cosmetic, so it gets a short deadline instead of
	// holding up the startup of §4 («cmd/bot/main.go: конфиг → store → … → bot»).
	PublishCommandsTimeout = 5 * time.Second
)

// Options — the wiring knobs of §10 День 4. Everything has a production default, and ServerURL is the
// one §11 needs: «с реальным HTTP-клиентом и bot.WithServerURL(fake.URL())».
type Options struct {
	// ServerURL points the client at the fake Telegram of §11; empty means api.telegram.org.
	ServerURL string
	// PollTimeout — the first argument of WithHTTPClient. Zero → DefaultPollTimeout.
	PollTimeout time.Duration
	// Workers — WithWorkers, i.e. how many updates are processed at once (§10 День 6 parallelism).
	// Zero → MAX_CONCURRENT_REQUESTS of §7, the number the rate limit already speaks.
	Workers int
	// SkipGetMe — WithSkipGetMe, for a fake that does not serve getMe. NewTelegram needs the username
	// of the bot for the normalization of §6, so with SkipGetMe the BotName of Deps is what stays.
	SkipGetMe bool
	// HTTPClient — the client of WithHTTPClient; nil → long poll plus two bounded route handshakes.
	HTTPClient *http.Client
	// WebhookSecretToken enables the X-Telegram-Bot-Api-Secret-Token check on the webhook handler.
	WebhookSecretToken string
}

func defaultTelegramHTTPClient(poll time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = TLSHandshakeTimeout
	return &http.Client{
		Transport: newTelegramFallbackTransport(transport),
		Timeout:   poll + 2*TLSHandshakeTimeout + 5*time.Second,
	}
}

// NewTelegram — the library bot wired to d, plus the handlers of §10 День 4:
//
//	RegisterHandlerMatchFunc — the registry decides, not the library matchers (§6: the bot_command
//	                                       entity hides the `@BotName` suffix, so MatchTypeCommand
//	                                       would never match `/ask@MyBot`)
//	WithDefaultHandler       — every other update (free text, unknown command, non-text) arrives at
//	                                       the same function, which is the fallback table of §6
//
// NewTelegram replaces deps.Replier with the sendMessage implementation, because the Replier needs the
// bot and the bot is built here. Both replacements happen before Start, so the worker goroutines of
// §10 День 6 never see a half-written Deps (no data race in the `-race` run of §10 День 6).
func (d *Dispatcher) NewTelegram(opts Options) (*tgbot.Bot, error) {
	var token = strings.TrimSpace(d.deps.Cfg.BotToken)
	if token == "" {
		return nil, errors.New("TELEGRAM_BOT_TOKEN is empty (§7): there is nothing to poll with")
	}
	var poll = opts.PollTimeout
	if poll <= 0 {
		poll = DefaultPollTimeout
	}
	var workers = opts.Workers
	if workers <= 0 {
		workers = int(d.deps.Cfg.MaxConcurrentRequests)
	}
	if workers <= 0 {
		// WithWorkers(0) makes Start spawn no worker and return at once: the bot would look alive and
		// answer nothing (§12 reliability).
		workers = 1
	}
	var client = opts.HTTPClient
	if client == nil {
		client = defaultTelegramHTTPClient(poll)
	}

	var setting = []tgbot.Option{
		tgbot.WithHTTPClient(poll, client),
		tgbot.WithCheckInitTimeout(InitTimeout),
		tgbot.WithWorkers(workers),
		// Accept text messages and menu callbacks; other update types stay out of the queue and the log.
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{"message", "callback_query"}),
		tgbot.WithDefaultHandler(d.telegramHandler),
		// §12 security: the default errorsHandler of the library prints errors carrying
		// `https://api.telegram.org/bot<TOKEN>/…`, so every text goes through Sanitize first. No
		// WithDebug (§16 change 18: the debug handler of the library prints request payloads).
		tgbot.WithErrorsHandler(func(err error) {
			d.deps.Logger.Error("the telegram library failed", "error", Sanitize(err, token))
		}),
	}
	if opts.ServerURL != "" {
		setting = append(setting, tgbot.WithServerURL(opts.ServerURL))
	}
	if opts.SkipGetMe {
		setting = append(setting, tgbot.WithSkipGetMe())
	}
	if opts.WebhookSecretToken != "" {
		setting = append(setting, tgbot.WithWebhookSecretToken(opts.WebhookSecretToken))
	}
	if d.startOffset > 0 {
		// §5 «Offset long polling»: bot_state last_update_id is the last PROCESSED update, and the
		// library sends offset = lastUpdateID + 1, so a restart continues after it (§10 День 4
		// «WithInitialOffset из bot_state», §13 риск «повторные ответы после рестарта»).
		setting = append(setting, tgbot.WithInitialOffset(d.startOffset))
	}

	var tg, err = tgbot.New(token, setting...)
	if err != nil {
		// §12 security: the error of the library carries the request URL, i.e. the token, so what
		// leaves this function — and what main.go prints — is the sanitized text, wrapped as an error
		// so the caller still sees a cause.
		return nil, fmt.Errorf("bot.New: %w", errors.New(Sanitize(err, token)))
	}
	var replier = &telegramReplier{d: d, bot: tg, cfg: d.deps.Cfg, users: map[int64]int64{}, logger: d.deps.Logger}
	d.deps.Replier = replier
	tg.RegisterHandlerMatchFunc(d.matchUpdate, d.telegramHandler)

	if !opts.SkipGetMe {
		// §6 normalization strips the `@BotName` suffix, and getMe is the only place that knows the
		// username. New has already called getMe (WithSkipGetMe off), so this costs one cheap request,
		// and it runs before Start — there is no concurrent reader of deps yet.
		var ctx, cancel = context.WithTimeout(context.Background(), InitTimeout)
		defer cancel()
		var me, meErr = tg.GetMe(ctx)
		if meErr != nil {
			d.deps.Logger.Warn("getMe did not answer, so the @BotName suffix of §6 will not be stripped",
				"error", Sanitize(meErr, token))
		} else if strings.TrimSpace(me.Username) != "" {
			d.deps.BotName = strings.TrimSpace(me.Username)
		}
	}
	return tg, nil
}

// matchUpdate — the MatchFunc of §10 День 4. Any text message is claimed: the registry of dispatch.go
// knows what a command is, and the fallback of §6 (free text → /ask, unknown command → the hint) also
// lives there, so a matcher that tried to be clever would duplicate the table of §6 (§16 change 27:
// there is no models.MessageTypeText in v1.27.0, Message.Type is MaybeInaccessibleMessageType, so the
// text itself is the only reliable signal).
func (d *Dispatcher) matchUpdate(upd *models.Update) bool {
	if upd == nil || upd.Message == nil {
		return false // edited_message, channel_post, callback_query: §6 п.7 drops them, the dispatcher never sees them
	}
	return strings.TrimSpace(upd.Message.Text) != ""
}

// telegramHandler — the single entry point of both paths (§10 День 4): the registered MatchFunc for
// text messages and WithDefaultHandler for everything else.
func (d *Dispatcher) telegramHandler(ctx context.Context, tg *tgbot.Bot, upd *models.Update) {
	// §12 reliability / §10 День 7: the first question in a live incident is always «did the update arrive
	// at all», and until today the process answered it only by silence. One debug line per update, with the
	// id and the length of the text rather than the text itself (§12 security: LOG_LEVEL=debug must not
	// start logging the conversations of the users).
	var updateID int64
	var textLen = 0
	if upd != nil {
		updateID = upd.ID
		if upd.Message != nil {
			textLen = len([]rune(upd.Message.Text))
		}
	}
	// §12 надёжность: the net the panic middleware of §10 День 4 cannot provide. `recovered` lives inside
	// Dispatch, so everything in this function that runs BEFORE Dispatch is unprotected — and by then the
	// library has already read the update, which means Telegram will not send it back: getUpdates deletes
	// what it returned, and §5 last_update_id is written only after Dispatch (§5 «Offset long polling»).
	// Measured 2026-10-31 on this machine: the «carries no text» line below read upd.Message.Chat.ID before
	// eventOf had checked Message for nil, so the first callback_query (or my_chat_member, or edited_message,
	// i.e. anything the MatchFunc of §10 День 4 does not claim) panicked in the goroutine of the library and
	// ended the process — an update lost with no row in §5 and no line in the log to explain the silence.
	// One defer turns that silent loss into one error line; the update itself is still gone, which is why the
	// wording says «lost» rather than «skipped».
	defer func() {
		var r = recover()
		if r == nil {
			return
		}
		d.deps.Logger.Error("the update handler panicked before Dispatch, the update is lost",
			"update_id", updateID, "panic", fmt.Sprint(r))
	}()
	if upd != nil && upd.CallbackQuery != nil {
		d.handleCallback(ctx, tg, upd)
		return
	}
	d.deps.Logger.Debug("the update arrived", "update_id", updateID, "text_len", textLen)
	var ev, ok = eventOf(upd)
	if !ok {
		// §6: a photo, a sticker, a callback carries no text — answering it would confuse the chat and
		// would store a row §5 has no place for. Debug would be enough, but §12 надёжность wants to
		// see that updates arrive and are skipped.
		var chatID int64
		if upd != nil && upd.Message != nil {
			chatID = upd.Message.Chat.ID
		}
		d.deps.Logger.Info("the update carries no text, skipped", "update_id", updateID, "chat_id", chatID)
		return
	}
	d.noteChatUser(ev.ChatID, ev.UserID) // the flood audit of §10 День 4 attributes events to a user
	if err := d.Dispatch(ctx, ev); err != nil {
		return // Dispatch has already logged it, sanitized (§12 security)
	}
}

// eventOf — the reduction of §6 п.7. An update with no message (edited_message, channel_post,
// callback_query) or with an empty text is not an Event: an edit would reuse the telegram_message_id of
// §5, so neither the UNIQUE guard nor the idempotency of §10 День 6 would stop a second model call, and
// §6 says an edit is not a new question.
func eventOf(upd *models.Update) (Event, bool) {
	if upd == nil || upd.Message == nil {
		return Event{}, false
	}
	var text = strings.TrimSpace(upd.Message.Text)
	if text == "" {
		return Event{}, false
	}
	var ev = Event{
		UpdateID:          upd.ID,
		TelegramMessageID: int64(upd.Message.ID),
		ChatID:            upd.Message.Chat.ID,
		ChatType:          string(upd.Message.Chat.Type),
		ChatTitle:         chatTitle(upd.Message.Chat),
		Text:              upd.Message.Text,
	}
	if upd.Message.From != nil {
		ev.UserID = upd.Message.From.ID
		ev.Username = upd.Message.From.Username
	}
	return ev, true
}

// chatTitle — the label §5 writes into chats.title («название чата (для групп)»): the title of a group,
// the two names of a private chat Telegram does not title itself.
func chatTitle(chat models.Chat) string {
	if strings.TrimSpace(chat.Title) != "" {
		return chat.Title
	}
	return strings.TrimSpace(strings.Join([]string{chat.FirstName, chat.LastName}, " "))
}

// telegramReplier — the Replier of §6 over sendMessage (§10 День 4).
//
// The 429 branch is the whole point of the file: the library answers TooManyRequestsError with
// RetryAfter, and §10 День 4 says «спать ровно RetryAfter секунд, записать events(kind='flood'), не
// слать следующее сообщение в этот чат». Sleeping INSIDE Send is what keeps the promise: the caller
// holds the per-chat lock of §10 День 4, so the next message of the same chat waits as well. The sleep
// respects the context, which is the reply budget of §7 (TG_REPLY_DEADLINE_SECONDS) — a retry that
// would outlive the user's patience is abandoned instead (§12 UX: «сообщение не зависает»).
type telegramReplier struct {
	d      *Dispatcher
	bot    *tgbot.Bot
	cfg    config.Config
	logger *slog.Logger
	// users maps a chat to the user of its last incoming message, because §5 gives the events table a
	// user_id and no chat_id; the flood event of §10 День 4 is attributed through it.
	mu    sync.Mutex
	users map[int64]int64
}

// noteChatUser — remembered by telegramHandler before the dispatch, so the reply that comes later can
// attribute its flood event (§5: events has user_id, chats have the user separately).
func (r *telegramReplier) noteChatUser(chatID int64, userID int64) {
	if userID <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[chatID] = userID
}

// chatUser — the user of the last incoming message of the chat, 0 when the chat is unknown.
func (r *telegramReplier) chatUser(chatID int64) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.users[chatID]
}

// Send — one piece of text into one chat, with the parse mode of §7. The chunks were already cut by
// reply.go (§16 change 7: escape, then cut), so Send sends what it is given and does not re-split.
func (r *telegramReplier) Send(ctx context.Context, chatID int64, text string) error {
	return r.send(ctx, chatID, text, nil)
}

// SendMenu sends a short menu message with its inline keyboard through the same retry path as any other reply.
func (r *telegramReplier) SendMenu(ctx context.Context, chatID int64, text string, markup models.InlineKeyboardMarkup) error {
	return r.send(ctx, chatID, text, &markup)
}

func (r *telegramReplier) send(ctx context.Context, chatID int64, text string, markup *models.InlineKeyboardMarkup) error {
	if r.bot == nil {
		return errors.New("the telegram bot is not attached (§10 День 4: NewTelegram must run before the first reply)")
	}
	var attempt = 0
	for {
		params := &tgbot.SendMessageParams{
			ChatID:    chatID,
			Text:      text,
			ParseMode: parseModeOf(r.cfg.ParseMode),
		}
		if markup != nil {
			params.ReplyMarkup = *markup
		}
		var _, err = r.bot.SendMessage(ctx, params)
		if err == nil {
			return nil
		}
		var flood *tgbot.TooManyRequestsError
		if !errors.As(err, &flood) || flood.RetryAfter <= 0 || attempt >= 1 {
			// Anything else (403 the bot was blocked, 400 a bad parse mode) is the caller's problem:
			// dispatch.go turns it into the failure answer of §12 UX.
			return err
		}
		attempt++
		var sleep = time.Duration(flood.RetryAfter) * time.Second
		if sleep > FloodSleepMax {
			sleep = FloodSleepMax
		}
		r.d.logEvent(ctx, r.chatUser(chatID), store.EventFlood, fmt.Sprintf("retry_after %d s", flood.RetryAfter))
		r.logger.Warn("telegram answered 429, the chat waits", "chat_id", chatID, "retry_after", flood.RetryAfter)
		select {
		case <-ctx.Done():
			return ctx.Err() // the reply budget of §7 won (§12 UX: no frozen answer)
		case <-time.After(sleep):
		}
	}
}

// SendStreamMessage — the placeholder of §15: the one message that will be rewritten while the model writes.
// It goes WITHOUT the parse mode of §7 on purpose: §15 keeps a growing message plain text, and the mode is put
// back by the final rewrite of deliver() once the text is complete and escaped (the half-open MarkdownV2 entity
// of §12 UX is what a formatting placeholder mid-stream is). The id it returns is what every rewrite addresses.
func (r *telegramReplier) SendStreamMessage(ctx context.Context, chatID int64, text string) (int64, error) {
	if r.bot == nil {
		return 0, errors.New("the telegram bot is not attached (§10 День 4: NewTelegram must run before the first reply)")
	}
	var msg, err = r.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
	if err != nil {
		return 0, err
	}
	if msg == nil || msg.ID == 0 {
		// An answer with no id is a placeholder nothing can be rewritten into. §15 names that rather than
		// editing nothing and calling the answer streamed (§16 change 34: the log and the chat must not claim a
		// stream that never existed).
		return 0, errors.New("Telegram answered the placeholder of §15 with no message id")
	}
	return int64(msg.ID), nil
}

// EditStreamMessage — one rewrite of the placeholder. `parse` says whether the text is finished and escaped:
// true carries the PARSE_MODE of §7, false sends NO parse mode at all. The naming trap of §16 change 27 again:
// an empty models.ParseMode is the field this library omits from the request, and an absent parse_mode is what
// Telegram reads as «render this as text» — which is exactly what a half-written answer needs.
//
// There is no 429 loop here the way Send has one. §7 STREAM_EDIT_INTERVAL_MS is the flood protection of §15 and
// a refused rewrite is dropped by the session rather than waited out: the GPU slot of §12 must not be held by a
// chat Telegram is throttling, and the full answer is still delivered at the end of the stream.
func (r *telegramReplier) EditStreamMessage(ctx context.Context, chatID int64, messageID int64, text string, parse bool) error {
	if r.bot == nil {
		return errors.New("the telegram bot is not attached (§10 День 4: NewTelegram must run before the first reply)")
	}
	var params = &tgbot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: int(messageID),
		Text:      text,
	}
	if parse {
		params.ParseMode = parseModeOf(r.cfg.ParseMode)
	}
	var _, err = r.bot.EditMessageText(ctx, params)
	return err
}

// parseModeOf — §7 PARSE_MODE «MarkdownV2 | HTML | plain» to the library constants. The naming trap of
// v1.27.0 (§16 change 27): ParseModeMarkdown carries the value "MarkdownV2", so the switch is written
// against the §7 names and not against the names of the library.
func parseModeOf(mode string) models.ParseMode {
	switch mode {
	case ParseMarkdownV2:
		return models.ParseModeMarkdown // the value is "MarkdownV2"
	case ParseHTML:
		return models.ParseModeHTML
	default: // plain: Telegram renders the text as it is (§7)
		return ""
	}
}

// noteChatUser — forward the chat→user pairing to the Replier when it wants it (§5 attributes events
// with user_id). A fake Replier of §11 does not, so the assertion is a no-op there (§6 v1).
func (d *Dispatcher) noteChatUser(chatID int64, userID int64) {
	var notifier, ok = d.deps.Replier.(interface {
		noteChatUser(chatID int64, userID int64)
	})
	if !ok {
		return
	}
	notifier.noteChatUser(chatID, userID)
}

// PublishCommands — «setMyCommands при старте» of §10 День 4: the menu Telegram shows when the user
// types «/». The registry of §6 is the only source, so a v2 command appears in the menu with no edit
// here (§6 «Реестр команд — слайс»). AlwaysAllowed commands are included: §16 change 13 lets /start and
// /whoami answer even a closed bot, and hiding them from the menu would tell the user the bot is dead.
func (d *Dispatcher) PublishCommands(ctx context.Context, tg *tgbot.Bot) error {
	if tg == nil {
		return errors.New("PublishCommands needs the bot of NewTelegram (§10 День 4)")
	}
	var cmds = make([]models.BotCommand, 0, len(d.commands))
	var i = 0
	for i < len(d.commands) {
		var cmd = d.commands[i]
		// The names of setMyCommands must be lowercase without arguments (§6 Normalize lowercases the
		// same way, so the menu and the dispatcher cannot disagree).
		cmds = append(cmds, models.BotCommand{Command: strings.ToLower(cmd.Name), Description: cmd.Description})
		i++
	}
	if _, err := tg.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{Commands: cmds}); err != nil {
		// §12 security: same reason as in NewTelegram — the URL of the library carries the token.
		return fmt.Errorf("setMyCommands: %w", errors.New(Sanitize(err, d.deps.Token)))
	}
	return nil
}

// Run — the wiring of §4 «cmd/bot/main.go: конфиг → store → model client → bot → healthz → цикл».
// The offset of §5 is read from bot_state when the caller did not seed Deps, the dispatcher is built,
// the library bot is attached, the command menu is published and Start blocks until the context is
// cancelled — the library has no Run of its own (§10 День 4 «Run у библиотеки нет»).
//
// A failing setMyCommands does not stop the bot: the menu is cosmetics, the commands of §6 still work
// when they are typed (§12 reliability — one cosmetic failure must not cost the user their answers).
func Run(ctx context.Context, deps Deps, opts Options) (*Dispatcher, error) {
	if deps.StartOffset == 0 && deps.DB != nil {
		var offset, found, err = store.LastUpdateID(ctx, deps.DB)
		if err != nil {
			return nil, fmt.Errorf("bot_state last_update_id: %w", err)
		}
		if found {
			deps.StartOffset = offset // §5: a restart continues after the last processed update
		}
	}
	var d = New(deps)
	var tg, err = d.NewTelegram(opts)
	if err != nil {
		return nil, err
	}
	var menuCtx, cancel = context.WithTimeout(ctx, PublishCommandsTimeout)
	defer cancel()
	if err := d.PublishCommands(menuCtx, tg); err != nil {
		d.deps.Logger.Warn("the command menu was not published, the commands still work", "error", err)
	}
	if deps.Cfg.WebhookEnabled {
		server, err := NewWebhookServer(tg, deps.Cfg.WebhookAddress(), deps.Cfg.WebhookPath)
		if err != nil {
			return nil, err
		}
		defer server.Close()
		if err := d.ConfigureWebhook(ctx, tg); err != nil {
			return nil, err
		}
		if err := server.Run(ctx); err != nil {
			return nil, err
		}
		return d, nil
	}
	tg.Start(ctx) // blocks: one getUpdates poller and `workers` handler goroutines (§10 День 4)
	return d, nil
}
