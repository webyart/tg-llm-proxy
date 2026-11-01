// Package bot — the dispatcher of §4 (`internal/bot`: «long polling, диспетчер, command registry,
// middleware») of §10 День 4.
//
// The package is deliberately independent of `github.com/go-telegram/bot`: Dispatch works on the
// Event below and answers through the Replier interface. Two consequences (§11): the §6 scenarios
// are unit-testable without a Telegram API and without a token, and run.go — the only file that
// speaks the Bot API — stays a thin adapter. reply.go borrows one helper from the library
// (EscapeMarkdown, the special-character set of MarkdownV2) and nothing else (§16 change 27).
//
// The registry of §6 is a slice, so a v2 command is one entry and no edit of the dispatcher
// ("Реестр команд — слайс []Command{Name, Handler, NeedsArgs, Description}"). The library matchers
// are NOT used for commands: MatchTypeCommand compares the pattern against
// `data[entity.Offset+1 : entity.Offset+entity.Length]`, and a Telegram bot_command entity never
// carries the `@BotName` suffix, so `/ask@MyBot` would not match (§6 «Как это регистрируется»,
// §16 change 27 records the second measured fact: models.MessageTypeText does not exist in
// v1.27.0, Message.Type is MaybeInaccessibleMessageType — the matcher tests the text instead).
package bot

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"time"

	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/ratelimit"
	"bot/internal/store"
)

// Event — one incoming update reduced to what §6 needs. The reduction happens in run.go, which is
// why edited_message can be dropped here (§6: an edit must not reach /ask — telegram_message_id is
// the same, so neither the UNIQUE guard of §5 nor the idempotency of §10 День 6 would stop a second
// model call).
type Event struct {
	UpdateID          int64
	TelegramMessageID int64
	ChatID            int64
	ChatType          string
	ChatTitle         string
	UserID            int64
	Username          string
	Text              string
	// Question — the text §5 writes as the user row of the conversation: the argument of /ask for a
	// command, the whole message for free text (§6 «обычный текст = то же, что /ask»). The command
	// prefix itself stays out of the row: it is a transport detail of §6, and a context of §5 full of
	// "/ask" prefixes makes the model see the shell instead of the conversation. Dispatch fills it
	// after Normalize and resolve, so the `stored` middleware does not need the argument of the
	// handler.
	Question string
}

// Replier — the outgoing side of §6: a finished piece of text into one chat. The fake of §11 records
// the pieces, the real implementation of run.go adds the parse mode, the chunks and the 429 sleep.
type Replier interface {
	Send(ctx context.Context, chatID int64, text string) error
}

// OffsetWriter — the bot_state write of §5 «Offset long polling» (§10 День 4: last_update_id is
// stored after the update is processed so a restart does not answer it twice).
type OffsetWriter func(ctx context.Context, updateID int64) error

// Command — one registry entry of §6. NeedsArgs and Description feed /help and setMyCommands;
// AlwaysAllowed is §16 change 13 (/start and /whoami answer even a closed bot); UsesModel marks the
// paths that touch STRATA, which is what the rate limit and the message rows of §5 are for.
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

// Middleware — the shape the library uses too, kept here so the chain is testable on its own (§4
// dispatch.go: "per-chat lock, rate limit, panic recovery").
type Middleware func(next HandlerFunc) HandlerFunc

// Deps — everything §10 День 4 wires together: config, store, model client, replier, limits, log.
type Deps struct {
	Cfg     config.Config
	DB      *sql.DB
	Model   model.Client
	Replier Replier
	Locks   *ratelimit.Locks
	Limit   *ratelimit.Limiter
	Logger  *slog.Logger
	Offset  OffsetWriter
	// StartOffset — bot_state last_update_id read at startup (§5 «Offset long polling»), so the
	// monotonic guard of persistOffset continues the previous run instead of starting at 0.
	StartOffset int64
	BotName     string
	BuildCommit string
	// Token is only the redaction key of Sanitize (§12 security): it is never logged, never sent to
	// the model and never written to SQLite — it is here so the errors of the library, which carry
	// `https://api.telegram.org/bot<TOKEN>/<method>`, cannot leak it into slog.
	Token string
}

// Dispatcher — the registry plus the middleware chain of §10 День 4.
type Dispatcher struct {
	deps        Deps
	commands    []Command
	index       map[string]Command
	created     time.Time
	startOffset int64
	offsetMu    sync.Mutex
}

// New — the dispatcher of §6 with the defaults §12 needs: a per-chat lock set, a limiter of §7 and a
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
	var d = &Dispatcher{deps: deps, index: map[string]Command{}, created: time.Now(), startOffset: deps.StartOffset}
	d.commands = d.registry()
	var i = 0
	for i < len(d.commands) {
		d.index[d.commands[i].Name] = d.commands[i]
		i++
	}
	return d
}

// Registry — the §6 slice, also the source of the /help listing and of setMyCommands (§10 День 4:
// "setMyCommands при старте"). Commands with AlwaysAllowed are listed last so a closed bot still
// shows them (§16 change 13).

// Normalize — the three rules of §6 «Нормализация перед диспатчем». Telegram sends `/ask@MyBot` on
// autocomplete and in groups, and the bot_command entity does not carry the suffix, which is exactly
// why the registry is matched here and not by the library (§6, §16 change 12):
//
//	"/Ask@MyBot hi"  → ("ask", "hi", true)   — п.1 the suffix, п.2 the case
//	"/ask /help"     → ("ask", "/help", true) — п.3 the whole rest is the argument
//	"hello"          → ("", "hello", false)   — the free-text path of §6
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
		// §11 create no bot) must not turn a real command into an unknown one. A suffix naming
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

// resolve — the §6 table plus §6 «Обработка прочего текста»: an unknown command and a plain message
// both become /ask when PROXY_FREE_TEXT=true, otherwise they get the /help hint. §6 keeps the whole
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

// chain — §10 День 4 «middleware: allowlist, rate limit, per-chat lock, panic recovery». The
// recovery is OUTERMOST because it must also catch a panic raised by the store call of the dedup
// step. The dedup insert of §2 п.7 sits INSIDE the per-chat lock: a user row that is written while
// the previous answer of the same chat is still with the model would land in SQLite after that
// answer (the AUTOINCREMENT id of §5 is the order of the context), and the glued request of §5 would
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

// Dispatch — one update through the chain. The offset of §5 is written even when the handler failed:
// an update that was seen but answered badly must not come back after a restart (§5 «Offset long
// polling» — a redelivery would answer the user twice, which is the failure §13 lists as a risk).
func (d *Dispatcher) Dispatch(ctx context.Context, ev Event) error {
	var name, arg, isCommand = Normalize(ev.Text, d.deps.BotName)
	var cmd, commandArg = d.resolve(ev, name, arg, isCommand)
	// §5: only the paths that reach STRATA produce a user row of the conversation, and that row holds
	// the question (§6 п.3 keeps the whole text after the command as the argument).
	ev.Question = ""
	if cmd.UsesModel {
		ev.Question = strings.TrimSpace(commandArg)
	}

	// §5 events («service: commands, errors, limits»): the audit row is written before the handler, so
	// a command that fails midway is still counted (§12 надёжность must not hide what happened).
	d.logEvent(ctx, ev.UserID, store.EventCommand, cmd.Name)

	var err = d.chain(func(ctx context.Context, ev Event, cmd Command) error {
		return cmd.Handler(ctx, ev, commandArg)
	})(ctx, ev, cmd)

	d.persistOffset(ctx, ev.UpdateID)

	// §12 security: the log never carries the raw error of the library, only the class of it.
	if err != nil {
		d.deps.Logger.Error("dispatch failed", "chat_id", ev.ChatID, "command", cmd.Name,
			"error", Sanitize(err, d.deps.Token))
	}
	return err
}

// persistOffset — the bot_state write of §5, MONOTONIC. The handlers of §10 День 4 run in several
// worker goroutines (bot.WithWorkerPool), so "write whatever update ID arrived last" could downgrade
// the offset, and a downgrade makes Telegram hand the same update back after a restart (§13 риск
// «повторные ответы», §5 «Offset long polling»). StartOffset comes from store.LastUpdateID at the
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

// reply — an answer of §6 through the Replier, i.e. escaped and chunked by reply.go (§12 UX).
func (d *Dispatcher) reply(ctx context.Context, chatID int64, text string) error {
	return d.Send(ctx, chatID, text)
}

// logEvent — the events table of §5 («service: commands, errors, limits»). A failed audit row must
// not cost the user their answer, so the error only goes to the log (§12 reliability).
func (d *Dispatcher) logEvent(ctx context.Context, userID int64, kind string, detail string) {
	if d.deps.DB == nil {
		return
	}
	if err := store.LogEvent(ctx, d.deps.DB, userID, kind, detail); err != nil {
		d.deps.Logger.Error("the event was not stored", "kind", kind, "error", Sanitize(err, d.deps.Token))
	}
}

// Sanitize — §12 security: the library builds `https://api.telegram.org/bot<TOKEN>/<method>` and its
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
