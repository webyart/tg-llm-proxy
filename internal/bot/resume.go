// resume.go — the §15 item «догонять вопрос, оборванный внутри обработчика», closed on 2026-10-31 after the
// container walk of §8.1 measured why the offset of §5 is not enough. The walk stopped the process 10 s into a
// live generation: exit 0 in 0.6 s, the WAL of §5 checkpointed, the row of the question kept, and
// last_update_id left at 920551679 while the interrupted update was 920551680 — exactly the window §2 п.7 was
// built for. Then the restarted bot asked Telegram for 920551680 (`WithInitialOffset` of run.go plus
// `offset = lastUpdateID + 1` of the library) and received empty batches for 9.5 minutes, with no failed
// request in the log to blame. Telegram re-delivers an update only when its getUpdates response never reached
// the client; a process that died AFTER the response, inside the handler, does not get a second chance. So the
// question row survived and nobody ever asked about it again — which is the loss §12 надёжность exists to
// prevent, just one floor higher than the duplicate answer §13 names.
//
// The remedy is to ask the database at the start of the process instead of waiting for Telegram: §5 knows which
// chats end with an unanswered question (store.UnansweredQuestions), and answering one is the same job the
// redelivery guard of §2 п.7 already performs — so the code below does not answer anything itself. It rebuilds
// the update of §6 from the stored row and pushes it through the SAME middleware chain as a live update
// (recovered → allowed → limited → locked → stored). Everything that follows is then the behaviour the tests of
// §11 already pin down: `stored` hits uq_messages_chat_tg_id of §5, the guard compares the row of that update
// with the newest visible row (store.RowIDByTelegramMessageID, store.LastVisibleMessage), finds the question
// still newest, and lets the handler of §6 run. The per-chat lock of §12 is what keeps this replay and a real
// redelivery of the same update from answering twice: whichever of them arrives second sees the assistant row
// the first one wrote and stays silent.
//
// What stays silently lost on purpose (§13 риск «двойная доставка»): a process stopped between writing the row
// of the answer and sending it. The newest visible row is then the assistant row, the scan finds nothing, and
// the bot refuses to send a second copy of an answer the user may already have.
package bot

import (
	"context"

	"bot/internal/store"
)

// ResumeScanLimit — how many chats the startup scan looks at, newest activity first. §9 asks for no unbounded
// work at the start of the process; a private bot of §7 has one chat per user, and twenty of them already
// covers a month of a small family deployment.
const ResumeScanLimit = 20

// ResumeInterruptedQuestions — one attempt, at the start of the process, for every stored question of §5 that
// has no answer of §6. It returns how many of them reached the handler; a question whose handler failed (STRATA
// down, a budget of §7 gone) is reported by the ask handler itself — the events row of §5 and the user notice —
// and the loop moves on, because a broken STRATA must not take the whole start of the process down
// (§12 надёжность: «один сбой STRATA ≠ падение бота»).
//
// Two things it deliberately does not do:
//   - it never writes the offset of §5. There is no update here to acknowledge: the Event is rebuilt from a row,
//     and its UpdateID stays 0. The chain is entered directly rather than through Dispatch, which is also why no
//     extra events(kind='command') row appears — the interrupted run already wrote that audit row, and a second
//     one would make `dbinspect` of §9 count one question as two (§5 events: commands, errors, limits).
//   - it never waits for the poller. main.go starts it next to the polling goroutine, so a model that takes its
//     whole STRATA_TIMEOUT_SECONDS cannot delay the first getUpdates of the new process (§12 UX: the bot must
//     answer the new messages it is asked to answer).
//
// One consequence worth naming before someone reads it as a bug: the replay goes through `allowed` of §7 like any
// other update, so a chat that left TELEGRAM_ALLOWED_USERS while its question was waiting gets the refusal text
// it would have got live, not an answer. That is the allowlist working, not the scan misbehaving.
func (d *Dispatcher) ResumeInterruptedQuestions(ctx context.Context) (int, error) {
	if d.deps.DB == nil {
		return 0, nil
	}
	var rows, err = store.UnansweredQuestions(ctx, d.deps.DB, ResumeScanLimit)
	if err != nil {
		return 0, err
	}
	var ask = d.index[CommandAsk]
	var answered = 0
	var i = 0
	for i < len(rows) {
		if err = ctx.Err(); err != nil {
			// The stop signal of §10 День 1: the loop leaves the remaining questions in §5 for the next
			// start, exactly where they are now. The one in flight returns through the cancellation check of
			// the ask handler, which writes no assistant row for an answer that never happened.
			return answered, err
		}
		var row = rows[i]
		var ev = Event{
			TelegramMessageID: *row.TelegramMessageID,
			ChatID:            row.ChatID,
			UserID:            row.UserID,
			Text:              row.Content,
			Question:          row.Content,
		}
		// §12 UX: a message that appears in the chat without the user asking again needs a reason in the log —
		// the operator sees the answer in Telegram and the question in §5, and only this line connects them.
		d.deps.Logger.Info("the stored question of §5 has no answer of §6, the start of the process answers it",
			"chat_id", ev.ChatID, "user_id", ev.UserID, "question_row", row.ID,
			"telegram_message_id", ev.TelegramMessageID, "stored_at_utc", row.CreatedAt)
		var chainErr = d.chain(func(handlerCtx context.Context, ev Event, cmd Command) error {
			return cmd.Handler(handlerCtx, ev, ev.Question)
		})(ctx, ev, ask)
		if chainErr != nil {
			// The user has already been answered with the failure text of §6 by the handler itself; this line
			// is for the operator, and the reason goes through Sanitize (§12 security).
			d.deps.Logger.Error("the stored question of §5 stayed unanswered",
				"chat_id", ev.ChatID, "question_row", row.ID,
				"error", Sanitize(chainErr, d.deps.Token))
			i++
			continue
		}
		answered++
		i++
	}
	return answered, nil
}
