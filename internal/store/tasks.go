package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	TaskStatusReady            = "ready"
	TaskStatusRunning          = "running"
	TaskStatusAwaitingApproval = "awaiting_approval"
	TaskStatusCompleted        = "completed"
	TaskStatusCancelled        = "cancelled"
	TaskStatusFailed           = "failed"
	TaskMaxSteps               = 3
	TaskMaxGoalRunes           = 2000
	TaskMaxStepResultRunes     = 4000
)

var (
	ErrTaskActive      = errors.New("an active task already exists for this chat")
	ErrTaskInvalid     = errors.New("task state is invalid")
	ErrTaskGoal        = errors.New("task goal is invalid")
	ErrTaskStepResult  = errors.New("task step result is invalid")
	ErrTaskStateChange = errors.New("task state transition did not match the claimed row")
)

type Task struct {
	ID                int64
	ChatID            int64
	UserID            int64
	Goal              string
	Model             string
	PlanJSON          string
	ResultsJSON       string
	NextStep          int
	Status            string
	Attempts          int
	DueAt             time.Time
	ClaimToken        string
	LeaseUntil        *time.Time
	ContinuationJSON  string
	ApprovalToken     string
	ApprovalMessageID int64
	ApprovalExpiresAt *time.Time
	ExpiresAt         time.Time
	Error             string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	FinishedAt        *time.Time
}

func ActiveTaskStatus(status string) bool {
	return status == TaskStatusReady || status == TaskStatusRunning || status == TaskStatusAwaitingApproval
}

func validateTaskForCreate(task Task) error {
	if task.ChatID == 0 || task.UserID <= 0 {
		return fmt.Errorf("%w: chat and positive user id are required", ErrTaskInvalid)
	}
	goalRunes := []rune(strings.TrimSpace(task.Goal))
	if len(goalRunes) == 0 || len(goalRunes) > TaskMaxGoalRunes || strings.ContainsRune(task.Goal, '\x00') {
		return ErrTaskGoal
	}
	if strings.TrimSpace(task.Model) == "" || len([]rune(task.Model)) > 200 {
		return fmt.Errorf("%w: model is empty or too long", ErrTaskInvalid)
	}
	if !json.Valid([]byte(task.PlanJSON)) || len(task.PlanJSON) == 0 || len(task.PlanJSON) > 8192 {
		return fmt.Errorf("%w: plan JSON is invalid", ErrTaskInvalid)
	}
	if task.ExpiresAt.IsZero() || task.DueAt.IsZero() {
		return fmt.Errorf("%w: due and expiry timestamps are required", ErrTaskInvalid)
	}
	return nil
}

func CreateTask(ctx context.Context, db *sql.DB, task Task) (Task, error) {
	task.Goal = strings.TrimSpace(task.Goal)
	task.Model = strings.TrimSpace(task.Model)
	if err := validateTaskForCreate(task); err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	if !task.CreatedAt.IsZero() {
		now = task.CreatedAt.UTC()
	}
	task.CreatedAt = now
	task.UpdatedAt = now
	task.Status = TaskStatusReady
	task.ResultsJSON = "[]"
	task.NextStep = 0
	result, err := db.ExecContext(ctx, `INSERT INTO tasks
		(chat_id, user_id, goal, model, plan_json, results_json, next_step, status, attempts, due_at, expires_at, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, '[]', 0, 'ready', 0, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM tasks WHERE chat_id = ? AND status IN ('ready', 'running', 'awaiting_approval'))`,
		task.ChatID, task.UserID, task.Goal, task.Model, task.PlanJSON,
		formatReminderTime(task.DueAt), formatReminderTime(task.ExpiresAt), formatReminderTime(now), formatReminderTime(now), task.ChatID)
	if err != nil {
		if strings.Contains(err.Error(), "uq_tasks_chat_active") {
			return Task{}, ErrTaskActive
		}
		return Task{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Task{}, err
	}
	if affected != 1 {
		return Task{}, ErrTaskActive
	}
	task.ID, err = result.LastInsertId()
	return task, err
}

const taskColumns = `id, chat_id, user_id, goal, model, plan_json, results_json, next_step, status, attempts,
	due_at, COALESCE(claim_token, ''), COALESCE(lease_until, ''), COALESCE(continuation_json, ''),
	COALESCE(approval_token, ''), COALESCE(approval_message_id, 0), COALESCE(approval_expires_at, ''),
	expires_at, COALESCE(error, ''), created_at, updated_at, COALESCE(finished_at, '')`

type taskScanner interface {
	Scan(dest ...any) error
}

func scanTask(scanner taskScanner) (Task, error) {
	var task Task
	var dueAt, leaseUntil, approvalExpiresAt, expiresAt, createdAt, updatedAt, finishedAt string
	if err := scanner.Scan(&task.ID, &task.ChatID, &task.UserID, &task.Goal, &task.Model, &task.PlanJSON, &task.ResultsJSON,
		&task.NextStep, &task.Status, &task.Attempts, &dueAt, &task.ClaimToken, &leaseUntil, &task.ContinuationJSON,
		&task.ApprovalToken, &task.ApprovalMessageID, &approvalExpiresAt, &expiresAt, &task.Error, &createdAt, &updatedAt, &finishedAt); err != nil {
		return Task{}, err
	}
	var err error
	if task.DueAt, err = parseReminderTime(dueAt); err != nil {
		return Task{}, err
	}
	if task.ExpiresAt, err = parseReminderTime(expiresAt); err != nil {
		return Task{}, err
	}
	if task.CreatedAt, err = parseReminderTime(createdAt); err != nil {
		return Task{}, err
	}
	if task.UpdatedAt, err = parseReminderTime(updatedAt); err != nil {
		return Task{}, err
	}
	for value, target := range map[string]**time.Time{leaseUntil: &task.LeaseUntil, approvalExpiresAt: &task.ApprovalExpiresAt, finishedAt: &task.FinishedAt} {
		if value == "" {
			continue
		}
		parsed, parseErr := parseReminderTime(value)
		if parseErr != nil {
			return Task{}, parseErr
		}
		*target = &parsed
	}
	return task, nil
}

func CurrentTask(ctx context.Context, db *sql.DB, chatID int64) (Task, bool, error) {
	row := db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE chat_id = ? ORDER BY id DESC LIMIT 1`, chatID)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	return task, true, nil
}

func ListAwaitingTasks(ctx context.Context, db *sql.DB, limit int) ([]Task, error) {
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	rows, err := db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE status = 'awaiting_approval' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		task, scanErr := scanTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func TaskByApprovalToken(ctx context.Context, db *sql.DB, token string) (Task, bool, error) {
	if len(token) != 32 {
		return Task{}, false, nil
	}
	row := db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks
		WHERE approval_token = ? AND status = 'awaiting_approval'`, token)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	return task, true, nil
}

func ClaimNextTask(ctx context.Context, db *sql.DB, now time.Time, lease time.Duration) (Task, bool, error) {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	token, err := reminderClaimToken()
	if err != nil {
		return Task{}, false, err
	}
	nowText := formatReminderTime(now)
	row := db.QueryRowContext(ctx, `UPDATE tasks
		SET status = 'running', attempts = attempts + 1, claim_token = ?, lease_until = ?, updated_at = ?
		WHERE id = (
			SELECT id FROM tasks WHERE expires_at > ? AND due_at <= ?
				AND (status = 'ready' OR (status = 'running' AND lease_until <= ?))
			ORDER BY due_at, id LIMIT 1
		) AND expires_at > ? AND due_at <= ?
			AND (status = 'ready' OR (status = 'running' AND lease_until <= ?))
		RETURNING `+taskColumns,
		token, formatReminderTime(now.Add(lease)), nowText, nowText, nowText, nowText, nowText, nowText, nowText)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	return task, true, nil
}

func SaveTaskApproval(ctx context.Context, db *sql.DB, task Task, token string, messageID int64, expiresAt time.Time, continuationJSON string, now time.Time) (bool, error) {
	if task.ID <= 0 || task.ClaimToken == "" || len(token) != 32 || messageID <= 0 || continuationJSON == "" || len(continuationJSON) > 131072 || !json.Valid([]byte(continuationJSON)) {
		return false, fmt.Errorf("%w: approval snapshot is invalid", ErrTaskInvalid)
	}
	result, err := db.ExecContext(ctx, `UPDATE tasks
		SET status = 'awaiting_approval', lease_until = NULL, continuation_json = ?, approval_token = ?,
			approval_message_id = ?, approval_expires_at = ?, due_at = ?, updated_at = ?
		WHERE id = ? AND status = 'running' AND claim_token = ? AND next_step < 3`,
		continuationJSON, token, messageID, formatReminderTime(expiresAt), formatReminderTime(expiresAt), formatReminderTime(now), task.ID, task.ClaimToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func ConsumeTaskApproval(ctx context.Context, db *sql.DB, taskID int64, chatID int64, userID int64, messageID int64, token string, now time.Time, lease time.Duration) (Task, bool, error) {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	claimToken, err := reminderClaimToken()
	if err != nil {
		return Task{}, false, err
	}
	nowText := formatReminderTime(now)
	row := db.QueryRowContext(ctx, `UPDATE tasks
		SET status = 'running', claim_token = ?, lease_until = ?, approval_token = NULL,
			approval_message_id = NULL, approval_expires_at = NULL, due_at = ?, updated_at = ?
		WHERE id = ? AND chat_id = ? AND user_id = ? AND status = 'awaiting_approval'
			AND approval_token = ? AND approval_message_id = ? AND approval_expires_at > ? AND expires_at > ?
		RETURNING `+taskColumns,
		claimToken, formatReminderTime(now.Add(lease)), nowText, nowText,
		taskID, chatID, userID, token, messageID, nowText, nowText)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	return task, true, nil
}

func CompleteTaskStep(ctx context.Context, db *sql.DB, task Task, resultText string, now time.Time) (bool, error) {
	resultText = strings.TrimSpace(resultText)
	if resultText == "" || len([]rune(resultText)) > TaskMaxStepResultRunes || strings.ContainsRune(resultText, '\x00') {
		return false, ErrTaskStepResult
	}
	if task.NextStep < 0 || task.NextStep >= TaskMaxSteps || task.ClaimToken == "" {
		return false, fmt.Errorf("%w: step completion needs a claimed step", ErrTaskInvalid)
	}
	var results []string
	if err := json.Unmarshal([]byte(task.ResultsJSON), &results); err != nil || len(results) != task.NextStep || len(results) >= TaskMaxSteps {
		return false, fmt.Errorf("%w: results do not match next step", ErrTaskInvalid)
	}
	results = append(results, resultText)
	encoded, err := json.Marshal(results)
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `UPDATE tasks
		SET status = 'ready', results_json = ?, next_step = next_step + 1, due_at = ?, claim_token = NULL,
			lease_until = NULL, continuation_json = NULL, approval_token = NULL, approval_message_id = NULL,
			approval_expires_at = NULL, updated_at = ?
		WHERE id = ? AND status = 'running' AND claim_token = ? AND next_step = ?`,
		string(encoded), formatReminderTime(now), formatReminderTime(now), task.ID, task.ClaimToken, task.NextStep)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func CompleteTask(ctx context.Context, db *sql.DB, task Task, answer Message, now time.Time) (bool, error) {
	if task.ID <= 0 || task.ClaimToken == "" || task.NextStep < 1 || task.NextStep > TaskMaxSteps {
		return false, fmt.Errorf("%w: final completion needs all planned steps", ErrTaskInvalid)
	}
	if answer.ChatID != task.ChatID || answer.Role != RoleAssistant || strings.TrimSpace(answer.Content) == "" {
		return false, fmt.Errorf("%w: final answer has the wrong chat, role, or empty content", ErrTaskInvalid)
	}
	var results []string
	if err := json.Unmarshal([]byte(task.ResultsJSON), &results); err != nil || len(results) != task.NextStep {
		return false, fmt.Errorf("%w: final completion results do not match the completed steps", ErrTaskInvalid)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE tasks
		SET status = 'completed', claim_token = NULL, lease_until = NULL, continuation_json = NULL,
			approval_token = NULL, approval_message_id = NULL, approval_expires_at = NULL,
			updated_at = ?, finished_at = ?, error = NULL
		WHERE id = ? AND status = 'running' AND claim_token = ? AND next_step BETWEEN 1 AND 3`,
		formatReminderTime(now), formatReminderTime(now), task.ID, task.ClaimToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages
		(chat_id, user_id, role, content, tokens_in, tokens_out, model, latency_ms, telegram_message_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, answer.ChatID, answer.UserID, answer.Role, answer.Content, answer.TokensIn, answer.TokensOut,
		nullable(answer.Model), answer.LatencyMs, answer.TelegramMessageID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func FailTask(ctx context.Context, db *sql.DB, task Task, reason string, now time.Time) (bool, error) {
	if task.ID <= 0 || task.ClaimToken == "" {
		return false, fmt.Errorf("%w: failure needs a claimed row", ErrTaskInvalid)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "task failed"
	}
	if runes := []rune(reason); len(runes) > 500 {
		reason = string(runes[:500])
	}
	result, err := db.ExecContext(ctx, `UPDATE tasks
		SET status = 'failed', error = ?, claim_token = NULL, lease_until = NULL, continuation_json = NULL,
			approval_token = NULL, approval_message_id = NULL, approval_expires_at = NULL,
			updated_at = ?, finished_at = ?
		WHERE id = ? AND status IN ('running', 'awaiting_approval') AND claim_token = ?`,
		reason, formatReminderTime(now), formatReminderTime(now), task.ID, task.ClaimToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func CancelTask(ctx context.Context, db *sql.DB, chatID int64, now time.Time) (Task, bool, error) {
	row := db.QueryRowContext(ctx, `UPDATE tasks
		SET status = 'cancelled', claim_token = NULL, lease_until = NULL, continuation_json = NULL,
			approval_token = NULL, approval_message_id = NULL, approval_expires_at = NULL,
			updated_at = ?, finished_at = ?
		WHERE id = (SELECT id FROM tasks WHERE chat_id = ? AND status IN ('ready', 'running', 'awaiting_approval') ORDER BY id DESC LIMIT 1)
			AND chat_id = ? AND status IN ('ready', 'running', 'awaiting_approval')
		RETURNING `+taskColumns, formatReminderTime(now), formatReminderTime(now), chatID, chatID)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	return task, true, nil
}

func ResumeTask(ctx context.Context, db *sql.DB, chatID int64, taskID int64, now time.Time, lifetime time.Duration) (Task, bool, error) {
	if chatID == 0 || taskID <= 0 || lifetime <= 0 {
		return Task{}, false, fmt.Errorf("%w: resume needs a chat, task id, and positive lifetime", ErrTaskInvalid)
	}
	nowText := formatReminderTime(now)
	row := db.QueryRowContext(ctx, `UPDATE tasks
		SET status = 'ready', attempts = 0, due_at = ?, claim_token = NULL, lease_until = NULL,
			continuation_json = NULL, approval_token = NULL, approval_message_id = NULL,
			approval_expires_at = NULL, expires_at = ?, error = NULL, finished_at = NULL, updated_at = ?
		WHERE id = (SELECT id FROM tasks WHERE chat_id = ? ORDER BY id DESC LIMIT 1)
			AND id = ? AND chat_id = ? AND status IN ('failed', 'cancelled')
		RETURNING `+taskColumns, nowText, formatReminderTime(now.Add(lifetime)), nowText, chatID, taskID, chatID)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	return task, true, nil
}

func ExpireTasks(ctx context.Context, db *sql.DB, now time.Time, limit int) ([]Task, error) {
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	nowText := formatReminderTime(now)
	rows, err := db.QueryContext(ctx, `UPDATE tasks
		SET status = 'failed', error = 'task or network approval expired', claim_token = NULL,
			lease_until = NULL, continuation_json = NULL, approval_token = NULL,
			approval_message_id = NULL, approval_expires_at = NULL, updated_at = ?, finished_at = ?
		WHERE id IN (
			SELECT id FROM tasks WHERE status IN ('ready', 'running', 'awaiting_approval')
				AND (expires_at <= ? OR (status = 'awaiting_approval' AND approval_expires_at <= ?))
			ORDER BY id LIMIT ?
		) RETURNING `+taskColumns, nowText, nowText, nowText, nowText, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var expired []Task
	for rows.Next() {
		task, scanErr := scanTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		expired = append(expired, task)
	}
	return expired, rows.Err()
}

func ClearTasks(ctx context.Context, db *sql.DB, chatID int64) (int64, error) {
	result, err := db.ExecContext(ctx, `DELETE FROM tasks WHERE chat_id = ?`, chatID)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return deleted, nil
}
