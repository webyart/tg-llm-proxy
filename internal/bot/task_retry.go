package bot

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"bot/internal/model"
	"bot/internal/store"
)

// taskStepRetryBackoff is the short pause before the single retry of the SAME step. It is long enough to
// let a provider's transient 5xx/429 window pass and short enough that the task keeps its place in the chat.
const taskStepRetryBackoff = 2 * time.Second

// taskStepRetryable reports whether a failed task model call may be retried once immediately. The retry exists
// for the transient model layer only: a retryable provider error, an empty answer, or a model-layer deadline
// while the caller's task context still has time. A user cancel, an expired step deadline, approval-pending, a
// stale lease, a model budget refusal, or a deterministic state error (invalid plan/step result) is never
// retried — repeating those either cannot succeed or would spend the user's budget twice for the same
// deterministic refusal.
func taskStepRetryable(ctx context.Context, cause error) bool {
	if cause == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		// The step or scheduler context is already done: a retry has no budget left, and a user cancel must
		// stop the task instead of starting another model call.
		return false
	}
	switch {
	case errors.Is(cause, errNetworkApprovalPending), errors.Is(cause, errTaskStale):
		return false
	case errors.Is(cause, context.Canceled):
		return false
	case errors.Is(cause, store.ErrTaskInvalid), errors.Is(cause, store.ErrTaskStepResult),
		errors.Is(cause, store.ErrTaskGoal), errors.Is(cause, ErrTaskPlan):
		return false
	case errors.Is(cause, ErrModelRateLimited), errors.Is(cause, ErrModelDailyLimited),
		errors.Is(cause, ErrChatModelDailyLimited), errors.Is(cause, model.ErrNoRoom):
		return false
	case errors.Is(cause, model.ErrEmptyAnswer):
		return true
	case errors.Is(cause, context.DeadlineExceeded):
		// The checked ctx is alive, so this is the model client's own per-call budget, not the step deadline:
		// the same step can still finish within the remaining step time.
		return true
	}
	return model.Retryable(cause)
}

// taskStepRetryReason names the retry class for the audit row and the log line, never the error text.
func taskStepRetryReason(cause error) string {
	switch {
	case errors.Is(cause, model.ErrEmptyAnswer):
		return "empty"
	case errors.Is(cause, context.DeadlineExceeded):
		return "timeout"
	}
	return "model"
}

// taskStepResponseEmpty reports the empty-answer class of a finished step call: no text and no tool call.
func taskStepResponseEmpty(response model.Response) bool {
	return strings.TrimSpace(stripTextualToolCalls(response.Content)) == "" && len(response.ToolCalls) == 0
}

// beginTaskModelRetry records the one retry of a task model call (a step or the final synthesis) and waits the
// short backoff. It returns false when the context ends during the wait; the caller then returns the context
// error and the scheduler leaves the task for a later pass instead of failing it on shutdown.
func (d *Dispatcher) beginTaskModelRetry(ctx context.Context, task store.Task, phase string, step int, cause error) bool {
	reason := taskStepRetryReason(cause)
	if d.deps.Logger != nil {
		if phase == taskPhaseSynthesis {
			d.deps.Logger.Warn("task synthesis failed transiently; retrying once",
				"task_id", task.ID, "phase", phase, "reason", reason)
		} else {
			d.deps.Logger.Warn("task step failed transiently; retrying the same step once",
				"task_id", task.ID, "step", step, "phase", phase, "reason", reason)
		}
	}
	d.countTaskStepRetry()
	detail := "step " + strconv.Itoa(step) + " reason " + reason
	if phase == taskPhaseSynthesis {
		detail = "synthesis reason " + reason
	}
	d.logChatEvent(ctx, task.ChatID, task.UserID, store.EventTaskStepRetry, detail)
	if d.taskStepRetryDelay <= 0 {
		return true
	}
	timer := time.NewTimer(d.taskStepRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
