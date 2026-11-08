// The package is deliberately independent of `github.com/go-telegram/bot`: Dispatch works on the
// are unit-testable without a Telegram API and without a token, and run.go — the only file that
// speaks the Bot API — stays a thin adapter. reply.go borrows one helper from the library
//
// ("Реестр команд — слайс []Command{Name, Handler, NeedsArgs, Description}"). The library matchers
// are NOT used for commands: MatchTypeCommand compares the pattern against
// `data[entity.Offset+1 : entity.Offset+entity.Length]`, and a Telegram bot_command entity never
// v1.27.0, Message.Type is MaybeInaccessibleMessageType — the matcher tests the text instead).
package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/ratelimit"
	"bot/internal/store"
)

// model call).
type Event struct {
	UpdateID              int64
	TelegramMessageID     int64
	ChatID                int64
	ChatType              string
	ChatTitle             string
	UserID                int64
	Username              string
	Text                  string
	Attachment            *Attachment
	AdditionalAttachments []Attachment
	MediaGroupID          string
	UnsupportedMedia      bool
	Recovery              bool
	MenuCallback          bool
	// "/ask" prefixes makes the model see the shell instead of the conversation. Dispatch fills it
	// after Normalize and resolve, so the `stored` middleware does not need the argument of the
	// handler.
	Question string
}

// the pieces, the real implementation of run.go adds the parse mode, the chunks and the 429 sleep.
type Replier interface {
	Send(ctx context.Context, chatID int64, text string) error
}

// stored after the update is processed so a restart does not answer it twice).
type OffsetWriter func(ctx context.Context, updateID int64) error

type MiniAppTicketIssuer interface {
	NewLaunchTicket(chatID int64, userID int64) (string, error)
}

type Command struct {
	Name          string
	Description   string
	NeedsArgs     bool
	AlwaysAllowed bool
	UsesModel     bool
	Handler       func(ctx context.Context, ev Event, arg string) error
}

// HandlerFunc — the tail of the middleware chain, one normalized command.
type HandlerFunc func(ctx context.Context, ev Event, cmd Command) error

// dispatch.go: "per-chat lock, rate limit, panic recovery").
type Middleware func(next HandlerFunc) HandlerFunc

type Deps struct {
	Cfg            config.Config
	DB             *sql.DB
	Model          model.Client
	TTS            model.SpeechSynthesizer
	Replier        Replier
	Attachments    AttachmentDownloader
	URLReader      *http.Client
	Locks          *ratelimit.Locks
	Limit          *ratelimit.Limiter
	Logger         *slog.Logger
	Offset         OffsetWriter
	MiniAppTickets MiniAppTicketIssuer
	// monotonic guard of persistOffset continues the previous run instead of starting at 0.
	StartOffset int64
	BotName     string
	BuildCommit string
	// the model and never written to SQLite — it is here so the errors of the library, which carry
	// `https://api.telegram.org/bot<TOKEN>/<method>`, cannot leak it into slog.
	Token string
}

const (
	updateQueueCapacity  = 32
	perChatQueueCapacity = 4
	// updateQueueTimeoutMin/Max bound the wait an update may spend on ITS OWN chat lock. The minimum is
	// the old fixed 2 s; the maximum keeps a stop from being indistinguishable from a hung process.
	updateQueueTimeoutMin = 2 * time.Second
	updateQueueTimeoutMax = 3 * time.Minute
	deniedReplyWindow     = 30 * time.Second
	bootstrapReplyWindow  = 5 * time.Second
	overloadReplyWindow   = 2 * time.Second
	coalescerMaxKeys      = 4096
	msgOverloaded         = "Бот перегружен: очередь заполнена. Попробуй ещё раз через несколько секунд."
	msgChatBusy           = "Предыдущий ответ в этом чате ещё не готов. Повтори запрос чуть позже."
)

// errExecutionSlotBusy — the chat lock was acquired but no global handler slot freed in time. It is a
// saturation of the whole bot, not of one chat, and it is named separately so the two cases can be
// answered with two different texts.
var errExecutionSlotBusy = errors.New("bot: no global handler slot freed inside the wait budget")

// chatQueueTimeoutFor — how long an update of one chat waits for that chat's lock. The lock is held for
// the whole answer (STRATA_TIMEOUT_SECONDS plus delivery, up to STREAM_TOTAL_DEADLINE_SECONDS), so the
// old fixed 2 s expired on every second message of a chat that was still answering: the update was then
// reported as an overload and acknowledged, which made Telegram drop it for good. The wait is now the
// reply budget of the chat itself.
func chatQueueTimeoutFor(replyDeadlineSeconds int64) time.Duration {
	var budget = time.Duration(replyDeadlineSeconds) * time.Second
	if budget < updateQueueTimeoutMin {
		return updateQueueTimeoutMin
	}
	if budget > updateQueueTimeoutMax {
		return updateQueueTimeoutMax
	}
	return budget
}

type chatLockHeldContextKey struct{}

type Dispatcher struct {
	deps                Deps
	commands            []Command
	index               map[string]Command
	created             time.Time
	startOffset         int64
	offsetMu            sync.Mutex
	albumMu             sync.Mutex
	albums              map[albumKey]*albumBatch
	admission           chan struct{}
	execution           chan struct{}
	deniedReplies       *ratelimit.Coalescer
	bootstrapReplies    *ratelimit.Coalescer
	overloadReplies     *ratelimit.Coalescer
	chatBusyReplies     *ratelimit.Coalescer
	chatQueueTimeout    time.Duration
	modelWork           *ratelimit.Limiter
	networkWork         *ratelimit.Limiter
	outboundTools       chan struct{}
	activeModelRequests atomic.Int64
	activeHandlers      atomic.Int64
	now                 func() time.Time
	taskWake            chan struct{}
	// The two sets of pending user decisions used to be package-level sync.Maps keyed by the *Dispatcher. A
	// global keyed by a pointer leaks one entry per dispatcher for the life of the process (every test creates
	// one), and the state it holds — approvals and their continuations — was invisible in the struct that owns
	// it. They are fields now; the Once values let the accessors stay lazy for a Dispatcher built as a literal.
	confirmations     *networkConfirmationSet
	confirmationsOnce sync.Once
	approvalScopes    *networkApprovalScopeSet
	approvalOnce      sync.Once
}

// logger. A missing model client or replier is a wiring mistake and stays visible as one.
func New(deps Deps) *Dispatcher {
	if deps.Locks == nil {
		deps.Locks = ratelimit.NewLocks()
	}
	if deps.Limit == nil {
		deps.Limit = ratelimit.New(int(deps.Cfg.RateLimitPerMinute))
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	var d = &Dispatcher{
		deps: deps, index: map[string]Command{}, created: time.Now(), startOffset: deps.StartOffset,
		albums:           map[albumKey]*albumBatch{},
		deniedReplies:    ratelimit.NewCoalescer(deniedReplyWindow, coalescerMaxKeys),
		bootstrapReplies: ratelimit.NewCoalescer(bootstrapReplyWindow, coalescerMaxKeys),
		overloadReplies:  ratelimit.NewCoalescer(overloadReplyWindow, coalescerMaxKeys),
		chatBusyReplies:  ratelimit.NewCoalescer(overloadReplyWindow, coalescerMaxKeys),
		chatQueueTimeout: chatQueueTimeoutFor(deps.Cfg.ReplyDeadlineSeconds),
		modelWork:        ratelimit.New(int(deps.Cfg.RateLimitPerMinute) * modelWorkPerQuestion),
		networkWork:      ratelimit.New(networkWorkPerMinute),
		outboundTools:    make(chan struct{}, globalOutboundToolLimit),
		now:              time.Now,
		taskWake:         make(chan struct{}, 1),
	}
	d.configureAdmission(int(deps.Cfg.MaxConcurrentRequests))
	// Warm both sets so the normal path never takes the Once: a nil set in the struct would still work through
	// the accessors, but building them here makes the ownership obvious.
	networkConfirmationsFor(d)
	networkApprovalScopesFor(d)
	d.commands = d.registry()
	var i = 0
	for i < len(d.commands) {
		d.index[d.commands[i].Name] = d.commands[i]
		i++
	}
	return d
}

func (d *Dispatcher) configureAdmission(workers int) {
	if workers <= 0 {
		workers = 1
	}
	d.execution = make(chan struct{}, workers)
	d.admission = make(chan struct{}, workers+updateQueueCapacity)
}

// "setMyCommands при старте"). Commands with AlwaysAllowed are listed last so a closed bot still

// autocomplete and in groups, and the bot_command entity does not carry the suffix, which is exactly
//
//	"/Ask@MyBot hi"  → ("ask", "hi", true)   — п.1 the suffix, п.2 the case
//	"/ask /help"     → ("ask", "/help", true) — п.3 the whole rest is the argument
func Normalize(text string, botName string) (name string, arg string, isCommand bool) {
	var trimmed = strings.TrimSpace(text)
	if trimmed == "" {
		return "", "", false
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", trimmed, false
	}
	var head = trimmed
	var rest = ""
	var space = strings.IndexAny(trimmed, " \t\n\r")
	if space >= 0 {
		head = trimmed[:space]
		rest = strings.TrimSpace(trimmed[space+1:])
	}
	var cmd = strings.ToLower(strings.TrimPrefix(head, "/"))
	if at := strings.Index(cmd, "@"); at >= 0 {
		var suffix = cmd[at+1:]
		// п.1. Telegram appends the suffix only for THIS bot, so an empty botName (the unit tests of
		// another bot is not a command for us at all.
		if botName != "" && suffix != "" && !strings.EqualFold(suffix, botName) {
			return "", trimmed, false
		}
		cmd = cmd[:at]
	}
	if cmd == "" {
		return "", trimmed, false
	}
	return cmd, rest, true
}

// text as the argument, so `/ask /help` asks the model about "/help" instead of running /help.
func (d *Dispatcher) resolve(ev Event, name string, arg string, isCommand bool) (Command, string) {
	if isCommand {
		if cmd, ok := d.index[name]; ok {
			return cmd, arg
		}
		if d.deps.Cfg.FreeText {
			return d.index[CommandAsk], strings.TrimSpace(ev.Text)
		}
		return d.index[CommandHelp], ""
	}
	if d.deps.Cfg.FreeText {
		return d.index[CommandAsk], strings.TrimSpace(arg)
	}
	return d.index[CommandHelp], ""
}

// recovery is OUTERMOST because it must also catch a panic raised by the store call of the dedup
// the previous answer of the same chat is still with the model would land in SQLite after that
// then end with an assistant turn instead of with the question the user just asked.
func (d *Dispatcher) chain(next HandlerFunc) HandlerFunc {
	var wrappers = []Middleware{d.recovered, d.allowed, d.limited, d.locked, d.stored}
	var wrapped = next
	var i = len(wrappers) - 1
	for i >= 0 {
		wrapped = wrappers[i](wrapped)
		i--
	}
	return wrapped
}

func (d *Dispatcher) commandFor(ev Event) (Command, string) {
	var name, arg, isCommand = Normalize(ev.Text, d.deps.BotName)
	var cmd, commandArg = d.resolve(ev, name, arg, isCommand)
	if ev.Attachment != nil || ev.UnsupportedMedia {
		cmd = d.index[CommandAsk]
		commandArg = strings.TrimSpace(ev.Text)
		if ev.Attachment != nil && ev.Attachment.Kind == AttachmentDocument && isCommand && name == CommandDocs && strings.HasPrefix(arg, "add") {
			cmd = d.index[CommandDocs]
			commandArg = arg
		}
	}
	if isCommand && name == CommandLinks && isLinkSummaryAction(commandArg) {
		cmd.UsesModel = true
	}
	if isCommand && name == CommandTask {
		cmd.UsesModel = isNewTaskGoal(commandArg)
	}
	if !isCommand && ev.Attachment == nil && !ev.UnsupportedMedia && !ev.Recovery {
		if _, ok := bareHTTPSURL(ev.Text); ok {
			cmd = d.index[CommandLinks]
			commandArg = "offer"
		}
	}
	return cmd, commandArg
}

func isNewTaskGoal(arg string) bool {
	action := strings.TrimSpace(arg)
	switch action {
	case "", "menu", "menu:cancel", "menu:resume", "menu:clear", "clear", "clear confirm", "menu:new":
		return false
	}
	if strings.EqualFold(action, "cancel") {
		return false
	}
	fields := strings.Fields(action)
	return len(fields) == 0 || !strings.EqualFold(fields[0], "resume")
}

func (d *Dispatcher) Dispatch(ctx context.Context, ev Event) error {
	cmd, _ := d.commandFor(ev)
	if !cmd.AlwaysAllowed && !d.deps.Cfg.Allows(ev.UserID) {
		return d.dispatchDenied(ctx, ev, cmd)
	}
	if cmd.AlwaysAllowed && !d.bootstrapReplies.Allow(ratelimit.CoalesceKey{UserID: ev.UserID, ChatID: ev.ChatID, Action: cmd.Name}) {
		d.persistOffset(ctx, ev.UpdateID)
		return nil
	}
	if isPhotoAlbum(ev) {
		d.collectAlbum(ctx, ev)
		return nil
	}
	return d.runBounded(ctx, ev, true)
}

func (d *Dispatcher) dispatchDenied(ctx context.Context, ev Event, cmd Command) error {
	var err error
	if d.deniedReplies.Allow(ratelimit.CoalesceKey{UserID: ev.UserID, ChatID: ev.ChatID}) {
		d.logEvent(ctx, ev.UserID, store.EventDenied, cmd.Name)
		if d.deps.Cfg.DenyByDefault {
			err = d.reply(ctx, ev.ChatID, refuseClosed(ev.UserID))
		} else {
			err = d.reply(ctx, ev.ChatID, refuseNotListed(ev.UserID))
		}
	}
	d.persistOffset(ctx, ev.UpdateID)
	return err
}

func (d *Dispatcher) runBounded(ctx context.Context, ev Event, acknowledge bool) error {
	select {
	case d.admission <- struct{}{}:
		defer func() { <-d.admission }()
	default:
		return d.dispatchOverloaded(ctx, ev)
	}
	queueCtx, cancel := context.WithTimeout(ctx, d.chatQueueTimeout)
	defer cancel()
	// `inChat` separates "never got the chat lock" (a busy chat) from an error the handler itself
	// returned after the deadline. Only the first one may be answered as a busy chat.
	var inChat bool
	err := d.deps.Locks.WithChatBounded(queueCtx, ev.ChatID, perChatQueueCapacity, func() error {
		inChat = true
		select {
		case d.execution <- struct{}{}:
			defer func() { <-d.execution }()
		case <-queueCtx.Done():
			return fmt.Errorf("%w: %w", errExecutionSlotBusy, queueCtx.Err())
		}
		d.activeHandlers.Add(1)
		defer d.activeHandlers.Add(-1)
		return d.dispatchEvent(context.WithValue(ctx, chatLockHeldContextKey{}, true), ev, acknowledge)
	})
	if ctx.Err() != nil {
		return err
	}
	switch {
	case errors.Is(err, ratelimit.ErrChatQueueFull), errors.Is(err, errExecutionSlotBusy):
		return d.dispatchOverloaded(ctx, ev)
	case errors.Is(err, context.DeadlineExceeded) && !inChat:
		return d.dispatchChatBusy(ctx, ev)
	}
	return err
}

// dispatchChatBusy — the chat was still answering when the wait budget of this update expired.
//
// The update is deliberately NOT acknowledged: bot_state keeps the previous offset, so a restart
// replays it instead of the message being lost for good, and the text names the real reason — the chat
// is busy with its own previous answer, the queue is not full.
func (d *Dispatcher) dispatchChatBusy(ctx context.Context, ev Event) error {
	var err error
	if d.chatBusyReplies.Allow(ratelimit.CoalesceKey{ChatID: ev.ChatID}) {
		d.deps.Logger.Warn("the chat is still answering the previous update",
			"chat_id", ev.ChatID, "update_id", ev.UpdateID, "waited", d.chatQueueTimeout)
		err = d.reply(ctx, ev.ChatID, msgChatBusy)
	}
	return err
}

func (d *Dispatcher) dispatchOverloaded(ctx context.Context, ev Event) error {
	var err error
	if d.overloadReplies.Allow(ratelimit.CoalesceKey{ChatID: ev.ChatID}) {
		d.deps.Logger.Warn("the update queue is full", "chat_id", ev.ChatID, "update_id", ev.UpdateID)
		err = d.reply(ctx, ev.ChatID, msgOverloaded)
	}
	d.persistOffset(ctx, ev.UpdateID)
	return err
}

func (d *Dispatcher) dispatchEvent(ctx context.Context, ev Event, acknowledge bool) error {
	cmd, commandArg := d.commandFor(ev)
	ev.Question = ""
	if cmd.UsesModel {
		ev.Question = strings.TrimSpace(commandArg)
		if ev.Attachment != nil {
			ev.Question = storedAttachmentQuestion(ev)
		}
	}

	d.logEvent(ctx, ev.UserID, store.EventCommand, cmd.Name)

	var err = d.chain(func(ctx context.Context, ev Event, cmd Command) error {
		return cmd.Handler(ctx, ev, commandArg)
	})(ctx, ev, cmd)

	if acknowledge {
		d.persistOffset(ctx, ev.UpdateID)
	}

	if err != nil {
		d.deps.Logger.Error("dispatch failed", "chat_id", ev.ChatID, "command", cmd.Name,
			"error", Sanitize(err, d.deps.Token))
	}
	return err
}

// worker goroutines (bot.WithWorkerPool), so "write whatever update ID arrived last" could downgrade
// start of the process, so a restart continues where the previous run stopped.
func (d *Dispatcher) persistOffset(ctx context.Context, updateID int64) {
	if d.deps.Offset == nil || updateID <= 0 {
		return
	}
	d.offsetMu.Lock()
	defer d.offsetMu.Unlock()
	if updateID <= d.startOffset {
		return
	}
	if err := d.deps.Offset(ctx, updateID); err != nil {
		d.deps.Logger.Error("bot_state last_update_id was not stored", "update_id", updateID,
			"error", Sanitize(err, d.deps.Token))
		return
	}
	d.startOffset = updateID
}

func (d *Dispatcher) reply(ctx context.Context, chatID int64, text string) error {
	return d.Send(ctx, chatID, text)
}

func (d *Dispatcher) logEvent(ctx context.Context, userID int64, kind string, detail string) {
	if d.deps.DB == nil {
		return
	}
	if err := store.LogEvent(ctx, d.deps.DB, userID, kind, detail); err != nil {
		d.deps.Logger.Error("the event was not stored", "kind", kind, "error", Sanitize(err, d.deps.Token))
	}
}

// default errorsHandler prints the error text, so any error that travelled through the library may
// carry the token. The token is replaced before anything reaches slog.
func Sanitize(err error, token string) string {
	if err == nil {
		return ""
	}
	var text = err.Error()
	var secret = strings.TrimSpace(token)
	if secret != "" {
		text = strings.ReplaceAll(text, secret, "***")
	}
	return text
}

func (d *Dispatcher) Registry() []Command { return d.commands }
