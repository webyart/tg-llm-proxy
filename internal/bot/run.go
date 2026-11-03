// WithInitialOffset из bot_state, WithHTTPClient(pollTimeout) и workers; обработка 429
// TooManyRequestsError; middleware allowlist; middleware panic-recovery; middleware rate limit;
// setMyCommands при старте».
//
// reply.go, which speak the Event/Replier vocabulary. This file only translates: models.Update →
//
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
	// library sends `timeout = pollTimeout - 1s` to getUpdates so the answer arrives before the client
	DefaultPollTimeout = 30 * time.Second
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
	PublishCommandsTimeout = 5 * time.Second
)

type Options struct {
	ServerURL string
	// PollTimeout — the first argument of WithHTTPClient. Zero → DefaultPollTimeout.
	PollTimeout time.Duration
	Workers     int
	// SkipGetMe — WithSkipGetMe, for a fake that does not serve getMe. NewTelegram needs the username
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

//	                                       entity hides the `@BotName` suffix, so MatchTypeCommand
//	                                       would never match `/ask@MyBot`)
//	WithDefaultHandler       — every other update (free text, unknown command, non-text) arrives at
//
// NewTelegram replaces deps.Replier with the sendMessage implementation, because the Replier needs the
// bot and the bot is built here. Both replacements happen before Start, so the worker goroutines of
func (d *Dispatcher) NewTelegram(opts Options) (*tgbot.Bot, error) {
	var token = strings.TrimSpace(d.deps.Cfg.BotToken)
	if token == "" {
		return nil, errors.New("TELEGRAM_BOT_TOKEN is empty : there is nothing to poll with")
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
		// Accept messages and menu callbacks; eventOf handles text, attachments and unsupported media.
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{"message", "callback_query"}),
		tgbot.WithDefaultHandler(d.telegramHandler),
		// `https://api.telegram.org/bot<TOKEN>/…`, so every text goes through Sanitize first. No
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
		setting = append(setting, tgbot.WithInitialOffset(d.startOffset))
	}

	var tg, err = tgbot.New(token, setting...)
	if err != nil {
		// leaves this function — and what main.go prints — is the sanitized text, wrapped as an error
		// so the caller still sees a cause.
		return nil, fmt.Errorf("bot.New: %w", errors.New(Sanitize(err, token)))
	}
	var replier = &telegramReplier{d: d, bot: tg, cfg: d.deps.Cfg, users: map[int64]int64{}, logger: d.deps.Logger}
	d.deps.Replier = replier
	d.deps.Attachments = &telegramAttachmentDownloader{bot: tg, client: client}
	tg.RegisterHandlerMatchFunc(d.matchUpdate, d.telegramHandler)

	if !opts.SkipGetMe {
		// username. New has already called getMe (WithSkipGetMe off), so this costs one cheap request,
		// and it runs before Start — there is no concurrent reader of deps yet.
		var ctx, cancel = context.WithTimeout(context.Background(), InitTimeout)
		defer cancel()
		var me, meErr = tg.GetMe(ctx)
		if meErr != nil {
			d.deps.Logger.Warn("getMe did not answer, so the @BotName suffix of will not be stripped",
				"error", Sanitize(meErr, token))
		} else if strings.TrimSpace(me.Username) != "" {
			d.deps.BotName = strings.TrimSpace(me.Username)
		}
	}
	return tg, nil
}

func (d *Dispatcher) matchUpdate(upd *models.Update) bool {
	_, ok := eventOf(upd)
	return ok
}

// eventOf accepts text, captions, supported attachments and explicit unsupported-media notices.
func (d *Dispatcher) telegramHandler(ctx context.Context, tg *tgbot.Bot, upd *models.Update) {
	// at all», and until today the process answered it only by silence. One debug line per update, with the
	// start logging the conversations of the users).
	var updateID int64
	var textLen = 0
	if upd != nil {
		updateID = upd.ID
		if upd.Message != nil {
			textLen = len([]rune(upd.Message.Text + upd.Message.Caption))
		}
	}
	// Dispatch, so everything in this function that runs BEFORE Dispatch is unprotected — and by then the
	// library has already read the update, which means Telegram will not send it back: getUpdates deletes
	// Measured 2026-10-31 on this machine: the «carries no text» line below read upd.Message.Chat.ID before
	// eventOf had checked Message for nil, so the first callback_query (or my_chat_member, or edited_message,
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
		// see that updates arrive and are skipped.
		var chatID int64
		if upd != nil && upd.Message != nil {
			chatID = upd.Message.Chat.ID
		}
		d.deps.Logger.Info("the update has no supported content, skipped", "update_id", updateID, "chat_id", chatID)
		return
	}
	d.noteChatUser(ev.ChatID, ev.UserID) // the flood audit of attributes events to a user
	if err := d.Dispatch(ctx, ev); err != nil {
		return // Dispatch has already logged it, sanitized
	}
}

// callback_query) or with an empty text is not an Event: an edit would reuse the telegram_message_id of
func eventOf(upd *models.Update) (Event, bool) {
	if upd == nil || upd.Message == nil {
		return Event{}, false
	}
	message := upd.Message
	text := message.Text
	if text == "" {
		text = message.Caption
	}
	var ev = Event{
		UpdateID:          upd.ID,
		TelegramMessageID: int64(message.ID),
		ChatID:            message.Chat.ID,
		ChatType:          string(message.Chat.Type),
		ChatTitle:         chatTitle(message.Chat),
		Text:              text,
	}
	if message.From != nil {
		ev.UserID = message.From.ID
		ev.Username = message.From.Username
	}
	if len(message.Photo) > 0 {
		photo := message.Photo[0]
		for _, candidate := range message.Photo[1:] {
			if candidate.Width*candidate.Height > photo.Width*photo.Height {
				photo = candidate
			}
		}
		ev.Attachment = &Attachment{Kind: AttachmentPhoto, FileID: photo.FileID, Size: int64(photo.FileSize)}
		return ev, true
	}
	if message.Document != nil {
		ev.Attachment = &Attachment{Kind: AttachmentDocument, FileID: message.Document.FileID, FileName: message.Document.FileName, MimeType: message.Document.MimeType, Size: message.Document.FileSize}
		return ev, true
	}
	if strings.TrimSpace(text) != "" {
		return ev, true
	}
	if message.Voice != nil || message.Audio != nil || message.Video != nil || message.VideoNote != nil || message.Animation != nil || message.Sticker != nil {
		ev.UnsupportedMedia = true
		return ev, true
	}
	return Event{}, false
}

// the two names of a private chat Telegram does not title itself.
func chatTitle(chat models.Chat) string {
	if strings.TrimSpace(chat.Title) != "" {
		return chat.Title
	}
	return strings.TrimSpace(strings.Join([]string{chat.FirstName, chat.LastName}, " "))
}

// The 429 branch is the whole point of the file: the library answers TooManyRequestsError with
// слать следующее сообщение в этот чат». Sleeping INSIDE Send is what keeps the promise: the caller
type telegramReplier struct {
	d      *Dispatcher
	bot    *tgbot.Bot
	cfg    config.Config
	logger *slog.Logger
	mu     sync.Mutex
	users  map[int64]int64
}

// noteChatUser — remembered by telegramHandler before the dispatch, so the reply that comes later can
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

func (r *telegramReplier) Send(ctx context.Context, chatID int64, text string) error {
	return r.send(ctx, chatID, text, nil)
}

// SendMenu sends a short menu message with its inline keyboard through the same retry path as any other reply.
func (r *telegramReplier) SendMenu(ctx context.Context, chatID int64, text string, markup models.InlineKeyboardMarkup) error {
	return r.send(ctx, chatID, text, &markup)
}

func (r *telegramReplier) send(ctx context.Context, chatID int64, text string, markup *models.InlineKeyboardMarkup) error {
	if r.bot == nil {
		return errors.New("the telegram bot is not attached ")
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
		if isParseEntitiesError(err) && params.ParseMode != "" {
			params.Text = plainForParseFallback(text, r.cfg.ParseMode)
			params.ParseMode = ""
			var _, fallbackErr = r.bot.SendMessage(ctx, params)
			if fallbackErr == nil {
				r.logger.Warn("telegram rejected formatted text; delivered it as plain text", "chat_id", chatID)
				return nil
			}
			err = fallbackErr
		}
		var flood *tgbot.TooManyRequestsError
		if !errors.As(err, &flood) || flood.RetryAfter <= 0 || attempt >= 1 {
			// Anything else (403 the bot was blocked, 400 a bad parse mode) is the caller's problem:
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
			return ctx.Err() // the reply budget of won
		case <-time.After(sleep):
		}
	}
}

// back by the final rewrite of deliver() once the text is complete and escaped (the half-open MarkdownV2 entity
func (r *telegramReplier) SendStreamMessage(ctx context.Context, chatID int64, text string) (int64, error) {
	if r.bot == nil {
		return 0, errors.New("the telegram bot is not attached ")
	}
	var msg, err = r.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
	if err != nil {
		return 0, err
	}
	if msg == nil || msg.ID == 0 {
		// stream that never existed).
		return 0, errors.New("Telegram answered the placeholder of with no message id")
	}
	return int64(msg.ID), nil
}

// EditStreamMessage — one rewrite of the placeholder. `parse` says whether the text is finished and escaped:
// an empty models.ParseMode is the field this library omits from the request, and an absent parse_mode is what
// Telegram reads as «render this as text» — which is exactly what a half-written answer needs.
//
// chat Telegram is throttling, and the full answer is still delivered at the end of the stream.
func (r *telegramReplier) EditStreamMessage(ctx context.Context, chatID int64, messageID int64, text string, parse bool) error {
	if r.bot == nil {
		return errors.New("the telegram bot is not attached ")
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
	if isParseEntitiesError(err) && params.ParseMode != "" {
		params.Text = plainForParseFallback(text, r.cfg.ParseMode)
		params.ParseMode = ""
		var _, fallbackErr = r.bot.EditMessageText(ctx, params)
		if fallbackErr == nil {
			r.logger.Warn("telegram rejected the formatted stream finalization; edited it as plain text", "chat_id", chatID, "message_id", messageID)
			return nil
		}
		err = fallbackErr
	}
	return err
}

func parseModeOf(mode string) models.ParseMode {
	switch mode {
	case ParseMarkdownV2:
		return models.ParseModeMarkdown // the value is "MarkdownV2"
	case ParseHTML:
		return models.ParseModeHTML
	default: // plain: Telegram renders the text as it is
		return ""
	}
}

func (d *Dispatcher) noteChatUser(chatID int64, userID int64) {
	var notifier, ok = d.deps.Replier.(interface {
		noteChatUser(chatID int64, userID int64)
	})
	if !ok {
		return
	}
	notifier.noteChatUser(chatID, userID)
}

// /whoami answer even a closed bot, and hiding them from the menu would tell the user the bot is dead.
func (d *Dispatcher) PublishCommands(ctx context.Context, tg *tgbot.Bot) error {
	if tg == nil {
		return errors.New("PublishCommands needs the bot of NewTelegram ")
	}
	var cmds = make([]models.BotCommand, 0, len(d.commands))
	var i = 0
	for i < len(d.commands) {
		var cmd = d.commands[i]
		// same way, so the menu and the dispatcher cannot disagree).
		cmds = append(cmds, models.BotCommand{Command: strings.ToLower(cmd.Name), Description: cmd.Description})
		i++
	}
	if _, err := tg.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{Commands: cmds}); err != nil {
		return fmt.Errorf("setMyCommands: %w", errors.New(Sanitize(err, d.deps.Token)))
	}
	return nil
}

// the library bot is attached, the command menu is published and Start blocks until the context is
func Run(ctx context.Context, deps Deps, opts Options) (*Dispatcher, error) {
	if deps.StartOffset == 0 && deps.DB != nil {
		var offset, found, err = store.LastUpdateID(ctx, deps.DB)
		if err != nil {
			return nil, fmt.Errorf("bot_state last_update_id: %w", err)
		}
		if found {
			deps.StartOffset = offset //: a restart continues after the last processed update
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
	tg.Start(ctx) // blocks: one getUpdates poller and `workers` handler goroutines
	return d, nil
}
