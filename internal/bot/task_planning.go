package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"bot/internal/config"
	"bot/internal/i18n"
	"bot/internal/store"
)

// The planning phase is deliberately asynchronous: POST /api/tasks and Telegram /task only insert the
// row and return, while a bounded set of workers calls the STRATA planner. These constants are the whole
// contract of that phase.
const (
	// taskPlanningRecoveryAge is how long a planning row may stay untouched before crash recovery
	// re-enqueues it. The row must also be free of a live lease: age alone never revokes a claim.
	taskPlanningRecoveryAge = 2 * time.Minute
	// taskPlanningAttemptsCap bounds the crash-recovery re-enqueues. The first insert is attempt one;
	// after this many starts a row that still cannot finish is failed with a clear error instead of being
	// retried forever.
	taskPlanningAttemptsCap = 3
	// taskPlanningConcurrency is the number of plans that may call the model at the same time. The row is
	// already visible, so the queue of waiting tasks is not lost when every worker is busy.
	taskPlanningConcurrency = 4
	// taskPlanningLeaseMargin is the part of the planning lease that exceeds the plan call's own deadline
	// (TASK_STEP_DEADLINE_SECONDS, applied by planTask). The TTL therefore covers a plan that spends its
	// whole budget even if a heartbeat is lost; the heartbeat keeps the claim alive while the plan runs.
	taskPlanningLeaseMargin = 30 * time.Second
)

// newTaskPlanningOwner returns the opaque instance id this process writes to tasks.planning_owner. Two bot
// instances share one database, so the id must be unique per process; a random 8-byte suffix is enough and
// carries no host, user or chat data.
func newTaskPlanningOwner() string {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("instance-%d", os.Getpid())
	}
	return "instance-" + hex.EncodeToString(suffix[:])
}

// taskPlanningDeadline is the budget of one plan model call. planTask applies it to the call context, and the
// planning lease covers at least the same deadline plus a margin.
func (d *Dispatcher) taskPlanningDeadline() time.Duration {
	seconds := d.deps.Cfg.TaskStepDeadlineSeconds
	if seconds <= 0 {
		seconds = config.DefaultTaskStepDeadlineSeconds
	}
	return time.Duration(seconds) * time.Second
}

// taskPlanningLease is the TTL of one planning claim. planningLeaseTTL is the test override; zero means the
// computed value, which covers the plan call's deadline plus a margin.
func (d *Dispatcher) taskPlanningLease() time.Duration {
	if d.planningLeaseTTL > 0 {
		return d.planningLeaseTTL
	}
	return d.taskPlanningDeadline() + taskPlanningLeaseMargin
}

// taskPlanningRenew is the heartbeat interval: a third of the TTL, so two lost renewals still leave a third
// of the lease before another instance may take the row.
func (d *Dispatcher) taskPlanningRenew() time.Duration {
	renew := d.taskPlanningLease() / 3
	if renew < 10*time.Millisecond {
		renew = 10 * time.Millisecond
	}
	return renew
}

// startTaskPlanning wakes a planner worker for one row. It never blocks: the caller is the HTTP or Telegram
// creation path. The row is only a hint — the worker claims its work from the database — and the in-process
// active set only avoids spawning a duplicate local worker; correctness rests on the planning lease.
func (d *Dispatcher) startTaskPlanning(task store.Task) {
	if d == nil || task.ID <= 0 || task.Status != store.TaskStatusPlanning {
		return
	}
	if !d.reserveTaskPlanning(task.ID) {
		return
	}
	go d.runTaskPlanning(task)
}

// reserveTaskPlanning marks a row as "a local worker is being started for it". A false result is a fast local
// duplicate, never a statement about the lease in the database.
func (d *Dispatcher) reserveTaskPlanning(taskID int64) bool {
	d.planningMu.Lock()
	defer d.planningMu.Unlock()
	if d.planningActive == nil {
		d.planningActive = map[int64]bool{}
	}
	if d.planningActive[taskID] {
		return false
	}
	d.planningActive[taskID] = true
	return true
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

// runTaskPlanning is one planner worker. It takes a model-call slot, claims a row from the database and idles
// when every planning row is leased by another instance. The hint is only the local dedupe key of the row
// that woke this worker; the claimed row may legitimately be another one, and planning it is the point of a
// shared queue.
func (d *Dispatcher) runTaskPlanning(hint store.Task) {
	ctx := d.taskPlanningContext()
	if hint.ID > 0 {
		defer d.finishTaskPlanning(hint.ID)
	}
	if d.planningSlots != nil {
		select {
		case d.planningSlots <- struct{}{}:
			defer func() { <-d.planningSlots }()
		case <-ctx.Done():
			// Shutdown before the claim: leave the row in 'planning' for crash recovery instead of
			// failing a task that never had a chance to run.
			return
		}
	}
	claimed, found, err := store.ClaimPlanningTask(ctx, d.deps.DB, d.now().UTC(), d.planningOwner, d.taskPlanningLease())
	if err != nil {
		d.deps.Logger.Error("the task planning lease was not claimed",
			"task_id", hint.ID, "error", Sanitize(err, d.deps.Token))
		return
	}
	if !found {
		// Every planning row is leased by another instance: this worker has nothing to do.
		return
	}
	if claimed.ID != hint.ID && d.reserveTaskPlanning(claimed.ID) {
		defer d.finishTaskPlanning(claimed.ID)
	}
	d.planClaimedTask(ctx, claimed)
}

// planClaimedTask produces the plan of a row whose lease this instance holds, renews that lease while the
// model call runs, and releases it on every exit path. The release uses the owner, so a late release cannot
// clear the claim of the instance that took the row over after an expiry.
func (d *Dispatcher) planClaimedTask(ctx context.Context, task store.Task) {
	defer func() {
		if _, err := store.ReleasePlanningLease(ctx, d.deps.DB, task.ID, d.planningOwner); err != nil {
			d.deps.Logger.Warn("the task planning lease was not released",
				"task_id", task.ID, "error", Sanitize(err, d.deps.Token))
		}
	}()
	started := d.now()
	planCtx, cancelPlan := context.WithCancel(ctx)
	stopHeartbeat := d.keepTaskPlanningLease(planCtx, task.ID, cancelPlan)
	plan, planErr := d.planTask(planCtx, task.UserID, task.Model, task.Goal)
	stopHeartbeat()
	lostLease := planCtx.Err() != nil && ctx.Err() == nil
	cancelPlan()
	elapsed := d.now().Sub(started)
	if planErr != nil {
		if lostLease {
			// The claim went away while the model was working (another instance took the row over, or it
			// was cancelled): the row is no longer ours to fail, and the attempt is not counted.
			d.deps.Logger.Warn("task planning stopped after its lease was lost", "task_id", task.ID)
			return
		}
		if ctx.Err() != nil && errors.Is(planErr, context.Canceled) {
			// The process is stopping. Keeping the row in 'planning' is what the recovery scan expects.
			return
		}
		d.failTaskPlan(ctx, task, planErr)
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

// keepTaskPlanningLease renews the planning lease in the background until the returned stop function is
// called. A failed renewal — the row left 'planning', or another instance owns it — cancels the plan context:
// the planner must stop and leave the row to its new owner instead of writing a plan over it.
func (d *Dispatcher) keepTaskPlanningLease(ctx context.Context, taskID int64, lost context.CancelFunc) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(d.taskPlanningRenew())
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				until := d.now().UTC().Add(d.taskPlanningLease())
				renewed, err := store.RenewPlanningLease(ctx, d.deps.DB, taskID, d.planningOwner, until)
				if err != nil {
					d.deps.Logger.Warn("the task planning lease was not renewed",
						"task_id", taskID, "error", Sanitize(err, d.deps.Token))
					lost()
					return
				}
				if !renewed {
					d.deps.Logger.Warn("the task planning lease was lost; the plan is left to its owner", "task_id", taskID)
					lost()
					return
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
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
	d.countTaskPlanningFailure(cause)
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
// reason. A row that another instance still holds under a live lease is skipped: only the owner's release or
// the expiry of its lease makes a row stale. The scan runs on every scheduler pass, so a row cannot stay
// invisible forever.
func (d *Dispatcher) recoverStaleTaskPlanning(ctx context.Context, now time.Time) {
	staleBefore := now.Add(-taskPlanningRecoveryAge)
	tasks, err := store.ListStalePlanningTasks(ctx, d.deps.DB, staleBefore, now, TaskBatchLimit)
	if err != nil {
		d.deps.Logger.Error("the stale task planning scan failed", "error", Sanitize(err, d.deps.Token))
		return
	}
	for _, task := range tasks {
		if d.taskPlanningActive(task.ID) {
			// A local worker is being started for this row: its plan is not a crash.
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
			d.countTaskPlanningFailure(errors.New("stale task planning"))
			d.countTaskFailed(errors.New("stale task planning"))
			d.observeTaskDuration(task, now)
			d.showTaskPlanFailureStatus(ctx, &task, reason)
			continue
		}
		reset, resetErr := store.ResetTaskPlanning(ctx, d.deps.DB, task.ID, staleBefore, now, TaskLifetime)
		if resetErr != nil {
			d.deps.Logger.Error("the stale planning task was not re-enqueued",
				"task_id", task.ID, "error", Sanitize(resetErr, d.deps.Token))
			continue
		}
		if !reset {
			// Another instance reset or re-claimed the row between the scan and this update.
			continue
		}
		task.PlanningAttempts++
		task.PlanningOwner = ""
		task.PlanningLeaseUntil = nil
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
