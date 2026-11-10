// Group mode. Telegram gives a bot every message of a group only when its privacy mode is off, and a
// bot that answers each of them is noise. The rules live in this file, outside Dispatch, so the same
// gate serves a direct Dispatch call, the Telegram handler (which needs it before pending menu input
// rewrites the text) and the tests.
package bot

import (
	"context"
	"strings"

	"bot/internal/store"
)

const (
	chatTypeGroup      = "group"
	chatTypeSupergroup = "supergroup"
)

// isGroupChat — Telegram gives groups and supergroups negative chat IDs. ChatType is checked as well:
// eventOf carries it, and a transport or a test may set only that field.
func isGroupChat(ev Event) bool {
	if ev.ChatID < 0 {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(ev.ChatType)) {
	case chatTypeGroup, chatTypeSupergroup:
		return true
	}
	return false
}

// groupGate decides whether an update of a group/supergroup may be answered. A private chat always
// passes, byte-identically to the behaviour before groups were supported.
//
// accepted=false means the update is ignored: no reply, no model call, no daily-quota reservation, and
// only a debug line. The offset is still advanced, otherwise Telegram would redeliver the ignored
// update after every restart. A non-nil error is a storage failure of EnsureChat and must stay
// unacknowledged.
func (d *Dispatcher) groupGate(ctx context.Context, ev *Event) (bool, error) {
	if !isGroupChat(*ev) {
		return true, nil
	}
	if !d.deps.Cfg.GroupsEnabled {
		d.deps.Logger.Debug("group update ignored: GROUPS_ENABLED is false",
			"chat_id", ev.ChatID, "update_id", ev.UpdateID)
		d.persistOffset(ctx, ev.UpdateID)
		return false, nil
	}
	// The allowlist names users, not chats: a group is open to a sender only when that sender is
	// allowed. Unlike a private chat, no refusal is sent here — an uninvited answer in a group is
	// noise, and the group has a single "no answer" behaviour for every refused sender.
	if !d.deps.Cfg.Allows(ev.UserID) {
		d.deps.Logger.Debug("group update ignored: the sender is not in TELEGRAM_ALLOWED_USERS",
			"chat_id", ev.ChatID, "user_id", ev.UserID, "update_id", ev.UpdateID)
		d.persistOffset(ctx, ev.UpdateID)
		return false, nil
	}
	// A menu callback is an answer to a message the bot itself sent — the user pressed its button — so
	// it is addressed by construction. A recovery event replays a question that was already accepted
	// and stored with its mention stripped; gating that text again would drop the question.
	if !ev.MenuCallback && !ev.Recovery && !d.addressesBot(*ev) {
		d.deps.Logger.Debug("group update ignored: the bot was not addressed",
			"chat_id", ev.ChatID, "user_id", ev.UserID, "update_id", ev.UpdateID)
		d.persistOffset(ctx, ev.UpdateID)
		return false, nil
	}
	ev.Text = stripBotMention(ev.Text, d.botUsername())
	// Store the chat under its own (negative) id with the SENDER as its user. A group id is never a
	// user id, and every per-user limit downstream keys on ev.UserID, not on ev.ChatID.
	if d.deps.DB != nil {
		if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
			return false, err
		}
	}
	ev.GroupAccepted = true
	return true, nil
}

// addressesBot — the three ways a group update may address the bot: an @mention anywhere in the text
// (the caption is already ev.Text), an explicit command suffix (`/cmd@BotName`), or a reply to one of
// the bot's own messages. A bare `/cmd` is deliberately absent: in a group it may be meant for
// another bot or for the chat's commands.
func (d *Dispatcher) addressesBot(ev Event) bool {
	var botName = d.botUsername()
	if mentionsBot(ev.Text, botName) {
		return true
	}
	if commandAddressedToBot(ev.Text, botName) {
		return true
	}
	return d.replyToBot(ev, botName)
}

// botUsername returns the username obtained from getMe. When it is unknown, mentions cannot be
// recognised; the reply and command forms still work. The degradation is logged once per process.
func (d *Dispatcher) botUsername() string {
	var name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(d.deps.BotName), "@"))
	if name == "" {
		d.missingUsername.Do(func() {
			d.deps.Logger.Warn("the bot username is unavailable: @mentions in groups are not recognised until getMe answers")
		})
	}
	return name
}

// replyToBot — `ReplyToMessage` is populated by eventOf. The numeric id from getMe is the strongest
// check; the username covers a dispatcher built without it; and when both are unknown, a reply to a
// message Telegram marks as a bot's own is the documented degraded route.
func (d *Dispatcher) replyToBot(ev Event, botName string) bool {
	if ev.ReplyToMessageID == 0 && ev.ReplyToUserID == 0 && ev.ReplyToUsername == "" && !ev.ReplyToIsBot {
		return false
	}
	if d.deps.BotID != 0 && ev.ReplyToUserID == d.deps.BotID {
		return true
	}
	if botName != "" {
		return strings.EqualFold(strings.TrimSpace(ev.ReplyToUsername), botName)
	}
	return ev.ReplyToIsBot
}

// commandAddressedToBot reports whether the first token is a command carrying an explicit @suffix for
// this bot. `/cmd` alone is not addressed in a group.
func commandAddressedToBot(text string, botName string) bool {
	var trimmed = strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return false
	}
	var head = trimmed
	if space := strings.IndexAny(trimmed, " \t\n\r"); space >= 0 {
		head = trimmed[:space]
	}
	var at = strings.Index(head, "@")
	if at < 0 {
		return false
	}
	var suffix = strings.TrimSpace(head[at+1:])
	if suffix == "" {
		return false
	}
	var name = strings.TrimPrefix(strings.TrimSpace(botName), "@")
	if name == "" {
		// getMe did not answer: without the username the command form is the only route left, so
		// accept the suffix rather than making the bot unreachable in groups.
		return true
	}
	return strings.EqualFold(suffix, name)
}

// mentionsBot reports whether the text carries an `@BotName` mention as a whole word. An unknown
// username means no mention can be recognised.
func mentionsBot(text string, botName string) bool {
	var start, _ = findBotMention(text, botName)
	return start >= 0
}

// findBotMention returns the byte offsets of the first whole `@BotName` mention, or (-1, -1).
// Telegram usernames are ASCII letters, digits and underscore; the boundary check keeps
// `@BotNameExtra` and `email@BotName` from counting as a mention of the bot.
func findBotMention(text string, botName string) (int, int) {
	var name = strings.TrimPrefix(strings.TrimSpace(botName), "@")
	if text == "" || name == "" {
		return -1, -1
	}
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		var end = i + 1 + len(name)
		if end > len(text) || !strings.EqualFold(text[i+1:end], name) {
			continue
		}
		if i > 0 && isUsernameByte(text[i-1]) {
			continue
		}
		if end < len(text) && isUsernameByte(text[end]) {
			continue
		}
		return i, end
	}
	return -1, -1
}

func isUsernameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// stripBotMention removes every mention of the bot from the text that is stored and sent to the model.
// Only the seam left by the removal is normalised (one space, or one line break); the rest of the
// user's formatting survives untouched.
func stripBotMention(text string, botName string) string {
	for {
		var start, end = findBotMention(text, botName)
		if start < 0 {
			return strings.TrimSpace(text)
		}
		var left = strings.TrimRight(text[:start], " \t")
		var right = strings.TrimLeft(text[end:], " \t")
		if strings.HasSuffix(left, "\n") || strings.HasSuffix(left, "\r") {
			right = strings.TrimLeft(right, "\r\n")
		}
		if strings.HasPrefix(right, "\n") || strings.HasPrefix(right, "\r") {
			left = strings.TrimRight(left, "\r\n")
		}
		if left != "" && right != "" &&
			!strings.HasSuffix(left, "\n") && !strings.HasSuffix(left, "\r") &&
			!strings.HasPrefix(right, "\n") && !strings.HasPrefix(right, "\r") {
			left += " "
		}
		text = left + right
	}
}
