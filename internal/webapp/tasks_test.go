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

type recordingTaskLifecycle struct {
	base        TaskLifecycle
	cancelCalls int
	resumeCalls int
	clearCalls  int
}

func (l *recordingTaskLifecycle) CancelTask(ctx context.Context, chatID int64) (store.Task, bool, error) {
	l.cancelCalls++
	return l.base.CancelTask(ctx, chatID)
}

func (l *recordingTaskLifecycle) ResumeTask(ctx context.Context, chatID int64, taskID int64) (store.Task, bool, error) {
	l.resumeCalls++
	return l.base.ResumeTask(ctx, chatID, taskID)
}

func (l *recordingTaskLifecycle) ClearTasks(ctx context.Context, chatID int64) (int64, error) {
	l.clearCalls++
	return l.base.ClearTasks(ctx, chatID)
}

func TestTaskControlsUseConfiguredLifecycle(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	lifecycle := &recordingTaskLifecycle{base: storeTaskLifecycle{db: app.db}}
	app.SetTaskLifecycle(lifecycle)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	created := createWebappTask(t, app, testChatID, now)

	status, body := settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/cancel", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/tasks/%d/resume", created.ID), bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/clear", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", status, body)
	}
	if lifecycle.cancelCalls != 1 || lifecycle.resumeCalls != 1 || lifecycle.clearCalls != 1 {
		t.Fatalf("lifecycle calls cancel=%d resume=%d clear=%d", lifecycle.cancelCalls, lifecycle.resumeCalls, lifecycle.clearCalls)
	}
	if got := webappTaskCount(t, app, testChatID); got != 0 {
		t.Fatalf("configured lifecycle left %d tasks", got)
	}
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

func webappTaskCount(t *testing.T, app *App, chatID int64) int {
	t.Helper()
	var count int
	if err := app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tasks WHERE chat_id = ?`, chatID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestTaskClearDeletesAllCurrentChatTasksOnly(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	createWebappTask(t, app, testChatID, now)
	claimed := claimWebappTask(t, app, now)
	if changed, err := store.CompleteTaskStep(ctx, app.db, claimed, "step result", now); err != nil || !changed {
		t.Fatalf("CompleteTaskStep changed=%v err=%v", changed, err)
	}
	claimed = claimWebappTask(t, app, now)
	answer := store.Message{ChatID: testChatID, UserID: testUserID, Role: store.RoleAssistant, Content: "completed result"}
	if changed, err := store.CompleteTask(ctx, app.db, claimed, answer, now); err != nil || !changed {
		t.Fatalf("CompleteTask changed=%v err=%v", changed, err)
	}
	createWebappTask(t, app, testChatID, now.Add(time.Second))
	if _, changed, err := store.CancelTask(ctx, app.db, testChatID, now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("CancelTask changed=%v err=%v", changed, err)
	}
	createWebappTask(t, app, testChatID, now.Add(2*time.Second))
	claimed = claimWebappTask(t, app, now.Add(2*time.Second))
	if changed, err := store.FailTask(ctx, app.db, claimed, "terminal failure", now.Add(2*time.Second)); err != nil || !changed {
		t.Fatalf("FailTask changed=%v err=%v", changed, err)
	}
	createWebappTask(t, app, testChatID, now.Add(3*time.Second))
	claimed = claimWebappTask(t, app, now.Add(3*time.Second))
	approvalToken := "fedcba9876543210fedcba9876543210"
	if saved, err := store.SaveTaskApproval(ctx, app.db, claimed, approvalToken, 700, now.Add(time.Minute), `{"request":{}}`, now); err != nil || !saved {
		t.Fatalf("SaveTaskApproval saved=%v err=%v", saved, err)
	}
	other := createWebappTask(t, app, testChatID+1, now)
	if got := webappTaskCount(t, app, testChatID); got != 4 {
		t.Fatalf("current chat fixture has %d tasks, want 4", got)
	}

	status, body := settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/clear", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", status, body)
	}
	var decoded struct {
		OK      bool  `json:"ok"`
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || !decoded.OK || decoded.Deleted != 4 {
		t.Fatalf("clear response=%s err=%v", body, err)
	}
	if got := webappTaskCount(t, app, testChatID); got != 0 {
		t.Fatalf("clear left %d current chat tasks", got)
	}
	if got := webappTaskCount(t, app, testChatID+1); got != 1 {
		t.Fatalf("clear changed other chat task count to %d", got)
	}
	if current := currentWebappTask(t, app, testChatID+1); current.ID != other.ID || current.Status != store.TaskStatusReady {
		t.Fatalf("other chat task changed: %+v", current)
	}
	if _, found, err := store.TaskByApprovalToken(ctx, app.db, approvalToken); err != nil || found {
		t.Fatalf("cleared approval remains restorable: found=%v err=%v", found, err)
	}

	status, body = settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/clear", bearer, "", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"deleted":0`) {
		t.Fatalf("empty clear status=%d body=%s", status, body)
	}
	if got := webappTaskCount(t, app, testChatID+1); got != 1 {
		t.Fatalf("empty clear changed other chat task count to %d", got)
	}
}

func TestTaskClearStrictMethodBodyAndQuery(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	createWebappTask(t, app, testChatID, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		status, body, headers := apiRequest(t, server, method, "/api/tasks/clear", bearer)
		if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodPost {
			t.Errorf("%s clear status=%d Allow=%q body=%s", method, status, headers.Get("Allow"), body)
		}
	}
	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{"JSON body", "/api/tasks/clear", `{}`},
		{"invalid body", "/api/tasks/clear", `{`},
		{"whitespace body", "/api/tasks/clear", " \n"},
		{"query", "/api/tasks/clear?chat_id=4242", ""},
	} {
		status, body := settingsRequest(t, server.URL, http.MethodPost, test.path, bearer, test.body, "")
		if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"invalid_body"`) {
			t.Errorf("%s status=%d body=%s", test.name, status, body)
		}
	}
	status, body := settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/clear", bearer, strings.Repeat("x", maxBodyBytes+1), "")
	if status != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), `"code":"body_too_large"`) {
		t.Fatalf("oversized clear status=%d body=%s", status, body)
	}
	if got := webappTaskCount(t, app, testChatID); got != 1 {
		t.Fatalf("rejected clear requests deleted tasks: count=%d", got)
	}
}

func TestTaskClearRejectsWrongSessionAndRemovedAllowlist(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	createWebappTask(t, app, testChatID, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	for _, wrongBearer := range []string{"", "not-a-session"} {
		status, body := settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/clear", wrongBearer, "", "")
		if status != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"session_expired"`) {
			t.Errorf("wrong session status=%d body=%s", status, body)
		}
	}
	if got := webappTaskCount(t, app, testChatID); got != 1 {
		t.Fatalf("wrong sessions deleted tasks: count=%d", got)
	}

	app, server = newTestApp(t)
	allowed := true
	app.auth.authorize = func(int64) bool { return allowed }
	bearer = launchSession(t, app, server)
	createWebappTask(t, app, testChatID, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	allowed = false
	status, body := settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/clear", bearer, "", "")
	if status != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"session_expired"`) {
		t.Fatalf("removed allowlist status=%d body=%s", status, body)
	}
	if got := webappTaskCount(t, app, testChatID); got != 1 {
		t.Fatalf("removed allowlist request deleted tasks: count=%d", got)
	}
}

func TestTaskClearReportsStoreFailure(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	if err := app.db.Close(); err != nil {
		t.Fatal(err)
	}
	status, body := settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/clear", bearer, "", "")
	if status != http.StatusInternalServerError || !strings.Contains(string(body), `"code":"task_mutation_failed"`) {
		t.Fatalf("store failure status=%d body=%s", status, body)
	}
}

func setWebappTaskStatus(t *testing.T, app *App, taskID int64, status string) {
	t.Helper()
	if _, err := app.db.ExecContext(context.Background(), `UPDATE tasks SET status = ? WHERE id = ?`, status, taskID); err != nil {
		t.Fatal(err)
	}
}

func webappTaskExists(t *testing.T, app *App, chatID int64, taskID int64) bool {
	t.Helper()
	var found int
	err := app.db.QueryRowContext(context.Background(), `SELECT 1 FROM tasks WHERE chat_id = ? AND id = ?`, chatID, taskID).Scan(&found)
	if err != nil {
		return false
	}
	return found == 1
}

func TestTaskDeleteAllowsTerminalOnlyAndIsScoped(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, testChatID+1, testUserID)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	terminal := make([]store.Task, 0, 3)
	for index, status := range []string{store.TaskStatusCompleted, store.TaskStatusFailed, store.TaskStatusCancelled} {
		created := createWebappTask(t, app, testChatID, now.Add(time.Duration(index)*time.Second))
		setWebappTaskStatus(t, app, created.ID, status)
		terminal = append(terminal, created)
	}
	activeTask := createWebappTask(t, app, testChatID, now.Add(3*time.Second))
	active := []store.Task{activeTask}
	other := createWebappTask(t, app, testChatID+1, now)
	setWebappTaskStatus(t, app, other.ID, store.TaskStatusCompleted)
	path := fmt.Sprintf("/api/tasks/%d/delete", terminal[0].ID)

	status, body := settingsRequest(t, server.URL, http.MethodPost, path, otherBearer, "", "")
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"task_not_deletable"`) {
		t.Fatalf("cross-chat delete status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/tasks/%d/delete", other.ID), bearer, "", "")
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"task_not_deletable"`) {
		t.Fatalf("wrong-chat delete status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, "/api/tasks/999999/delete", bearer, "", "")
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"task_not_deletable"`) {
		t.Fatalf("missing delete status=%d body=%s", status, body)
	}
	for _, taskStatus := range []string{store.TaskStatusReady, store.TaskStatusRunning, store.TaskStatusAwaitingApproval} {
		setWebappTaskStatus(t, app, activeTask.ID, taskStatus)
		status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/tasks/%d/delete", activeTask.ID), bearer, "", "")
		if status != http.StatusConflict || !strings.Contains(string(body), `"code":"task_not_deletable"`) {
			t.Errorf("active %s task delete status=%d body=%s", taskStatus, status, body)
		}
	}
	for _, invalidPath := range []string{
		"/api/tasks/01/delete", "/api/tasks/0/delete", "/api/tasks/-1/delete", "/api/tasks/+1/delete",
		"/api/tasks/1.0/delete", "/api/tasks/9223372036854775808/delete", "/api/tasks/1/extra/delete",
		"/api/tasks/delete", "/api/tasks/1/delete/",
	} {
		status, body = settingsRequest(t, server.URL, http.MethodPost, invalidPath, bearer, "", "")
		if status != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
			t.Errorf("non-canonical path %q status=%d body=%s", invalidPath, status, body)
		}
	}
	if got := webappTaskCount(t, app, testChatID); got != 4 || !webappTaskExists(t, app, testChatID+1, other.ID) {
		t.Fatalf("rejected deletes changed tasks: current=%d", got)
	}

	for _, task := range terminal {
		status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/tasks/%d/delete", task.ID), bearer, "", "")
		if status != http.StatusOK {
			t.Fatalf("terminal task %d delete status=%d body=%s", task.ID, status, body)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil || len(decoded) != 2 || decoded["ok"] != true || decoded["task_id"] != float64(task.ID) {
			t.Fatalf("task %d delete response=%s err=%v", task.ID, body, err)
		}
		if webappTaskExists(t, app, testChatID, task.ID) {
			t.Fatalf("terminal task %d still exists", task.ID)
		}
	}
	for _, task := range active {
		if !webappTaskExists(t, app, testChatID, task.ID) {
			t.Fatalf("active task %d was deleted", task.ID)
		}
	}
	if !webappTaskExists(t, app, testChatID+1, other.ID) {
		t.Fatal("deletes changed another chat")
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, path, bearer, "", "")
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"task_not_deletable"`) {
		t.Fatalf("second delete status=%d body=%s", status, body)
	}
}

func TestTaskDeleteRequiresStrictMethodBodyAndQuery(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	created := createWebappTask(t, app, testChatID, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	setWebappTaskStatus(t, app, created.ID, store.TaskStatusCompleted)
	path := fmt.Sprintf("/api/tasks/%d/delete", created.ID)

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		status, body, headers := apiRequest(t, server, method, path, bearer)
		if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodPost {
			t.Errorf("%s delete status=%d Allow=%q body=%s", method, status, headers.Get("Allow"), body)
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
		status, body := settingsRequest(t, server.URL, http.MethodPost, test.path, bearer, test.body, "")
		if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"invalid_body"`) {
			t.Errorf("%s status=%d body=%s", test.name, status, body)
		}
	}
	status, body := settingsRequest(t, server.URL, http.MethodPost, path, bearer, strings.Repeat("x", maxBodyBytes+1), "")
	if status != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), `"code":"body_too_large"`) {
		t.Fatalf("oversized delete status=%d body=%s", status, body)
	}
	if !webappTaskExists(t, app, testChatID, created.ID) {
		t.Fatal("rejected delete requests removed the task")
	}
}

func TestTaskDeleteRejectsWrongSessionAndRemovedAllowlist(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	created := createWebappTask(t, app, testChatID, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	setWebappTaskStatus(t, app, created.ID, store.TaskStatusCompleted)
	path := fmt.Sprintf("/api/tasks/%d/delete", created.ID)
	for _, wrongBearer := range []string{"", "not-a-session"} {
		status, body := settingsRequest(t, server.URL, http.MethodPost, path, wrongBearer, "", "")
		if status != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"session_expired"`) {
			t.Errorf("wrong session status=%d body=%s", status, body)
		}
	}
	if !webappTaskExists(t, app, testChatID, created.ID) {
		t.Fatal("wrong sessions deleted the task")
	}

	app, server = newTestApp(t)
	allowed := true
	app.auth.authorize = func(int64) bool { return allowed }
	bearer = launchSession(t, app, server)
	created = createWebappTask(t, app, testChatID, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	setWebappTaskStatus(t, app, created.ID, store.TaskStatusCompleted)
	allowed = false
	status, body := settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/tasks/%d/delete", created.ID), bearer, "", "")
	if status != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"session_expired"`) {
		t.Fatalf("removed allowlist status=%d body=%s", status, body)
	}
	if !webappTaskExists(t, app, testChatID, created.ID) {
		t.Fatal("removed allowlist request deleted the task")
	}
}

func TestTaskDeleteReportsStoreFailure(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	created := createWebappTask(t, app, testChatID, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	setWebappTaskStatus(t, app, created.ID, store.TaskStatusCompleted)
	if err := app.db.Close(); err != nil {
		t.Fatal(err)
	}
	status, body := settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/tasks/%d/delete", created.ID), bearer, "", "")
	if status != http.StatusInternalServerError || !strings.Contains(string(body), `"code":"task_mutation_failed"`) {
		t.Fatalf("store failure status=%d body=%s", status, body)
	}
}
