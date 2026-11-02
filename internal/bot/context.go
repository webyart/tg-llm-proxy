// user row BEFORE the model call, so the newest row of RecentByChat is the question itself. That is
// why BuildMessages takes only the chat — a second copy of the text would appear in the request as a
package bot

import (
	"context"
	"strings"

	"bot/internal/model"
	"bot/internal/store"
)

// that no longer fit drop out, never the newest one — without the question there is nothing to ask
func (d *Dispatcher) BuildMessages(ctx context.Context, chatID int64) ([]model.Message, error) {
	var rows, err = store.RecentByChat(ctx, d.deps.DB, chatID, int(d.deps.Cfg.ContextMessages))
	if err != nil {
		return nil, err
	}

	prompt, _, _, err := d.effectivePersona(ctx, chatID)
	if err != nil {
		return nil, err
	}
	prompt = strings.TrimSpace(prompt)
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

func withContent(row store.Message, content string) store.Message {
	row.Content = content
	return row
}
