package webapp

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bot/internal/bot"
	"bot/internal/store"
)

const (
	defaultPageSize = 10
	maxPageSize     = 20
	maxHistoryRunes = 2000
	maxLinkRunes    = 800
	maxTaskRunes    = 1000
)

type page struct {
	Before int64
	Limit  int
}

func readPage(req *http.Request) (page, bool) {
	result := page{Limit: defaultPageSize}
	for key, values := range req.URL.Query() {
		if len(values) != 1 || (key != "before" && key != "limit") {
			return page{}, false
		}
		value := values[0]
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return page{}, false
		}
		switch key {
		case "before":
			if number <= 0 {
				return page{}, false
			}
			result.Before = number
		case "limit":
			if number < 1 || number > maxPageSize {
				return page{}, false
			}
			result.Limit = int(number)
		}
	}
	return result, true
}

func (a *App) queryFailed(w http.ResponseWriter, operation string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	a.logger.Error("Mini App read query failed", "operation", operation, "error", err)
	writeError(w, http.StatusInternalServerError, "read_failed", "The data could not be read.")
}

func boundedText(text string, maximum int) (string, bool) {
	if !utf8.ValidString(text) {
		return "", true
	}
	runes := []rune(text)
	if len(runes) <= maximum {
		return text, false
	}
	return string(runes[:maximum]) + "…", true
}

func nextID(count int, limit int, id int64) *int64 {
	if count <= limit {
		return nil
	}
	return &id
}

// chatBodyError mirrors the strict-JSON envelope of the settings endpoints for the chat routes.
func chatBodyError(w http.ResponseWriter, status int) {
	if status == http.StatusUnsupportedMediaType {
		writeError(w, status, "content_type", "Send a strict application/json body.")
		return
	}
	if status == http.StatusRequestEntityTooLarge {
		writeError(w, status, "body_too_large", "Request body is too large.")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON or unknown fields.")
}

// chatErrorMessage — the text shown for a failure after admission. Secrets are stripped and the
// message is bounded; the stream never carries a provider body or an authenticated URL.
func (a *App) chatErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	for _, secret := range []string{a.cfg.BotToken, a.cfg.StrataKey, a.cfg.WebhookSecretToken} {
		if secret = strings.TrimSpace(secret); secret != "" {
			text = strings.ReplaceAll(text, secret, "***")
		}
	}
	text, _ = boundedText(text, 300)
	if strings.TrimSpace(text) == "" {
		return "The chat request could not be completed. Retry."
	}
	return text
}

// writeChatFailure maps the shared admission sentinels of the bot package to HTTP statuses. It is
// only used before the first SSE event; after that the failure travels as an `event: error`.
func (a *App) writeChatFailure(w http.ResponseWriter, operation string, code string, err error) {
	var limited *bot.MiniAppRateLimitError
	switch {
	case errors.Is(err, bot.ErrMiniAppDenied):
		writeError(w, http.StatusForbidden, "chat_forbidden", "The chat is available only to allowlisted users.")
	case errors.As(err, &limited):
		seconds := int64((limited.RetryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests. Try again later.")
	case errors.Is(err, bot.ErrMiniAppEmptyText), errors.Is(err, bot.ErrMiniAppTextTooLong):
		writeError(w, http.StatusBadRequest, "invalid_text", "Send a nonempty message of at most 2000 runes.")
	case errors.Is(err, bot.ErrMiniAppUnavailable):
		writeError(w, http.StatusServiceUnavailable, "chat_unavailable", "The chat is unavailable in this Mini App deployment.")
	case errors.Is(err, context.Canceled):
		return
	default:
		a.logger.Error("Mini App chat action failed", "operation", operation, "error", a.chatErrorMessage(err))
		writeError(w, http.StatusInternalServerError, code, "The chat action could not be completed. Retry.")
	}
}

// chatMessage — POST /api/messages. The client sends `{"text":"..."}` and receives an SSE stream:
//
//	event: stage  {"stage":"thinking"|"read_url"|"web_search"}
//	event: delta  {"text":"fragment"}
//	event: done   {"text":"final answer, already stored in history","message_id":123}
//	event: error  {"code":"chat_failed","message":"safe text"}
//
// message_id is the stored assistant row id, so the client can send feedback for this exact
// answer without refetching /api/history. It is omitted when a backend did not produce a row.
//
// Admission failures are ordinary JSON errors with their real status, because no SSE header has
// been written yet at that point.
func (a *App) chatMessage(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Message sending does not accept query parameters.")
		return
	}
	var input struct {
		Text string `json:"text"`
	}
	if status, ok := decodeSettingsJSON(req, &input); !ok {
		chatBodyError(w, status)
		return
	}
	text := strings.TrimSpace(input.Text)
	if text == "" || !utf8.ValidString(text) {
		writeError(w, http.StatusBadRequest, "invalid_text", "Send a nonempty message of at most 2000 runes.")
		return
	}
	if len([]rune(text)) > bot.MiniAppMaxTextRunes || strings.ContainsRune(text, '\x00') {
		writeError(w, http.StatusBadRequest, "text_too_long", "The message must be at most 2000 runes.")
		return
	}
	if a.chat == nil {
		writeError(w, http.StatusServiceUnavailable, "chat_unavailable", "The chat is unavailable in this Mini App deployment.")
		return
	}

	// http.Server.WriteTimeout is 10s, which is shorter than one answer. Only this response gets a
	// longer write deadline (see chatStreamWriteBudget in server.go); the listener stays at 10s.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(chatStreamWriteBudget(a.cfg)))
	stream := newSSEResponse(w)
	err := a.chat.AnswerForMiniApp(req.Context(), current.ChatID, current.UserID, text, func(event bot.MiniAppStreamEvent) error {
		switch {
		case event.Error != "":
			stream.errorSeen = true
			return stream.send("error", map[string]string{"code": "chat_failed", "message": event.Error})
		case event.Approval != nil:
			return stream.send("approval", map[string]any{
				"token":              event.Approval.Token,
				"tool":               event.Approval.Tool,
				"summary":            event.Approval.Summary,
				"expires_in_seconds": event.Approval.ExpiresInSeconds,
			})
		case event.Stage != "":
			return stream.send("stage", map[string]string{"stage": event.Stage})
		case event.Done:
			payload := map[string]any{"text": event.Delta}
			if event.MessageID > 0 {
				payload["message_id"] = event.MessageID
			}
			return stream.send("done", payload)
		case event.Delta != "":
			return stream.send("delta", map[string]string{"text": event.Delta})
		default:
			return nil
		}
	})
	if err == nil {
		if !stream.started {
			// A backend that produced no events must not answer with an empty 200.
			_ = stream.send("done", map[string]string{"text": ""})
		}
		return
	}
	if errors.Is(err, context.Canceled) && req.Context().Err() != nil {
		return
	}
	if !stream.started {
		a.writeChatFailure(w, "messages", "chat_failed", err)
		return
	}
	// The dispatcher emits its own sanitized error event. A backend that failed without emitting
	// one still gets a fallback, and neither path reports the failure twice.
	if !stream.errorSeen {
		stream.errorSeen = true
		_ = stream.send("error", map[string]string{"code": "chat_failed", "message": a.chatErrorMessage(err)})
	}
}

// chatApproval — POST /api/chat/approval `{"token":"…","approve":true|false}`. It delivers the
// decision to the stream blocked in /api/messages and returns immediately; the stream is the one
// that resumes and sends `delta`/`done` afterwards. The token belongs to the authenticated session
// chat AND user (any mismatch is 404), expired or already used tokens are 410, and a bad body is
// 400. No response body is streamed here.
func (a *App) chatApproval(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Approval decisions do not accept query parameters.")
		return
	}
	var input struct {
		Token   string `json:"token"`
		Approve *bool  `json:"approve"`
	}
	if status, ok := decodeSettingsJSON(req, &input); !ok {
		chatBodyError(w, status)
		return
	}
	if input.Token == "" || len(input.Token) > 64 || input.Approve == nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "The token and approve fields are required.")
		return
	}
	if a.chat == nil {
		writeError(w, http.StatusServiceUnavailable, "chat_unavailable", "The chat is unavailable in this Mini App deployment.")
		return
	}
	if allowed, retryAfter := a.rateLimits.allowApproval(current.UserID); !allowed {
		writeRateLimited(w, retryAfter)
		return
	}
	err := a.chat.ResolveMiniAppApproval(req.Context(), input.Token, current.ChatID, current.UserID, *input.Approve)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, bot.ErrMiniAppApprovalExpired):
		writeError(w, http.StatusGone, "approval_expired", "This network approval has expired or was already used.")
	case errors.Is(err, bot.ErrMiniAppApprovalNotFound):
		writeError(w, http.StatusNotFound, "approval_not_found", "This network approval does not exist for this chat.")
	default:
		a.logger.Error("Mini App approval decision failed", "chat_id", current.ChatID, "error", a.chatErrorMessage(err))
		writeError(w, http.StatusInternalServerError, "approval_failed", "The approval decision could not be delivered.")
	}
}

// resetChatContext — POST /api/context/reset, the Mini App /reset. The message rows stay; the
// model simply stops seeing them (store.ClearChat moves the context watermark).
func (a *App) resetChatContext(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Context reset does not accept query parameters.")
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		if status == http.StatusRequestEntityTooLarge {
			writeError(w, status, "body_too_large", "Request body is too large.")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_body", "Context reset accepts an empty request body.")
		return
	}
	if a.chat == nil {
		writeError(w, http.StatusServiceUnavailable, "chat_unavailable", "The chat is unavailable in this Mini App deployment.")
		return
	}
	if err := a.chat.ResetContextForMiniApp(req.Context(), current.ChatID, current.UserID); err != nil {
		a.writeChatFailure(w, "context.reset", "chat_mutation_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// skipPendingChat — POST /api/pending/skip `{"skip":true|false}`, the Mini App /skip and /unskip.
// `changed` tells the UI whether there was a pending question to suppress (or a suppression to
// clear), so "nothing to skip" is not an error.
func (a *App) skipPendingChat(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Pending recovery controls do not accept query parameters.")
		return
	}
	var input struct {
		Skip *bool `json:"skip"`
	}
	if status, ok := decodeSettingsJSON(req, &input); !ok {
		chatBodyError(w, status)
		return
	}
	if input.Skip == nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "The skip field is required.")
		return
	}
	if a.chat == nil {
		writeError(w, http.StatusServiceUnavailable, "chat_unavailable", "The chat is unavailable in this Mini App deployment.")
		return
	}
	changed, err := a.chat.SkipPendingForMiniApp(req.Context(), current.ChatID, current.UserID, *input.Skip)
	if err != nil {
		a.writeChatFailure(w, "pending.skip", "chat_mutation_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed})
}

// miniAppWhoami — GET /api/whoami, the Mini App /whoami. It answers from the authenticated
// session only, so a client cannot ask about another chat.
func (a *App) miniAppWhoami(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodGet {
		methodAllowed(w, http.MethodGet)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "whoami does not accept query parameters.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":         current.UserID,
		"chat_id":         current.ChatID,
		"allowed":         a.cfg.Allows(current.UserID),
		"deny_by_default": a.cfg.DenyByDefault,
	})
}

func (a *App) status(w http.ResponseWriter, req *http.Request, current scope) {
	var response struct {
		Messages           int64  `json:"messages"`
		Documents          int64  `json:"documents"`
		Links              int64  `json:"links"`
		Tasks              int64  `json:"tasks"`
		ActiveTasks        int64  `json:"active_tasks"`
		Pending            bool   `json:"pending"`
		RecoverySuppressed bool   `json:"recovery_suppressed"`
		LastActivity       string `json:"last_activity,omitempty"`
	}
	var lastActivity *string
	err := a.db.QueryRowContext(req.Context(), `
		SELECT
			(SELECT COUNT(*) FROM messages WHERE chat_id = ?),
			(SELECT COUNT(*) FROM rag_documents WHERE chat_id = ?),
			(SELECT COUNT(*) FROM saved_links WHERE chat_id = ?),
			(SELECT COUNT(*) FROM tasks WHERE chat_id = ?),
			(SELECT COUNT(*) FROM tasks WHERE chat_id = ? AND status IN ('ready', 'running', 'awaiting_approval')),
			(SELECT MAX(created_at) FROM messages WHERE chat_id = ?)`,
		current.ChatID, current.ChatID, current.ChatID, current.ChatID, current.ChatID, current.ChatID).
		Scan(&response.Messages, &response.Documents, &response.Links, &response.Tasks, &response.ActiveTasks, &lastActivity)
	if err != nil {
		a.queryFailed(w, "status", err)
		return
	}
	rowID, visibleRole, err := store.LastVisibleMessage(req.Context(), a.db, current.ChatID)
	if err != nil {
		a.queryFailed(w, "status last visible message", err)
		return
	}
	response.Pending = rowID > 0 && visibleRole == store.RoleUser
	if response.Pending {
		skipped, suppress, err := store.ResumeSkip(req.Context(), a.db, current.ChatID)
		if err != nil {
			a.queryFailed(w, "status resume skip", err)
			return
		}
		response.RecoverySuppressed = suppress && skipped == rowID
	}
	if lastActivity != nil {
		response.LastActivity = *lastActivity
	}
	writeJSON(w, http.StatusOK, response)
}

type historyItem struct {
	ID        int64  `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	CreatedAt string `json:"created_at"`
	// Feedback — the current user's rating of this assistant answer: 1, -1 or 0 when unrated.
	Feedback int `json:"feedback"`
}

// attachHistoryFeedback fills the current user's ratings of the given page in one query.
func (a *App) attachHistoryFeedback(req *http.Request, current scope, items []historyItem) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	ratings, err := store.MessageFeedbackFor(req.Context(), a.db, current.ChatID, current.UserID, ids)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].Feedback = ratings[items[i].ID]
	}
	return nil
}

func (a *App) history(w http.ResponseWriter, req *http.Request, current scope) {
	page, ok := readPage(req)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_pagination", "Use only a positive before ID and a limit from 1 to 20.")
		return
	}
	rows, err := a.db.QueryContext(req.Context(), `
		SELECT id, role, content, created_at FROM messages
		WHERE chat_id = ? AND role IN ('user', 'assistant') AND (? = 0 OR id < ?)
		ORDER BY id DESC LIMIT ?`, current.ChatID, page.Before, page.Before, page.Limit+1)
	if err != nil {
		a.queryFailed(w, "history", err)
		return
	}
	defer rows.Close()
	items := []historyItem{}
	for rows.Next() {
		var item historyItem
		if err := rows.Scan(&item.ID, &item.Role, &item.Content, &item.CreatedAt); err != nil {
			a.queryFailed(w, "history", err)
			return
		}
		item.Content, item.Truncated = boundedText(item.Content, maxHistoryRunes)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		a.queryFailed(w, "history", err)
		return
	}
	var next *int64
	if len(items) > page.Limit {
		next = nextID(len(items), page.Limit, items[page.Limit-1].ID)
		items = items[:page.Limit]
	}
	if err := a.attachHistoryFeedback(req, current, items); err != nil {
		a.queryFailed(w, "history feedback", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_before_id": next})
}

func strictQueryValue(req *http.Request, name string) (string, bool) {
	values, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil || len(values) != 1 {
		return "", false
	}
	entries, found := values[name]
	if !found || len(entries) != 1 {
		return "", false
	}
	return entries[0], true
}

func canonicalIDQuery(req *http.Request) (int64, bool) {
	raw, ok := strictQueryValue(req, "id")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, false
	}
	return id, true
}

func invalidItemQuery(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_query", "Use exactly one positive canonical id.")
}

func (a *App) searchHistory(w http.ResponseWriter, req *http.Request, current scope) {
	query, ok := strictQueryValue(req, "q")
	if !ok || strings.TrimSpace(query) == "" || !utf8.ValidString(query) || len([]rune(query)) > store.RAGQueryMaxRunes {
		writeError(w, http.StatusBadRequest, "invalid_query", "Use exactly one nonempty q of at most 4000 runes.")
		return
	}
	rows, err := store.SearchMessages(req.Context(), a.db, current.ChatID, query, bot.SearchRowLimit+1)
	if err != nil {
		a.queryFailed(w, "history search", err)
		return
	}
	// `truncated` now means "older matches were left out", not "the search looked at a window": the FTS index
	// covers the whole history, so there is no unscanned tail to warn about.
	corpusTruncated := len(rows) > bot.SearchRowLimit
	if corpusTruncated {
		// SearchMessages answers oldest first, so the newest limit matches are the TAIL of the slice.
		rows = rows[len(rows)-bot.SearchRowLimit:]
	}
	items := make([]historyItem, 0, len(rows))
	for _, row := range rows {
		item := historyItem{ID: row.ID, Role: row.Role, Content: row.Content, CreatedAt: row.CreatedAt}
		item.Content, item.Truncated = boundedText(item.Content, maxHistoryRunes)
		items = append(items, item)
	}
	if err := a.attachHistoryFeedback(req, current, items); err != nil {
		a.queryFailed(w, "history search feedback", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "truncated": corpusTruncated})
}

func (a *App) historyByID(w http.ResponseWriter, req *http.Request, current scope) {
	id, ok := canonicalIDQuery(req)
	if !ok {
		invalidItemQuery(w)
		return
	}
	var item historyItem
	err := a.db.QueryRowContext(req.Context(), `
		SELECT id, role, content, created_at FROM messages WHERE chat_id = ? AND id = ?`, current.ChatID, id).
		Scan(&item.ID, &item.Role, &item.Content, &item.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Message not found.")
		return
	}
	if err != nil {
		a.queryFailed(w, "history item", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *App) exportHistory(w http.ResponseWriter, req *http.Request, current scope) {
	format, ok := strictQueryValue(req, "format")
	if !ok || format != "md" && format != "json" {
		writeError(w, http.StatusBadRequest, "invalid_query", "Use exactly one format of md or json.")
		return
	}
	data, err := bot.ExportHistoryData(req.Context(), a.db, current.ChatID, format)
	if errors.Is(err, bot.ErrHistoryExportTooLarge) || len(data) > bot.ExportMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "export_too_large", "History export exceeds 5 MiB.")
		return
	}
	if errors.Is(err, bot.ErrHistoryExportFormat) {
		writeError(w, http.StatusBadRequest, "invalid_query", "Use exactly one format of md or json.")
		return
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, req.Context().Err()) {
			a.logger.Error("Mini App history export failed", "error", err)
			writeError(w, http.StatusInternalServerError, "export_failed", "The history could not be exported.")
		}
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusNotFound, "history_empty", "There are no messages to export.")
		return
	}
	contentType := "text/markdown; charset=utf-8"
	if format == "json" {
		contentType = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="history-`+strconv.FormatInt(current.ChatID, 10)+`.`+format+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func deleteHistoryID(path string) (int64, bool) {
	if !strings.HasPrefix(path, "/api/history/") || !strings.HasSuffix(path, "/delete") {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, "/api/history/"), "/delete")
	if raw == "" || strings.Contains(raw, "/") {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, false
	}
	return id, true
}

func historyBodyError(w http.ResponseWriter, status int) {
	if status == http.StatusRequestEntityTooLarge {
		writeError(w, status, "body_too_large", "Request body is too large.")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_body", "History controls accept an empty request body.")
}

func (a *App) historyMutationFailed(w http.ResponseWriter, req *http.Request, operation string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, req.Context().Err()) {
		return
	}
	a.logger.Error("Mini App history mutation failed", "operation", operation, "error", err)
	writeError(w, http.StatusInternalServerError, "history_mutation_failed", "The history could not be changed.")
}

func (a *App) clearHistory(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "History controls do not accept query parameters.")
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		historyBodyError(w, status)
		return
	}
	deleted, err := store.DeleteChatHistory(req.Context(), a.db, current.ChatID)
	if err != nil {
		a.historyMutationFailed(w, req, "clear", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": deleted})
}

func (a *App) deleteHistory(w http.ResponseWriter, req *http.Request, current scope) {
	messageID, valid := deleteHistoryID(req.URL.Path)
	if !valid {
		writeError(w, http.StatusNotFound, "not_found", "API endpoint not found.")
		return
	}
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "History controls do not accept query parameters.")
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		historyBodyError(w, status)
		return
	}
	deleted, err := store.DeleteMessage(req.Context(), a.db, current.ChatID, messageID)
	if err != nil {
		a.historyMutationFailed(w, req, "delete", err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "not_found", "Message not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": messageID})
}

func (a *App) historyControl(w http.ResponseWriter, req *http.Request, current scope) {
	if req.URL.Path == "/api/history/clear" {
		a.clearHistory(w, req, current)
		return
	}
	if messageID, ok := feedbackMessageID(req.URL.Path); ok {
		a.messageFeedback(w, req, current, messageID)
		return
	}
	a.deleteHistory(w, req, current)
}

// feedbackMessageID parses /api/history/{id}/feedback with the same canonical-id rule as /delete.
func feedbackMessageID(path string) (int64, bool) {
	if !strings.HasPrefix(path, "/api/history/") || !strings.HasSuffix(path, "/feedback") {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, "/api/history/"), "/feedback")
	if raw == "" || strings.Contains(raw, "/") {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, false
	}
	return id, true
}

// messageFeedback — POST /api/history/{id}/feedback {"value":1|-1}. The chat and the user come only
// from the authenticated session; the body cannot name another chat.
func (a *App) messageFeedback(w http.ResponseWriter, req *http.Request, current scope, messageID int64) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Feedback does not accept query parameters.")
		return
	}
	var input struct {
		Value *int `json:"value"`
	}
	if status, ok := decodeSettingsJSON(req, &input); !ok {
		chatBodyError(w, status)
		return
	}
	if input.Value == nil || (*input.Value != 1 && *input.Value != -1) {
		writeError(w, http.StatusBadRequest, "invalid_body", "The feedback value must be 1 or -1.")
		return
	}
	if _, err := store.SetMessageFeedback(req.Context(), a.db, current.ChatID, current.UserID, messageID, *input.Value); err != nil {
		switch {
		case errors.Is(err, store.ErrFeedbackNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Message not found.")
		case errors.Is(err, store.ErrFeedbackNotAllowed):
			writeError(w, http.StatusForbidden, "feedback_forbidden", "Only an assistant message of this chat can be rated.")
		case errors.Is(err, store.ErrFeedbackValue):
			writeError(w, http.StatusBadRequest, "invalid_body", "The feedback value must be 1 or -1.")
		default:
			a.historyMutationFailed(w, req, "feedback", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "value": *input.Value})
}

type documentItem struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	Chunks    int    `json:"chunks"`
	CreatedAt string `json:"created_at"`
}

func (a *App) documents(w http.ResponseWriter, req *http.Request, current scope) {
	page, ok := readPage(req)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_pagination", "Use only a positive before ID and a limit from 1 to 20.")
		return
	}
	rows, err := a.db.QueryContext(req.Context(), `
		SELECT d.id, d.name, d.size_bytes, d.created_at, COUNT(c.id)
		FROM rag_documents d LEFT JOIN rag_chunks c ON c.document_id = d.id
		WHERE d.chat_id = ? AND (? = 0 OR d.id < ?)
		GROUP BY d.id ORDER BY d.id DESC LIMIT ?`, current.ChatID, page.Before, page.Before, page.Limit+1)
	if err != nil {
		a.queryFailed(w, "documents", err)
		return
	}
	defer rows.Close()
	items := []documentItem{}
	for rows.Next() {
		var item documentItem
		if err := rows.Scan(&item.ID, &item.Name, &item.SizeBytes, &item.CreatedAt, &item.Chunks); err != nil {
			a.queryFailed(w, "documents", err)
			return
		}
		item.Name, _ = boundedText(item.Name, 240)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		a.queryFailed(w, "documents", err)
		return
	}
	var next *int64
	if len(items) > page.Limit {
		next = nextID(len(items), page.Limit, items[page.Limit-1].ID)
		items = items[:page.Limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_before_id": next})
}

type documentSearchItem struct {
	DocumentID int64  `json:"document_id"`
	Name       string `json:"name"`
	Score      int    `json:"score"`
	Content    string `json:"content"`
}

type documentSearchResponse struct {
	Items  []documentSearchItem `json:"items"`
	Corpus struct {
		Scanned   int  `json:"scanned"`
		Total     int  `json:"total"`
		Truncated bool `json:"truncated"`
	} `json:"corpus"`
}

func (a *App) searchDocuments(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodGet {
		methodAllowed(w, http.MethodGet)
		return
	}
	values, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil || len(values) != 1 {
		writeError(w, http.StatusBadRequest, "invalid_query", "Use exactly one nonempty q of at most 4000 runes.")
		return
	}
	queries, found := values["q"]
	if !found || len(queries) != 1 {
		writeError(w, http.StatusBadRequest, "invalid_query", "Use exactly one nonempty q of at most 4000 runes.")
		return
	}
	query := queries[0]
	if strings.TrimSpace(query) == "" || !utf8.ValidString(query) || len([]rune(query)) > store.RAGQueryMaxRunes {
		writeError(w, http.StatusBadRequest, "invalid_query", "Use exactly one nonempty q of at most 4000 runes.")
		return
	}
	options := store.SearchOptions{Limit: bot.RAGResultLimit, MaxRunes: bot.RAGMaxRunes}
	if a.embeddings != nil {
		// One embedding call per request; a failure is logged once and the lexical search answers.
		vectors, embedErr := a.embeddings.Embed(req.Context(), []string{query})
		if embedErr != nil {
			a.logger.Warn("Mini App RAG query embedding failed, the search continues lexically", "error", embedErr)
		} else if len(vectors) == 1 && len(vectors[0]) > 0 {
			options.QueryVector = vectors[0]
			options.EmbeddingModel = a.embeddings.Model()
			options.EmbeddingWeight = store.DefaultEmbeddingWeight
		}
	}
	result, err := store.SearchRAGChunksWithOptions(req.Context(), a.db, current.ChatID, query, options)
	if err != nil {
		a.queryFailed(w, "document search", err)
		return
	}
	response := documentSearchResponse{Items: make([]documentSearchItem, 0, len(result.Chunks))}
	for _, chunk := range result.Chunks {
		response.Items = append(response.Items, documentSearchItem{
			DocumentID: chunk.DocumentID, Name: chunk.Name, Score: chunk.Score, Content: chunk.Content,
		})
	}
	response.Corpus.Scanned = result.ScannedChunks
	response.Corpus.Total = result.TotalChunks
	response.Corpus.Truncated = result.CorpusTruncated
	writeJSON(w, http.StatusOK, response)
}

func documentBodyError(w http.ResponseWriter, status int) {
	if status == http.StatusRequestEntityTooLarge {
		writeError(w, status, "body_too_large", "Request body is too large.")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_body", "Document controls accept an empty request body.")
}

func (a *App) documentMutationFailed(w http.ResponseWriter, req *http.Request, operation string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, req.Context().Err()) {
		return
	}
	a.logger.Error("Mini App document mutation failed", "operation", operation, "error", err)
	writeError(w, http.StatusInternalServerError, "document_mutation_failed", "The documents could not be changed.")
}

func (a *App) clearDocuments(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Document controls do not accept query parameters.")
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		documentBodyError(w, status)
		return
	}
	deleted, err := store.ClearRAGDocuments(req.Context(), a.db, current.ChatID)
	if err != nil {
		a.documentMutationFailed(w, req, "clear", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": deleted})
}

func deleteDocumentID(path string) (int64, bool) {
	if !strings.HasPrefix(path, "/api/documents/") || !strings.HasSuffix(path, "/delete") {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, "/api/documents/"), "/delete")
	if raw == "" || strings.Contains(raw, "/") {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, false
	}
	return id, true
}

func (a *App) deleteDocument(w http.ResponseWriter, req *http.Request, current scope) {
	documentID, valid := deleteDocumentID(req.URL.Path)
	if !valid {
		writeError(w, http.StatusNotFound, "not_found", "API endpoint not found.")
		return
	}
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Document controls do not accept query parameters.")
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		documentBodyError(w, status)
		return
	}
	deleted, err := store.DeleteRAGDocument(req.Context(), a.db, current.ChatID, documentID)
	if err != nil {
		a.documentMutationFailed(w, req, "delete", err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "not_found", "Document not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": documentID})
}

func (a *App) documentControl(w http.ResponseWriter, req *http.Request, current scope) {
	if req.URL.Path == "/api/documents/clear" {
		a.clearDocuments(w, req, current)
		return
	}
	a.deleteDocument(w, req, current)
}

type linkItem struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	UpdatedAt string `json:"updated_at"`
}

func makeLinkItem(link store.SavedLink) linkItem {
	item := linkItem{ID: link.ID, URL: link.CanonicalURL, UpdatedAt: reminderTime(link.UpdatedAt)}
	item.Title, _ = boundedText(link.Title, store.SavedLinkMaxTitleRunes)
	item.Content, item.Truncated = boundedText(link.Content, maxLinkRunes)
	return item
}

func managementDecodeError(w http.ResponseWriter, status int) {
	switch status {
	case http.StatusUnsupportedMediaType:
		writeError(w, status, "content_type", "Send a strict application/json body.")
	case http.StatusRequestEntityTooLarge:
		writeError(w, status, "body_too_large", "Request body is too large.")
	default:
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON or unknown fields.")
	}
}

func writeLifecycleUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "lifecycle_unavailable", "This action is unavailable in the standalone Mini App.")
}

func (a *App) searchLinks(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodGet {
		methodAllowed(w, http.MethodGet)
		return
	}
	query, ok := strictQueryValue(req, "q")
	if !ok || strings.TrimSpace(query) == "" || !utf8.ValidString(query) || len([]rune(query)) > store.SavedLinkSearchMaxRunes {
		writeError(w, http.StatusBadRequest, "invalid_query", "Use exactly one nonempty q of at most 200 runes.")
		return
	}
	links, err := store.SearchSavedLinks(req.Context(), a.db, current.ChatID, query, store.SavedLinkListLimit)
	if err != nil {
		a.queryFailed(w, "link search", err)
		return
	}
	items := make([]linkItem, 0, len(links))
	for _, link := range links {
		items = append(items, makeLinkItem(link))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) createLink(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Link creation does not accept query parameters.")
		return
	}
	var input struct {
		URL string `json:"url"`
	}
	if status, ok := decodeSettingsJSON(req, &input); !ok {
		managementDecodeError(w, status)
		return
	}
	rawURL := strings.TrimSpace(input.URL)
	if rawURL == "" || !utf8.ValidString(rawURL) || len(rawURL) > store.SavedLinkMaxURLBytes || strings.ContainsRune(rawURL, '\x00') {
		writeError(w, http.StatusBadRequest, "invalid_url", "Use one nonempty HTTPS URL of at most 2048 bytes.")
		return
	}
	created, _, err := a.linkLifecycle.CreateLink(req.Context(), current.ChatID, current.UserID, rawURL)
	if errors.Is(err, errLifecycleUnavailable) {
		writeLifecycleUnavailable(w)
		return
	}
	if errors.Is(err, bot.ErrLinkReaderDisabled) {
		writeError(w, http.StatusForbidden, "url_reader_disabled", "Saving a link reads the page; the URL reader is disabled in this deployment.")
		return
	}
	if errors.Is(err, store.ErrSavedLinkLimit) {
		writeError(w, http.StatusConflict, "link_limit", "The saved link limit for this chat has been reached.")
		return
	}
	if errors.Is(err, store.ErrSavedLinkInput) {
		writeError(w, http.StatusBadRequest, "invalid_url", "The saved link is invalid.")
		return
	}
	if err != nil {
		a.linkMutationFailed(w, req, "create", err)
		return
	}
	writeJSON(w, http.StatusOK, makeLinkItem(created))
}

func (a *App) links(w http.ResponseWriter, req *http.Request, current scope) {
	page, ok := readPage(req)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_pagination", "Use only a positive before ID and a limit from 1 to 20.")
		return
	}
	rows, err := a.db.QueryContext(req.Context(), `
		SELECT id, title, canonical_url, content, updated_at FROM saved_links
		WHERE chat_id = ? AND (? = 0 OR id < ?)
		ORDER BY id DESC LIMIT ?`, current.ChatID, page.Before, page.Before, page.Limit+1)
	if err != nil {
		a.queryFailed(w, "links", err)
		return
	}
	defer rows.Close()
	items := []linkItem{}
	for rows.Next() {
		var item linkItem
		if err := rows.Scan(&item.ID, &item.Title, &item.URL, &item.Content, &item.UpdatedAt); err != nil {
			a.queryFailed(w, "links", err)
			return
		}
		item.Title, _ = boundedText(item.Title, 200)
		item.Content, item.Truncated = boundedText(item.Content, maxLinkRunes)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		a.queryFailed(w, "links", err)
		return
	}
	var next *int64
	if len(items) > page.Limit {
		next = nextID(len(items), page.Limit, items[page.Limit-1].ID)
		items = items[:page.Limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_before_id": next})
}

func (a *App) linkByID(w http.ResponseWriter, req *http.Request, current scope) {
	id, ok := canonicalIDQuery(req)
	if !ok {
		invalidItemQuery(w)
		return
	}
	var item linkItem
	err := a.db.QueryRowContext(req.Context(), `
		SELECT id, title, canonical_url, content, updated_at FROM saved_links WHERE chat_id = ? AND id = ?`, current.ChatID, id).
		Scan(&item.ID, &item.Title, &item.URL, &item.Content, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Saved link not found.")
		return
	}
	if err != nil {
		a.queryFailed(w, "link item", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func linkBodyError(w http.ResponseWriter, status int) {
	if status == http.StatusRequestEntityTooLarge {
		writeError(w, status, "body_too_large", "Request body is too large.")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_body", "Link controls accept an empty request body.")
}

func (a *App) linkMutationFailed(w http.ResponseWriter, req *http.Request, operation string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, req.Context().Err()) {
		return
	}
	a.logger.Error("Mini App link mutation failed", "operation", operation, "error", err)
	writeError(w, http.StatusInternalServerError, "link_mutation_failed", "The links could not be changed.")
}

func (a *App) clearLinks(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Link controls do not accept query parameters.")
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		linkBodyError(w, status)
		return
	}
	deleted, err := store.ClearSavedLinks(req.Context(), a.db, current.ChatID)
	if err != nil {
		a.linkMutationFailed(w, req, "clear", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": deleted})
}

func deleteLinkID(path string) (int64, bool) {
	if !strings.HasPrefix(path, "/api/links/") || !strings.HasSuffix(path, "/delete") {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, "/api/links/"), "/delete")
	if raw == "" || strings.Contains(raw, "/") {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, false
	}
	return id, true
}

func (a *App) deleteLink(w http.ResponseWriter, req *http.Request, current scope) {
	linkID, valid := deleteLinkID(req.URL.Path)
	if !valid {
		writeError(w, http.StatusNotFound, "not_found", "API endpoint not found.")
		return
	}
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Link controls do not accept query parameters.")
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		linkBodyError(w, status)
		return
	}
	deleted, err := store.DeleteSavedLink(req.Context(), a.db, current.ChatID, linkID)
	if err != nil {
		a.linkMutationFailed(w, req, "delete", err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "not_found", "Saved link not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": linkID})
}

func (a *App) linkControl(w http.ResponseWriter, req *http.Request, current scope) {
	if req.URL.Path == "/api/links/clear" {
		a.clearLinks(w, req, current)
		return
	}
	a.deleteLink(w, req, current)
}

const maxTaskErrorRunes = 300

type taskItem struct {
	ID             int64  `json:"id"`
	Goal           string `json:"goal"`
	Truncated      bool   `json:"truncated"`
	Status         string `json:"status"`
	NextStep       int    `json:"next_step"`
	Attempts       int    `json:"attempts"`
	DueAt          string `json:"due_at"`
	ExpiresAt      string `json:"expires_at"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	Error          string `json:"error,omitempty"`
	ErrorTruncated bool   `json:"error_truncated,omitempty"`
}

func (a *App) createTask(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Task creation does not accept query parameters.")
		return
	}
	var input struct {
		Goal string `json:"goal"`
	}
	if status, ok := decodeSettingsJSON(req, &input); !ok {
		managementDecodeError(w, status)
		return
	}
	goal := strings.TrimSpace(input.Goal)
	if goal == "" || !utf8.ValidString(goal) || len([]rune(goal)) > store.TaskMaxGoalRunes || strings.ContainsRune(goal, '\x00') {
		writeError(w, http.StatusBadRequest, "invalid_goal", "Use a nonempty goal of at most 2000 runes without NUL characters.")
		return
	}
	created, err := a.taskLifecycle.CreateTask(req.Context(), current.ChatID, current.UserID, goal)
	if errors.Is(err, errLifecycleUnavailable) {
		writeLifecycleUnavailable(w)
		return
	}
	if errors.Is(err, bot.ErrTaskRateLimited) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "The per-minute model budget of this user is spent. Retry after a minute.")
		return
	}
	if errors.Is(err, store.ErrTaskActive) {
		writeError(w, http.StatusConflict, "task_active", "An active task already exists in this chat.")
		return
	}
	if errors.Is(err, store.ErrTaskGoal) {
		writeError(w, http.StatusBadRequest, "invalid_goal", "The task goal is invalid.")
		return
	}
	if err != nil {
		a.taskMutationFailed(w, req, "create", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": created.ID, "status": created.Status})
}

func (a *App) tasks(w http.ResponseWriter, req *http.Request, current scope) {
	page, ok := readPage(req)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_pagination", "Use only a positive before ID and a limit from 1 to 20.")
		return
	}
	rows, err := a.db.QueryContext(req.Context(), `
		SELECT id, goal, status, next_step, attempts, due_at, expires_at, created_at, updated_at, COALESCE(error, '') FROM tasks
		WHERE chat_id = ? AND (? = 0 OR id < ?)
		ORDER BY id DESC LIMIT ?`, current.ChatID, page.Before, page.Before, page.Limit+1)
	if err != nil {
		a.queryFailed(w, "tasks", err)
		return
	}
	defer rows.Close()
	items := []taskItem{}
	for rows.Next() {
		var item taskItem
		if err := rows.Scan(&item.ID, &item.Goal, &item.Status, &item.NextStep, &item.Attempts,
			&item.DueAt, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt, &item.Error); err != nil {
			a.queryFailed(w, "tasks", err)
			return
		}
		item.Goal, item.Truncated = boundedText(item.Goal, maxTaskRunes)
		if item.Status == store.TaskStatusFailed {
			for _, secret := range []string{a.cfg.BotToken, a.cfg.StrataKey} {
				if secret = strings.TrimSpace(secret); secret != "" {
					item.Error = strings.ReplaceAll(item.Error, secret, "***")
				}
			}
			item.Error, item.ErrorTruncated = boundedText(item.Error, maxTaskErrorRunes)
		} else {
			item.Error = ""
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		a.queryFailed(w, "tasks", err)
		return
	}
	var next *int64
	if len(items) > page.Limit {
		next = nextID(len(items), page.Limit, items[page.Limit-1].ID)
		items = items[:page.Limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_before_id": next})
}

func taskBodyError(w http.ResponseWriter, status int) {
	if status == http.StatusRequestEntityTooLarge {
		writeError(w, status, "body_too_large", "Request body is too large.")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_body", "Task controls accept an empty request body.")
}

func (a *App) taskMutationFailed(w http.ResponseWriter, req *http.Request, operation string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, req.Context().Err()) {
		return
	}
	a.logger.Error("Mini App task mutation failed", "operation", operation, "error", err)
	writeError(w, http.StatusInternalServerError, "task_mutation_failed", "The task could not be changed.")
}

func (a *App) cancelTask(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Task controls do not accept query parameters.")
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		taskBodyError(w, status)
		return
	}
	cancelled, changed, err := a.taskLifecycle.CancelTask(req.Context(), current.ChatID)
	if err != nil {
		a.taskMutationFailed(w, req, "cancel", err)
		return
	}
	if !changed {
		writeError(w, http.StatusConflict, "task_not_active", "There is no active task to cancel.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task_id": cancelled.ID, "status": cancelled.Status})
}

func resumeTaskID(path string) (int64, bool) {
	if !strings.HasPrefix(path, "/api/tasks/") || !strings.HasSuffix(path, "/resume") {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, "/api/tasks/"), "/resume")
	if raw == "" || strings.Contains(raw, "/") {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, false
	}
	return id, true
}

func (a *App) resumeTask(w http.ResponseWriter, req *http.Request, current scope) {
	taskID, valid := resumeTaskID(req.URL.Path)
	if !valid {
		writeError(w, http.StatusNotFound, "not_found", "API endpoint not found.")
		return
	}
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Task controls do not accept query parameters.")
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		taskBodyError(w, status)
		return
	}
	resumed, changed, err := a.taskLifecycle.ResumeTask(req.Context(), current.ChatID, taskID)
	if err != nil {
		a.taskMutationFailed(w, req, "resume", err)
		return
	}
	if !changed {
		writeError(w, http.StatusConflict, "task_not_resumable", "Only the latest failed or cancelled task can be resumed.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task_id": resumed.ID, "status": resumed.Status})
}

func deleteTaskID(path string) (int64, bool) {
	if !strings.HasPrefix(path, "/api/tasks/") || !strings.HasSuffix(path, "/delete") {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, "/api/tasks/"), "/delete")
	if raw == "" || strings.Contains(raw, "/") {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, false
	}
	return id, true
}

func (a *App) deleteTask(w http.ResponseWriter, req *http.Request, current scope) {
	taskID, valid := deleteTaskID(req.URL.Path)
	if !valid {
		writeError(w, http.StatusNotFound, "not_found", "API endpoint not found.")
		return
	}
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Task controls do not accept query parameters.")
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		taskBodyError(w, status)
		return
	}
	deleted, err := store.DeleteTask(req.Context(), a.db, current.ChatID, taskID)
	if err != nil {
		a.taskMutationFailed(w, req, "delete", err)
		return
	}
	if !deleted {
		writeError(w, http.StatusConflict, "task_not_deletable", "Only a completed, failed, or cancelled task in this chat can be deleted.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task_id": taskID})
}

func (a *App) clearTasks(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Task controls do not accept query parameters.")
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		taskBodyError(w, status)
		return
	}
	deleted, err := a.taskLifecycle.ClearTasks(req.Context(), current.ChatID)
	if err != nil {
		a.taskMutationFailed(w, req, "clear", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": deleted})
}

func (a *App) taskControl(w http.ResponseWriter, req *http.Request, current scope) {
	switch req.URL.Path {
	case "/api/tasks/cancel":
		a.cancelTask(w, req, current)
	case "/api/tasks/clear":
		a.clearTasks(w, req, current)
	default:
		if strings.HasSuffix(req.URL.Path, "/delete") {
			a.deleteTask(w, req, current)
			return
		}
		a.resumeTask(w, req, current)
	}
}

const maxFailedReminders = 5

type reminderItem struct {
	ID         int64   `json:"id"`
	Text       string  `json:"text"`
	Cadence    string  `json:"cadence"`
	DailyTime  string  `json:"daily_time"`
	NextRun    string  `json:"next_run"`
	Status     string  `json:"status"`
	Attempts   int     `json:"attempts"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	LastSentAt *string `json:"last_sent_at"`
	FinishedAt *string `json:"finished_at"`
}

func reminderTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func optionalReminderTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := reminderTime(*value)
	return &formatted
}

func makeReminderItem(reminder store.Reminder) reminderItem {
	return reminderItem{
		ID: reminder.ID, Text: reminder.Text, Cadence: reminder.Cadence, DailyTime: reminder.DailyTime,
		NextRun: reminderTime(reminder.NextRunAt), Status: reminder.Status, Attempts: reminder.Attempts,
		CreatedAt: reminderTime(reminder.CreatedAt), UpdatedAt: reminderTime(reminder.UpdatedAt),
		LastSentAt: optionalReminderTime(reminder.LastSentAt), FinishedAt: optionalReminderTime(reminder.FinishedAt),
	}
}

func (a *App) createReminder(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Reminder creation does not accept query parameters.")
		return
	}
	var input struct {
		Text      string `json:"text"`
		Cadence   string `json:"cadence"`
		NextRun   string `json:"next_run"`
		DailyTime string `json:"daily_time"`
	}
	if status, ok := decodeSettingsJSON(req, &input); !ok {
		managementDecodeError(w, status)
		return
	}
	text := strings.TrimSpace(input.Text)
	if text == "" || !utf8.ValidString(text) || len([]rune(text)) > store.ReminderMaxTextRunes || strings.ContainsRune(text, '\x00') {
		writeError(w, http.StatusBadRequest, "invalid_reminder", "Use nonempty reminder text of at most 500 runes without NUL characters.")
		return
	}
	now := time.Now().UTC()
	reminder := store.Reminder{
		ChatID: current.ChatID, UserID: current.UserID, Text: text, Cadence: input.Cadence, CreatedAt: now,
	}
	switch input.Cadence {
	case store.ReminderCadenceOnce:
		if input.NextRun == "" || input.DailyTime != "" {
			writeError(w, http.StatusBadRequest, "invalid_reminder", "A one-time reminder needs only next_run in RFC3339 format.")
			return
		}
		next, err := time.Parse(time.RFC3339, input.NextRun)
		if err != nil || !next.After(now) || next.After(now.Add(bot.MaxReminderDelay)) {
			writeError(w, http.StatusBadRequest, "invalid_reminder", "A one-time reminder must be in the future and no more than 3650 days ahead.")
			return
		}
		reminder.NextRunAt = next.UTC()
	case store.ReminderCadenceDaily:
		if input.DailyTime == "" || input.NextRun != "" {
			writeError(w, http.StatusBadRequest, "invalid_reminder", "A daily reminder needs only daily_time in HH:MM format.")
			return
		}
		location := a.cfg.ReminderLocation
		if location == nil {
			location = time.UTC
		}
		next, err := bot.NextDailyReminder(now, input.DailyTime, location)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_reminder", "Daily time must use HH:MM from 00:00 to 23:59.")
			return
		}
		reminder.DailyTime = input.DailyTime
		reminder.NextRunAt = next
	default:
		writeError(w, http.StatusBadRequest, "invalid_reminder", "Cadence must be once or daily.")
		return
	}
	if err := store.EnsureChat(req.Context(), a.db, current.ChatID, current.UserID, ""); err != nil {
		a.reminderMutationFailed(w, req, "create", err)
		return
	}
	created, err := store.CreateReminder(req.Context(), a.db, reminder)
	if errors.Is(err, store.ErrReminderLimit) {
		writeError(w, http.StatusConflict, "reminder_limit", "The active reminder limit for this chat has been reached.")
		return
	}
	if errors.Is(err, store.ErrReminderText) || errors.Is(err, store.ErrReminderCadence) || errors.Is(err, store.ErrReminderTime) {
		writeError(w, http.StatusBadRequest, "invalid_reminder", "The reminder is invalid.")
		return
	}
	if err != nil {
		a.reminderMutationFailed(w, req, "create", err)
		return
	}
	writeJSON(w, http.StatusOK, makeReminderItem(created))
}

func (a *App) reminders(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodGet {
		methodAllowed(w, http.MethodGet)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "The reminder list does not accept query parameters.")
		return
	}
	active, err := store.ListReminders(req.Context(), a.db, current.ChatID, store.ReminderListLimit)
	if err != nil {
		a.queryFailed(w, "active reminders", err)
		return
	}
	failed, err := store.ListFailedReminders(req.Context(), a.db, current.ChatID, maxFailedReminders)
	if err != nil {
		a.queryFailed(w, "failed reminders", err)
		return
	}
	response := struct {
		Active []reminderItem `json:"active"`
		Failed []reminderItem `json:"failed"`
	}{Active: make([]reminderItem, 0, len(active)), Failed: make([]reminderItem, 0, len(failed))}
	for _, reminder := range active {
		response.Active = append(response.Active, makeReminderItem(reminder))
	}
	for _, reminder := range failed {
		response.Failed = append(response.Failed, makeReminderItem(reminder))
	}
	writeJSON(w, http.StatusOK, response)
}

func reminderActionID(path string) (int64, string, bool) {
	if !strings.HasPrefix(path, "/api/reminders/") {
		return 0, "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/reminders/"), "/")
	if len(parts) != 2 || (parts[1] != "cancel" && parts[1] != "retry" && parts[1] != "delete") {
		return 0, "", false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != parts[0] {
		return 0, "", false
	}
	return id, parts[1], true
}

func reminderBodyError(w http.ResponseWriter, status int) {
	if status == http.StatusRequestEntityTooLarge {
		writeError(w, status, "body_too_large", "Request body is too large.")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_body", "Reminder controls accept an empty request body.")
}

func (a *App) reminderMutationFailed(w http.ResponseWriter, req *http.Request, action string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, req.Context().Err()) {
		return
	}
	a.logger.Error("Mini App reminder mutation failed", "operation", action, "error", err)
	writeError(w, http.StatusInternalServerError, "reminder_mutation_failed", "The reminder could not be changed.")
}

func (a *App) reminderControl(w http.ResponseWriter, req *http.Request, current scope) {
	reminderID, action, valid := reminderActionID(req.URL.Path)
	if !valid {
		writeError(w, http.StatusNotFound, "not_found", "API endpoint not found.")
		return
	}
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Reminder controls do not accept query parameters.")
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		reminderBodyError(w, status)
		return
	}
	var changed bool
	var err error
	switch action {
	case "cancel":
		changed, err = store.CancelReminder(req.Context(), a.db, current.ChatID, reminderID, time.Now().UTC())
	case "retry":
		changed, err = store.RetryFailedReminder(req.Context(), a.db, current.ChatID, reminderID, time.Now().UTC())
	case "delete":
		changed, err = store.DeleteFailedReminder(req.Context(), a.db, current.ChatID, reminderID)
	}
	if err != nil {
		a.reminderMutationFailed(w, req, action, err)
		return
	}
	if !changed {
		writeError(w, http.StatusConflict, "reminder_not_mutable", "The reminder is missing or cannot be changed in its current state.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": reminderID})
}

type usageModel struct {
	Model     string `json:"model"`
	Calls     int64  `json:"calls"`
	TokensIn  int64  `json:"tokens_in"`
	TokensOut int64  `json:"tokens_out"`
}

type usageResponse struct {
	Date   string       `json:"date"`
	Models []usageModel `json:"models"`
	Tools  struct {
		ReadURL    int64 `json:"read_url"`
		WebSearch  int64 `json:"web_search"`
		Calculator int64 `json:"calculator"`
		RAG        int64 `json:"rag"`
	} `json:"tools"`
	Limits struct {
		Model        usageLimit `json:"model"`
		NetworkTools usageLimit `json:"network_tools"`
	} `json:"limits"`
}

type usageLimit struct {
	Used      int64  `json:"used"`
	Limit     int64  `json:"limit"`
	Remaining *int64 `json:"remaining"`
}

func makeUsageLimit(used int64, limit int64) usageLimit {
	result := usageLimit{Used: used, Limit: limit}
	if limit > 0 {
		remaining := limit - used
		if remaining < 0 {
			remaining = 0
		}
		result.Remaining = &remaining
	}
	return result
}

func (a *App) usage(w http.ResponseWriter, req *http.Request, current scope) {
	now := time.Now().UTC()
	daily, err := store.UserDailyUsage(req.Context(), a.db, current.UserID, now)
	if err != nil {
		a.queryFailed(w, "usage", err)
		return
	}
	var response usageResponse
	response.Date = now.Format("2006-01-02")
	response.Models = make([]usageModel, 0, len(daily.Models))
	for _, model := range daily.Models {
		response.Models = append(response.Models, usageModel{
			Model: model.Model, Calls: model.Calls, TokensIn: model.TokensIn, TokensOut: model.TokensOut,
		})
	}
	response.Tools.ReadURL = daily.Tools.ReadURL
	response.Tools.WebSearch = daily.Tools.WebSearch
	response.Tools.Calculator = daily.Tools.Calculator
	response.Tools.RAG = daily.Tools.RAG
	limits := a.cfg.DailyLimitsFor(current.UserID)
	response.Limits.Model = makeUsageLimit(daily.ModelCalls(), limits.Model)
	response.Limits.NetworkTools = makeUsageLimit(daily.NetworkToolCalls(), limits.NetworkTools)
	writeJSON(w, http.StatusOK, response)
}
