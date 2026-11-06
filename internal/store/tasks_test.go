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
