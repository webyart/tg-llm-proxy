// middleware.go — the four guards of §10 День 4 («middleware: allowlist, rate limit, per-chat lock,
// panic recovery») plus the idempotency step of §2 п.7. Each one answers the user itself and returns
// nil: a refusal of §6/§7 is an ANSWER, not a failure, and the dispatch log of §12 stays for real
// errors only.
package bot

import (
	"context"
	"fmt"
	"time"

	"bot/internal/ratelimit"
	"bot/internal/store"
)

// msgPanic — §12 надёжность: «один сбой STRATA ≠ падение бота» and the same for our own bugs.
const msgPanic = "Бот ошибся на этом сообщении (panic перехвачен, §12 надёжность). Попробуй ещё раз."

// msgTooOften — §7 RATE_LIMIT_PER_MINUTE with the concrete wait (§12 UX: say what to do).
const msgTooOftenFormat = "Слишком часто: не больше %d запросов в минуту (§7 RATE_LIMIT_PER_MINUTE). Подожди %s и попробуй ещё раз."

// stored — the UNIQUE guard of §5 `uq_messages_chat_tg_id` used as the idempotency key of §2 п.7:
// the incoming message is written BEFORE the model call, so the second delivery of the same update
// after a restart (or a redelivery by Telegram) hits the constraint and answers nothing. Only the
// paths with UsesModel get a row — /help and /ping are not conversation content of §5.
func (d *Dispatcher) stored(next HandlerFunc) HandlerFunc {
	return func(ctx context.Context, ev Event, cmd Command) error {
		var question = ev.Question
		if !cmd.UsesModel || question == "" {
			// §6: `/ask` without an argument is answered with a hint, and a hint is not a question —
			// an empty user row would put noise into the context of §5.
			return next(ctx, ev, cmd)
		}
		var tgID = ev.TelegramMessageID
		var _, err = store.Append(ctx, d.deps.DB, store.Message{
			ChatID:            ev.ChatID,
			UserID:            ev.UserID,
			Role:              store.RoleUser,
			Content:           question,
			TelegramMessageID: messageIDOrNull(tgID),
		})
		if err != nil {
			if store.IsDuplicate(err) {
				// §13 risk «повторные ответы»: this update was already answered. Silence, not a
				// second model request, and the log says why (§10 День 4 DoD: restart repeats nothing).
				d.deps.Logger.Info("the update was already answered, skipped",
					"chat_id", ev.ChatID, "telegram_message_id", tgID)
				return nil
			}
			return err
		}
		return next(ctx, ev, cmd)
	}
}

// allowed — §7 deny-by-default with §16 change 13: /start and /whoami answer even a closed bot,
// otherwise a user could never read their own id to get into ALLOWED_USER_IDS. The refusal is
// audited as events(kind='denied') (§10 День 4 DoD).
func (d *Dispatcher) allowed(next HandlerFunc) HandlerFunc {
	return func(ctx context.Context, ev Event, cmd Command) error {
		if cmd.AlwaysAllowed || d.deps.Cfg.Allows(ev.UserID) {
			return next(ctx, ev, cmd)
		}
		d.logEvent(ctx, ev.UserID, store.EventDenied, cmd.Name)
		if d.deps.Cfg.DenyByDefault {
			return d.reply(ctx, ev.ChatID, refuseClosed(ev.UserID))
		}
		return d.reply(ctx, ev.ChatID, refuseNotListed(ev.UserID))
	}
}

// limited — RATE_LIMIT_PER_MINUTE of §7, applied only to the commands that reach the GPU
// (UsesModel): the limit of §12 exists to protect the model, not SQLite.
func (d *Dispatcher) limited(next HandlerFunc) HandlerFunc {
	return func(ctx context.Context, ev Event, cmd Command) error {
		if !cmd.UsesModel {
			return next(ctx, ev, cmd)
		}
		if d.deps.Limit.Allow(ev.UserID) {
			return next(ctx, ev, cmd)
		}
		var wait = d.deps.Limit.RetryAfter(ev.UserID).Round(time.Second)
		if wait <= 0 {
			wait = ratelimit.Window
		}
		d.logEvent(ctx, ev.UserID, store.EventLimit, cmd.Name)
		return d.reply(ctx, ev.ChatID, fmt.Sprintf(msgTooOftenFormat, d.deps.Cfg.RateLimitPerMinute, wait))
	}
}

// locked — §12 «per-chat lock гарантирует порядок ответов в одном чате». The wait for the lock is
// cancellable (ratelimit.WithChat), so an expired reply budget frees the queue instead of parking
// a goroutine (§12.1.4).
func (d *Dispatcher) locked(next HandlerFunc) HandlerFunc {
	return func(ctx context.Context, ev Event, cmd Command) error {
		return d.deps.Locks.WithChat(ctx, ev.ChatID, func() error {
			return next(ctx, ev, cmd)
		})
	}
}

// recovered — §12 надёжность «panic recovery в обработчике». Outermost of the chain (§10 День 4):
// a panic in the store write or in a command handler must not take the process down, and the user
// gets an answer instead of silence.
func (d *Dispatcher) recovered(next HandlerFunc) HandlerFunc {
	return func(ctx context.Context, ev Event, cmd Command) (err error) {
		defer func() {
			var r = recover()
			if r == nil {
				return
			}
			err = fmt.Errorf("panic in the handler of /%s: %v", cmd.Name, r)
			d.deps.Logger.Error("recovered a panic", "chat_id", ev.ChatID, "command", cmd.Name,
				"panic", fmt.Sprint(r))
			d.logEvent(ctx, ev.UserID, store.EventCommand, "panic:"+cmd.Name)
			if sendErr := d.reply(ctx, ev.ChatID, msgPanic); sendErr != nil {
				d.deps.Logger.Error("the panic notice was not delivered", "chat_id", ev.ChatID,
					"error", Sanitize(sendErr, d.deps.Token))
			}
		}()
		return next(ctx, ev, cmd)
	}
}

// messageIDOrNull — §5: telegram_message_id is NULL for model answers, and 0 is not a Telegram id.
func messageIDOrNull(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}
