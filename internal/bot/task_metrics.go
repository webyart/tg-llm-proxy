package bot

import (
	"context"
	"errors"
	"time"

	"bot/internal/model"
	"bot/internal/store"
)

// Task metric family names. They are bounded and label-free except for the failure reason, so a series is
// never created per chat, user or task. No goal, step text or model answer may become a label or value.
const (
	taskMetricStarted     = "bot_tasks_started_total"
	taskMetricCompleted   = "bot_tasks_completed_total"
	taskMetricFailed      = "bot_tasks_failed_total"
	taskMetricStepRetries = "bot_task_step_retries_total"
	taskMetricDuration    = "bot_task_duration_seconds"
)

const (
	taskMetricStartedHelp     = "Tasks created and persisted in this process."
	taskMetricCompletedHelp   = "Tasks that reached the completed state."
	taskMetricFailedHelp      = "Tasks that stopped with an error, by bounded reason."
	taskMetricStepRetriesHelp = "Task steps retried once after a transient model failure."
	taskMetricDurationHelp    = "Wall time of a task from creation to a terminal state, in seconds."
)

// taskDurationBuckets cover a fast 1-step task through a long 3-step task with network approvals.
var taskDurationBuckets = []float64{5, 15, 30, 60, 120, 300, 600, 1800, 3600}

// taskFailureReason classifies a task error into the bounded label set
// model|empty|invalid|timeout|budget|other. The classification never carries the error text.
func taskFailureReason(cause error) string {
	switch {
	case cause == nil:
		return "other"
	case errors.Is(cause, model.ErrEmptyAnswer):
		return "empty"
	case errors.Is(cause, ErrModelRateLimited), errors.Is(cause, ErrModelDailyLimited), errors.Is(cause, ErrChatModelDailyLimited):
		return "budget"
	case errors.Is(cause, store.ErrTaskInvalid), errors.Is(cause, store.ErrTaskStepResult),
		errors.Is(cause, store.ErrTaskGoal), errors.Is(cause, ErrTaskPlan):
		return "invalid"
	case errors.Is(cause, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(cause, context.Canceled):
		return "other"
	case errors.Is(cause, model.ErrNoRoom):
		return "other"
	}
	var apiErr *model.APIError
	if errors.As(cause, &apiErr) {
		return "model"
	}
	if model.Retryable(cause) {
		return "model"
	}
	return "other"
}

func (d *Dispatcher) countTaskStarted() {
	if d == nil {
		return
	}
	if reg := d.metrics.Load(); reg != nil {
		reg.Counter(taskMetricStarted, taskMetricStartedHelp).Add(nil, 1)
	}
}

func (d *Dispatcher) countTaskCompleted() {
	if d == nil {
		return
	}
	if reg := d.metrics.Load(); reg != nil {
		reg.Counter(taskMetricCompleted, taskMetricCompletedHelp).Add(nil, 1)
	}
}

func (d *Dispatcher) countTaskFailed(cause error) {
	if d == nil {
		return
	}
	if reg := d.metrics.Load(); reg != nil {
		reg.Counter(taskMetricFailed, taskMetricFailedHelp).Add(map[string]string{"reason": taskFailureReason(cause)}, 1)
	}
}

func (d *Dispatcher) countTaskStepRetry() {
	if d == nil {
		return
	}
	if reg := d.metrics.Load(); reg != nil {
		reg.Counter(taskMetricStepRetries, taskMetricStepRetriesHelp).Add(nil, 1)
	}
}

// observeTaskDuration records the lifetime of a terminal task. A zero CreatedAt means the row was built by a
// caller that did not set it; such a task is not measured rather than measured as an absurd age.
func (d *Dispatcher) observeTaskDuration(task store.Task, now time.Time) {
	if d == nil || task.CreatedAt.IsZero() {
		return
	}
	if reg := d.metrics.Load(); reg != nil {
		seconds := now.Sub(task.CreatedAt).Seconds()
		if seconds < 0 {
			seconds = 0
		}
		reg.Histogram(taskMetricDuration, taskMetricDurationHelp, taskDurationBuckets).Observe(nil, seconds)
	}
}
