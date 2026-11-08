package webapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"bot/internal/store"
)

func TestMessageFeedbackRatesOnlyTheSessionsAssistantMessage(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, otherTestUserID, otherTestUserID)
	assistantID := appendWebappMessage(t, app, testChatID, store.RoleAssistant, "ответ ассистента")
	userID := appendWebappMessage(t, app, testChatID, store.RoleUser, "вопрос")
	systemID := appendWebappMessage(t, app, testChatID, store.RoleSystem, "system")
	messagePath := func(id int64) string { return fmt.Sprintf("/api/history/%d/feedback", id) }

	status, body, headers := managementRequest(t, server, http.MethodPost, messagePath(assistantID), "", `{"value":1}`, "application/json")
	if status != http.StatusUnauthorized {
		t.Fatalf("feedback without a session status=%d body=%s", status, body)
	}
	if headers.Get("Content-Type") == "" {
		t.Error("an unauthorized feedback response must still be a JSON error")
	}

	// Another chat's session cannot rate this assistant message.
	status, body, _ = managementRequest(t, server, http.MethodPost, messagePath(assistantID), otherBearer, `{"value":1}`, "application/json")
	if status != http.StatusForbidden {
		t.Fatalf("cross-chat feedback status=%d body=%s", status, body)
	}
	requireAPIErrorCode(t, body, "feedback_forbidden")
	for _, id := range []int64{userID, systemID} {
		status, body, _ = managementRequest(t, server, http.MethodPost, messagePath(id), bearer, `{"value":1}`, "application/json")
		if status != http.StatusForbidden {
			t.Fatalf("feedback on message %d status=%d body=%s", id, status, body)
		}
		requireAPIErrorCode(t, body, "feedback_forbidden")
	}
	status, body, _ = managementRequest(t, server, http.MethodPost, messagePath(999999), bearer, `{"value":1}`, "application/json")
	if status != http.StatusNotFound {
		t.Fatalf("feedback on a missing message status=%d body=%s", status, body)
	}
	requireAPIErrorCode(t, body, "not_found")

	status, body, _ = managementRequest(t, server, http.MethodPost, messagePath(assistantID), bearer, `{"value":1}`, "application/json")
	if status != http.StatusOK {
		t.Fatalf("feedback status=%d body=%s", status, body)
	}
	var rated struct {
		OK    bool `json:"ok"`
		Value int  `json:"value"`
	}
	if err := json.Unmarshal(body, &rated); err != nil || !rated.OK || rated.Value != 1 {
		t.Fatalf("feedback response=%s decoded=%+v err=%v", body, rated, err)
	}
	status, body, _ = managementRequest(t, server, http.MethodPost, messagePath(assistantID), bearer, `{"value":-1}`, "application/json")
	if status != http.StatusOK || !strings.Contains(string(body), `"value":-1`) {
		t.Fatalf("replacement status=%d body=%s", status, body)
	}

	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/history?limit=10", bearer)
	if status != http.StatusOK {
		t.Fatalf("history status=%d body=%s", status, body)
	}
	var history struct {
		Items []historyItem `json:"items"`
	}
	if err := json.Unmarshal(body, &history); err != nil {
		t.Fatalf("history decode: %v body=%s", err, body)
	}
	var seen int
	for _, item := range history.Items {
		if item.ID == assistantID {
			seen++
			if item.Feedback != -1 {
				t.Fatalf("history feedback=%d, want the user's -1", item.Feedback)
			}
		} else if item.Feedback != 0 {
			t.Fatalf("message %d carries feedback %d, want 0", item.ID, item.Feedback)
		}
	}
	if seen != 1 {
		t.Fatalf("assistant message found in history %d times, want 1: %+v", seen, history.Items)
	}

	// The user's own chat and user id are the only scope: no other chat saw the rating.
	var chatID, userIDStored int64
	var stored int
	if err := app.db.QueryRowContext(t.Context(), `SELECT chat_id, user_id, value FROM message_feedback WHERE message_id = ?`, assistantID).
		Scan(&chatID, &userIDStored, &stored); err != nil {
		t.Fatal(err)
	}
	if chatID != testChatID || userIDStored != testUserID || stored != -1 {
		t.Fatalf("stored feedback chat=%d user=%d value=%d, want %d/%d/-1", chatID, userIDStored, stored, testChatID, testUserID)
	}
}

func TestMessageFeedbackStrictBodyMethodAndPaths(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	messageID := appendWebappMessage(t, app, testChatID, store.RoleAssistant, "ответ")
	path := fmt.Sprintf("/api/history/%d/feedback", messageID)

	for _, test := range []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{"empty body", "", "application/json", http.StatusBadRequest, "invalid_json"},
		{"not JSON", "value=1", "application/json", http.StatusBadRequest, "invalid_json"},
		{"missing value", `{}`, "application/json", http.StatusBadRequest, "invalid_body"},
		{"zero value", `{"value":0}`, "application/json", http.StatusBadRequest, "invalid_body"},
		{"value two", `{"value":2}`, "application/json", http.StatusBadRequest, "invalid_body"},
		{"string value", `{"value":"1"}`, "application/json", http.StatusBadRequest, "invalid_json"},
		{"unknown field", `{"value":1,"chat_id":4242}`, "application/json", http.StatusBadRequest, "invalid_json"},
		{"trailing JSON", `{"value":1}{"value":-1}`, "application/json", http.StatusBadRequest, "invalid_json"},
		{"wrong content type", `{"value":1}`, "text/plain", http.StatusUnsupportedMediaType, "content_type"},
	} {
		status, body, _ := managementRequest(t, server, http.MethodPost, path, bearer, test.body, test.contentType)
		if status != test.wantStatus {
			t.Errorf("%s status=%d body=%s, want %d", test.name, status, body, test.wantStatus)
			continue
		}
		requireAPIErrorCode(t, body, test.wantCode)
	}
	status, body, _ := managementRequest(t, server, http.MethodPost, path+"?chat_id=4242", bearer, `{"value":1}`, "application/json")
	if status != http.StatusBadRequest {
		t.Fatalf("query parameters status=%d body=%s", status, body)
	}
	requireAPIErrorCode(t, body, "invalid_body")
	status, body, _ = managementRequest(t, server, http.MethodPost, path, bearer, `{"value":1,"padding":"`+strings.Repeat("x", maxBodyBytes)+`"}`, "application/json")
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized feedback status=%d len=%d body=%s", status, len(body), body)
	}
	requireAPIErrorCode(t, body, "body_too_large")

	status, body, headers := managementRequest(t, server, http.MethodGet, path, bearer, "", "")
	if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodPost {
		t.Fatalf("GET feedback status=%d Allow=%q body=%s", status, headers.Get("Allow"), body)
	}

	for _, invalid := range []string{
		"/api/history/01/feedback", "/api/history/0/feedback", "/api/history/-1/feedback",
		"/api/history/+1/feedback", "/api/history/1.0/feedback", "/api/history/9223372036854775808/feedback",
		"/api/history/1/extra/feedback", "/api/history/feedback", "/api/history/1/feedback/",
	} {
		status, body, _ := managementRequest(t, server, http.MethodPost, invalid, bearer, `{"value":1}`, "application/json")
		if status != http.StatusNotFound {
			t.Errorf("non-canonical %s status=%d body=%s", invalid, status, body)
		}
	}

	// A rejected request must not have rated anything.
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/history?limit=10", bearer)
	if status != http.StatusOK {
		t.Fatalf("history status=%d body=%s", status, body)
	}
	var history struct {
		Items []historyItem `json:"items"`
	}
	if err := json.Unmarshal(body, &history); err != nil || len(history.Items) != 1 || history.Items[0].Feedback != 0 {
		t.Fatalf("rejected feedback changed the history: %+v err=%v", history.Items, err)
	}
}
