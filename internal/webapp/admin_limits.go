package webapp

// Admin chat limits API of the Mini App.
//
// The endpoints are admin-only by TELEGRAM_ADMIN_USERS (config.IsAdmin); a non-admin gets 403
// admin_required before anything is read or written. The list names every chat the bot knows about —
// a private chat has a positive id, a group a negative one — with today's usage and the current limits;
// the two mutation endpoints set or reset exactly one chat_limits row.
//
// The payload is strict: unknown fields are rejected by the shared decoder, every number is required
// and limited to 0..store.ChatLimitMax (0 = unlimited), and disabled_tools accepts only the known tool
// names. "Unknown chat" is a 404, a bad body a 400, and every successful mutation is audited by the
// generic Mini App mutation log as admin.limits.set / admin.limits.reset.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"bot/internal/store"
)

const (
	adminChatListDefaultLimit = 50
	adminChatListMaxLimit     = 200
)

type adminChatLimits struct {
	DailyModelLimit   int64    `json:"daily_model_limit"`
	DailyNetworkLimit int64    `json:"daily_network_limit"`
	PerMinuteLimit    int64    `json:"per_minute_limit"`
	DisabledTools     []string `json:"disabled_tools"`
}

type adminChatToday struct {
	ModelCalls int64 `json:"model_calls"`
	ReadURL    int64 `json:"read_url"`
	WebSearch  int64 `json:"web_search"`
	Calculator int64 `json:"calculator"`
}

type adminChatItem struct {
	ID           int64           `json:"id"`
	Title        string          `json:"title"`
	Type         string          `json:"type"`
	UserID       int64           `json:"user_id"`
	LastActivity string          `json:"last_activity"`
	Today        adminChatToday  `json:"today"`
	Limits       adminChatLimits `json:"limits"`
	HasLimits    bool            `json:"has_limits"`
}

// adminChatItemFor maps the store row to the wire shape. The disabled-tools list is always an array,
// never null, so the UI can iterate it without a guard.
func adminChatItemFor(chat store.ActiveChat) adminChatItem {
	item := adminChatItem{
		ID: chat.ID, Title: chat.Title, Type: chat.Type, UserID: chat.UserID,
		LastActivity: chat.LastActivity, HasLimits: chat.HasLimits,
		Today: adminChatToday{
			ModelCalls: chat.Today.ModelCalls, ReadURL: chat.Today.ReadURL,
			WebSearch: chat.Today.WebSearch, Calculator: chat.Today.Calculator,
		},
		Limits: adminChatLimits{
			DailyModelLimit: chat.Limits.DailyModelLimit, DailyNetworkLimit: chat.Limits.DailyNetworkLimit,
			PerMinuteLimit: chat.Limits.PerMinuteLimit,
			DisabledTools:  append([]string{}, chat.Limits.DisabledTools...),
		},
	}
	if item.Limits.DisabledTools == nil {
		item.Limits.DisabledTools = []string{}
	}
	return item
}

// adminChats — GET /api/admin/chats?limit=50. Only the `limit` query parameter is accepted.
func (a *App) adminChats(w http.ResponseWriter, req *http.Request, current scope) {
	if !a.cfg.IsAdmin(current.UserID) {
		writeError(w, http.StatusForbidden, "admin_required", "Administrator access is required.")
		return
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		methodAllowed(w, http.MethodGet)
		return
	}
	limit, ok := adminChatListLimit(req)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_limit", "The limit must be an integer from 1 to 200.")
		return
	}
	chats, err := store.ListActiveChats(req.Context(), a.db, limit)
	if err != nil {
		a.queryFailed(w, "admin chats", err)
		return
	}
	items := make([]adminChatItem, 0, len(chats))
	for _, chat := range chats {
		items = append(items, adminChatItemFor(chat))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit})
}

// adminChatListLimit parses the single accepted query parameter. An absent limit means 50; anything
// unknown, repeated or out of range is a 400 instead of a silent default, so a typo in the UI is loud.
func adminChatListLimit(req *http.Request) (int, bool) {
	query := req.URL.Query()
	for name := range query {
		if name != "limit" {
			return 0, false
		}
	}
	values, present := query["limit"]
	if !present {
		return adminChatListDefaultLimit, true
	}
	if len(values) != 1 {
		return 0, false
	}
	limit, err := strconv.Atoi(values[0])
	if err != nil || limit < 1 || limit > adminChatListMaxLimit {
		return 0, false
	}
	return limit, true
}

// adminChatLimitsID extracts {id} from /api/admin/chats/{id}/limits. Only one path segment is accepted;
// anything else is not this route.
func adminChatLimitsID(path string) (int64, bool) {
	const prefix = "/api/admin/chats/"
	const suffix = "/limits"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if raw == "" || strings.Contains(raw, "/") {
		return 0, false
	}
	chatID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || chatID == 0 {
		return 0, false
	}
	return chatID, true
}

// adminChatLimits — PUT/DELETE /api/admin/chats/{id}/limits.
func (a *App) adminChatLimits(w http.ResponseWriter, req *http.Request, current scope, chatID int64) {
	if !a.cfg.IsAdmin(current.UserID) {
		writeError(w, http.StatusForbidden, "admin_required", "Administrator access is required.")
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "Chat limits do not accept query parameters.")
		return
	}
	switch req.Method {
	case http.MethodPut:
		a.setAdminChatLimits(w, req, chatID)
	case http.MethodDelete:
		a.resetAdminChatLimits(w, req, chatID)
	default:
		methodAllowed(w, http.MethodPut, http.MethodDelete)
	}
}

func (a *App) setAdminChatLimits(w http.ResponseWriter, req *http.Request, chatID int64) {
	var input struct {
		DailyModelLimit   *int64    `json:"daily_model_limit"`
		DailyNetworkLimit *int64    `json:"daily_network_limit"`
		PerMinuteLimit    *int64    `json:"per_minute_limit"`
		DisabledTools     *[]string `json:"disabled_tools"`
	}
	if status, ok := decodeSettingsJSON(req, &input); !ok {
		settingsDecodeError(w, status)
		return
	}
	if input.DailyModelLimit == nil || input.DailyNetworkLimit == nil || input.PerMinuteLimit == nil || input.DisabledTools == nil {
		writeError(w, http.StatusBadRequest, "invalid_limits", "All four fields are required: daily_model_limit, daily_network_limit, per_minute_limit, disabled_tools.")
		return
	}
	for _, value := range []int64{*input.DailyModelLimit, *input.DailyNetworkLimit, *input.PerMinuteLimit} {
		if value < 0 || value > store.ChatLimitMax {
			writeError(w, http.StatusBadRequest, "invalid_limits", "Every limit must be an integer from 0 to 1000000 (0 means unlimited).")
			return
		}
	}
	tools, err := store.NormalizeDisabledTools(*input.DisabledTools)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_tools", "disabled_tools may contain only read_url, web_search and calculator.")
		return
	}
	exists, err := store.ChatExists(req.Context(), a.db, chatID)
	if err != nil {
		a.queryFailed(w, "admin chat limits existence", err)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "chat_not_found", "The chat does not exist.")
		return
	}
	limits := store.ChatLimits{
		ChatID:            chatID,
		DailyModelLimit:   *input.DailyModelLimit,
		DailyNetworkLimit: *input.DailyNetworkLimit,
		PerMinuteLimit:    *input.PerMinuteLimit,
		DisabledTools:     tools,
	}
	if err := store.SetChatLimits(req.Context(), a.db, limits); err != nil {
		if errors.Is(err, store.ErrChatNotFound) {
			writeError(w, http.StatusNotFound, "chat_not_found", "The chat does not exist.")
			return
		}
		if errors.Is(err, store.ErrInvalidChatLimits) {
			writeError(w, http.StatusBadRequest, "invalid_limits", "The limits are invalid.")
			return
		}
		a.queryFailed(w, "admin chat limits set", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"id": chatID,
		"limits": adminChatLimits{
			DailyModelLimit: limits.DailyModelLimit, DailyNetworkLimit: limits.DailyNetworkLimit,
			PerMinuteLimit: limits.PerMinuteLimit,
			DisabledTools:  append([]string{}, limits.DisabledTools...),
		},
	})
}

func (a *App) resetAdminChatLimits(w http.ResponseWriter, req *http.Request, chatID int64) {
	if status, ok := emptySettingsBody(req); !ok {
		settingsDecodeError(w, status)
		return
	}
	exists, err := store.ChatExists(req.Context(), a.db, chatID)
	if err != nil {
		a.queryFailed(w, "admin chat limits existence", err)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "chat_not_found", "The chat does not exist.")
		return
	}
	if _, err := store.ResetChatLimits(req.Context(), a.db, chatID); err != nil {
		a.queryFailed(w, "admin chat limits reset", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": chatID})
}
