package webapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"bot/internal/bot"
	"bot/internal/store"
)

func appendWebappMessage(t *testing.T, app *App, chatID int64, role string, content string) int64 {
	t.Helper()
	id, err := store.Append(context.Background(), app.db, store.Message{
		ChatID: chatID, UserID: testUserID, Role: role, Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func requireAPIErrorCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Error.Code != want {
		t.Fatalf("error body=%s code=%q want=%q err=%v", body, response.Error.Code, want, err)
	}
}

func TestHistorySearchLimitTruncationAndStrictQuery(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	ids := make([]int64, 0, 25)
	for index := 0; index < 25; index++ {
		ids = append(ids, appendWebappMessage(t, app, testChatID, store.RoleUser, fmt.Sprintf("NeEdLe %02d", index)))
	}
	longContent := "длинное " + strings.Repeat("я", maxHistoryRunes+100) + " полный-хвост"
	longID := appendWebappMessage(t, app, testChatID, store.RoleAssistant, longContent)
	appendWebappMessage(t, app, testChatID+1, store.RoleUser, "needle other-chat-secret")

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/history/search?q=nEeDlE", bearer)
	if status != http.StatusOK {
		t.Fatalf("search status=%d body=%s", status, body)
	}
	var result struct {
		Items     []historyItem `json:"items"`
		Truncated bool          `json:"truncated"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Items) != bot.SearchRowLimit {
		t.Fatalf("search items=%d err=%v body=%s", len(result.Items), err, body)
	}
	if result.Items[0].ID != ids[5] || result.Items[len(result.Items)-1].ID != ids[24] {
		t.Fatalf("search window first=%d last=%d want=%d/%d", result.Items[0].ID, result.Items[len(result.Items)-1].ID, ids[5], ids[24])
	}
	if strings.Contains(string(body), "other-chat-secret") {
		t.Fatalf("search leaked another chat: %s", body)
	}

	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/history/search?q="+url.QueryEscape("ДЛИННОЕ"), bearer)
	if status != http.StatusOK || json.Unmarshal(body, &result) != nil || len(result.Items) != 1 {
		t.Fatalf("Unicode search status=%d body=%s", status, body)
	}
	if result.Items[0].ID != longID || !result.Items[0].Truncated || len([]rune(result.Items[0].Content)) != maxHistoryRunes+1 {
		t.Fatalf("bounded search item=%+v runes=%d", result.Items[0], len([]rune(result.Items[0].Content)))
	}
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/history/search?q=absent", bearer)
	if status != http.StatusOK || string(body) != "{\"items\":[],\"truncated\":false}\n" {
		t.Fatalf("empty search status=%d body=%s", status, body)
	}

	for _, path := range []string{
		"/api/history/search", "/api/history/search?q=", "/api/history/search?q=%20%20",
		"/api/history/search?q=one&q=two", "/api/history/search?q=one&extra=1",
		"/api/history/search?extra=1", "/api/history/search?q=one;extra=1",
		"/api/history/search?q=" + url.QueryEscape(strings.Repeat("й", store.RAGQueryMaxRunes+1)),
	} {
		status, body, _ = apiRequest(t, server, http.MethodGet, path, bearer)
		if status != http.StatusBadRequest {
			t.Errorf("invalid search %q status=%d body=%s", path, status, body)
			continue
		}
		requireAPIErrorCode(t, body, "invalid_query")
	}
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/history/search?q="+url.QueryEscape(strings.Repeat("й", store.RAGQueryMaxRunes)), bearer)
	if status != http.StatusOK {
		t.Fatalf("maximum query status=%d body=%s", status, body)
	}
	status, body, headers := apiRequest(t, server, http.MethodPost, "/api/history/search?q=needle", bearer)
	if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodGet {
		t.Fatalf("search POST status=%d Allow=%q body=%s", status, headers.Get("Allow"), body)
	}
}

func TestHistorySearchReportsOnlyTheTenThousandMessageWindow(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	oldestID := appendWebappMessage(t, app, testChatID, store.RoleUser, "needle outside the newest window")
	tx, err := app.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.PrepareContext(context.Background(), `INSERT INTO messages (chat_id, user_id, role, content) VALUES (?, ?, 'user', 'filler')`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < bot.ExportRowLimit; index++ {
		if _, err := statement.ExecContext(context.Background(), testChatID, testUserID); err != nil {
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	readSearch := func() (int, []historyItem, bool) {
		t.Helper()
		status, body, _ := apiRequest(t, server, http.MethodGet, "/api/history/search?q=needle", bearer)
		if status != http.StatusOK {
			t.Fatalf("windowed search status=%d body=%s", status, body)
		}
		var response struct {
			Items     []historyItem `json:"items"`
			Truncated bool          `json:"truncated"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		return status, response.Items, response.Truncated
	}
	_, items, truncated := readSearch()
	if !truncated || len(items) != 0 {
		t.Fatalf("10001-message search items=%d truncated=%v", len(items), truncated)
	}
	if _, err := app.db.ExecContext(context.Background(), `DELETE FROM messages WHERE id = (SELECT MAX(id) FROM messages WHERE chat_id = ?)`, testChatID); err != nil {
		t.Fatal(err)
	}
	_, items, truncated = readSearch()
	if truncated || len(items) != 1 || items[0].ID != oldestID {
		t.Fatalf("10000-message search items=%+v truncated=%v", items, truncated)
	}
}

func TestHistoryAndLinkItemsReturnFullContentOnlyFromCurrentChat(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, testChatID+1, testUserID)
	messageContent := strings.Repeat("сообщение ", maxHistoryRunes) + "конец"
	messageID := appendWebappMessage(t, app, testChatID, store.RoleAssistant, messageContent)
	otherMessageID := appendWebappMessage(t, app, testChatID+1, store.RoleUser, "другой чат")

	status, body, _ := apiRequest(t, server, http.MethodGet, fmt.Sprintf("/api/history/item?id=%d", messageID), bearer)
	if status != http.StatusOK {
		t.Fatalf("history item status=%d body=%s", status, body)
	}
	var message historyItem
	if err := json.Unmarshal(body, &message); err != nil || message.ID != messageID || message.Content != messageContent || message.Truncated {
		t.Fatalf("full history item=%+v err=%v", message, err)
	}

	linkContent := strings.Repeat("ссылка ", maxLinkRunes) + "конец"
	created, replaced, err := store.PutSavedLink(context.Background(), app.db, store.SavedLink{
		ChatID: testChatID, UserID: testUserID, Title: "Полная ссылка",
		CanonicalURL: "https://example.test/full", Content: linkContent,
	})
	if err != nil || replaced {
		t.Fatalf("PutSavedLink replaced=%v err=%v", replaced, err)
	}
	status, body, _ = apiRequest(t, server, http.MethodGet, fmt.Sprintf("/api/links/item?id=%d", created.ID), bearer)
	if status != http.StatusOK {
		t.Fatalf("link item status=%d body=%s", status, body)
	}
	var link linkItem
	if err := json.Unmarshal(body, &link); err != nil || link.ID != created.ID || link.Content != linkContent || link.Title != created.Title || link.URL != created.CanonicalURL || link.Truncated {
		t.Fatalf("full link item=%+v err=%v", link, err)
	}

	for _, endpoint := range []string{"history", "links"} {
		missingID := int64(999999)
		if endpoint == "history" {
			missingID = otherMessageID
		}
		for _, path := range []string{
			fmt.Sprintf("/api/%s/item?id=%d", endpoint, missingID),
			fmt.Sprintf("/api/%s/item?id=%d", endpoint, messageID),
		} {
			activeBearer := bearer
			if strings.Contains(path, strconv.FormatInt(messageID, 10)) {
				activeBearer = otherBearer
			}
			status, body, _ = apiRequest(t, server, http.MethodGet, path, activeBearer)
			if status != http.StatusNotFound {
				t.Errorf("scoped item %s status=%d body=%s", path, status, body)
				continue
			}
			requireAPIErrorCode(t, body, "not_found")
		}
		for _, query := range []string{"", "?id=", "?id=0", "?id=-1", "?id=%2B1", "?id=01", "?id=1.0", "?id=9223372036854775808", "?id=1&id=2", "?id=1&chat_id=4242", "?chat_id=4242"} {
			status, body, _ = apiRequest(t, server, http.MethodGet, "/api/"+endpoint+"/item"+query, bearer)
			if status != http.StatusBadRequest {
				t.Errorf("invalid %s item query %q status=%d body=%s", endpoint, query, status, body)
				continue
			}
			requireAPIErrorCode(t, body, "invalid_query")
		}
		status, body, headers := apiRequest(t, server, http.MethodPost, "/api/"+endpoint+"/item?id=1", bearer)
		if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodGet {
			t.Errorf("%s item POST status=%d Allow=%q body=%s", endpoint, status, headers.Get("Allow"), body)
		}
	}
	status, body, _ = apiRequest(t, server, http.MethodGet, fmt.Sprintf("/api/history/item?id=%d", messageID), "")
	if status != http.StatusUnauthorized {
		t.Fatalf("item without bearer status=%d body=%s", status, body)
	}
}

func TestHistoryExportAttachmentsContainExactData(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	first := "Первый *текст* с \"кавычками\""
	second := strings.Repeat("Второй ответ ", 100) + "конец"
	appendWebappMessage(t, app, testChatID, store.RoleUser, first)
	appendWebappMessage(t, app, testChatID, store.RoleAssistant, second)
	appendWebappMessage(t, app, testChatID+1, store.RoleUser, "other-chat-export-secret")

	expected, err := bot.ExportHistoryData(context.Background(), app.db, testChatID, "md")
	if err != nil {
		t.Fatal(err)
	}
	status, body, headers := apiRequest(t, server, http.MethodGet, "/api/history/export?format=md", bearer)
	if status != http.StatusOK || !bytes.Equal(body, expected) {
		t.Fatalf("Markdown export status=%d bytes=%d expected=%d body=%q", status, len(body), len(expected), body)
	}
	if got := headers.Get("Content-Type"); got != "text/markdown; charset=utf-8" {
		t.Errorf("Markdown Content-Type=%q", got)
	}
	if got, want := headers.Get("Content-Disposition"), fmt.Sprintf(`attachment; filename="history-%d.md"`, testChatID); got != want {
		t.Errorf("Markdown Content-Disposition=%q want=%q", got, want)
	}
	if headers.Get("Content-Length") != strconv.Itoa(len(body)) || strings.Contains(string(body), "other-chat-export-secret") {
		t.Errorf("Markdown headers=%v scoped body=%q", headers, body)
	}

	status, body, headers = apiRequest(t, server, http.MethodGet, "/api/history/export?format=json", bearer)
	if status != http.StatusOK {
		t.Fatalf("JSON export status=%d body=%s", status, body)
	}
	var exported struct {
		ChatID     int64  `json:"chat_id"`
		ExportedAt string `json:"exported_at"`
		Messages   []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &exported); err != nil || exported.ChatID != testChatID || len(exported.Messages) != 2 {
		t.Fatalf("JSON export=%s err=%v", body, err)
	}
	if exported.Messages[0].Content != first || exported.Messages[1].Content != second {
		t.Fatalf("JSON export did not preserve full content: %+v", exported.Messages)
	}
	if _, err := time.Parse(time.RFC3339, exported.ExportedAt); err != nil {
		t.Errorf("exported_at=%q err=%v", exported.ExportedAt, err)
	}
	if headers.Get("Content-Type") != "application/json; charset=utf-8" || !strings.HasSuffix(headers.Get("Content-Disposition"), `.json"`) || headers.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("JSON export headers=%v", headers)
	}
}

func TestHistoryExportValidationEmptyAndFiveMiBLimit(t *testing.T) {
	for index, query := range []string{"", "?format=", "?format=MD", "?format=xml", "?format=md&format=json", "?format=md&extra=1", "?other=md"} {
		t.Run(fmt.Sprintf("query-%d", index), func(t *testing.T) {
			app, server := newTestApp(t)
			bearer := launchSession(t, app, server)
			status, body, headers := apiRequest(t, server, http.MethodGet, "/api/history/export"+query, bearer)
			if status != http.StatusBadRequest || headers.Get("Content-Disposition") != "" {
				t.Fatalf("invalid export %q status=%d headers=%v body=%s", query, status, headers, body)
			}
			requireAPIErrorCode(t, body, "invalid_query")
		})
	}

	t.Run("empty", func(t *testing.T) {
		app, server := newTestApp(t)
		bearer := launchSession(t, app, server)
		status, body, headers := apiRequest(t, server, http.MethodGet, "/api/history/export?format=md", bearer)
		if status != http.StatusNotFound || headers.Get("Content-Disposition") != "" {
			t.Fatalf("empty export status=%d headers=%v body=%s", status, headers, body)
		}
		requireAPIErrorCode(t, body, "history_empty")
	})

	t.Run("too large", func(t *testing.T) {
		app, server := newTestApp(t)
		bearer := launchSession(t, app, server)
		appendWebappMessage(t, app, testChatID, store.RoleUser, strings.Repeat("x", bot.ExportMaxBytes+1))
		status, body, headers := apiRequest(t, server, http.MethodGet, "/api/history/export?format=json", bearer)
		if status != http.StatusRequestEntityTooLarge || len(body) > maxBodyBytes || headers.Get("Content-Disposition") != "" {
			t.Fatalf("oversize export status=%d bytes=%d headers=%v", status, len(body), headers)
		}
		requireAPIErrorCode(t, body, "export_too_large")
	})

	t.Run("storage failure", func(t *testing.T) {
		app, server := newTestApp(t)
		bearer := launchSession(t, app, server)
		if err := app.db.Close(); err != nil {
			t.Fatal(err)
		}
		status, body, _ := apiRequest(t, server, http.MethodGet, "/api/history/export?format=md", bearer)
		if status != http.StatusInternalServerError {
			t.Fatalf("failed export status=%d body=%s", status, body)
		}
		requireAPIErrorCode(t, body, "export_failed")
	})
}
