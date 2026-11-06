package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"bot/internal/store"
)

const webappReminderTimeLayout = "2006-01-02T15:04:05.000000000Z"

func createWebappReminder(t *testing.T, app *App, chatID int64, text string, cadence string, nextRun time.Time) store.Reminder {
	t.Helper()
	if err := store.EnsureChat(context.Background(), app.db, chatID, testUserID, ""); err != nil {
		t.Fatal(err)
	}
	reminder := store.Reminder{
		ChatID: chatID, UserID: testUserID, Text: text, Cadence: cadence,
		NextRunAt: nextRun, CreatedAt: nextRun.Add(-time.Hour),
	}
	if cadence == store.ReminderCadenceDaily {
		reminder.DailyTime = "09:30"
	}
	created, err := store.CreateReminder(context.Background(), app.db, reminder)
	if err != nil {
		t.Fatalf("CreateReminder: %v", err)
	}
	return created
}

func failWebappReminder(t *testing.T, app *App, reminderID int64, finishedAt time.Time) {
	t.Helper()
	formatted := finishedAt.UTC().Format(webappReminderTimeLayout)
	if _, err := app.db.ExecContext(context.Background(), `UPDATE reminders
		SET status = 'failed', attempts = 5, finished_at = ?, updated_at = ?, last_sent_at = ? WHERE id = ?`,
		formatted, formatted, formatted, reminderID); err != nil {
		t.Fatal(err)
	}
}

func webappReminderState(t *testing.T, app *App, reminderID int64) (string, int, bool) {
	t.Helper()
	var status string
	var attempts int
	err := app.db.QueryRowContext(context.Background(), `SELECT status, attempts FROM reminders WHERE id = ?`, reminderID).Scan(&status, &attempts)
	if err != nil {
		return "", 0, false
	}
	return status, attempts, true
}

func TestReminderListIsScopedBoundedAndHasNoLeaseState(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	failed := make([]store.Reminder, 0, 7)
	for index := 0; index < 7; index++ {
		created := createWebappReminder(t, app, testChatID, fmt.Sprintf("failed-%02d", index), store.ReminderCadenceOnce, now)
		failWebappReminder(t, app, created.ID, now.Add(time.Duration(index)*time.Minute))
		failed = append(failed, created)
	}
	active := make([]store.Reminder, 0, 20)
	for index := 0; index < 20; index++ {
		cadence := store.ReminderCadenceOnce
		if index == 0 {
			cadence = store.ReminderCadenceDaily
		}
		created := createWebappReminder(t, app, testChatID, fmt.Sprintf("active-%02d", index), cadence, now.Add(time.Duration(index+1)*time.Hour))
		active = append(active, created)
	}
	formatted := now.Add(48 * time.Hour).Format(webappReminderTimeLayout)
	if _, err := app.db.ExecContext(ctx, `INSERT INTO reminders
		(chat_id, user_id, text, cadence, next_run_at, status, attempts, created_at, updated_at)
		VALUES (?, ?, 'overflow-active', 'once', ?, 'active', 0, ?, ?)`,
		testChatID, testUserID, formatted, formatted, formatted); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.ExecContext(ctx, `UPDATE reminders SET claim_token = 'private-claim-token', lease_until = ? WHERE id = ?`, formatted, active[0].ID); err != nil {
		t.Fatal(err)
	}
	otherActive := createWebappReminder(t, app, testChatID+1, "other-chat-active", store.ReminderCadenceOnce, now)
	otherFailed := createWebappReminder(t, app, testChatID+1, "other-chat-failed", store.ReminderCadenceOnce, now)
	failWebappReminder(t, app, otherFailed.ID, now)

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/reminders", bearer)
	if status != http.StatusOK {
		t.Fatalf("reminders status=%d body=%s", status, body)
	}
	var decoded struct {
		Active []reminderItem `json:"active"`
		Failed []reminderItem `json:"failed"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("reminders response=%s err=%v", body, err)
	}
	if len(decoded.Active) != 20 || len(decoded.Failed) != maxFailedReminders {
		t.Fatalf("reminder counts active=%d failed=%d", len(decoded.Active), len(decoded.Failed))
	}
	first := decoded.Active[0]
	if first.ID != active[0].ID || first.Text != "active-00" || first.Cadence != store.ReminderCadenceDaily || first.DailyTime != "09:30" ||
		first.Status != store.ReminderStatusActive || first.Attempts != 0 || first.NextRun == "" || first.CreatedAt == "" || first.UpdatedAt == "" ||
		first.LastSentAt != nil || first.FinishedAt != nil {
		t.Fatalf("first active reminder=%+v", first)
	}
	for _, timestamp := range []string{first.NextRun, first.CreatedAt, first.UpdatedAt} {
		if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
			t.Errorf("invalid reminder timestamp %q: %v", timestamp, err)
		}
	}
	latestFailed := decoded.Failed[0]
	if latestFailed.ID != failed[6].ID || latestFailed.Status != store.ReminderStatusFailed || latestFailed.Attempts != 5 ||
		latestFailed.FinishedAt == nil || latestFailed.LastSentAt == nil {
		t.Fatalf("latest failed reminder=%+v want id=%d", latestFailed, failed[6].ID)
	}
	joined := string(body)
	for _, forbidden := range []string{
		"overflow-active", "failed-00", "failed-01", "other-chat-active", "other-chat-failed",
		"claim_token", "lease_until", "private-claim-token", fmt.Sprintf(`"id":%d`, otherActive.ID),
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("reminder list leaked %q: %s", forbidden, joined)
		}
	}
}

func TestReminderActionsAreScopedAndEnforceState(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, testChatID+1, testUserID)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	active := createWebappReminder(t, app, testChatID, "cancel active", store.ReminderCadenceOnce, now.Add(time.Hour))
	retryMe := createWebappReminder(t, app, testChatID, "retry failed", store.ReminderCadenceOnce, now)
	failWebappReminder(t, app, retryMe.ID, now)
	deleteMe := createWebappReminder(t, app, testChatID, "delete failed", store.ReminderCadenceOnce, now)
	failWebappReminder(t, app, deleteMe.ID, now)
	otherActive := createWebappReminder(t, app, testChatID+1, "other active", store.ReminderCadenceOnce, now.Add(time.Hour))
	otherFailed := createWebappReminder(t, app, testChatID+1, "other failed", store.ReminderCadenceOnce, now)
	failWebappReminder(t, app, otherFailed.ID, now)

	for _, test := range []struct {
		name   string
		path   string
		bearer string
	}{
		{"other session", fmt.Sprintf("/api/reminders/%d/cancel", active.ID), otherBearer},
		{"other chat cancel", fmt.Sprintf("/api/reminders/%d/cancel", otherActive.ID), bearer},
		{"other chat retry", fmt.Sprintf("/api/reminders/%d/retry", otherFailed.ID), bearer},
		{"other chat delete", fmt.Sprintf("/api/reminders/%d/delete", otherFailed.ID), bearer},
		{"active retry", fmt.Sprintf("/api/reminders/%d/retry", active.ID), bearer},
		{"active delete", fmt.Sprintf("/api/reminders/%d/delete", active.ID), bearer},
		{"failed cancel", fmt.Sprintf("/api/reminders/%d/cancel", retryMe.ID), bearer},
		{"missing cancel", "/api/reminders/999999/cancel", bearer},
		{"missing retry", "/api/reminders/999999/retry", bearer},
		{"missing delete", "/api/reminders/999999/delete", bearer},
	} {
		status, body := settingsRequest(t, server.URL, http.MethodPost, test.path, test.bearer, "", "")
		if status != http.StatusConflict || !strings.Contains(string(body), `"code":"reminder_not_mutable"`) {
			t.Errorf("%s status=%d body=%s", test.name, status, body)
		}
	}

	status, body := settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/reminders/%d/cancel", active.ID), bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", status, body)
	}
	var actionResponse map[string]any
	if err := json.Unmarshal(body, &actionResponse); err != nil || len(actionResponse) != 2 || actionResponse["ok"] != true || actionResponse["id"] != float64(active.ID) {
		t.Fatalf("cancel response=%s err=%v", body, err)
	}
	if state, _, found := webappReminderState(t, app, active.ID); !found || state != store.ReminderStatusCancelled {
		t.Fatalf("cancelled state=%q found=%v", state, found)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/reminders/%d/cancel", active.ID), bearer, "", "")
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"reminder_not_mutable"`) {
		t.Fatalf("second cancel status=%d body=%s", status, body)
	}

	beforeRetry := time.Now().UTC()
	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/reminders/%d/retry", retryMe.ID), bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", status, body)
	}
	rows, err := store.ListReminders(context.Background(), app.db, testChatID, store.ReminderListLimit)
	if err != nil || len(rows) != 1 || rows[0].ID != retryMe.ID || rows[0].Attempts != 0 || rows[0].FinishedAt != nil || rows[0].NextRunAt.Before(beforeRetry) {
		t.Fatalf("retried reminders=%+v err=%v", rows, err)
	}
	for _, action := range []string{"retry", "delete"} {
		status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/reminders/%d/%s", retryMe.ID, action), bearer, "", "")
		if status != http.StatusConflict || !strings.Contains(string(body), `"code":"reminder_not_mutable"`) {
			t.Errorf("retried %s status=%d body=%s", action, status, body)
		}
	}

	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/reminders/%d/delete", deleteMe.ID), bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", status, body)
	}
	if _, _, found := webappReminderState(t, app, deleteMe.ID); found {
		t.Fatal("failed reminder still exists after delete")
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/reminders/%d/delete", deleteMe.ID), bearer, "", "")
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"reminder_not_mutable"`) {
		t.Fatalf("second delete status=%d body=%s", status, body)
	}
	if state, _, _ := webappReminderState(t, app, otherActive.ID); state != store.ReminderStatusActive {
		t.Fatalf("other active state=%q", state)
	}
	if state, _, _ := webappReminderState(t, app, otherFailed.ID); state != store.ReminderStatusFailed {
		t.Fatalf("other failed state=%q", state)
	}

	for _, path := range []string{
		"/api/reminders/01/cancel", "/api/reminders/0/cancel", "/api/reminders/-1/cancel", "/api/reminders/+1/cancel",
		"/api/reminders/1.0/retry", "/api/reminders/9223372036854775808/delete", "/api/reminders/1/extra/cancel",
		"/api/reminders/cancel", "/api/reminders/1/delete/", "/api/reminders/1/approve",
	} {
		status, body = settingsRequest(t, server.URL, http.MethodPost, path, bearer, "", "")
		if status != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
			t.Errorf("non-canonical path %q status=%d body=%s", path, status, body)
		}
	}
}

func TestReminderEndpointsRequireStrictMethodsBodiesAndQueries(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	active := createWebappReminder(t, app, testChatID, "strict active", store.ReminderCadenceOnce, now.Add(time.Hour))
	failed := createWebappReminder(t, app, testChatID, "strict failed", store.ReminderCadenceOnce, now)
	failWebappReminder(t, app, failed.ID, now)

	status, body, headers := apiRequest(t, server, http.MethodPost, "/api/reminders", bearer)
	if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodGet {
		t.Fatalf("POST reminder list status=%d Allow=%q body=%s", status, headers.Get("Allow"), body)
	}
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/reminders?limit=20", bearer)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"invalid_query"`) {
		t.Fatalf("reminder list query status=%d body=%s", status, body)
	}

	paths := []string{
		fmt.Sprintf("/api/reminders/%d/cancel", active.ID),
		fmt.Sprintf("/api/reminders/%d/retry", failed.ID),
		fmt.Sprintf("/api/reminders/%d/delete", failed.ID),
	}
	for _, path := range paths {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			status, body, headers = apiRequest(t, server, method, path, bearer)
			if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodPost {
				t.Errorf("%s %s status=%d Allow=%q body=%s", method, path, status, headers.Get("Allow"), body)
			}
		}
		for _, test := range []struct {
			name string
			path string
			body string
		}{
			{"JSON body", path, `{}`},
			{"invalid body", path, `{`},
			{"whitespace body", path, " \n"},
			{"query", path + "?chat_id=4242", ""},
		} {
			status, body = settingsRequest(t, server.URL, http.MethodPost, test.path, bearer, test.body, "")
			if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"invalid_body"`) {
				t.Errorf("%s %s status=%d body=%s", test.name, path, status, body)
			}
		}
		status, body = settingsRequest(t, server.URL, http.MethodPost, path, bearer, strings.Repeat("x", maxBodyBytes+1), "")
		if status != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), `"code":"body_too_large"`) {
			t.Errorf("oversized %s status=%d body=%s", path, status, body)
		}
	}
	if state, _, _ := webappReminderState(t, app, active.ID); state != store.ReminderStatusActive {
		t.Fatalf("strict requests changed active reminder to %q", state)
	}
	if state, _, found := webappReminderState(t, app, failed.ID); !found || state != store.ReminderStatusFailed {
		t.Fatalf("strict requests changed failed reminder to %q found=%v", state, found)
	}
}

func TestReminderEndpointsRejectWrongSessionAndRemovedAllowlist(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	active := createWebappReminder(t, app, testChatID, "private reminder", store.ReminderCadenceOnce, now.Add(time.Hour))
	path := fmt.Sprintf("/api/reminders/%d/cancel", active.ID)
	for _, wrongBearer := range []string{"", "not-a-session"} {
		status, body, _ := apiRequest(t, server, http.MethodGet, "/api/reminders", wrongBearer)
		if status != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"session_expired"`) {
			t.Errorf("wrong session list status=%d body=%s", status, body)
		}
		status, body = settingsRequest(t, server.URL, http.MethodPost, path, wrongBearer, "", "")
		if status != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"session_expired"`) {
			t.Errorf("wrong session action status=%d body=%s", status, body)
		}
	}
	if state, _, _ := webappReminderState(t, app, active.ID); state != store.ReminderStatusActive {
		t.Fatalf("wrong sessions changed reminder to %q", state)
	}

	app, server = newTestApp(t)
	allowed := true
	app.auth.authorize = func(int64) bool { return allowed }
	bearer = launchSession(t, app, server)
	active = createWebappReminder(t, app, testChatID, "private reminder", store.ReminderCadenceOnce, now.Add(time.Hour))
	allowed = false
	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/reminders", bearer)
	if status != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"session_expired"`) {
		t.Fatalf("removed allowlist list status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/reminders/%d/cancel", active.ID), bearer, "", "")
	if status != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"session_expired"`) {
		t.Fatalf("removed allowlist action status=%d body=%s", status, body)
	}
	if state, _, _ := webappReminderState(t, app, active.ID); state != store.ReminderStatusActive {
		t.Fatalf("removed allowlist changed reminder to %q", state)
	}
}
