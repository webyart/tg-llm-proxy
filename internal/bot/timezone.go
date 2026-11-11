package bot

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"bot/internal/config"
	"bot/internal/i18n"
	"bot/internal/store"
	"bot/internal/timefmt"
)

// EffectiveTimezone resolves the display zone of one chat: the chat_timezone override (migration 024) or
// DEFAULT_TIMEZONE from the config. The bool reports whether an override exists. A broken stored value is
// not allowed to hide the chat: the resolver falls back to the configured default and the caller logs it.
func EffectiveTimezone(ctx context.Context, db *sql.DB, cfg config.Config, chatID int64) (string, *time.Location, bool, error) {
	defaultName := strings.TrimSpace(cfg.Timezone)
	defaultLocation := cfg.TimezoneLocation
	if defaultName == "" {
		defaultName = config.DefaultTimezone
	}
	if defaultLocation == nil {
		location, err := time.LoadLocation(defaultName)
		if err != nil {
			defaultName = config.DefaultTimezone
			location, _ = time.LoadLocation(defaultName)
		}
		defaultLocation = location
	}
	if db != nil && chatID != 0 {
		stored, found, err := store.GetChatTimezone(ctx, db, chatID)
		if err != nil {
			return defaultName, defaultLocation, false, err
		}
		if found {
			if location, loadErr := time.LoadLocation(stored); loadErr == nil {
				return stored, location, true, nil
			}
			// A corrupt row (hand-edited database, removed tzdata) must not break every screen.
			return defaultName, defaultLocation, false, nil
		}
	}
	return defaultName, defaultLocation, false, nil
}

// chatTimezoneState is the dispatcher form of EffectiveTimezone: a database error is logged and treated as
// "no override", because a diagnostic must not stop a command from rendering.
func (d *Dispatcher) chatTimezoneState(ctx context.Context, chatID int64) (string, *time.Location, bool) {
	name, location, override, err := EffectiveTimezone(ctx, d.deps.DB, d.deps.Cfg, chatID)
	if err != nil && d.deps.Logger != nil {
		d.deps.Logger.Debug("the chat timezone could not be read; using DEFAULT_TIMEZONE",
			"chat_id", chatID, "error", Sanitize(err, d.deps.Token))
	}
	if location == nil {
		location = time.UTC
	}
	return name, location, override
}

func (d *Dispatcher) chatLocation(ctx context.Context, chatID int64) *time.Location {
	_, location, _ := d.chatTimezoneState(ctx, chatID)
	return location
}

func (d *Dispatcher) chatTimezoneName(ctx context.Context, chatID int64) string {
	name, _, _ := d.chatTimezoneState(ctx, chatID)
	return name
}

// timezoneControl — /timezone: show the effective zone and its source, store a per-chat override, or reset
// to DEFAULT_TIMEZONE. Writing is the same permission rule as /limits: an administrator anywhere, the owner
// of a private chat; a group is managed by an administrator only. Reading is open to every allowed user.
func (d *Dispatcher) timezoneControl(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	current, location, override := d.chatTimezoneState(ctx, ev.ChatID)
	source := i18n.T(lang, "timezone.source_default")
	if override {
		source = i18n.T(lang, "timezone.source_chat")
	}
	action := strings.TrimSpace(arg)
	switch {
	case action == "":
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "timezone.current", current, source, d.deps.Cfg.Timezone,
			timefmt.DateTimeZoned(d.now(), location, lang)))
	case strings.EqualFold(action, "reset") || strings.EqualFold(action, "default"):
		if !d.canManageChatTimezone(ctx, ev) {
			d.logEvent(ctx, ev.UserID, store.EventDenied, "timezone")
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "timezone.denied"))
		}
		if d.deps.DB == nil {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "timezone.usage"))
		}
		if err := store.DeleteChatTimezone(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
		d.logChatEvent(ctx, ev.ChatID, ev.UserID, store.EventCommand, "timezone:reset")
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "timezone.reset", d.deps.Cfg.Timezone))
	default:
		if !d.canManageChatTimezone(ctx, ev) {
			d.logEvent(ctx, ev.UserID, store.EventDenied, "timezone")
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "timezone.denied"))
		}
		name, _, err := store.NormalizeTimezone(action)
		if err != nil {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "timezone.invalid", action))
		}
		if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
			return err
		}
		if err := store.SetChatTimezone(ctx, d.deps.DB, ev.ChatID, name); err != nil {
			if errors.Is(err, store.ErrInvalidTimezone) {
				return d.reply(ctx, ev.ChatID, i18n.T(lang, "timezone.invalid", action))
			}
			return err
		}
		d.logChatEvent(ctx, ev.ChatID, ev.UserID, store.EventCommand, "timezone:set "+name)
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "timezone.set", name))
	}
}

// canManageChatTimezone is the write permission of /timezone and of PUT /api/settings/timezone. It is the
// /limits rule by construction, so a group member who is not an administrator can never move the group's
// display zone (just as they cannot lift its limits).
func (d *Dispatcher) canManageChatTimezone(ctx context.Context, ev Event) bool {
	return d.canManageChatLimits(ctx, ev)
}
