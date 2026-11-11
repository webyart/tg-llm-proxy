package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"bot/internal/i18n"
	"bot/internal/store"
)

// The planning phase is deliberately asynchronous: POST /api/tasks and Telegram /task only insert the
// row and return, while a bounded set of workers calls the STRATA planner. These constants are the whole
// contract of that phase.
const (
	// taskPlanningRecoveryAge is how long a planning row may stay untouched before crash recovery
	// re-enqueues it. An in-process planner is excluded by planningActive, so the age only matters for a
	// row left by a process that died before finishing its plan.
	taskPlanningRecoveryAge = 2 * time.Minute
	// taskPlanningAttemptsCap bounds the crash-recovery re-enqueues. The first insert is attempt one;
	// after this many starts a row that still cannot finish is failed with a clear error instead of being
	// retried forever.
	taskPlanningAttemptsCap = 3
	// taskPlanningConcurrency is the number of plans that may call the model at the same time. The row is
	// already visible, so the queue of waiting tasks is not lost when every worker is busy.
	taskPlanningConcurrency = 4
)

// startTaskPlanning launches the background planner for one row. It never blocks: the caller is the HTTP
// or Telegram creation path. The in-memory active set deduplicates a crash-recovery re-enqueue racing the
// original job.
func (d *Dispatcher) startTaskPlanning(task store.Task) {
	if d == nil || task.ID <= 0 || task.Status != store.TaskStatusPlanning {
		return
	}
	d.planningMu.Lock()
	if d.planningActive == nil {
		d.planningActive = map[int64]bool{}
	}
	if d.planningActive[task.ID] {
		d.planningMu.Unlock()
		return
	}
	d.planningActive[task.ID] = true
	d.planningMu.Unlock()
	go d.runTaskPlanning(task)
}

func (d *Dispatcher) finishTaskPlanning(taskID int64) {
	d.planningMu.Lock()
	delete(d.planningActive, taskID)
	d.planningMu.Unlock()
}

func (d *Dispatcher) taskPlanningActive(taskID int64) bool {
	d.planningMu.Lock()
	defer d.planningMu.Unlock()
	return d.planningActive[taskID]
}

// setTaskPlanningContext lets RunTaskScheduler bind the planning workers to the process lifetime. A plan
// started before the scheduler (the Mini App listener is up first) keeps the background context and is
// bounded by the task budget; a plan started after shutdown is cancelled before its model call.
func (d *Dispatcher) setTaskPlanningContext(ctx context.Context) {
	d.planningCtxMu.Lock()
	d.planningCtx = ctx
	d.planningCtxMu.Unlock()
}

func (d *Dispatcher) taskPlanningContext() context.Context {
	d.planningCtxMu.RLock()
	ctx := d.planningCtx
	d.planningCtxMu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (d *Dispatcher) runTaskPlanning(task store.Task) {
	defer d.finishTaskPlanning(task.ID)
	ctx := d.taskPlanningContext()
	if d.planningSlots != nil {
		select {
		case d.planningSlots <- struct{}{}:
			defer func() { <-d.planningSlots }()
		case <-ctx.Done():
			// Shutdown before the plan started: leave the row in 'planning' for crash recovery instead of
			// failing a task that never had a chance to run.
			return
		}
	}
	started := time.Now()
	plan, err := d.planTask(ctx, task.UserID, task.Model, task.Goal)
	elapsed := time.Since(started)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			// The process is stopping. Keeping the row in 'planning' is what the startup scan expects.
			return
		}
		d.failTaskPlan(ctx, task, err)
		d.observeTaskPlanning(elapsed)
		return
	}
	encoded, err := taskPlanJSON(plan)
	if err != nil {
		d.failTaskPlan(ctx, task, err)
		d.observeTaskPlanning(elapsed)
		return
	}
	now := d.now().UTC()
	saved, saveErr := store.SaveTaskPlan(ctx, d.deps.DB, task.ID, encoded, now)
	if saveErr != nil {
		d.deps.Logger.Error("the generated task plan was not stored",
			"task_id", task.ID, "error", Sanitize(saveErr, d.deps.Token))
		d.observeTaskPlanning(elapsed)
		return
	}
	if !saved {
		// The task was cancelled, deleted, or reset by crash recovery while the model was working.
		return
	}
	task.PlanJSON = encoded
	task.Status = store.TaskStatusReady
	task.DueAt = now
	task.UpdatedAt = now
	// Write the plan into the SAME message before the scheduler is woken: otherwise the first step could
	// claim the task and rewrite the status message with a step line before the plan line arrives.
	d.showTaskPlanStatus(ctx, &task, plan)
	d.observeTaskPlanning(elapsed)
	d.wakeTasks()
}

// failTaskPlan stores a localized, bounded reason. The raw model/provider error never reaches the user
// screen: only the already localized task wording does.
func (d *Dispatcher) failTaskPlan(ctx context.Context, task store.Task, cause error) {
	lang := d.chatLanguage(ctx, task.ChatID)
	reason := d.taskPlanningFailureReason(lang, cause)
	now := d.now().UTC()
	saved, err := store.FailTaskPlanning(ctx, d.deps.DB, task.ID, reason, now)
	if err != nil {
		d.deps.Logger.Error("the failed task plan was not stored",
			"task_id", task.ID, "error", Sanitize(err, d.deps.Token))
		return
	}
	if !saved {
		return
	}
	task.Status = store.TaskStatusFailed
	task.Error = reason
	task.FinishedAt = &now
	d.countTaskFailed(cause)
	d.observeTaskDuration(task, now)
	d.showTaskPlanFailureStatus(ctx, &task, reason)
}

// taskPlanningFailureReason classifies a planning error into a user-facing text. A provider message is
// never quoted: the plan prompt and the provider error can carry unrelated details.
func (d *Dispatcher) taskPlanningFailureReason(lang string, cause error) string {
	if notice, limited := modelLimitNoticeFor(lang, cause); limited {
		return notice
	}
	switch {
	case errors.Is(cause, ErrTaskPlan):
		return i18n.T(lang, "task.reason.plan_invalid")
	case errors.Is(cause, context.DeadlineExceeded):
		return i18n.T(lang, "task.reason.plan_timeout")
	default:
		return i18n.T(lang, "task.reason.plan_model")
	}
}

func taskPlanText(lang string, taskID int64, plan TaskPlan) string {
	lines := []string{i18n.T(lang, "task.planning.ready", taskID, len(plan.Steps))}
	for index, step := range plan.Steps {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, step.Description))
	}
	lines = append(lines, i18n.T(lang, "task.created_hint"))
	return strings.Join(lines, "\n")
}

// showTaskPlanStatus edits the creation message with the finished plan. A task created from the Mini App
// has no Telegram message (id 0), and a replier without message ids falls back to a plain reply; in both
// cases no second Telegram message appears for a task that already has one.
func (d *Dispatcher) showTaskPlanStatus(ctx context.Context, task *store.Task, plan TaskPlan) {
	if task == nil || task.ID <= 0 {
		return
	}
	lang := d.chatLanguage(ctx, task.ChatID)
	text := taskPlanText(lang, task.ID, plan)
	messenger, capable := d.taskStatusMessenger()
	if capable && task.StatusMessageID > 0 {
		if err := messenger.EditTaskStatus(ctx, task.ChatID, task.StatusMessageID, text); err != nil {
			d.logTaskStatusFailure("task plan status message was not edited", task, err)
			return
		}
		d.noteTaskStatusEdit(task.ChatID, d.now())
		return
	}
	if capable {
		// A Mini App task: the UI shows the plan, no Telegram notification is invented for it.
		return
	}
	if d.deps.Replier == nil {
		return
	}
	if err := d.reply(ctx, task.ChatID, text); err != nil {
		d.deps.Logger.Warn("task plan reply was not sent", "task_id", task.ID, "error", Sanitize(err, d.deps.Token))
	}
}

func (d *Dispatcher) showTaskPlanFailureStatus(ctx context.Context, task *store.Task, reason string) {
	if task == nil || task.ID <= 0 {
		return
	}
	lang := d.chatLanguage(ctx, task.ChatID)
	text := i18n.T(lang, "task.planning.failed", task.ID, reason) + "\n" + i18n.T(lang, "task.planning.failed_hint")
	messenger, capable := d.taskStatusMessenger()
	if capable && task.StatusMessageID > 0 {
		if err := messenger.EditTaskStatus(ctx, task.ChatID, task.StatusMessageID, text); err != nil {
			d.logTaskStatusFailure("task plan failure message was not edited", task, err)
			return
		}
		d.forgetTaskStatusEdit(task.ChatID)
		return
	}
	if capable || d.deps.Replier == nil {
		return
	}
	if err := d.reply(ctx, task.ChatID, text); err != nil {
		d.deps.Logger.Warn("task plan failure reply was not sent", "task_id", task.ID, "error", Sanitize(err, d.deps.Token))
	}
}

// showTaskPlanningStatus rewrites the status message for a replan, so the user sees the retry start in
// the message that will later receive the new plan.
func (d *Dispatcher) showTaskPlanningStatus(ctx context.Context, task store.Task) {
	lang := d.chatLanguage(ctx, task.ChatID)
	text := i18n.T(lang, "task.planning.restarted", task.ID)
	messenger, capable := d.taskStatusMessenger()
	if capable && task.StatusMessageID > 0 {
		if err := messenger.EditTaskStatus(ctx, task.ChatID, task.StatusMessageID, text); err != nil {
			d.logTaskStatusFailure("task replan status message was not edited", &task, err)
			return
		}
		d.noteTaskStatusEdit(task.ChatID, d.now())
		return
	}
	if capable || d.deps.Replier == nil {
		return
	}
	_ = d.reply(ctx, task.ChatID, text)
}

// recoverStaleTaskPlanning is the crash-recovery scan. A row left in 'planning' by a dead process is
// re-enqueued while its attempt counter allows it; after the cap it is failed with a clear localized
// reason. The scan runs on every scheduler pass, so a row cannot stay invisible forever.
func (d *Dispatcher) recoverStaleTaskPlanning(ctx context.Context, now time.Time) {
	tasks, err := store.ListStalePlanningTasks(ctx, d.deps.DB, now.Add(-taskPlanningRecoveryAge), TaskBatchLimit)
	if err != nil {
		d.deps.Logger.Error("the stale task planning scan failed", "error", Sanitize(err, d.deps.Token))
		return
	}
	for _, task := range tasks {
		if d.taskPlanningActive(task.ID) {
			continue
		}
		lang := d.chatLanguage(ctx, task.ChatID)
		if task.PlanningAttempts >= taskPlanningAttemptsCap {
			reason := i18n.T(lang, "task.reason.planning_interrupted")
			saved, failErr := store.FailTaskPlanning(ctx, d.deps.DB, task.ID, reason, now)
			if failErr != nil {
				d.deps.Logger.Error("the stale planning task was not failed",
					"task_id", task.ID, "error", Sanitize(failErr, d.deps.Token))
				continue
			}
			if !saved {
				continue
			}
			task.Status = store.TaskStatusFailed
			task.Error = reason
			task.FinishedAt = &now
			d.countTaskFailed(errors.New("stale task planning"))
			d.observeTaskDuration(task, now)
			d.showTaskPlanFailureStatus(ctx, &task, reason)
			continue
		}
		reset, resetErr := store.ResetTaskPlanning(ctx, d.deps.DB, task.ID, now, TaskLifetime)
		if resetErr != nil {
			d.deps.Logger.Error("the stale planning task was not re-enqueued",
				"task_id", task.ID, "error", Sanitize(resetErr, d.deps.Token))
			continue
		}
		if !reset {
			continue
		}
		task.PlanningAttempts++
		task.DueAt = now
		task.ExpiresAt = now.Add(TaskLifetime)
		task.UpdatedAt = now
		d.startTaskPlanning(task)
	}
}

// ReplanTask re-arms a task whose plan generation failed and starts the planner again. Only the latest
// failed task with an empty plan of the chat is eligible; a task that failed during execution keeps its
// plan and must be resumed instead.
func (d *Dispatcher) ReplanTask(ctx context.Context, chatID int64, taskID int64) (store.Task, bool, error) {
	// A replan is another planner model call, so it spends the same per-minute budget as the original
	// creation. The check is here (not only in the HTTP handler) because the Telegram menu reaches this
	// method with UsesModel=false.
	current, found, err := store.CurrentTask(ctx, d.deps.DB, chatID)
	if err != nil {
		return store.Task{}, false, err
	}
	if !found || current.ID != taskID || current.Status != store.TaskStatusFailed || current.PlanJSON != "" {
		return store.Task{}, false, nil
	}
	if !d.deps.Limit.Allow(current.UserID) {
		return store.Task{}, false, ErrTaskRateLimited
	}
	replanned, changed, err := store.ReplanTask(ctx, d.deps.DB, chatID, taskID, d.now().UTC(), TaskLifetime)
	if err != nil || !changed {
		return replanned, changed, err
	}
	d.showTaskPlanningStatus(ctx, replanned)
	d.startTaskPlanning(replanned)
	return replanned, true, nil
}
