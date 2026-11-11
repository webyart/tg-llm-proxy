package bot

import (
	"context"
	"fmt"
	"time"

	"bot/internal/i18n"
	"bot/internal/store"
)

// taskStatusEditInterval is the minimum gap between two edits of the same task status message. Telegram
// counts message edits against a flood budget of its own; a task step can finish in a second, so without
// this gate a fast task would rewrite its message on every claim and every stored result.
const taskStatusEditInterval = 15 * time.Second

// taskStatusReplier is an OPTIONAL transport capability, deliberately not a method of Replier: a test
// fake or a transport without message ids keeps working through the old per-step reply. The real
// telegramReplier implements it in run.go.
type taskStatusReplier interface {
	SendTaskStatus(ctx context.Context, chatID int64, text string) (int64, error)
	EditTaskStatus(ctx context.Context, chatID int64, messageID int64, text string) error
}

// taskStatusMessenger returns the live-progress capability of the configured replier.
func (d *Dispatcher) taskStatusMessenger() (taskStatusReplier, bool) {
	if d == nil || d.deps.Replier == nil {
		return nil, false
	}
	messenger, ok := d.deps.Replier.(taskStatusReplier)
	return messenger, ok
}

// formatTaskElapsed renders a task duration as m:ss or h:mm:ss.
func formatTaskElapsed(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	seconds := int64(value / time.Second)
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds%60)
}

// taskStatusETA estimates the remaining time from the average of the already stored steps. The bool is
// false while no step has completed: inventing an ETA from nothing would be a lie in the status line.
func taskStatusETA(elapsed time.Duration, completed int, total int) (time.Duration, bool) {
	if completed <= 0 || total <= 0 || completed >= total {
		return 0, false
	}
	return elapsed / time.Duration(completed) * time.Duration(total-completed), true
}

// taskStepStatusText is the live line of one step: «Задача #N · шаг K/M · title · elapsed / ETA».
func taskStepStatusText(lang string, task store.Task, plan TaskPlan, stepIndex int, completed int, now time.Time) string {
	total := len(plan.Steps)
	title := ""
	if stepIndex >= 0 && stepIndex < total {
		title = brief(plan.Steps[stepIndex].Description, 60)
	}
	elapsed := now.Sub(task.CreatedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	if eta, known := taskStatusETA(elapsed, completed, total); known {
		return i18n.T(lang, "task.live.progress_eta", task.ID, stepIndex+1, total, title, formatTaskElapsed(elapsed), formatTaskElapsed(eta))
	}
	return i18n.T(lang, "task.live.progress", task.ID, stepIndex+1, total, title, formatTaskElapsed(elapsed))
}

// taskApprovalStatusText is the same line while a network action waits for the user's confirmation.
func taskApprovalStatusText(lang string, task store.Task, plan TaskPlan, now time.Time) string {
	total := len(plan.Steps)
	stepIndex := task.NextStep
	if stepIndex < 0 {
		stepIndex = 0
	}
	if stepIndex >= total {
		stepIndex = total - 1
	}
	title := ""
	if stepIndex >= 0 && stepIndex < total {
		title = brief(plan.Steps[stepIndex].Description, 60)
	}
	elapsed := now.Sub(task.CreatedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	return i18n.T(lang, "task.live.approval", task.ID, stepIndex+1, total, title, formatTaskElapsed(elapsed))
}

// taskFinalStatusText keeps the last progress line and appends the terminal line requested by the user.
func taskFinalStatusText(lang string, task store.Task, plan TaskPlan, final string, now time.Time) string {
	total := len(plan.Steps)
	if total == 0 {
		return final
	}
	stepIndex := task.NextStep
	completed := task.NextStep
	if task.Status == store.TaskStatusCompleted {
		stepIndex = total - 1
		completed = total
	}
	if stepIndex >= total {
		stepIndex = total - 1
	}
	if stepIndex < 0 {
		stepIndex = 0
	}
	return taskStepStatusText(lang, task, plan, stepIndex, completed, now) + "\n" + final
}

// taskStatusEditDue reports whether the per-chat edit budget allows another rewrite of the status message.
func (d *Dispatcher) taskStatusEditDue(chatID int64, now time.Time) bool {
	if taskStatusEditInterval <= 0 {
		return true
	}
	d.taskStatusMu.Lock()
	defer d.taskStatusMu.Unlock()
	last, seen := d.taskStatusEdits[chatID]
	return !seen || now.Sub(last) >= taskStatusEditInterval
}

// noteTaskStatusEdit remembers a successful rewrite.
func (d *Dispatcher) noteTaskStatusEdit(chatID int64, now time.Time) {
	d.taskStatusMu.Lock()
	defer d.taskStatusMu.Unlock()
	if d.taskStatusEdits == nil {
		d.taskStatusEdits = map[int64]time.Time{}
	}
	d.taskStatusEdits[chatID] = now
}

// forgetTaskStatusEdit drops the per-chat entry once a task is terminal, so the map does not outlive the
// tasks that created it.
func (d *Dispatcher) forgetTaskStatusEdit(chatID int64) {
	d.taskStatusMu.Lock()
	defer d.taskStatusMu.Unlock()
	delete(d.taskStatusEdits, chatID)
}

// refreshTaskStatus creates the single live status message on the first step and rewrites that same
// message on later transitions. `stepIndex` is zero-based, `completed` is the number of stored steps.
// The function never returns an error: progress is a diagnostic of the task, not part of its outcome.
func (d *Dispatcher) refreshTaskStatus(ctx context.Context, task *store.Task, plan TaskPlan, stepIndex int, completed int, now time.Time, terminal bool) {
	if task == nil || task.ID <= 0 {
		return
	}
	messenger, ok := d.taskStatusMessenger()
	if !ok {
		return
	}
	lang := d.chatLanguage(ctx, task.ChatID)
	text := taskStepStatusText(lang, *task, plan, stepIndex, completed, now)
	if task.StatusMessageID <= 0 {
		// A terminal task with no message must not create one just to announce its end: the existing
		// final answer or stop notice is the only message that user should get in that case.
		if terminal {
			return
		}
		messageID, err := messenger.SendTaskStatus(ctx, task.ChatID, text)
		if err != nil {
			d.countSendFailure()
			d.logTaskStatusFailure("task status message was not sent", task, err)
			return
		}
		if messageID <= 0 {
			d.logTaskStatusFailure("task status message has no id", task, fmt.Errorf("telegram returned no message id"))
			return
		}
		task.StatusMessageID = messageID
		if d.deps.DB != nil {
			saved, saveErr := store.SetTaskStatusMessage(ctx, d.deps.DB, task.ID, task.ChatID, messageID, now)
			if saveErr != nil || !saved {
				d.logTaskStatusFailure("task status message id was not stored", task, saveErr)
			}
		}
		return
	}
	if !terminal && !d.taskStatusEditDue(task.ChatID, now) {
		return
	}
	if err := messenger.EditTaskStatus(ctx, task.ChatID, task.StatusMessageID, text); err != nil {
		d.logTaskStatusFailure("task status message was not edited", task, err)
		return
	}
	d.noteTaskStatusEdit(task.ChatID, now)
}

// refreshTaskApprovalStatus rewrites an existing status message to say that the step now waits for the
// user's network confirmation. It never creates a message (the approval prompt itself is the new one).
func (d *Dispatcher) refreshTaskApprovalStatus(ctx context.Context, task *store.Task, plan TaskPlan, now time.Time) {
	if task == nil || task.StatusMessageID <= 0 {
		return
	}
	messenger, ok := d.taskStatusMessenger()
	if !ok || !d.taskStatusEditDue(task.ChatID, now) {
		return
	}
	lang := d.chatLanguage(ctx, task.ChatID)
	text := taskApprovalStatusText(lang, *task, plan, now)
	if err := messenger.EditTaskStatus(ctx, task.ChatID, task.StatusMessageID, text); err != nil {
		d.logTaskStatusFailure("task approval status message was not edited", task, err)
		return
	}
	d.noteTaskStatusEdit(task.ChatID, now)
}

// finishTaskStatus appends the terminal line to the existing status message. It is a no-op when the task
// never had a status message (an old row, the Mini App, or a cancel before the first claim).
func (d *Dispatcher) finishTaskStatus(ctx context.Context, task store.Task, plan TaskPlan, final string, now time.Time) {
	if task.StatusMessageID <= 0 {
		return
	}
	messenger, ok := d.taskStatusMessenger()
	if !ok {
		return
	}
	lang := d.chatLanguage(ctx, task.ChatID)
	text := taskFinalStatusText(lang, task, plan, final, now)
	if err := messenger.EditTaskStatus(ctx, task.ChatID, task.StatusMessageID, text); err != nil {
		d.logTaskStatusFailure("terminal task status message was not edited", &task, err)
		return
	}
	d.forgetTaskStatusEdit(task.ChatID)
}

// logTaskStatusFailure reports a progress failure without the message text: the line carries model output.
func (d *Dispatcher) logTaskStatusFailure(message string, task *store.Task, err error) {
	if d.deps.Logger == nil {
		return
	}
	detail := "no matching row"
	if err != nil {
		detail = Sanitize(err, d.deps.Token)
	}
	d.deps.Logger.Warn(message, "task_id", task.ID, "chat_id", task.ChatID, "error", detail)
}

// taskPlanForStatus parses the stored plan; a broken plan yields an empty one so the final line still goes
// out without a progress prefix.
func taskPlanForStatus(task store.Task) TaskPlan {
	plan, err := ParseTaskPlan(task.PlanJSON)
	if err != nil {
		return TaskPlan{}
	}
	return plan
}
