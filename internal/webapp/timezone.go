package webapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"bot/internal/bot"
	"bot/internal/config"
	"bot/internal/store"
)

// timezoneSettings is the wire shape shared by GET /api/settings and GET /api/status. current is the
// effective zone; default is DEFAULT_TIMEZONE; source is "chat" for a stored override and "global" for the
// deployment default (the same vocabulary the model/stream settings already use).
type timezoneSettings struct {
	Current string `json:"current"`
	Default string `json:"default"`
	Source  string `json:"source"`
}

func (a *App) timezoneSettingsFor(ctx context.Context, chatID int64) (timezoneSettings, *time.Location, error) {
	name, location, override, err := bot.EffectiveTimezone(ctx, a.db, a.cfg, chatID)
	if err != nil {
		return timezoneSettings{}, nil, err
	}
	source := "global"
	if override {
		source = "chat"
	}
	defaultName := strings.TrimSpace(a.cfg.Timezone)
	if defaultName == "" {
		defaultName = config.DefaultTimezone
	}
	return timezoneSettings{Current: name, Default: defaultName, Source: source}, location, nil
}

// canManageTimezone is the write permission of PUT /api/settings/timezone: an administrator anywhere; in a
// private chat its owner too. A group is administrator-only, the same rule as /limits, so a member cannot
// change the group's display zone through a crafted session.
func (a *App) canManageTimezone(ctx context.Context, userID int64, chatID int64) bool {
	if a.cfg.IsAdmin(userID) {
		return true
	}
	if chatID < 0 {
		return false
	}
	if chatID == userID {
		return true
	}
	owner, found, err := store.ChatOwner(ctx, a.db, chatID)
	if err != nil {
		a.logger.Error("the chat owner could not be read for the timezone permission", "chat_id", chatID, "error", err)
		return false
	}
	return found && owner == userID
}

// settingsTimezone — PUT /api/settings/timezone with a strict body {"timezone":"Asia/Yekaterinburg"}; an
// empty string resets the override to DEFAULT_TIMEZONE. An unknown or malformed IANA name is a 400, never a
// silent fallback: a typo in the UI must be loud. A successful set/reset is audited as
// settings.timezone.set / settings.timezone.reset by the generic Mini App mutation log.
func (a *App) settingsTimezone(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPut {
		methodAllowed(w, http.MethodPut)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "Timezone settings do not accept query parameters.")
		return
	}
	var input struct {
		Timezone *string `json:"timezone"`
	}
	if status, ok := decodeSettingsJSON(req, &input); !ok {
		settingsDecodeError(w, status)
		return
	}
	if input.Timezone == nil {
		writeError(w, http.StatusBadRequest, "invalid_timezone", "Set timezone to an IANA name or an empty string to reset.")
		return
	}
	if !a.canManageTimezone(req.Context(), current.UserID, current.ChatID) {
		writeError(w, http.StatusForbidden, "timezone_forbidden", "Only the chat owner or an administrator can change the timezone.")
		return
	}
	if err := store.EnsureChat(req.Context(), a.db, current.ChatID, current.UserID, ""); err != nil {
		a.queryFailed(w, "settings ensure chat", err)
		return
	}
	value := strings.TrimSpace(*input.Timezone)
	if value == "" {
		if err := store.DeleteChatTimezone(req.Context(), a.db, current.ChatID); err != nil {
			a.queryFailed(w, "settings reset timezone", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	name, _, err := store.NormalizeTimezone(value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_timezone", "Use a valid IANA timezone name, for example Asia/Yekaterinburg.")
		return
	}
	if err := store.SetChatTimezone(req.Context(), a.db, current.ChatID, name); err != nil {
		if errors.Is(err, store.ErrInvalidTimezone) {
			writeError(w, http.StatusBadRequest, "invalid_timezone", "Use a valid IANA timezone name, for example Asia/Yekaterinburg.")
			return
		}
		a.queryFailed(w, "settings set timezone", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
