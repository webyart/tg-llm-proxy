package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
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

func TestTaskMigration9PreservesDataAndIndexes(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	migrations := MigrationSQL
	MigrationSQL = append([]string(nil), migrations[:8]...)
	defer func() { MigrationSQL = migrations }()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureChat(ctx, db, 100, 11, "kept"); err != nil {
		t.Fatal(err)
	}
	MigrationSQL = migrations
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != 9 || version != len(MigrationSQL) {
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
	ok, err := CompleteTask(ctx, db, finalClaim, Message{ChatID: 100, Role: RoleAssistant, Content: "consolidated", Model: "selected-model"}, now)
	if err != nil || !ok {
		t.Fatalf("CompleteTask ok=%v err=%v", ok, err)
	}
	if ok, err := CompleteTask(ctx, db, finalClaim, Message{ChatID: 100, Role: RoleAssistant, Content: "duplicate"}, now); err != nil || ok {
		t.Fatalf("duplicate final completion ok=%v err=%v", ok, err)
	}
	rows, err := HistoryOfChat(ctx, db, 100, 10)
	if err != nil || len(rows) != 1 || rows[0].Content != "consolidated" {
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
	if err != nil || !found || finalClaim.NextStep != 1 || finalClaim.ResultsJSON != `["persisted result"]` {
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
				resumed.ResultsJSON != `["first result"]` || resumed.NextStep != 1 || resumed.Status != TaskStatusReady || resumed.Attempts != 0 {
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
			if err != nil || !found || secondClaim.NextStep != 1 || secondClaim.ResultsJSON != `["first result"]` || secondClaim.Attempts != 1 {
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
			if err != nil || !found || current.ResultsJSON != `["first result","second result"]` || current.NextStep != 2 {
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
