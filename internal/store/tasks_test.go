package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testTaskPlan = `{"steps":[{"description":"Reason about the goal"}]}`

func taskTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenMemory(context.Background(), strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newStoreTask(chatID int64, now time.Time) Task {
	return Task{ChatID: chatID, UserID: 11, Goal: "answer a bounded question", Model: "selected-model", PlanJSON: testTaskPlan,
		DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now}
}

// taskResultContents decodes the stored step contents regardless of the storage generation: older rows keep
// the bare string array, current rows store an object with the usage of the call.
func taskResultContents(t *testing.T, task Task) []string {
	t.Helper()
	results, err := ParseTaskResults(task.ResultsJSON)
	if err != nil {
		t.Fatalf("ParseTaskResults(%q): %v", task.ResultsJSON, err)
	}
	contents := make([]string, len(results))
	for index := range results {
		contents[index] = results[index].Content
	}
	return contents
}

func TestTaskMigration9PreservesDataAndIndexes(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 8)
	if err := EnsureChat(ctx, db, 100, 11, "kept"); err != nil {
		t.Fatal(err)
	}
	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var title string
	if err := db.QueryRowContext(ctx, `SELECT title FROM chats WHERE chat_id = 100`).Scan(&title); err != nil || title != "kept" {
		t.Fatalf("preserved title=%q err=%v", title, err)
	}
	for _, index := range []string{"uq_tasks_chat_active", "uq_tasks_approval_token", "idx_tasks_due", "idx_tasks_lease", "idx_tasks_chat"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s count=%d err=%v", index, count, err)
		}
	}
}

func TestTaskSingletonClaimStepAndFinalCompletion(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, chatID := range []int64{100, 101} {
		if err := EnsureChat(ctx, db, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
	}
	created, err := CreateTask(ctx, db, newStoreTask(100, now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTask(ctx, db, newStoreTask(100, now)); !errors.Is(err, ErrTaskActive) {
		t.Fatalf("second active task err=%v", err)
	}
	other, err := CreateTask(ctx, db, newStoreTask(101, now))
	if err != nil || other.ChatID != 101 {
		t.Fatalf("other chat task=%+v err=%v", other, err)
	}
	claimed, found, err := ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found || claimed.ID != created.ID || claimed.Status != TaskStatusRunning || claimed.Attempts != 1 {
		t.Fatalf("claim=%+v found=%v err=%v", claimed, found, err)
	}
	otherClaim, found, err := ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found || otherClaim.ChatID != 101 {
		t.Fatalf("independent chat claim=%+v found=%v err=%v", otherClaim, found, err)
	}
	if _, ok, err := CancelTask(ctx, db, 101, now); err != nil || !ok {
		t.Fatalf("cancel independent chat ok=%v err=%v", ok, err)
	}
	if ok, err := CompleteTaskStep(ctx, db, claimed, "step result", now); err != nil || !ok {
		t.Fatalf("CompleteTaskStep ok=%v err=%v", ok, err)
	}
	if ok, err := CompleteTaskStep(ctx, db, claimed, "duplicate", now); err != nil || ok {
		t.Fatalf("stale step completion ok=%v err=%v", ok, err)
	}
	finalClaim, found, err := ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found || finalClaim.NextStep != 1 {
		t.Fatalf("final claim=%+v found=%v err=%v", finalClaim, found, err)
	}
	ok, err := CompleteTask(ctx, db, finalClaim, Message{ChatID: 100, UserID: 11, Role: RoleAssistant, Content: "consolidated", Model: "selected-model"}, now)
	if err != nil || !ok {
		t.Fatalf("CompleteTask ok=%v err=%v", ok, err)
	}
	if ok, err := CompleteTask(ctx, db, finalClaim, Message{ChatID: 100, UserID: 11, Role: RoleAssistant, Content: "duplicate"}, now); err != nil || ok {
		t.Fatalf("duplicate final completion ok=%v err=%v", ok, err)
	}
	rows, err := HistoryOfChat(ctx, db, 100, 10)
	if err != nil || len(rows) != 1 || rows[0].Content != "consolidated" || rows[0].UserID != 11 {
		t.Fatalf("history=%+v err=%v", rows, err)
	}
	if _, found, err := ClaimNextTask(ctx, db, now.Add(2*time.Minute), time.Minute); err != nil || found {
		t.Fatalf("completed task reclaimed: found=%v err=%v", found, err)
	}
	if _, err := CreateTask(ctx, db, newStoreTask(100, now)); err != nil {
		t.Fatalf("terminal task did not release singleton: %v", err)
	}
}

func TestTaskCancellationLeaseRecoveryAndExpiry(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	created, err := CreateTask(ctx, db, newStoreTask(100, now))
	if err != nil {
		t.Fatal(err)
	}
	_, found, err := ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if _, found, err := ClaimNextTask(ctx, db, now.Add(30*time.Second), time.Minute); err != nil || found {
		t.Fatalf("unexpired lease recovered found=%v err=%v", found, err)
	}
	recovered, found, err := ClaimNextTask(ctx, db, now.Add(time.Minute), time.Minute)
	if err != nil || !found || recovered.ID != created.ID || recovered.Attempts != 2 {
		t.Fatalf("recovered=%+v found=%v err=%v", recovered, found, err)
	}
	cancelled, ok, err := CancelTask(ctx, db, 100, now.Add(time.Minute))
	if err != nil || !ok || cancelled.Status != TaskStatusCancelled {
		t.Fatalf("cancel=%+v ok=%v err=%v", cancelled, ok, err)
	}
	if ok, err := CompleteTaskStep(ctx, db, recovered, "must not advance", now); err != nil || ok {
		t.Fatalf("cancelled claim advanced ok=%v err=%v", ok, err)
	}
	if _, ok, err := CancelTask(ctx, db, 101, now); err != nil || ok {
		t.Fatalf("cross-chat cancel ok=%v err=%v", ok, err)
	}
	second, err := CreateTask(ctx, db, newStoreTask(100, now))
	if err != nil {
		t.Fatal(err)
	}
	expired, err := ExpireTasks(ctx, db, now.Add(2*time.Hour), 4)
	if err != nil || len(expired) != 1 || expired[0].ID != second.ID || expired[0].Status != TaskStatusFailed {
		t.Fatalf("expired=%+v err=%v", expired, err)
	}
	current, found, err := CurrentTask(ctx, db, 100)
	if err != nil || !found || current.Error == "" {
		t.Fatalf("expired current=%+v found=%v err=%v", current, found, err)
	}
}

func TestTaskApprovalConsumptionIsExactAndSingleUse(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTask(ctx, db, newStoreTask(100, now)); err != nil {
		t.Fatal(err)
	}
	claimed, _, err := ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token := "0123456789abcdef0123456789abcdef"
	saved, err := SaveTaskApproval(ctx, db, claimed, token, 700, now.Add(5*time.Minute), `{"request":{}}`, now)
	if err != nil || !saved {
		t.Fatalf("SaveTaskApproval saved=%v err=%v", saved, err)
	}
	pending, found, err := TaskByApprovalToken(ctx, db, token)
	if err != nil || !found || pending.Status != TaskStatusAwaitingApproval {
		t.Fatalf("pending=%+v found=%v err=%v", pending, found, err)
	}
	if _, found, err := ClaimNextTask(ctx, db, now.Add(time.Hour), time.Minute); err != nil || found {
		t.Fatalf("awaiting approval was executed by scheduler: found=%v err=%v", found, err)
	}
	if _, ok, err := ConsumeTaskApproval(ctx, db, pending.ID, 100, 12, 700, token, now, time.Minute); err != nil || ok {
		t.Fatalf("wrong user consumed approval ok=%v err=%v", ok, err)
	}
	resumed, ok, err := ConsumeTaskApproval(ctx, db, pending.ID, 100, 11, 700, token, now, time.Minute)
	if err != nil || !ok || resumed.Status != TaskStatusRunning || resumed.ApprovalToken != "" {
		t.Fatalf("resumed=%+v ok=%v err=%v", resumed, ok, err)
	}
	if _, ok, err := ConsumeTaskApproval(ctx, db, pending.ID, 100, 11, 700, token, now, time.Minute); err != nil || ok {
		t.Fatalf("replayed approval consumed ok=%v err=%v", ok, err)
	}
	if _, found, err := TaskByApprovalToken(ctx, db, token); err != nil || found {
		t.Fatalf("consumed token remained restorable found=%v err=%v", found, err)
	}
}

func TestDeleteTaskDeletesTerminalTasksOnlyForCurrentChat(t *testing.T) {
	for _, status := range []string{TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			db := taskTestDB(t)
			now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
			for _, chatID := range []int64{100, 101} {
				if err := EnsureChat(ctx, db, chatID, 11, ""); err != nil {
					t.Fatal(err)
				}
			}
			created, err := CreateTask(ctx, db, newStoreTask(100, now))
			if err != nil {
				t.Fatal(err)
			}
			switch status {
			case TaskStatusCompleted:
				claimed, found, err := ClaimNextTask(ctx, db, now, time.Minute)
				if err != nil || !found {
					t.Fatalf("claim found=%v err=%v", found, err)
				}
				if ok, err := CompleteTaskStep(ctx, db, claimed, "step result", now); err != nil || !ok {
					t.Fatalf("CompleteTaskStep ok=%v err=%v", ok, err)
				}
				finalClaim, found, err := ClaimNextTask(ctx, db, now, time.Minute)
				if err != nil || !found {
					t.Fatalf("final claim found=%v err=%v", found, err)
				}
				answer := Message{ChatID: 100, UserID: 11, Role: RoleAssistant, Content: "done"}
				if ok, err := CompleteTask(ctx, db, finalClaim, answer, now); err != nil || !ok {
					t.Fatalf("CompleteTask ok=%v err=%v", ok, err)
				}
			case TaskStatusFailed:
				claimed, found, err := ClaimNextTask(ctx, db, now, time.Minute)
				if err != nil || !found {
					t.Fatalf("claim found=%v err=%v", found, err)
				}
				if ok, err := FailTask(ctx, db, claimed, "failed", now); err != nil || !ok {
					t.Fatalf("FailTask ok=%v err=%v", ok, err)
				}
			case TaskStatusCancelled:
				if _, ok, err := CancelTask(ctx, db, 100, now); err != nil || !ok {
					t.Fatalf("CancelTask ok=%v err=%v", ok, err)
				}
			}
			other, err := CreateTask(ctx, db, newStoreTask(101, now))
			if err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name   string
				chatID int64
				taskID int64
			}{
				{"zero chat", 0, created.ID},
				{"zero task", 100, 0},
				{"negative task", 100, -1},
				{"missing", 100, created.ID + 1000},
				{"wrong chat", 101, created.ID},
			} {
				t.Run(test.name, func(t *testing.T) {
					if deleted, err := DeleteTask(ctx, db, test.chatID, test.taskID); err != nil || deleted {
						t.Fatalf("deleted=%v err=%v", deleted, err)
					}
				})
			}
			current, found, err := CurrentTask(ctx, db, 100)
			if err != nil || !found || current.Status != status {
				t.Fatalf("task after rejected deletes=%+v found=%v err=%v", current, found, err)
			}
			if deleted, err := DeleteTask(ctx, db, 100, created.ID); err != nil || !deleted {
				t.Fatalf("owner delete deleted=%v err=%v", deleted, err)
			}
			if deleted, err := DeleteTask(ctx, db, 100, created.ID); err != nil || deleted {
				t.Fatalf("stale delete deleted=%v err=%v", deleted, err)
			}
			if _, found, err := CurrentTask(ctx, db, 100); err != nil || found {
				t.Fatalf("deleted task remains found=%v err=%v", found, err)
			}
			if current, found, err := CurrentTask(ctx, db, 101); err != nil || !found || current.ID != other.ID {
				t.Fatalf("other chat task=%+v found=%v err=%v", current, found, err)
			}
		})
	}
}

func TestDeleteTaskPreservesActiveTasksAndTransientState(t *testing.T) {
	for _, status := range []string{TaskStatusReady, TaskStatusRunning, TaskStatusAwaitingApproval} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			db := taskTestDB(t)
			now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
			if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := CreateTask(ctx, db, newStoreTask(100, now)); err != nil {
				t.Fatal(err)
			}
			var claimed Task
			if status != TaskStatusReady {
				var found bool
				var err error
				claimed, found, err = ClaimNextTask(ctx, db, now, time.Minute)
				if err != nil || !found {
					t.Fatalf("claim found=%v err=%v", found, err)
				}
			}
			if status == TaskStatusAwaitingApproval {
				token := "0123456789abcdef0123456789abcdef"
				saved, err := SaveTaskApproval(ctx, db, claimed, token, 700, now.Add(5*time.Minute), `{"request":{}}`, now)
				if err != nil || !saved {
					t.Fatalf("SaveTaskApproval saved=%v err=%v", saved, err)
				}
			}
			before, found, err := CurrentTask(ctx, db, 100)
			if err != nil || !found || before.Status != status {
				t.Fatalf("before=%+v found=%v err=%v", before, found, err)
			}
			if status == TaskStatusRunning && (before.ClaimToken == "" || before.LeaseUntil == nil) {
				t.Fatalf("running fixture has no claim: %+v", before)
			}
			if status == TaskStatusAwaitingApproval && (before.ClaimToken == "" || before.ContinuationJSON == "" ||
				before.ApprovalToken == "" || before.ApprovalMessageID == 0 || before.ApprovalExpiresAt == nil) {
				t.Fatalf("approval fixture is incomplete: %+v", before)
			}
			if deleted, err := DeleteTask(ctx, db, 100, before.ID); err != nil || deleted {
				t.Fatalf("active delete deleted=%v err=%v", deleted, err)
			}
			after, found, err := CurrentTask(ctx, db, 100)
			if err != nil || !found || !reflect.DeepEqual(after, before) {
				t.Fatalf("after=%+v before=%+v found=%v err=%v", after, before, found, err)
			}
		})
	}
}

func TestClearTasksDeletesOnlyCurrentChat(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Now().UTC()
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	if err := EnsureChat(ctx, db, 101, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTask(ctx, db, newStoreTask(100, now)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CancelTask(ctx, db, 100, now); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTask(ctx, db, newStoreTask(100, now)); err != nil {
		t.Fatal(err)
	}
	other, err := CreateTask(ctx, db, newStoreTask(101, now))
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := ClearTasks(ctx, db, 100)
	if err != nil || deleted != 2 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	current, found, err := CurrentTask(ctx, db, 100)
	if err != nil || found {
		t.Fatalf("current chat tasks remain: %+v found=%v err=%v", current, found, err)
	}
	if _, found, err := CurrentTask(ctx, db, 101); err != nil || !found {
		t.Fatalf("other chat task %d was deleted: found=%v err=%v", other.ID, found, err)
	}
}

func TestTaskRestartDoesNotDuplicateCompletedStepOrAnswer(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTask(ctx, db, newStoreTask(100, now)); err != nil {
		t.Fatal(err)
	}
	claimed, _, _ := ClaimNextTask(ctx, db, now, time.Minute)
	if ok, err := CompleteTaskStep(ctx, db, claimed, "persisted result", now); err != nil || !ok {
		t.Fatalf("CompleteTaskStep ok=%v err=%v", ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	finalClaim, found, err := ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found || finalClaim.NextStep != 1 || strings.Join(taskResultContents(t, finalClaim), "|") != "persisted result" {
		t.Fatalf("recovered final=%+v found=%v err=%v", finalClaim, found, err)
	}
	if ok, err := CompleteTask(ctx, db, finalClaim, Message{ChatID: 100, Role: RoleAssistant, Content: "once"}, now); err != nil || !ok {
		t.Fatalf("CompleteTask ok=%v err=%v", ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := ClaimNextTask(ctx, db, now.Add(2*time.Minute), time.Minute); err != nil || found {
		t.Fatalf("restart duplicated completed task found=%v err=%v", found, err)
	}
	rows, err := HistoryOfChat(ctx, db, 100, 10)
	if err != nil || len(rows) != 1 || rows[0].Content != "once" {
		t.Fatalf("restart history=%+v err=%v", rows, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResumeTaskPreservesProgressAndClearsTransientState(t *testing.T) {
	for _, status := range []string{TaskStatusFailed, TaskStatusCancelled} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			db := taskTestDB(t)
			now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
			started := now.Add(-2 * time.Hour)
			if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
				t.Fatal(err)
			}
			created, err := CreateTask(ctx, db, Task{ChatID: 100, UserID: 11, Goal: "preserve this goal", Model: "preserved-model",
				PlanJSON: `{"steps":[{"description":"First"},{"description":"Second"}]}`, DueAt: started, ExpiresAt: started.Add(time.Hour), CreatedAt: started})
			if err != nil {
				t.Fatal(err)
			}
			firstClaim, found, err := ClaimNextTask(ctx, db, started, time.Minute)
			if err != nil || !found {
				t.Fatalf("first claim found=%v err=%v", found, err)
			}
			if ok, err := CompleteTaskStep(ctx, db, firstClaim, "first result", started); err != nil || !ok {
				t.Fatalf("first step ok=%v err=%v", ok, err)
			}
			interrupted, found, err := ClaimNextTask(ctx, db, started, time.Minute)
			if err != nil || !found || interrupted.NextStep != 1 {
				t.Fatalf("interrupted claim=%+v found=%v err=%v", interrupted, found, err)
			}
			oldToken := "0123456789abcdef0123456789abcdef"
			if status == TaskStatusFailed {
				if saved, err := SaveTaskApproval(ctx, db, interrupted, oldToken, 700, started.Add(5*time.Minute), `{"request":{}}`, started); err != nil || !saved {
					t.Fatalf("SaveTaskApproval saved=%v err=%v", saved, err)
				}
				if ok, err := FailTask(ctx, db, interrupted, "interrupted", started.Add(time.Minute)); err != nil || !ok {
					t.Fatalf("FailTask ok=%v err=%v", ok, err)
				}
			} else if _, ok, err := CancelTask(ctx, db, 100, started.Add(time.Minute)); err != nil || !ok {
				t.Fatalf("CancelTask ok=%v err=%v", ok, err)
			}
			staleTime := formatReminderTime(started.Add(time.Minute))
			if _, err := db.ExecContext(ctx, `UPDATE tasks SET attempts = 7, due_at = ?, claim_token = 'stale-claim', lease_until = ?,
				continuation_json = '{"request":{}}', approval_token = ?, approval_message_id = 700,
				approval_expires_at = ?, error = 'stale error' WHERE id = ?`, staleTime, staleTime, oldToken, staleTime, created.ID); err != nil {
				t.Fatal(err)
			}
			resumed, ok, err := ResumeTask(ctx, db, 100, created.ID, now, time.Hour)
			if err != nil || !ok {
				t.Fatalf("ResumeTask ok=%v err=%v", ok, err)
			}
			if resumed.ID != created.ID || resumed.ChatID != created.ChatID || resumed.UserID != created.UserID ||
				resumed.Goal != created.Goal || resumed.Model != created.Model || resumed.PlanJSON != created.PlanJSON ||
				strings.Join(taskResultContents(t, resumed), "|") != "first result" || resumed.NextStep != 1 || resumed.Status != TaskStatusReady || resumed.Attempts != 0 {
				t.Fatalf("resumed task did not preserve progress: %+v", resumed)
			}
			if !resumed.DueAt.Equal(now) || !resumed.ExpiresAt.Equal(now.Add(time.Hour)) || !resumed.UpdatedAt.Equal(now) || !resumed.CreatedAt.Equal(created.CreatedAt) {
				t.Fatalf("resumed timestamps=%+v", resumed)
			}
			if resumed.ClaimToken != "" || resumed.LeaseUntil != nil || resumed.ContinuationJSON != "" || resumed.ApprovalToken != "" ||
				resumed.ApprovalMessageID != 0 || resumed.ApprovalExpiresAt != nil || resumed.Error != "" || resumed.FinishedAt != nil {
				t.Fatalf("resumed transient state was not cleared: %+v", resumed)
			}
			if _, ok, err := ConsumeTaskApproval(ctx, db, created.ID, 100, 11, 700, oldToken, now, time.Minute); err != nil || ok {
				t.Fatalf("stale approval consumed ok=%v err=%v", ok, err)
			}
			if ok, err := CompleteTaskStep(ctx, db, interrupted, "must not duplicate", now); err != nil || ok {
				t.Fatalf("stale claim completed a step ok=%v err=%v", ok, err)
			}
			secondClaim, found, err := ClaimNextTask(ctx, db, now, time.Minute)
			if err != nil || !found || secondClaim.NextStep != 1 || strings.Join(taskResultContents(t, secondClaim), "|") != "first result" || secondClaim.Attempts != 1 {
				t.Fatalf("second claim=%+v found=%v err=%v", secondClaim, found, err)
			}
			if ok, err := CompleteTaskStep(ctx, db, secondClaim, "second result", now); err != nil || !ok {
				t.Fatalf("second step ok=%v err=%v", ok, err)
			}
			finalClaim, found, err := ClaimNextTask(ctx, db, now, time.Minute)
			if err != nil || !found {
				t.Fatalf("final claim found=%v err=%v", found, err)
			}
			if ok, err := CompleteTask(ctx, db, finalClaim, Message{ChatID: 100, Role: RoleAssistant, Content: "final answer"}, now); err != nil || !ok {
				t.Fatalf("CompleteTask ok=%v err=%v", ok, err)
			}
			current, found, err := CurrentTask(ctx, db, 100)
			if err != nil || !found || strings.Join(taskResultContents(t, current), "|") != "first result|second result" || current.NextStep != 2 {
				t.Fatalf("completed task=%+v found=%v err=%v", current, found, err)
			}
			rows, err := HistoryOfChat(ctx, db, 100, 10)
			if err != nil || len(rows) != 1 || rows[0].Content != "final answer" {
				t.Fatalf("history=%+v err=%v", rows, err)
			}
			if _, ok, err := ResumeTask(ctx, db, 100, created.ID, now, time.Hour); err != nil || ok {
				t.Fatalf("completed task resumed ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestResumeTaskRejectsWrongChatNonLatestAndActiveTasks(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, chatID := range []int64{100, 101} {
		if err := EnsureChat(ctx, db, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
	}
	older, err := CreateTask(ctx, db, newStoreTask(100, now))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := CancelTask(ctx, db, 100, now); err != nil || !ok {
		t.Fatalf("cancel older ok=%v err=%v", ok, err)
	}
	latest, err := CreateTask(ctx, db, newStoreTask(100, now))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := CancelTask(ctx, db, 100, now); err != nil || !ok {
		t.Fatalf("cancel latest ok=%v err=%v", ok, err)
	}
	if _, ok, err := ResumeTask(ctx, db, 100, older.ID, now, time.Hour); err != nil || ok {
		t.Fatalf("non-latest task resumed ok=%v err=%v", ok, err)
	}
	if _, ok, err := ResumeTask(ctx, db, 101, latest.ID, now, time.Hour); err != nil || ok {
		t.Fatalf("wrong chat resumed task ok=%v err=%v", ok, err)
	}
	if _, ok, err := ResumeTask(ctx, db, 100, latest.ID, now, time.Hour); err != nil || !ok {
		t.Fatalf("latest task did not resume ok=%v err=%v", ok, err)
	}
	if _, ok, err := ResumeTask(ctx, db, 100, latest.ID, now, time.Hour); err != nil || ok {
		t.Fatalf("ready task resumed ok=%v err=%v", ok, err)
	}
	claimed, found, err := ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if _, ok, err := ResumeTask(ctx, db, 100, latest.ID, now, time.Hour); err != nil || ok {
		t.Fatalf("running task resumed ok=%v err=%v", ok, err)
	}
	token := "fedcba9876543210fedcba9876543210"
	if saved, err := SaveTaskApproval(ctx, db, claimed, token, 700, now.Add(time.Minute), `{"request":{}}`, now); err != nil || !saved {
		t.Fatalf("SaveTaskApproval saved=%v err=%v", saved, err)
	}
	if _, ok, err := ResumeTask(ctx, db, 100, latest.ID, now, time.Hour); err != nil || ok {
		t.Fatalf("awaiting task resumed ok=%v err=%v", ok, err)
	}
}

func TestTaskStatusMessageMigrationFromVersion20(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 20)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	created, err := CreateTask(ctx, db, newStoreTask(100, now))
	if err != nil {
		t.Fatalf("create a task at schema 20: %v", err)
	}
	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("upgrade schema 20 -> 21: %v", err)
	}
	if version, err := SchemaVersion(ctx, db); err != nil || version != len(MigrationSQL) {
		t.Fatalf("SchemaVersion=%d err=%v, want %d", version, err, len(MigrationSQL))
	}
	old, found, err := CurrentTask(ctx, db, 100)
	if err != nil || !found || old.ID != created.ID {
		t.Fatalf("the pre-021 task was lost: found=%v old=%+v err=%v", found, old, err)
	}
	if old.StatusMessageID != 0 {
		t.Fatalf("a pre-021 task invented a status message id %d", old.StatusMessageID)
	}
	saved, err := SetTaskStatusMessage(ctx, db, old.ID, old.ChatID, 4242, now)
	if err != nil || !saved {
		t.Fatalf("SetTaskStatusMessage saved=%v err=%v", saved, err)
	}
	updated, found, err := CurrentTask(ctx, db, 100)
	if err != nil || !found || updated.StatusMessageID != 4242 {
		t.Fatalf("status message id was not read back: task=%+v err=%v", updated, err)
	}
}

func TestSetTaskStatusMessageValidatesAndPersists(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	created, err := CreateTask(ctx, db, newStoreTask(100, now))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		name      string
		taskID    int64
		chatID    int64
		messageID int64
	}{
		{name: "task", taskID: 0, chatID: 100, messageID: 1},
		{name: "chat", taskID: created.ID, chatID: 0, messageID: 1},
		{name: "message", taskID: created.ID, chatID: 100, messageID: 0},
	} {
		if saved, err := SetTaskStatusMessage(ctx, db, bad.taskID, bad.chatID, bad.messageID, now); err == nil || saved {
			t.Errorf("%s: SetTaskStatusMessage saved=%v err=%v, want an invalid-argument error", bad.name, saved, err)
		}
	}
	if saved, err := SetTaskStatusMessage(ctx, db, created.ID, 999, 77, now); err != nil || saved {
		t.Errorf("wrong chat: saved=%v err=%v, want no row updated", saved, err)
	}
	if saved, err := SetTaskStatusMessage(ctx, db, created.ID, 100, 77, now); err != nil || !saved {
		t.Fatalf("SetTaskStatusMessage saved=%v err=%v", saved, err)
	}
	current, found, err := CurrentTask(ctx, db, 100)
	if err != nil || !found || current.StatusMessageID != 77 {
		t.Fatalf("stored status message id=%d found=%v err=%v", current.StatusMessageID, found, err)
	}
}

func TestTaskPlanningLifecycleStoresPlanOnlyWhenReady(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	created, err := CreatePlanningTask(ctx, db, Task{ChatID: 100, UserID: 11, Goal: "plan this", Model: "m",
		DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != TaskStatusPlanning || created.PlanJSON != "" || created.PlanningAttempts != 1 || !ActiveTaskStatus(created.Status) {
		t.Fatalf("created planning task=%+v", created)
	}
	if _, err := CreatePlanningTask(ctx, db, Task{ChatID: 100, UserID: 11, Goal: "second", Model: "m",
		DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now}); !errors.Is(err, ErrTaskActive) {
		t.Fatalf("second planning row err=%v, want ErrTaskActive", err)
	}
	if _, found, err := ClaimNextTask(ctx, db, now, time.Minute); err != nil || found {
		t.Fatalf("planning row was claimed: found=%v err=%v", found, err)
	}
	stored, found, err := CurrentTask(ctx, db, 100)
	if err != nil || !found || stored.Status != TaskStatusPlanning || stored.PlanJSON != "" {
		t.Fatalf("stored planning task=%+v found=%v err=%v", stored, found, err)
	}
	saved, err := SaveTaskPlan(ctx, db, created.ID, testTaskPlan, now.Add(time.Second))
	if err != nil || !saved {
		t.Fatalf("SaveTaskPlan saved=%v err=%v", saved, err)
	}
	ready, found, err := CurrentTask(ctx, db, 100)
	if err != nil || !found || ready.Status != TaskStatusReady || ready.PlanJSON != testTaskPlan {
		t.Fatalf("ready task=%+v found=%v err=%v", ready, found, err)
	}
	if saved, err := SaveTaskPlan(ctx, db, created.ID, testTaskPlan, now.Add(2*time.Second)); err != nil || saved {
		t.Fatalf("second SaveTaskPlan saved=%v err=%v", saved, err)
	}
	claimed, found, err := ClaimNextTask(ctx, db, now.Add(2*time.Second), time.Minute)
	if err != nil || !found || claimed.ID != created.ID {
		t.Fatalf("claim after plan=%+v found=%v err=%v", claimed, found, err)
	}
}

func TestTaskPlanningCancelAndFailureTransitions(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	cancelledRow, err := CreatePlanningTask(ctx, db, Task{ChatID: 100, UserID: 11, Goal: "cancel planning", Model: "m",
		DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, ok, err := CancelTask(ctx, db, 100, now)
	if err != nil || !ok || cancelled.ID != cancelledRow.ID || cancelled.Status != TaskStatusCancelled {
		t.Fatalf("cancel planning task=%+v ok=%v err=%v", cancelled, ok, err)
	}
	if saved, err := SaveTaskPlan(ctx, db, cancelledRow.ID, testTaskPlan, now); err != nil || saved {
		t.Fatalf("cancelled planning row accepted a plan: saved=%v err=%v", saved, err)
	}
	if _, ok, err := ResumeTask(ctx, db, 100, cancelledRow.ID, now, time.Hour); err != nil || ok {
		t.Fatalf("planless cancelled task resumed: ok=%v err=%v", ok, err)
	}

	failedRow, err := CreatePlanningTask(ctx, db, Task{ChatID: 100, UserID: 11, Goal: "fail planning", Model: "m",
		DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if failed, err := FailTaskPlanning(ctx, db, failedRow.ID, "не удалось составить план", now); err != nil || !failed {
		t.Fatalf("FailTaskPlanning changed=%v err=%v", failed, err)
	}
	replanned, changed, err := ReplanTask(ctx, db, 100, failedRow.ID, now.Add(time.Second), time.Hour)
	if err != nil || !changed || replanned.ID != failedRow.ID || replanned.Status != TaskStatusPlanning ||
		replanned.PlanJSON != "" || replanned.PlanningAttempts != 2 || replanned.FinishedAt != nil || replanned.Error != "" {
		t.Fatalf("replanned task=%+v changed=%v err=%v", replanned, changed, err)
	}
	if saved, err := SaveTaskPlan(ctx, db, replanned.ID, testTaskPlan, now.Add(2*time.Second)); err != nil || !saved {
		t.Fatalf("SaveTaskPlan after replan saved=%v err=%v", saved, err)
	}
}

func TestTaskReplanRejectsAStoredPlanAndResumeRejectsAPlanlessRow(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	created, err := CreateTask(ctx, db, newStoreTask(100, now))
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if changed, err := FailTask(ctx, db, claimed, "execution failed", now); err != nil || !changed {
		t.Fatalf("FailTask changed=%v err=%v", changed, err)
	}
	if _, changed, err := ReplanTask(ctx, db, 100, created.ID, now, time.Hour); err != nil || changed {
		t.Fatalf("replan of a task with a plan changed=%v err=%v", changed, err)
	}
	if _, ok, err := ResumeTask(ctx, db, 100, created.ID, now, time.Hour); err != nil || !ok {
		t.Fatalf("a failed execution task with a plan must stay resumable: ok=%v err=%v", ok, err)
	}
}

func TestTaskStalePlanningScanAndReset(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	created, err := CreatePlanningTask(ctx, db, Task{ChatID: 100, UserID: 11, Goal: "stale", Model: "m",
		DueAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := ListStalePlanningTasks(ctx, db, now.Add(-2*time.Minute), 10)
	if err != nil || len(stale) != 1 || stale[0].ID != created.ID {
		t.Fatalf("stale tasks=%+v err=%v", stale, err)
	}
	if reset, err := ResetTaskPlanning(ctx, db, created.ID, now, time.Hour); err != nil || !reset {
		t.Fatalf("ResetTaskPlanning changed=%v err=%v", reset, err)
	}
	reset, found, err := CurrentTask(ctx, db, 100)
	if err != nil || !found || reset.PlanningAttempts != 2 || reset.Status != TaskStatusPlanning || reset.Error != "" {
		t.Fatalf("reset planning task=%+v found=%v err=%v", reset, found, err)
	}
	if after, err := ListStalePlanningTasks(ctx, db, now.Add(-2*time.Minute), 10); err != nil || len(after) != 0 {
		t.Fatalf("reset row is still stale: %+v err=%v", after, err)
	}
}

func TestMigration025PreservesTasksAndAcceptsPlanning(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 24)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks
		(chat_id, user_id, goal, model, plan_json, results_json, next_step, status, attempts, due_at,
		 expires_at, error, created_at, updated_at, finished_at, status_message_id)
		VALUES (100, 11, 'old goal', 'old-model', ?, '[]', 0, 'ready', 0, ?, ?, 'old error', ?, ?, NULL, 777)`,
		testTaskPlan, formatReminderTime(now), formatReminderTime(now.Add(time.Hour)),
		formatReminderTime(now), formatReminderTime(now)); err != nil {
		t.Fatal(err)
	}
	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("upgrade to 25: %v", err)
	}
	var goal, status, plan string
	var messageID int64
	var planningAttempts int
	if err := db.QueryRowContext(ctx, `SELECT goal, status, plan_json, status_message_id, planning_attempts FROM tasks WHERE chat_id = 100`).
		Scan(&goal, &status, &plan, &messageID, &planningAttempts); err != nil {
		t.Fatalf("read migrated task: %v", err)
	}
	if goal != "old goal" || status != TaskStatusReady || plan != testTaskPlan || messageID != 777 || planningAttempts != 0 {
		t.Fatalf("migrated row goal=%q status=%q plan=%q message=%d planning=%d", goal, status, plan, messageID, planningAttempts)
	}
	if err := EnsureChat(ctx, db, 101, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks
		(chat_id, user_id, goal, model, plan_json, results_json, next_step, status, attempts, planning_attempts,
		 due_at, expires_at, created_at, updated_at)
		VALUES (101, 11, 'planning goal', 'm', '', '[]', 0, 'planning', 0, 1, ?, ?, ?, ?)`,
		formatReminderTime(now), formatReminderTime(now.Add(time.Hour)), formatReminderTime(now), formatReminderTime(now)); err != nil {
		t.Fatalf("the migrated schema rejected a planning row: %v", err)
	}
}
