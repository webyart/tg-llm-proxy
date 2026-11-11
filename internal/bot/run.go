// WithInitialOffset из bot_state, WithHTTPClient(pollTimeout) и workers; обработка 429
// TooManyRequestsError; middleware allowlist; middleware panic-recovery; middleware rate limit;
// setMyCommands и setChatMenuButton при старте».
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
	// PublishCommandsTimeout — command and Mini App menu publication is cosmetic, so it gets a short deadline.
	PublishCommandsTimeout = 5 * time.Second
	// telegramReplierUsersMax bounds the chat→user map used only to attribute a flood audit event. It must not
	// grow one entry per chat ever seen; the oldest chat is evicted once the ring is full.
	telegramReplierUsersMax = 4096
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

	d.configureAdmission(workers)
	var setting = []tgbot.Option{
		tgbot.WithHTTPClient(poll, client),
		tgbot.WithCheckInitTimeout(InitTimeout),
		tgbot.WithWorkers(workers + updateQueueCapacity),
		tgbot.WithNotAsyncHandlers(),
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
	mediaMaxBytes := d.deps.Cfg.MediaMaxBytes
	if mediaMaxBytes <= 0 {
		mediaMaxBytes = PhotoMaxBytes
	}
	speechMaxSeconds := int(d.deps.Cfg.SpeechMaxSeconds)
	if speechMaxSeconds <= 0 {
		speechMaxSeconds = VoiceMaxSeconds
	}
	d.deps.Attachments = &telegramAttachmentDownloader{bot: tg, client: client, mediaMaxBytes: mediaMaxBytes, speechMaxSeconds: speechMaxSeconds}
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
		} else {
			d.deps.BotID = me.ID
			if strings.TrimSpace(me.Username) != "" {
				d.deps.BotName = strings.TrimSpace(me.Username)
			}
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
			"update_id", updateID, "panic", d.recoveredPanicText(r))
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
	// The gate runs before pendingEvent: that function can rewrite the text of a group message into
	// `/ask …`, dropping the @mention the user wrote. Once accepted, the event carries GroupAccepted,
	// so Dispatch does not gate the rewritten command a second time.
	var accepted, gateErr = d.groupGate(ctx, &ev)
	if gateErr != nil {
		d.deps.Logger.Error("the group gate failed, the update is not dispatched",
			"chat_id", ev.ChatID, "update_id", ev.UpdateID, "error", Sanitize(gateErr, d.deps.Token))
		return
	}
	if !accepted {
		return
	}
	if pendingEv, consumed := d.pendingEvent(ev); consumed {
		ev = pendingEv
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
		MediaGroupID:      message.MediaGroupID,
	}
	if message.From != nil {
		ev.UserID = message.From.ID
		ev.Username = message.From.Username
	}
	if message.ReplyToMessage != nil {
		ev.ReplyToMessageID = int64(message.ReplyToMessage.ID)
		if message.ReplyToMessage.From != nil {
			ev.ReplyToUserID = message.ReplyToMessage.From.ID
			ev.ReplyToUsername = message.ReplyToMessage.From.Username
			ev.ReplyToIsBot = message.ReplyToMessage.From.IsBot
		}
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
	if message.Voice != nil {
		ev.Attachment = &Attachment{Kind: AttachmentVoice, FileID: message.Voice.FileID, FileName: "voice.ogg", MimeType: "audio/ogg", Size: message.Voice.FileSize, Duration: message.Voice.Duration}
		return ev, true
	}
	if message.Audio != nil {
		fileName := strings.TrimSpace(message.Audio.FileName)
		if fileName == "" {
			fileName = "audio.bin"
		}
		ev.Attachment = &Attachment{Kind: AttachmentAudio, FileID: message.Audio.FileID, FileName: fileName, MimeType: message.Audio.MimeType, Size: message.Audio.FileSize, Duration: message.Audio.Duration}
		return ev, true
	}
	if message.VideoNote != nil {
		ev.Attachment = &Attachment{Kind: AttachmentVideoNote, FileID: message.VideoNote.FileID, FileName: "video-note.mp4", MimeType: "video/mp4", Size: int64(message.VideoNote.FileSize), Duration: message.VideoNote.Duration}
		return ev, true
	}
	if strings.TrimSpace(text) != "" {
		return ev, true
	}
	if message.Video != nil || message.Animation != nil || message.Sticker != nil {
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
	// userRing is the eviction order of users: when the map is full the oldest chat is dropped. The map is
	// only read for the user attribution of a flood audit event, so dropping history is preferable to an
	// unbounded per-chat map in a process that can outlive thousands of chats.
	userRing []int64
	userNext int
}

// noteChatUser — remembered by telegramHandler before the dispatch, so the reply that comes later can
func (r *telegramReplier) noteChatUser(chatID int64, userID int64) {
	if userID <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.users == nil {
		r.users = make(map[int64]int64, telegramReplierUsersMax)
	}
	if r.userRing == nil {
		r.userRing = make([]int64, 0, telegramReplierUsersMax)
	}
	if _, known := r.users[chatID]; !known {
		if len(r.users) >= telegramReplierUsersMax {
			var evicted = r.userRing[r.userNext]
			delete(r.users, evicted)
			r.userRing[r.userNext] = chatID
			r.userNext = (r.userNext + 1) % telegramReplierUsersMax
		} else {
			r.userRing = append(r.userRing, chatID)
		}
	}
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

func (r *telegramReplier) SendPlain(ctx context.Context, chatID int64, text string) error {
	return r.sendPlain(ctx, chatID, text, nil)
}

func (r *telegramReplier) SendTyping(ctx context.Context, chatID int64) error {
	if r.bot == nil {
		return errors.New("the telegram bot is not attached ")
	}
	_, err := r.bot.SendChatAction(ctx, &tgbot.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping})
	return err
}

// SendMenu sends a short menu message with its inline keyboard through the same retry path as any other reply.
func (r *telegramReplier) SendMenu(ctx context.Context, chatID int64, text string, markup models.InlineKeyboardMarkup) error {
	return r.send(ctx, chatID, text, &markup)
}

func (r *telegramReplier) send(ctx context.Context, chatID int64, text string, markup *models.InlineKeyboardMarkup) error {
	return r.sendWithMode(ctx, chatID, text, markup, parseModeOf(r.cfg.ParseMode))
}

func (r *telegramReplier) sendPlain(ctx context.Context, chatID int64, text string, markup *models.InlineKeyboardMarkup) error {
	return r.sendWithMode(ctx, chatID, text, markup, "")
}

func (r *telegramReplier) sendWithMode(ctx context.Context, chatID int64, text string, markup *models.InlineKeyboardMarkup, parseMode models.ParseMode) error {
	_, err := r.sendWithModeID(ctx, chatID, text, markup, parseMode)
	return err
}

// sendWithModeID is the shared send path that also returns the message id. Live task progress needs that id
// once, at creation, so every later step can rewrite the same message.
func (r *telegramReplier) sendWithModeID(ctx context.Context, chatID int64, text string, markup *models.InlineKeyboardMarkup, parseMode models.ParseMode) (int64, error) {
	if r.bot == nil {
		return 0, errors.New("the telegram bot is not attached ")
	}
	var attempt = 0
	for {
		params := &tgbot.SendMessageParams{
			ChatID:    chatID,
			Text:      text,
			ParseMode: parseMode,
		}
		if markup != nil {
			params.ReplyMarkup = *markup
		}
		var msg, err = r.bot.SendMessage(ctx, params)
		if err == nil {
			if msg == nil || msg.ID == 0 {
				return 0, errors.New("telegram answered the send with no message id")
			}
			return int64(msg.ID), nil
		}
		if isParseEntitiesError(err) && params.ParseMode != "" {
			params.Text = plainForParseFallback(text, r.cfg.ParseMode)
			params.ParseMode = ""
			var fallbackMsg, fallbackErr = r.bot.SendMessage(ctx, params)
			if fallbackErr == nil {
				r.logger.Warn("telegram rejected formatted text; delivered it as plain text", "chat_id", chatID)
				if fallbackMsg == nil || fallbackMsg.ID == 0 {
					return 0, errors.New("telegram answered the send with no message id")
				}
				return int64(fallbackMsg.ID), nil
			}
			err = fallbackErr
		}
		var flood *tgbot.TooManyRequestsError
		if !errors.As(err, &flood) || flood.RetryAfter <= 0 || attempt >= 1 {
			// Anything else (403 the bot was blocked, 400 a bad parse mode) is the caller's problem:
			return 0, err
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
			return 0, ctx.Err() // the reply budget of won
		case <-time.After(sleep):
		}
	}
}

// SendTaskStatus sends the one message a live task keeps rewriting. It is the optional transport capability
// asserted by task_progress.go; a plain Replier simply never gets it and the old per-step replies remain.
func (r *telegramReplier) SendTaskStatus(ctx context.Context, chatID int64, text string) (int64, error) {
	return r.sendWithModeID(ctx, chatID, text, nil, parseModeOf(r.cfg.ParseMode))
}

// EditTaskStatus rewrites an existing task status message with the ordinary parse-mode fallback.
func (r *telegramReplier) EditTaskStatus(ctx context.Context, chatID int64, messageID int64, text string) error {
	return r.EditStreamMessage(ctx, chatID, messageID, text, true)
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

// SendDraft sends one interim update of a streamed answer as a Bot API draft message. Drafts are plain
// text on purpose: a half-written answer has no balanced markup, and the final message keeps the ordinary
// formatting of Send.
func (r *telegramReplier) SendDraft(ctx context.Context, chatID int64, draftID, text string) error {
	if r.bot == nil {
		return errors.New("the telegram bot is not attached ")
	}
	_, err := r.bot.SendMessageDraft(ctx, &tgbot.SendMessageDraftParams{
		ChatID:  chatID,
		DraftID: draftID,
		Text:    text,
	})
	return err
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
	if isNotModifiedError(err) {
		// The message already carries exactly this text: the rewrite did its job. Counting the harmless
		// 400 as a failure used to mute a perfectly healthy stream after three of them.
		return nil
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
	go d.RunReminderScheduler(ctx)
	go d.RunTaskScheduler(ctx)
	var menuCtx, cancel = context.WithTimeout(ctx, PublishCommandsTimeout)
	defer cancel()
	if err := d.PublishCommands(menuCtx, tg); err != nil {
		d.deps.Logger.Warn("the command menu was not published, the commands still work", "error", err)
	}
	if err := d.PublishMenuButton(menuCtx, tg); err != nil {
		d.deps.Logger.Warn("the Telegram menu button was not published", "error", err)
	}
	if deps.Cfg.WebhookEnabled {
		server, err := NewWebhookServer(tg, deps.Cfg.WebhookAddress(), deps.Cfg.WebhookPath, deps.Cfg.WebhookSecretToken)
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
	tg.Start(ctx) // blocks: one getUpdates poller and a bounded set of admission goroutines
	return d, nil
}
