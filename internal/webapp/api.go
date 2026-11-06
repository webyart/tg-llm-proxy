package webapp

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_before_id": next})
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
