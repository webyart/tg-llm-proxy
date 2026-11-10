package bot

import (
	"context"
	"strings"

	"bot/internal/i18n"
	"bot/internal/store"
)

// chatLanguageState resolves the interface language of one chat and whether it comes from a stored
// override. The override lives in chat_language; without it the chat follows DEFAULT_LANGUAGE. The
// lookup happens per handler call and the resolved value is passed down to the formatting functions:
// the dispatcher keeps no process-global language cache, so two chats never share a language and a
// change made by /language applies to the very next command.
func (d *Dispatcher) chatLanguageState(ctx context.Context, chatID int64) (string, bool) {
	if d != nil && d.deps.DB != nil {
		language, found, err := store.ChatLanguage(ctx, d.deps.DB, chatID)
		if err == nil && found {
			return i18n.Normalize(language), true
		}
		if err != nil {
			d.deps.Logger.Debug("the chat language could not be read; using DEFAULT_LANGUAGE",
				"chat_id", chatID, "error", Sanitize(err, d.deps.Token))
		}
	}
	if d == nil {
		return i18n.Default, false
	}
	return i18n.Normalize(d.deps.Cfg.DefaultLanguage), false
}

// chatLanguage is the resolution rule in one place: the chat override, otherwise DEFAULT_LANGUAGE.
func (d *Dispatcher) chatLanguage(ctx context.Context, chatID int64) string {
	language, _ := d.chatLanguageState(ctx, chatID)
	return language
}

// languageControl — /language: report the language and its source, store a per-chat override, or drop
// it. It never calls the model and never writes on an unknown argument.
func (d *Dispatcher) languageControl(ctx context.Context, ev Event, arg string) error {
	current, override := d.chatLanguageState(ctx, ev.ChatID)
	switch action := strings.ToLower(strings.TrimSpace(arg)); action {
	case "":
		source := i18n.T(current, "language.source_default")
		if override {
			source = i18n.T(current, "language.source_chat")
		}
		return d.reply(ctx, ev.ChatID, i18n.T(current, "language.current", i18n.T(current, "language.self"), source))
	case i18n.RU, i18n.EN:
		if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
			return err
		}
		if err := store.SetChatLanguage(ctx, d.deps.DB, ev.ChatID, action); err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, i18n.T(action, "language.set", i18n.T(action, "language.self")))
	case "reset":
		if err := store.ResetChatLanguage(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
		fallback := i18n.Normalize(d.deps.Cfg.DefaultLanguage)
		return d.reply(ctx, ev.ChatID, i18n.T(fallback, "language.reset", i18n.T(fallback, "language.self")))
	default:
		return d.reply(ctx, ev.ChatID, i18n.T(current, "language.usage"))
	}
}
