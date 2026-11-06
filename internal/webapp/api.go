package webapp

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"unicode/utf8"
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
		Messages     int64  `json:"messages"`
		Documents    int64  `json:"documents"`
		Links        int64  `json:"links"`
		Tasks        int64  `json:"tasks"`
		ActiveTasks  int64  `json:"active_tasks"`
		Pending      bool   `json:"pending"`
		LastActivity string `json:"last_activity,omitempty"`
	}
	var lastRole string
	var lastActivity *string
	err := a.db.QueryRowContext(req.Context(), `
		SELECT
			(SELECT COUNT(*) FROM messages WHERE chat_id = ?),
			(SELECT COUNT(*) FROM rag_documents WHERE chat_id = ?),
			(SELECT COUNT(*) FROM saved_links WHERE chat_id = ?),
			(SELECT COUNT(*) FROM tasks WHERE chat_id = ?),
			(SELECT COUNT(*) FROM tasks WHERE chat_id = ? AND status IN ('ready', 'running', 'awaiting_approval')),
			COALESCE((SELECT role FROM messages WHERE chat_id = ? AND role IN ('user', 'assistant') ORDER BY id DESC LIMIT 1), ''),
			(SELECT MAX(created_at) FROM messages WHERE chat_id = ?)`,
		current.ChatID, current.ChatID, current.ChatID, current.ChatID, current.ChatID, current.ChatID, current.ChatID).
		Scan(&response.Messages, &response.Documents, &response.Links, &response.Tasks, &response.ActiveTasks, &lastRole, &lastActivity)
	if err != nil {
		a.queryFailed(w, "status", err)
		return
	}
	response.Pending = lastRole == "user"
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

type taskItem struct {
	ID        int64  `json:"id"`
	Goal      string `json:"goal"`
	Truncated bool   `json:"truncated"`
	Status    string `json:"status"`
	NextStep  int    `json:"next_step"`
	Attempts  int    `json:"attempts"`
	DueAt     string `json:"due_at"`
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (a *App) tasks(w http.ResponseWriter, req *http.Request, current scope) {
	page, ok := readPage(req)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_pagination", "Use only a positive before ID and a limit from 1 to 20.")
		return
	}
	rows, err := a.db.QueryContext(req.Context(), `
		SELECT id, goal, status, next_step, attempts, due_at, expires_at, created_at, updated_at FROM tasks
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
			&item.DueAt, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			a.queryFailed(w, "tasks", err)
			return
		}
		item.Goal, item.Truncated = boundedText(item.Goal, maxTaskRunes)
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
