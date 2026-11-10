// errors only.
package bot

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"bot/internal/i18n"
	"bot/internal/ratelimit"
	"bot/internal/store"
)

const msgPanic = "Бот не смог обработать это сообщение. Попробуй ещё раз."
const recoveredPanicMaxBytes = 1024

const msgTooOftenFormat = "Слишком часто: не больше %d запросов в минуту. Подожди %s и попробуй ещё раз."

// panicNotice and tooOftenNotice are the language-aware forms of msgPanic and msgTooOftenFormat. The
// constants keep their historical Russian wording for callers and tests without a chat language.
func panicNotice(lang string) string {
	return i18n.T(lang, "middleware.panic")
}

func tooOftenNotice(lang string, limit int64, wait time.Duration) string {
	return i18n.T(lang, "middleware.too_often", limit, wait)
}

// message is written BEFORE the model call, so the second delivery of the same update after a restart (or a
// redelivery by Telegram) hits the constraint. Only the paths with UsesModel get a row — /help and /ping are not
// comparing the row of this update with the newest row of the chat, see the comment inside.
func (d *Dispatcher) stored(next HandlerFunc) HandlerFunc {
	return func(ctx context.Context, ev Event, cmd Command) error {
		var question = ev.Question
		if !cmd.UsesModel || question == "" {
			return next(ctx, ev, cmd)
		}
		var tgID = ev.TelegramMessageID
		if ev.MenuCallback {
			// A menu message id is reused by EVERY action of that menu. Writing it into
			// messages.telegram_message_id puts the second model action of the same menu into the
			// UNIQUE(chat_id, telegram_message_id) conflict below, where it is read as "this update
			// was already answered" and dropped without an answer or an error. A callback carries no
			// message of its own, so it gets no message identity — only real messages do.
			tgID = 0
		}
		var _, err = store.Append(ctx, d.deps.DB, store.Message{
			ChatID:            ev.ChatID,
			UserID:            ev.UserID,
			Role:              store.RoleUser,
			Content:           question,
			TelegramMessageID: messageIDOrNull(tgID),
		})
		if err != nil {
			if store.IsDuplicate(err) {
				// «Ctrl-C / docker compose down при длинном /ask» (2026-10-31) settled the case the old
				// comment simply assumed: this very function writes role='user' BEFORE the model call,
				// so a process stopped in the middle of a generation leaves the question stored and
				// unanswered. Silence there was not a second line of defence against a duplicate answer — it
				// happens next was measured the same day: Telegram does NOT hand such an update back, so the
				//
				// The guard therefore asks the database, and it asks it about THIS update: the row of the
				// So the question of this update is still waiting exactly when its own row is the newest row
				// who asked twice: the chat then ends with the row of the SECOND question, and the answer to
				// the first one — already given — would look missing.
				var questionID, rowErr = store.RowIDByTelegramMessageID(ctx, d.deps.DB, ev.ChatID, tgID)
				if rowErr != nil {
					d.deps.Logger.Error("the redelivered update stays unanswered: the row of could not be read",
						"chat_id", ev.ChatID, "telegram_message_id", tgID,
						"error", Sanitize(rowErr, d.deps.Token))
					return nil
				}
				var lastID, lastRole, lastErr = store.LastVisibleMessage(ctx, d.deps.DB, ev.ChatID)
				if lastErr != nil {
					d.deps.Logger.Error("the redelivered update stays unanswered: the newest message could not be read",
						"chat_id", ev.ChatID, "telegram_message_id", tgID,
						"error", Sanitize(lastErr, d.deps.Token))
					return nil
				}
				if questionID != 0 && questionID == lastID && lastRole == store.RoleUser {
					d.deps.Logger.Info("this update's question has no answer yet; answering it",
						"chat_id", ev.ChatID, "telegram_message_id", tgID, "question_row", questionID)
					return next(ctx, ev, cmd)
				}
				d.deps.Logger.Info("the update was already answered, skipped",
					"chat_id", ev.ChatID, "telegram_message_id", tgID,
					"question_row", questionID, "newest_row", lastID, "newest_role", lastRole)
				return nil
			}
			return err
		}
		return next(ctx, ev, cmd)
	}
}

// otherwise a user could never read their own id to get into TELEGRAM_ALLOWED_USERS. The refusal is
func (d *Dispatcher) allowed(next HandlerFunc) HandlerFunc {
	return func(ctx context.Context, ev Event, cmd Command) error {
		if cmd.AlwaysAllowed || d.deps.Cfg.Allows(ev.UserID) {
			return next(ctx, ev, cmd)
		}
		d.logEvent(ctx, ev.UserID, store.EventDenied, cmd.Name)
		lang := d.chatLanguage(ctx, ev.ChatID)
		if d.deps.Cfg.DenyByDefault {
			return d.reply(ctx, ev.ChatID, refuseClosed(ev.UserID, lang))
		}
		return d.reply(ctx, ev.ChatID, refuseNotListed(ev.UserID, lang))
	}
}

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
		return d.reply(ctx, ev.ChatID, tooOftenNotice(d.chatLanguage(ctx, ev.ChatID), d.deps.Cfg.RateLimitPerMinute, wait))
	}
}

// cancellable (ratelimit.WithChat), so an expired reply budget frees the queue instead of parking
func (d *Dispatcher) locked(next HandlerFunc) HandlerFunc {
	return func(ctx context.Context, ev Event, cmd Command) error {
		if ctx.Value(chatLockHeldContextKey{}) != nil {
			return next(ctx, ev, cmd)
		}
		return d.deps.Locks.WithChat(ctx, ev.ChatID, func() error {
			return next(ctx, ev, cmd)
		})
	}
}

// a panic in the store write or in a command handler must not take the process down, and the user
// gets an answer instead of silence.
func (d *Dispatcher) recovered(next HandlerFunc) HandlerFunc {
	return func(ctx context.Context, ev Event, cmd Command) (err error) {
		defer func() {
			var r = recover()
			if r == nil {
				return
			}
			panicText := d.recoveredPanicText(r)
			err = fmt.Errorf("panic in the handler of /%s: %s", cmd.Name, panicText)
			d.deps.Logger.Error("recovered a panic", "chat_id", ev.ChatID, "command", cmd.Name,
				"panic", panicText)
			d.logEvent(ctx, ev.UserID, store.EventCommand, "panic:"+cmd.Name)
			if sendErr := d.reply(ctx, ev.ChatID, panicNotice(d.chatLanguage(ctx, ev.ChatID))); sendErr != nil {
				d.deps.Logger.Error("the panic notice was not delivered", "chat_id", ev.ChatID,
					"error", Sanitize(sendErr, d.deps.Token))
			}
		}()
		return next(ctx, ev, cmd)
	}
}

func (d *Dispatcher) recoveredPanicText(recovered any) string {
	text := fmt.Sprint(recovered)
	for _, secret := range []string{d.deps.Token, d.deps.Cfg.BotToken, d.deps.Cfg.StrataKey, d.deps.Cfg.WebhookSecretToken} {
		text = Sanitize(errors.New(text), secret)
	}
	if len(text) <= recoveredPanicMaxBytes {
		return text
	}
	text = text[:recoveredPanicMaxBytes]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}

func messageIDOrNull(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}
