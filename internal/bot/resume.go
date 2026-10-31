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
// has no answer of §6. It returns how many questions actually RECEIVED an answer of §6 and how many are still
// without one; a question whose handler failed (STRATA down, a budget of §7 gone) is reported by the ask handler
// itself — the events row of §5 and the user notice — and the loop moves on, because a broken STRATA must not
// take the whole start of the process down (§12 надёжность: «один сбой STRATA ≠ падение бота»).
//
// The second number is not decoration, and the container of §8.1 found that out on 2026-10-31 at 15:55:28: the
// scan named the one waiting question, the guard let it through, the model spent the whole
// STRATA_TIMEOUT_SECONDS 60s on it and answered nothing — and the process still printed «answered 1», because
// the ask handler of §6 handles a model failure itself (the events row and the notice to the user) and returns
// nil. What the handler returns therefore says nothing about the answer; only §5 knows. So the count is taken
// from store.LastVisibleMessage, the same function the dedup guard of §2 п.7 decides with: an assistant row
// newer than the question row means the user got the answer, anything else leaves the question exactly where it
// was for the next start of the process. The comparison is `id > row.ID` rather than «the chat ends with an
// answer», because the poller of §5 runs next to this loop and a new question can arrive while this one is in
// flight — that must not read as a failure of the replay.
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
//
// One decision worth recording here, taken on 2026-10-31 after the second live run of §8.1: `STRATA_TIMEOUT_SECONDS`
// of §7 stays 60 and this scan gets no age cap. A question whose answer is longer than one call budget is therefore
// retried at every start of the process — sixty seconds of the local model per start, one events(kind='timeout') row
// of §5 per start, and §12 printing `answered 0 … left 1` about it — until the §15 «стриминг ответов» item makes an
// answer of 7248 tokens fit inside one call at all. A bigger number would answer that one question and leave every
// longer one in exactly the same place, while also holding a slot of MAX_CONCURRENT_MODEL_REQUESTS §7 open for as
// long as the model takes to write.
func (d *Dispatcher) ResumeInterruptedQuestions(ctx context.Context) (answered int, pending int, err error) {
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
			// The stop signal of §10 День 1: the loop leaves the remaining questions in §5 for the next
			// start, exactly where they are now. The one in flight returns through the cancellation check of
			// the ask handler, which writes no assistant row for an answer that never happened.
			return answered, pending, err
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
			pending++
			i++
			continue
		}
		// The chain returned without an error, which is NOT the same thing as an answer: the ask handler of §6
		// swallows a model failure of §10 День 3 on purpose (one STRATA failure must not travel up as a broken
		// update), so only §5 can say what the replay actually left behind. LastVisibleMessage is the same read
		// the guard of §2 п.7 decides with, which keeps the two paths using one definition of «answered».
		var newestID, newestRole, checkErr = store.LastVisibleMessage(ctx, d.deps.DB, ev.ChatID)
		if checkErr != nil {
			// Counting this as answered would be the other kind of lie: the operator would look for an answer
			// that §5 has no record of. The question stays where it is, and the next start tries it again.
			d.deps.Logger.Error("the start of the process could not check whether its replay wrote an answer of §6",
				"chat_id", ev.ChatID, "question_row", row.ID,
				"error", Sanitize(checkErr, d.deps.Token))
			pending++
			i++
			continue
		}
		// `> row.ID` and not just the role: the poller of §5 runs next to this loop, so a NEW question of this
		// chat can be the newest row by the time the replay ends. An assistant row newer than the question this
		// replay was built from is the only thing that proves the user was finally answered.
		if newestRole == store.RoleAssistant && newestID > row.ID {
			answered++
			i++
			continue
		}
		// Silent otherwise is the §13 риск «пользователь так и не получил ответ»: the model spent its budget, or
		// `allowed`/`limited` of §7 refused the replay — the chain logs the ones of those that are errors, this
		// line is the one nobody else would print (§12 UX).
		d.deps.Logger.Warn("the replay of the start of the process left the stored question without an answer of §6, the next start tries it again",
			"chat_id", ev.ChatID, "question_row", row.ID, "newest_row", newestID, "newest_role", newestRole)
		pending++
		i++
	}
	return answered, pending, nil
}
