// built for. Then the restarted bot asked Telegram for 920551680 (`WithInitialOffset` of run.go plus
// `offset = lastUpdateID + 1` of the library) and received empty batches for 9.5 minutes, with no failed
// request in the log to blame. Telegram re-delivers an update only when its getUpdates response never reached
// the client; a process that died AFTER the response, inside the handler, does not get a second chance. So the
//
// chats end with an unanswered question (store.UnansweredQuestions), and answering one is the same job the
// (recovered → allowed → limited → locked → stored). Everything that follows is then the behaviour the tests of
// with the newest visible row (store.RowIDByTelegramMessageID, store.LastVisibleMessage), finds the question
// redelivery of the same update from answering twice: whichever of them arrives second sees the assistant row
// the first one wrote and stays silent.
//
// of the answer and sending it. The newest visible row is then the assistant row, the scan finds nothing, and
// the bot refuses to send a second copy of an answer the user may already have.
package bot

import (
	"context"
	"fmt"
	"time"

	"bot/internal/store"
)

// covers a month of a small family deployment.
const ResumeScanLimit = 20
const ResumeMaxAttempts = 3

const (
	ResumeInitialBackoff = time.Minute
	ResumeMaxBackoff     = 15 * time.Minute
)

// scan named the one waiting question, the guard let it through, the model spent the whole
// STRATA_TIMEOUT_SECONDS 60s on it and answered nothing — and the process still printed «answered 1», because
// newer than the question row means the user got the answer, anything else leaves the question exactly where it
// was for the next start of the process. The comparison is `id > row.ID` rather than «the chat ends with an
// flight — that must not read as a failure of the replay.
//
// Two things it deliberately does not do:
//
//	  and its UpdateID stays 0. The chain is entered directly rather than through Dispatch, which is also why no
//	  extra events(kind='command') row appears — the interrupted run already wrote that audit row, and a second
//	- it never waits for the poller. main.go starts it next to the polling goroutine, so a model that takes its
//	  answer the new messages it is asked to answer).
//
// other update, so a chat that left TELEGRAM_ALLOWED_USERS while its question was waiting gets the refusal text
// it would have got live, not an answer. That is the allowlist working, not the scan misbehaving.
//
// RECOVERY_MAX_AGE_HOURS bounds automatic retries. An expired row receives the same bot_state suppression as
// /skip without another model call; history stays intact. /unskip does not override the age cap.
func (d *Dispatcher) ResumeInterruptedQuestions(ctx context.Context) (answered int, pending int, err error) {
	return d.resumeInterruptedQuestionsAt(ctx, time.Now().UTC())
}

func (d *Dispatcher) resumeInterruptedQuestionsAt(ctx context.Context, now time.Time) (answered int, pending int, err error) {
	if d.deps.DB == nil {
		return 0, 0, nil
	}
	var rows, scanErr = store.UnansweredQuestions(ctx, d.deps.DB, ResumeScanLimit)
	if scanErr != nil {
		return 0, 0, scanErr
	}
	var ask = d.index[CommandAsk]
	var i = 0
	for i < len(rows) {
		if err = ctx.Err(); err != nil {
			// start, exactly where they are now. The one in flight returns through the cancellation check of
			// the ask handler, which writes no assistant row for an answer that never happened.
			return answered, pending, err
		}
		var row = rows[i]
		expired, ageErr := recoveryExpired(row.CreatedAt, d.deps.Cfg.RecoveryMaxAgeHours, now)
		if ageErr != nil {
			return answered, pending, ageErr
		}
		if expired {
			if skipErr := store.SetResumeSkip(ctx, d.deps.DB, row.ChatID, row.ID); skipErr != nil {
				return answered, pending, skipErr
			}
			d.deps.Logger.Info("startup recovery skipped an expired question",
				"chat_id", row.ChatID, "question_row", row.ID, "stored_at_utc", row.CreatedAt,
				"max_age_hours", d.deps.Cfg.RecoveryMaxAgeHours)
			i++
			continue
		}

		recovery, found, recoveryErr := store.ResumeRecovery(ctx, d.deps.DB, row.ChatID, row.ID)
		if recoveryErr != nil {
			return answered, pending, recoveryErr
		}
		if found && recovery.Attempts >= ResumeMaxAttempts {
			if skipErr := store.SetResumeSkip(ctx, d.deps.DB, row.ChatID, row.ID); skipErr != nil {
				return answered, pending, skipErr
			}
			d.deps.Logger.Info("startup recovery suppressed a question after repeated failures",
				"chat_id", row.ChatID, "question_row", row.ID, "attempts", recovery.Attempts)
			i++
			continue
		}
		if found && now.Before(recovery.NextRecoveryAt) {
			d.deps.Logger.Info("startup recovery is waiting for the persisted backoff",
				"chat_id", row.ChatID, "question_row", row.ID, "attempts", recovery.Attempts,
				"next_recovery_at", recovery.NextRecoveryAt.UTC())
			pending++
			i++
			continue
		}

		attempts := 1
		if found {
			attempts = recovery.Attempts + 1
		}
		nextRecoveryAt := now.Add(recoveryBackoff(attempts))
		if setErr := store.SetResumeRecovery(ctx, d.deps.DB, row.ChatID, row.ID, attempts, nextRecoveryAt); setErr != nil {
			return answered, pending, setErr
		}

		var ev = Event{
			TelegramMessageID: *row.TelegramMessageID,
			ChatID:            row.ChatID,
			UserID:            row.UserID,
			Text:              row.Content,
			Question:          row.Content,
			Recovery:          true,
		}
		d.deps.Logger.Info("the stored question has no answer; startup is answering it",
			"chat_id", ev.ChatID, "user_id", ev.UserID, "question_row", row.ID,
			"telegram_message_id", ev.TelegramMessageID, "stored_at_utc", row.CreatedAt,
			"recovery_attempt", attempts, "next_recovery_at", nextRecoveryAt)
		var chainErr = d.chain(func(handlerCtx context.Context, ev Event, cmd Command) error {
			return cmd.Handler(handlerCtx, ev, ev.Question)
		})(ctx, ev, ask)
		if chainErr != nil {
			// The answer is stored before Telegram delivery. A send failure with that row present is the deliberate
			var newestID, newestRole, checkErr = store.LastVisibleMessage(ctx, d.deps.DB, ev.ChatID)
			if checkErr == nil && newestRole == store.RoleAssistant && newestID > row.ID {
				if clearErr := store.ClearResumeRecovery(ctx, d.deps.DB, row.ChatID, row.ID); clearErr != nil {
					return answered, pending, clearErr
				}
				d.deps.Logger.Error("the answer was stored, but Telegram delivery is incomplete; automatic replay stays disabled to prevent duplicates",
					"chat_id", ev.ChatID, "question_row", row.ID,
					"error", Sanitize(chainErr, d.deps.Token))
				answered++
				i++
				continue
			}
			if attempts >= ResumeMaxAttempts {
				if skipErr := store.SetResumeSkip(ctx, d.deps.DB, row.ChatID, row.ID); skipErr != nil {
					return answered, pending, skipErr
				}
			}
			d.deps.Logger.Error("the stored question remains unanswered",
				"chat_id", ev.ChatID, "question_row", row.ID, "recovery_attempt", attempts,
				"next_recovery_at", nextRecoveryAt, "error", Sanitize(chainErr, d.deps.Token))
			pending++
			i++
			continue
		}
		if isStoredAttachment(row.Content) {
			if skipErr := store.SetResumeSkip(ctx, d.deps.DB, ev.ChatID, row.ID); skipErr != nil {
				return answered, pending, skipErr
			}
			pending++
			i++
			continue
		}
		var newestID, newestRole, checkErr = store.LastVisibleMessage(ctx, d.deps.DB, ev.ChatID)
		if checkErr != nil {
			// Counting this as answered would be the other kind of lie: the operator would look for an answer
			d.deps.Logger.Error("startup could not verify whether replay stored an answer",
				"chat_id", ev.ChatID, "question_row", row.ID,
				"error", Sanitize(checkErr, d.deps.Token))
			pending++
			i++
			continue
		}
		// chat can be the newest row by the time the replay ends. An assistant row newer than the question this
		// replay was built from is the only thing that proves the user was finally answered.
		if newestRole == store.RoleAssistant && newestID > row.ID {
			if clearErr := store.ClearResumeRecovery(ctx, d.deps.DB, row.ChatID, row.ID); clearErr != nil {
				return answered, pending, clearErr
			}
			answered++
			i++
			continue
		}
		if attempts >= ResumeMaxAttempts {
			if skipErr := store.SetResumeSkip(ctx, d.deps.DB, row.ChatID, row.ID); skipErr != nil {
				return answered, pending, skipErr
			}
		}
		d.deps.Logger.Warn("replay left the stored question unanswered; persisted backoff will delay the next attempt",
			"chat_id", ev.ChatID, "question_row", row.ID, "newest_row", newestID, "newest_role", newestRole,
			"recovery_attempt", attempts, "next_recovery_at", nextRecoveryAt)
		pending++
		i++
	}
	return answered, pending, nil
}

func recoveryBackoff(attempt int) time.Duration {
	delay := ResumeInitialBackoff
	for current := 1; current < attempt && delay < ResumeMaxBackoff; current++ {
		delay *= 2
		if delay > ResumeMaxBackoff {
			return ResumeMaxBackoff
		}
	}
	return delay
}

func recoveryExpired(createdAt string, maxAgeHours int64, now time.Time) (bool, error) {
	if maxAgeHours <= 0 {
		return false, nil
	}
	created, err := time.Parse(time.DateTime, createdAt)
	if err != nil {
		return false, fmt.Errorf("recovery question created_at %q is not a UTC SQLite timestamp: %w", createdAt, err)
	}
	return now.Sub(created) > time.Duration(maxAgeHours)*time.Hour, nil
}
