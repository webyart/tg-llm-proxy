package bot

import (
	"context"
	"strings"

	"bot/internal/model"
	"bot/internal/store"
)

// BuildMessages builds the model request for one chat from RecentByChat rows.
//
// The caller stores the question BEFORE the model call, so the newest row of RecentByChat is the
// question itself. That is why BuildMessages takes only the chat: a second copy of the text would
// appear in the request.
//
// The rows are grouped into turns — a user row opens a turn, and every assistant row after it
// belongs to that turn (a tool loop leaves several assistant rows for one question). The window is
// filled from the newest turn backwards and a turn is included whole or dropped whole, because a
// per-message cut used to leave an assistant answer without its question and could make the request
// start with an assistant message. The newest turn is the only exception: its question always
// survives, cut to what the budget still has, and its assistant rows are cut or dropped first. The
// persona stays the first system message and its runes are counted against the budget as before.
func (d *Dispatcher) BuildMessages(ctx context.Context, chatID int64) ([]model.Message, error) {
	var rows, err = store.RecentByChat(ctx, d.deps.DB, chatID, int(d.deps.Cfg.ContextMessages))
	if err != nil {
		return nil, err
	}

	var prompt string
	prompt, _, _, err = d.effectivePersona(ctx, chatID)
	if err != nil {
		return nil, err
	}
	prompt = strings.TrimSpace(prompt)
	var budget = int(d.deps.Cfg.MaxContextChars)
	var used = Runes(prompt)
	var turns = contextTurns(rows)
	var window []model.Message

	if len(turns) > 0 {
		window, used = d.newestTurnMessages(turns[len(turns)-1], budget, used)
	}

	// Older turns, newest first: the first turn that does not fit closes the window. Dropping whole
	// turns instead of single rows is what keeps an answer paired with its question.
	var i = len(turns) - 2
	for i >= 0 {
		var messages, cost = d.turnMessages(turns[i])
		if used+cost > budget {
			break
		}
		used += cost
		window = append(messages, window...)
		i--
	}

	// A window must never open with an answer. contextTurns already drops the orphan prefix left by
	// CONTEXT_MESSAGES; this guard states the invariant for any future grouping.
	for len(window) > 0 && window[0].Role != model.RoleUser {
		window = window[1:]
	}

	var out = make([]model.Message, 0, len(window)+1)
	if prompt != "" {
		out = append(out, model.Message{Role: model.RoleSystem, Content: prompt})
	}
	out = append(out, window...)
	return out, nil
}

// contextTurns groups rows (oldest first) into turns. A user row opens a turn, and every row after
// it belongs to that turn. Rows before the first user row are the tail of a turn that
// CONTEXT_MESSAGES cut in half: keeping them would put an answer into the request without its
// question, so that prefix is dropped.
func contextTurns(rows []store.Message) [][]store.Message {
	var turns [][]store.Message
	for _, row := range rows {
		if row.Role == store.RoleUser {
			turns = append(turns, []store.Message{row})
			continue
		}
		if len(turns) == 0 {
			continue
		}
		turns[len(turns)-1] = append(turns[len(turns)-1], row)
	}
	return turns
}

// newestTurnMessages renders the newest turn under the remaining budget. Its question is never
// dropped: when it does not fit whole, it is cut to the room the persona left (at least one rune).
// The assistant rows of the turn are added newest-first, so a tight budget keeps the final answer
// and loses intermediate tool-loop replies; the row that stops fitting is cut, and the older rows
// are dropped instead of the question.
func (d *Dispatcher) newestTurnMessages(turn []store.Message, budget int, used int) ([]model.Message, int) {
	var question = d.forModel(turn[0])
	if Runes(question) == 0 {
		return nil, used
	}
	var room = budget - used
	if Runes(question) > room {
		if room < 1 {
			room = 1
		}
		question, _ = CutRunes(question, room)
	}
	used += Runes(question)
	var messages = []model.Message{{Role: turn[0].Role, Content: question}}
	var i = len(turn) - 1
	for i >= 1 {
		var content = d.forModel(turn[i])
		var cost = Runes(content)
		if cost == 0 {
			i--
			continue
		}
		room = budget - used
		if cost > room {
			if room < 1 {
				break
			}
			content, _ = CutRunes(content, room)
			cost = Runes(content)
			messages = append(messages, model.Message{Role: turn[i].Role, Content: content})
			used += cost
			break
		}
		used += cost
		messages = append(messages, model.Message{Role: turn[i].Role, Content: content})
		i--
	}
	// messages holds the question and then the assistant rows newest-first: restore the chat order.
	for lo, hi := 1, len(messages)-1; lo < hi; lo, hi = lo+1, hi-1 {
		messages[lo], messages[hi] = messages[hi], messages[lo]
	}
	return messages, used
}

// turnMessages renders one whole turn and reports its character cost, so the caller can include it
// or drop it whole.
func (d *Dispatcher) turnMessages(turn []store.Message) ([]model.Message, int) {
	var messages = make([]model.Message, 0, len(turn))
	var cost = 0
	for _, row := range turn {
		var content = d.forModel(row)
		var runes = Runes(content)
		if runes == 0 {
			continue
		}
		cost += runes
		messages = append(messages, model.Message{Role: row.Role, Content: content})
	}
	return messages, cost
}

func (d *Dispatcher) forModel(row store.Message) string {
	if row.Role != store.RoleUser {
		return row.Content
	}
	var cut, changed = CutRunes(row.Content, int(d.deps.Cfg.MaxInputChars))
	if !changed {
		return row.Content
	}
	return cut
}
