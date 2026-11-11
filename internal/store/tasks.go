package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// TaskStatusPlanning is the state of a task whose row exists but whose plan is still being produced
	// by the background planner. The scheduler never claims it; only SaveTaskPlan moves it to ready.
	TaskStatusPlanning         = "planning"
	TaskStatusReady            = "ready"
	TaskStatusRunning          = "running"
	TaskStatusAwaitingApproval = "awaiting_approval"
	TaskStatusCompleted        = "completed"
	TaskStatusCancelled        = "cancelled"
	TaskStatusFailed           = "failed"
	TaskMaxSteps               = 3
	TaskMaxGoalRunes           = 2000
	TaskMaxStepResultRunes     = 12000
)

var (
	ErrTaskActive      = errors.New("an active task already exists for this chat")
	ErrTaskInvalid     = errors.New("task state is invalid")
	ErrTaskGoal        = errors.New("task goal is invalid")
	ErrTaskStepResult  = errors.New("task step result is invalid")
	ErrTaskStateChange = errors.New("task state transition did not match the claimed row")
)

// EventTaskStepRetry — one audit row per retried step. The detail names the step number and the bounded
// failure class only; the goal, step text and model answer are never written here.
const EventTaskStepRetry = "task_step_retry"

type Task struct {
	ID          int64
	ChatID      int64
	UserID      int64
	Goal        string
	Model       string
	PlanJSON    string
	ResultsJSON string
	NextStep    int
	Status      string
	Attempts    int
	// PlanningAttempts counts how many times the background planner was started for this row. Crash
	// recovery increments it; the counter caps the retries instead of re-enqueueing a broken row forever.
	PlanningAttempts int
	// PlanningOwner is the opaque id of the instance that currently holds the planning lease, and
	// PlanningLeaseUntil is the moment that claim expires. Both are empty/nil when the row is not being
	// planned: ANY instance (or this process after a crash) may claim it then. The owner never names a user
	// or a chat, so it is safe to store and to log beside a task id.
	PlanningOwner      string
	PlanningLeaseUntil *time.Time
	DueAt              time.Time
	ClaimToken         string
	LeaseUntil         *time.Time
	ContinuationJSON   string
	ApprovalToken      string
	ApprovalMessageID  int64
	ApprovalExpiresAt  *time.Time
	// StatusMessageID is the single Telegram message a live task rewrites on step transitions. Zero means
	// "no status message yet" (a row written by an older release, or a task created from the Mini App).
	StatusMessageID int64
	ExpiresAt       time.Time
	Error           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      *time.Time
}

// ActiveTaskStatus reports whether the chat's single active task slot is occupied. A planning row is
// active: its plan is being produced and the chat must not start a second task beside it.
func ActiveTaskStatus(status string) bool {
	return status == TaskStatusPlanning || status == TaskStatusReady || status == TaskStatusRunning ||
		status == TaskStatusAwaitingApproval
}

// TaskStepResult is one stored step outcome: the model answer plus the usage of the call that produced it.
// It lives inside the existing results_json column as a JSON object. Rows written by an older release store a
// bare string per step; ParseTaskResults still accepts those and reports an empty usage for them, so no
// migration is needed and a resumed old task keeps working.
type TaskStepResult struct {
	Content string `json:"content"`
	// TokensIn/TokensOut are nil when the provider did not report usage. A nil token count is shown as 0 and
	// costs nothing, exactly like the NULL columns of messages.
	TokensIn   *int64 `json:"tokens_in,omitempty"`
	TokensOut  *int64 `json:"tokens_out,omitempty"`
	Model      string `json:"model,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// validateTaskStepContent is the one rule both the store and its callers rely on: a step result is non-empty,
// has no NUL and fits the column contract.
func validateTaskStepContent(content string) error {
	content = strings.TrimSpace(content)
	if content == "" || strings.ContainsRune(content, '\x00') || len([]rune(content)) > TaskMaxStepResultRunes {
		return ErrTaskStepResult
	}
	return nil
}

// ParseTaskResults decodes results_json. An element may be the legacy bare string or the current object; a
// mixed array (an old step resumed by this release) is valid. Content of every element is validated so a
// corrupted row fails with ErrTaskStepResult instead of reaching the model.
func ParseTaskResults(raw string) ([]TaskStepResult, error) {
	var elements []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &elements); err != nil {
		return nil, fmt.Errorf("%w: stored results are not a JSON array", ErrTaskStepResult)
	}
	results := make([]TaskStepResult, 0, len(elements))
	for index, element := range elements {
		var legacy string
		if err := json.Unmarshal(element, &legacy); err == nil {
			results = append(results, TaskStepResult{Content: legacy})
			continue
		}
		var result TaskStepResult
		decoder := json.NewDecoder(bytes.NewReader(element))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&result); err != nil || !decoderHasEnd(decoder) {
			return nil, fmt.Errorf("%w: stored result %d is not a string or a step result object", ErrTaskStepResult, index+1)
		}
		results = append(results, result)
	}
	for index := range results {
		result := &results[index]
		result.Content = strings.TrimSpace(result.Content)
		if err := validateTaskStepContent(result.Content); err != nil {
			return nil, fmt.Errorf("%w: stored result %d has an invalid content", ErrTaskStepResult, index+1)
		}
		if (result.TokensIn != nil && *result.TokensIn < 0) || (result.TokensOut != nil && *result.TokensOut < 0) ||
			result.DurationMs < 0 {
			return nil, fmt.Errorf("%w: stored result %d has a negative usage counter", ErrTaskStepResult, index+1)
		}
	}
	return results, nil
}

// decoderHasEnd reports whether the decoder consumed exactly one JSON value: a trailing second value would
// otherwise be silently ignored by Decode.
func decoderHasEnd(decoder *json.Decoder) bool {
	var extra json.RawMessage
	return decoder.Decode(&extra) != nil
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
		WHERE NOT EXISTS (SELECT 1 FROM tasks WHERE chat_id = ? AND status IN ('planning', 'ready', 'running', 'awaiting_approval'))`,
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

// CreatePlanningTask inserts the row BEFORE the model is called: the plan is empty, the status is
// 'planning', and planning_attempts starts at one. The caller gets the id immediately and a background
// worker later fills the plan with SaveTaskPlan. The same per-chat active guard as CreateTask applies, so
// a chat cannot create a second task while its first plan is still being generated.
func CreatePlanningTask(ctx context.Context, db *sql.DB, task Task) (Task, error) {
	task.Goal = strings.TrimSpace(task.Goal)
	task.Model = strings.TrimSpace(task.Model)
	goalRunes := []rune(task.Goal)
	if len(goalRunes) == 0 || len(goalRunes) > TaskMaxGoalRunes || strings.ContainsRune(task.Goal, '\x00') {
		return Task{}, ErrTaskGoal
	}
	if task.ChatID == 0 || task.UserID <= 0 {
		return Task{}, fmt.Errorf("%w: chat and positive user id are required", ErrTaskInvalid)
	}
	if task.Model == "" || len([]rune(task.Model)) > 200 {
		return Task{}, fmt.Errorf("%w: model is empty or too long", ErrTaskInvalid)
	}
	if task.ExpiresAt.IsZero() || task.DueAt.IsZero() {
		return Task{}, fmt.Errorf("%w: due and expiry timestamps are required", ErrTaskInvalid)
	}
	now := time.Now().UTC()
	if !task.CreatedAt.IsZero() {
		now = task.CreatedAt.UTC()
	}
	task.CreatedAt = now
	task.UpdatedAt = now
	task.Status = TaskStatusPlanning
	task.PlanJSON = ""
	task.ResultsJSON = "[]"
	task.NextStep = 0
	task.PlanningAttempts = 1
	result, err := db.ExecContext(ctx, `INSERT INTO tasks
		(chat_id, user_id, goal, model, plan_json, results_json, next_step, status, attempts, planning_attempts,
		 due_at, expires_at, created_at, updated_at)
		SELECT ?, ?, ?, ?, '', '[]', 0, 'planning', 0, 1, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM tasks WHERE chat_id = ? AND status IN ('planning', 'ready', 'running', 'awaiting_approval'))`,
		task.ChatID, task.UserID, task.Goal, task.Model,
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
	planning_attempts, due_at, COALESCE(claim_token, ''), COALESCE(lease_until, ''), COALESCE(continuation_json, ''),
	COALESCE(approval_token, ''), COALESCE(approval_message_id, 0), COALESCE(approval_expires_at, ''),
	expires_at, COALESCE(error, ''), created_at, updated_at, COALESCE(finished_at, ''),
	COALESCE(status_message_id, 0), COALESCE(planning_owner, ''), COALESCE(planning_lease_until, '')`

type taskScanner interface {
	Scan(dest ...any) error
}

func scanTask(scanner taskScanner) (Task, error) {
	var task Task
	var dueAt, leaseUntil, approvalExpiresAt, expiresAt, createdAt, updatedAt, finishedAt, planningLeaseUntil string
	if err := scanner.Scan(&task.ID, &task.ChatID, &task.UserID, &task.Goal, &task.Model, &task.PlanJSON, &task.ResultsJSON,
		&task.NextStep, &task.Status, &task.Attempts, &task.PlanningAttempts, &dueAt, &task.ClaimToken, &leaseUntil, &task.ContinuationJSON,
		&task.ApprovalToken, &task.ApprovalMessageID, &approvalExpiresAt, &expiresAt, &task.Error, &createdAt, &updatedAt, &finishedAt,
		&task.StatusMessageID, &task.PlanningOwner, &planningLeaseUntil); err != nil {
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
	for value, target := range map[string]**time.Time{leaseUntil: &task.LeaseUntil, approvalExpiresAt: &task.ApprovalExpiresAt, finishedAt: &task.FinishedAt, planningLeaseUntil: &task.PlanningLeaseUntil} {
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

// SaveTaskPlan stores the plan produced by the background planner and moves the task to the state the
// scheduler already claims ('ready'). It only matches a row still in 'planning': a cancel, a delete, or a
// crash-recovery reset that won the race makes the update a no-op instead of resurrecting the task. The
// planning lease is cleared here as well: a process that dies between this write and its own release must not
// leave a claim on a row that is already ready.
func SaveTaskPlan(ctx context.Context, db *sql.DB, taskID int64, planJSON string, now time.Time) (bool, error) {
	if taskID <= 0 || planJSON == "" || len(planJSON) > 8192 || !json.Valid([]byte(planJSON)) {
		return false, fmt.Errorf("%w: a stored plan needs a task and valid plan JSON", ErrTaskInvalid)
	}
	result, err := db.ExecContext(ctx, `UPDATE tasks
		SET plan_json = ?, status = 'ready', due_at = ?, error = NULL, updated_at = ?,
			planning_owner = NULL, planning_lease_until = NULL
		WHERE id = ? AND status = 'planning'`,
		planJSON, formatReminderTime(now), formatReminderTime(now), taskID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// FailTaskPlanning is the terminal transition of a failed background plan. It is deliberately separate
// from FailTask: a planning row has no claim token, and its error is already localized by the caller.
func FailTaskPlanning(ctx context.Context, db *sql.DB, taskID int64, reason string, now time.Time) (bool, error) {
	if taskID <= 0 {
		return false, fmt.Errorf("%w: planning failure needs a task", ErrTaskInvalid)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "task planning failed"
	}
	if runes := []rune(reason); len(runes) > 500 {
		reason = string(runes[:500])
	}
	result, err := db.ExecContext(ctx, `UPDATE tasks
		SET status = 'failed', error = ?, claim_token = NULL, lease_until = NULL, continuation_json = NULL,
			approval_token = NULL, approval_message_id = NULL, approval_expires_at = NULL,
			planning_owner = NULL, planning_lease_until = NULL,
			updated_at = ?, finished_at = ?
		WHERE id = ? AND status = 'planning'`,
		reason, formatReminderTime(now), formatReminderTime(now), taskID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// planningLeaseFree is the condition of a planning row that any instance may claim: no owner, no deadline, or
// a deadline that has already passed. The claim, the renewal-free stale scan and the crash recovery all use
// this one condition, so recovery can never see a row the claim would refuse (or the reverse). The single ?
// is the current time in the fixed reminder layout, so the TEXT comparison is chronological.
const planningLeaseFree = "(planning_owner IS NULL OR planning_lease_until IS NULL OR planning_lease_until <= ?)"

// The row predicates of the three atomic planning claims. Each one contains its own ? for the free-lease
// check, so the caller passes the current time once per occurrence. The claim helper below always appends
// those arguments after the three SET arguments, in the same order as the placeholders appear here.
const (
	planningClaimOldest = `id = (
			SELECT id FROM tasks WHERE status = 'planning' AND ` + planningLeaseFree + `
			ORDER BY due_at, id LIMIT 1
		) AND status = 'planning' AND ` + planningLeaseFree
	planningClaimFair = `id = (
			SELECT id FROM tasks WHERE status = 'planning' AND ` + planningLeaseFree + `
			ORDER BY updated_at, chat_id, id LIMIT 1
		) AND status = 'planning' AND ` + planningLeaseFree
	planningClaimByID = `id = ? AND status = 'planning' AND ` + planningLeaseFree
)

// claimPlanningTask is the one atomic claim statement: UPDATE ... RETURNING serializes the writers, and the
// loser re-evaluates the predicate against the committed row, so two instances — or two stores of one process
// — never both win the same row. predicate is one of the planningClaim* constants; predicateArgs are its
// placeholders (the task id, and the current time for every planningLeaseFree occurrence).
//
// A claim does not touch planning_attempts: the counter is the crash-recovery counter of ResetTaskPlanning
// (creation already counts the first attempt), so a normal plan is never charged twice.
func claimPlanningTask(ctx context.Context, db *sql.DB, now time.Time, owner string, lease time.Duration, predicate string, predicateArgs []any) (Task, bool, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" || len([]rune(owner)) > 200 || lease <= 0 {
		return Task{}, false, fmt.Errorf("%w: a planning claim needs an owner and a positive lease", ErrTaskInvalid)
	}
	nowText := formatReminderTime(now)
	args := make([]any, 0, 3+len(predicateArgs))
	args = append(args, owner, formatReminderTime(now.Add(lease)), nowText)
	args = append(args, predicateArgs...)
	row := db.QueryRowContext(ctx, `UPDATE tasks
		SET planning_owner = ?, planning_lease_until = ?, updated_at = ?
		WHERE `+predicate+`
		RETURNING `+taskColumns, args...)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	return task, true, nil
}

// ClaimPlanningTask atomically takes the lease of the oldest-due planning row that any instance may claim.
// It is the strict-FIFO claim kept for callers and tests that want exactly that order; the planner itself
// uses ClaimPlanningTaskByID first and ClaimPlanningTaskFair as its fallback, so that a worker serves the
// chat that woke it and the shared queue rotates between chats.
func ClaimPlanningTask(ctx context.Context, db *sql.DB, now time.Time, owner string, lease time.Duration) (Task, bool, error) {
	nowText := formatReminderTime(now)
	return claimPlanningTask(ctx, db, now, owner, lease, planningClaimOldest, []any{nowText, nowText})
}

// ClaimPlanningTaskByID atomically takes the lease of one named planning row when it is still in the
// 'planning' state and its lease is free (or expired). A row that is already leased by another instance, or
// that has reached a terminal state, is not claimable and the result is found=false — exactly the same lease
// rules as ClaimPlanningTask, just scoped to the row that woke this worker.
func ClaimPlanningTaskByID(ctx context.Context, db *sql.DB, taskID int64, now time.Time, owner string, lease time.Duration) (Task, bool, error) {
	if taskID <= 0 {
		return Task{}, false, fmt.Errorf("%w: a planning claim by id needs a task", ErrTaskInvalid)
	}
	nowText := formatReminderTime(now)
	return claimPlanningTask(ctx, db, now, owner, lease, planningClaimByID, []any{taskID, nowText})
}

// ClaimPlanningTaskFair takes the free planning row with the oldest updated_at, ties broken by chat_id and
// then id. This is the shared-queue fallback of a worker whose own task was not claimable.
//
// The rule is per-chat round robin, not FIFO by creation: a claim writes updated_at = now, so the chat that
// was just served sorts after every chat that has waited longer. Because uq_tasks_chat_active permits at most
// one active row per chat, the free planning rows are one per waiting chat; ordering them by updated_at is
// therefore least-recently-served order over chats. An interrupted attempt keeps its claim-time updated_at
// when it is released or expires, so it also moves behind the chats that have not been tried yet. No schema
// migration is needed: the selection only sorts the free planning rows (bounded by the number of chats with
// a live task) and reuses the existing idx_tasks_planning_lease for the filter.
func ClaimPlanningTaskFair(ctx context.Context, db *sql.DB, now time.Time, owner string, lease time.Duration) (Task, bool, error) {
	nowText := formatReminderTime(now)
	return claimPlanningTask(ctx, db, now, owner, lease, planningClaimFair, []any{nowText, nowText})
}

// RenewPlanningLease extends the lease of a row this instance still owns. A false result means the claim was
// lost — the row left 'planning' (a cancel, a save, an expiry) or another instance now owns it — and the
// planner must stop instead of writing a plan over the new owner's work. updated_at is deliberately not
// touched: the lease, not the age, is what proves that a plan is alive.
func RenewPlanningLease(ctx context.Context, db *sql.DB, taskID int64, owner string, until time.Time) (bool, error) {
	owner = strings.TrimSpace(owner)
	if taskID <= 0 || owner == "" || until.IsZero() {
		return false, fmt.Errorf("%w: a lease renewal needs a task, an owner and a deadline", ErrTaskInvalid)
	}
	result, err := db.ExecContext(ctx, `UPDATE tasks SET planning_lease_until = ?
		WHERE id = ? AND status = 'planning' AND planning_owner = ?`,
		formatReminderTime(until), taskID, owner)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// ReleasePlanningLease clears a claim this instance still owns. It matches the owner only, so a late release
// can never clear the lease of the instance that has already taken the row over; the status is not part of
// the condition because a completed or failed row is released exactly the same way.
func ReleasePlanningLease(ctx context.Context, db *sql.DB, taskID int64, owner string) (bool, error) {
	owner = strings.TrimSpace(owner)
	if taskID <= 0 || owner == "" {
		return false, fmt.Errorf("%w: a lease release needs a task and an owner", ErrTaskInvalid)
	}
	result, err := db.ExecContext(ctx, `UPDATE tasks SET planning_owner = NULL, planning_lease_until = NULL
		WHERE id = ? AND planning_owner = ?`, taskID, owner)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// ListStalePlanningTasks returns planning rows whose lease is free or expired and whose last update is at or
// before olderThan. A row leased by another instance is not stale whatever its age: only the owner's release
// or the expiry of its lease can make it claimable.
func ListStalePlanningTasks(ctx context.Context, db *sql.DB, olderThan time.Time, now time.Time, limit int) ([]Task, error) {
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	rows, err := db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks
		WHERE status = 'planning' AND updated_at <= ? AND `+planningLeaseFree+`
		ORDER BY updated_at, id LIMIT ?`, formatReminderTime(olderThan), formatReminderTime(now), limit)
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

// ResetTaskPlanning re-arms a planning row found by crash recovery: the attempt counter grows, the stale
// error state and any claim leftovers are cleared, and the task lifetime is extended from now. staleBefore is
// the discovery threshold (not the claim time): the update only matches a row that has not been reset or
// re-claimed since the scan, so two instances that listed the same row cannot both bump the attempt counter.
func ResetTaskPlanning(ctx context.Context, db *sql.DB, taskID int64, staleBefore time.Time, now time.Time, lifetime time.Duration) (bool, error) {
	if taskID <= 0 || lifetime <= 0 {
		return false, fmt.Errorf("%w: planning reset needs a task and a positive lifetime", ErrTaskInvalid)
	}
	result, err := db.ExecContext(ctx, `UPDATE tasks
		SET status = 'planning', planning_attempts = planning_attempts + 1, due_at = ?, expires_at = ?,
			claim_token = NULL, lease_until = NULL, continuation_json = NULL, approval_token = NULL,
			approval_message_id = NULL, approval_expires_at = NULL, error = NULL, finished_at = NULL,
			planning_owner = NULL, planning_lease_until = NULL, updated_at = ?
		WHERE id = ? AND status = 'planning' AND updated_at <= ?`,
		formatReminderTime(now), formatReminderTime(now.Add(lifetime)), formatReminderTime(now), taskID, formatReminderTime(staleBefore))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// ReplanTask re-arms the latest failed task of a chat whose plan was NEVER produced (the failed planning
// row). A task that failed during execution has a valid plan and must use resume instead, so plan_json
// must be empty for this transition.
func ReplanTask(ctx context.Context, db *sql.DB, chatID int64, taskID int64, now time.Time, lifetime time.Duration) (Task, bool, error) {
	if chatID == 0 || taskID <= 0 || lifetime <= 0 {
		return Task{}, false, fmt.Errorf("%w: replan needs a chat, task id, and positive lifetime", ErrTaskInvalid)
	}
	nowText := formatReminderTime(now)
	row := db.QueryRowContext(ctx, `UPDATE tasks
		SET status = 'planning', plan_json = '', planning_attempts = planning_attempts + 1, attempts = 0,
			due_at = ?, expires_at = ?, claim_token = NULL, lease_until = NULL, continuation_json = NULL,
			approval_token = NULL, approval_message_id = NULL, approval_expires_at = NULL,
			error = NULL, finished_at = NULL, planning_owner = NULL, planning_lease_until = NULL, updated_at = ?
		WHERE id = (SELECT id FROM tasks WHERE chat_id = ? AND status = 'failed' ORDER BY id DESC LIMIT 1)
			AND id = ? AND chat_id = ? AND status = 'failed' AND plan_json = ''
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
	nowText := formatReminderTime(now)
	var workExists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM tasks WHERE expires_at > ? AND due_at <= ?
			AND (status = 'ready' OR (status = 'running' AND lease_until <= ?))
	)`, nowText, nowText, nowText).Scan(&workExists); err != nil {
		return Task{}, false, err
	}
	if !workExists {
		return Task{}, false, nil
	}
	token, err := reminderClaimToken()
	if err != nil {
		return Task{}, false, err
	}
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

func SetTaskStatusMessage(ctx context.Context, db *sql.DB, taskID int64, chatID int64, messageID int64, now time.Time) (bool, error) {
	if taskID <= 0 || chatID == 0 || messageID <= 0 {
		return false, fmt.Errorf("%w: a status message needs a task, a chat and a positive message id", ErrTaskInvalid)
	}
	result, err := db.ExecContext(ctx, `UPDATE tasks SET status_message_id = ?, updated_at = ?
		WHERE id = ? AND chat_id = ?`, messageID, formatReminderTime(now), taskID, chatID)
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
	return CompleteTaskStepWithUsage(ctx, db, task, TaskStepResult{Content: resultText}, now)
}

// CompleteTaskStepWithUsage stores one step result together with the usage of the model call that produced it.
// The content rules are exactly the old ones; the usage is optional, so an old caller or a provider without a
// usage report writes zero tokens.
func CompleteTaskStepWithUsage(ctx context.Context, db *sql.DB, task Task, step TaskStepResult, now time.Time) (bool, error) {
	step.Content = strings.TrimSpace(step.Content)
	if err := validateTaskStepContent(step.Content); err != nil {
		return false, err
	}
	if (step.TokensIn != nil && *step.TokensIn < 0) || (step.TokensOut != nil && *step.TokensOut < 0) || step.DurationMs < 0 {
		return false, ErrTaskStepResult
	}
	if task.NextStep < 0 || task.NextStep >= TaskMaxSteps || task.ClaimToken == "" {
		return false, fmt.Errorf("%w: step completion needs a claimed step", ErrTaskInvalid)
	}
	results, err := ParseTaskResults(task.ResultsJSON)
	if err != nil || len(results) != task.NextStep || len(results) >= TaskMaxSteps {
		return false, fmt.Errorf("%w: results do not match next step", ErrTaskInvalid)
	}
	results = append(results, step)
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
	if results, parseErr := ParseTaskResults(task.ResultsJSON); parseErr != nil || len(results) != task.NextStep {
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
		(chat_id, user_id, role, content, tokens_in, tokens_out, model, latency_ms, first_token_ms, telegram_message_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, answer.ChatID, answer.UserID, answer.Role, answer.Content, answer.TokensIn, answer.TokensOut,
		nullable(answer.Model), answer.LatencyMs, firstTokenOrNull(answer.FirstTokenMs), answer.TelegramMessageID); err != nil {
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
			planning_owner = NULL, planning_lease_until = NULL,
			updated_at = ?, finished_at = ?
		WHERE id = (SELECT id FROM tasks WHERE chat_id = ? AND status IN ('planning', 'ready', 'running', 'awaiting_approval') ORDER BY id DESC LIMIT 1)
			AND chat_id = ? AND status IN ('planning', 'ready', 'running', 'awaiting_approval')
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
			approval_expires_at = NULL, expires_at = ?, error = NULL, finished_at = NULL, updated_at = ?,
			planning_owner = NULL, planning_lease_until = NULL
		WHERE id = (SELECT id FROM tasks WHERE chat_id = ? ORDER BY id DESC LIMIT 1)
			AND id = ? AND chat_id = ? AND status IN ('failed', 'cancelled') AND plan_json <> ''
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
	var workExists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM tasks WHERE status IN ('planning', 'ready', 'running', 'awaiting_approval')
			AND (expires_at <= ? OR (status = 'awaiting_approval' AND approval_expires_at <= ?))
	)`, nowText, nowText).Scan(&workExists); err != nil {
		return nil, err
	}
	if !workExists {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `UPDATE tasks
		SET status = 'failed', error = 'task or network approval expired', claim_token = NULL,
			lease_until = NULL, continuation_json = NULL, approval_token = NULL,
			approval_message_id = NULL, approval_expires_at = NULL,
			planning_owner = NULL, planning_lease_until = NULL, updated_at = ?, finished_at = ?
		WHERE id IN (
			SELECT id FROM tasks WHERE status IN ('planning', 'ready', 'running', 'awaiting_approval')
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

func DeleteTask(ctx context.Context, db *sql.DB, chatID int64, taskID int64) (bool, error) {
	if chatID == 0 || taskID <= 0 {
		return false, nil
	}
	result, err := db.ExecContext(ctx, `DELETE FROM tasks
		WHERE chat_id = ? AND id = ? AND status IN ('completed', 'failed', 'cancelled')`, chatID, taskID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
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
