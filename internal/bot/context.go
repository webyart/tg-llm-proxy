// context.go — the context builder of §10 День 4 and the «Правила контекста (v1)» of §5: SYSTEM_PROMPT
// of §7 plus the last CONTEXT_MESSAGES rows of the chat, glued chronologically and bounded by
// MAX_CONTEXT_CHARS. §16 change 10 added the two char budgets because a count of messages alone lets
// one enormous message push the whole window out of the model's context (§13 risk).
//
// The incoming turn is already in SQLite when this runs: the `stored` middleware of §2 п.7 writes the
// user row BEFORE the model call, so the newest row of RecentByChat is the question itself. That is
// why BuildMessages takes only the chat — a second copy of the text would appear in the request as a
// duplicated turn (§10 День 3 write invariant covers the answer, not the question).
package bot

import (
	"context"
	"strings"

	"bot/internal/model"
	"bot/internal/store"
)

// BuildMessages — the glued request of §5. The rows are taken newest-first (that is what
// idx_messages_chat_time of §5 is for) and appended until MAX_CONTEXT_CHARS is spent; the older rows
// that no longer fit drop out, never the newest one — without the question there is nothing to ask
// (§5 «Лимита по объёму в v1 не было: одно огромное сообщение пользователя выбивает контекст»).
func (d *Dispatcher) BuildMessages(ctx context.Context, chatID int64) ([]model.Message, error) {
	var rows, err = store.RecentByChat(ctx, d.deps.DB, chatID, int(d.deps.Cfg.ContextMessages))
	if err != nil {
		return nil, err
	}

	var prompt = strings.TrimSpace(d.deps.Cfg.SystemPrompt)
	var budget = int(d.deps.Cfg.MaxContextChars)
	var used = Runes(prompt)
	var newestFirst = []store.Message{}

	var i = len(rows) - 1
	for i >= 0 {
		var row = rows[i]
		var content = d.forModel(row)
		var cost = Runes(content)
		if cost == 0 {
			i--
			continue
		}
		if used+cost > budget {
			if len(newestFirst) == 0 {
				// The newest turn survives even alone: it is cut to what the budget still has.
				var room = budget - used
				if room < 1 {
					room = 1
				}
				content, _ = CutRunes(content, room)
				newestFirst = append(newestFirst, withContent(row, content))
			}
			break
		}
		used += cost
		newestFirst = append(newestFirst, withContent(row, content))
		i--
	}

	var out = []model.Message{}
	if prompt != "" {
		out = append(out, model.Message{Role: model.RoleSystem, Content: prompt})
	}
	var j = len(newestFirst) - 1
	for j >= 0 {
		var row = newestFirst[j]
		out = append(out, model.Message{Role: row.Role, Content: row.Content})
		j--
	}
	return out, nil
}

// forModel — MAX_INPUT_CHARS of §7: the text of a USER turn is cut with the marker of §5. An
// assistant turn is not cut here — it already went through MAX_MESSAGE_CHUNK on the way out (§7),
// and rewriting the history of the model would make /history of §6 disagree with what was sent.
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

// withContent — a copy with the content the model will see (§5 keeps the full text in SQLite for
// /history of §6).
func withContent(row store.Message, content string) store.Message {
	row.Content = content
	return row
}
