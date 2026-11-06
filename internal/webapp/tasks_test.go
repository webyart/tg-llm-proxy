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

func createWebappTask(t *testing.T, app *App, chatID int64, now time.Time) store.Task {
	t.Helper()
	ctx := context.Background()
	if err := store.EnsureChat(ctx, app.db, chatID, testUserID, ""); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateTask(ctx, app.db, store.Task{
		ChatID: chatID, UserID: testUserID, Goal: "Mini App task", Model: "test-model",
		PlanJSON: `[{"description":"one step"}]`, DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func claimWebappTask(t *testing.T, app *App, now time.Time) store.Task {
	t.Helper()
	claimed, found, err := store.ClaimNextTask(context.Background(), app.db, now, time.Minute)
	if err != nil || !found {
		t.Fatalf("ClaimNextTask found=%v err=%v", found, err)
	}
	return claimed
}

func currentWebappTask(t *testing.T, app *App, chatID int64) store.Task {
	t.Helper()
	current, found, err := store.CurrentTask(context.Background(), app.db, chatID)
	if err != nil || !found {
		t.Fatalf("CurrentTask found=%v err=%v", found, err)
	}
	return current
}

func TestTaskListShowsSanitizedFailureAndExpiry(t *testing.T) {
	app, server := newTestApp(t)
	app.cfg.StrataKey = "test-strata-key"
	bearer := launchSession(t, app, server)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	created := createWebappTask(t, app, testChatID, now)
	claimed := claimWebappTask(t, app, now)
	reason := "<script>alert('safe text only')</script> " + app.cfg.BotToken + " " + app.cfg.StrataKey + " " + strings.Repeat("я", 400)
	if changed, err := store.FailTask(context.Background(), app.db, claimed, reason, now); err != nil || !changed {
		t.Fatalf("FailTask changed=%v err=%v", changed, err)
	}

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/tasks?limit=1", bearer)
	if status != http.StatusOK {
		t.Fatalf("tasks status=%d body=%s", status, body)
	}
	var decoded struct {
		Items []taskItem `json:"items"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.Items) != 1 {
		t.Fatalf("tasks body=%s err=%v", body, err)
	}
	item := decoded.Items[0]
	if item.ID != created.ID || item.Status != store.TaskStatusFailed || item.ExpiresAt == "" {
		t.Fatalf("task item=%+v", item)
	}
	if !item.ErrorTruncated || len([]rune(item.Error)) != maxTaskErrorRunes+1 {
		t.Fatalf("error was not bounded: runes=%d truncated=%v", len([]rune(item.Error)), item.ErrorTruncated)
	}
	for _, forbidden := range []string{app.cfg.BotToken, app.cfg.StrataKey} {
		if forbidden == "" {
			continue
		}
		if strings.Contains(item.Error, forbidden) || strings.Contains(string(body), forbidden) {
			t.Fatal("task failure exposed a configured token")
		}
	}
	if !strings.Contains(item.Error, "***") || strings.Contains(string(body), "<script>") {
		t.Fatalf("task failure was not safely encoded and redacted: %s", body)
	}
}

func TestTaskCancelIsScopedAndRejectsWrongStatus(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, testChatID+1, testUserID)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	created := createWebappTask(t, app, testChatID, now)

	status, body := settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/cancel", otherBearer, "", "")
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"task_not_active"`) {
		t.Fatalf("wrong-scope cancel status=%d body=%s", status, body)
	}
	if current := currentWebappTask(t, app, testChatID); current.Status != store.TaskStatusReady {
		t.Fatalf("wrong-scope cancel changed task to %q", current.Status)
	}

	status, body = settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/cancel", bearer, "", "")
	if status != http.StatusOK || !strings.Contains(string(body), fmt.Sprintf(`"task_id":%d`, created.ID)) {
		t.Fatalf("cancel status=%d body=%s", status, body)
	}
	current := currentWebappTask(t, app, testChatID)
	if current.Status != store.TaskStatusCancelled || current.FinishedAt == nil || current.ClaimToken != "" {
		t.Fatalf("cancelled task=%+v", current)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/cancel", bearer, "", "")
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"task_not_active"`) {
		t.Fatalf("second cancel status=%d body=%s", status, body)
	}
}

func TestTaskResumeIsLatestOnlyAndRejectsWrongScopeAndStatus(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, testChatID+1, testUserID)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	older := createWebappTask(t, app, testChatID, now)
	if _, changed, err := store.CancelTask(context.Background(), app.db, testChatID, now); err != nil || !changed {
		t.Fatalf("cancel older changed=%v err=%v", changed, err)
	}
	latest := createWebappTask(t, app, testChatID, now.Add(time.Second))
	claimed := claimWebappTask(t, app, now.Add(time.Second))
	if changed, err := store.FailTask(context.Background(), app.db, claimed, "retryable failure", now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("FailTask changed=%v err=%v", changed, err)
	}

	latestPath := fmt.Sprintf("/api/tasks/%d/resume", latest.ID)
	status, body := settingsRequest(t, server.URL, http.MethodPost, latestPath, otherBearer, "", "")
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"task_not_resumable"`) {
		t.Fatalf("wrong-scope resume status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/tasks/%d/resume", older.ID), bearer, "", "")
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"task_not_resumable"`) {
		t.Fatalf("non-latest resume status=%d body=%s", status, body)
	}
	if current := currentWebappTask(t, app, testChatID); current.ID != latest.ID || current.Status != store.TaskStatusFailed {
		t.Fatalf("rejected resumes changed latest task=%+v", current)
	}

	status, body = settingsRequest(t, server.URL, http.MethodPost, latestPath, bearer, "", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"status":"ready"`) {
		t.Fatalf("resume status=%d body=%s", status, body)
	}
	resumed := currentWebappTask(t, app, testChatID)
	if resumed.ID != latest.ID || resumed.Status != store.TaskStatusReady || resumed.Attempts != 0 || resumed.Error != "" || resumed.FinishedAt != nil {
		t.Fatalf("resumed task=%+v", resumed)
	}
	var olderStatus string
	if err := app.db.QueryRowContext(context.Background(), `SELECT status FROM tasks WHERE id = ?`, older.ID).Scan(&olderStatus); err != nil || olderStatus != store.TaskStatusCancelled {
		t.Fatalf("older task status=%q err=%v", olderStatus, err)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, latestPath, bearer, "", "")
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"task_not_resumable"`) {
		t.Fatalf("active resume status=%d body=%s", status, body)
	}
}

func TestTaskResumeCompletedDoesNotDuplicateResult(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	created := createWebappTask(t, app, testChatID, now)
	claimed := claimWebappTask(t, app, now)
	if changed, err := store.CompleteTaskStep(context.Background(), app.db, claimed, "step result", now); err != nil || !changed {
		t.Fatalf("CompleteTaskStep changed=%v err=%v", changed, err)
	}
	claimed = claimWebappTask(t, app, now)
	answer := store.Message{ChatID: testChatID, UserID: testUserID, Role: store.RoleAssistant, Content: "one completed result"}
	if changed, err := store.CompleteTask(context.Background(), app.db, claimed, answer, now); err != nil || !changed {
		t.Fatalf("CompleteTask changed=%v err=%v", changed, err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		status, body := settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/tasks/%d/resume", created.ID), bearer, "", "")
		if status != http.StatusConflict || !strings.Contains(string(body), `"code":"task_not_resumable"`) {
			t.Fatalf("completed resume %d status=%d body=%s", attempt, status, body)
		}
	}
	history, err := store.HistoryOfChat(context.Background(), app.db, testChatID, 10)
	if err != nil || len(history) != 1 || history[0].Content != answer.Content {
		t.Fatalf("history after completed resumes=%+v err=%v", history, err)
	}
	if current := currentWebappTask(t, app, testChatID); current.Status != store.TaskStatusCompleted {
		t.Fatalf("completed task changed to %q", current.Status)
	}
}

func TestTaskNetworkApprovalRemainsTelegramOnly(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	created := createWebappTask(t, app, testChatID, now)
	claimed := claimWebappTask(t, app, now)
	approvalToken := "0123456789abcdef0123456789abcdef"
	if saved, err := store.SaveTaskApproval(context.Background(), app.db, claimed, approvalToken, 700, now.Add(time.Minute), `{"request":{}}`, now); err != nil || !saved {
		t.Fatalf("SaveTaskApproval saved=%v err=%v", saved, err)
	}

	status, body := settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/tasks/%d/approve", created.ID), bearer, "", "")
	if status != http.StatusNotFound {
		t.Fatalf("Mini App approval status=%d body=%s", status, body)
	}
	if current := currentWebappTask(t, app, testChatID); current.Status != store.TaskStatusAwaitingApproval || current.ApprovalToken != approvalToken {
		t.Fatalf("approval endpoint changed task=%+v", current)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/cancel", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("cancel awaiting approval status=%d body=%s", status, body)
	}
	if current := currentWebappTask(t, app, testChatID); current.Status != store.TaskStatusCancelled || current.ApprovalToken != "" {
		t.Fatalf("cancelled approval task=%+v", current)
	}
}
