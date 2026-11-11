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
	taskMetricStarted          = "bot_tasks_started_total"
	taskMetricCompleted        = "bot_tasks_completed_total"
	taskMetricFailed           = "bot_tasks_failed_total"
	taskMetricStepRetries      = "bot_task_step_retries_total"
	taskMetricStepTimeouts     = "bot_task_step_timeouts_total"
	taskMetricPlanningFailures = "bot_tasks_planning_failures_total"
	taskMetricQueueDepth       = "bot_task_queue_depth"
	taskMetricDuration         = "bot_task_duration_seconds"
	taskMetricPlanning         = "bot_tasks_planning_seconds"
)

const (
	taskMetricStartedHelp          = "Tasks created and persisted in this process."
	taskMetricCompletedHelp        = "Tasks that reached the completed state."
	taskMetricFailedHelp           = "Tasks that stopped with an error, by bounded reason."
	taskMetricStepRetriesHelp      = "Task steps retried once after a transient model failure."
	taskMetricStepTimeoutsHelp     = "Task model calls that ended with a spent deadline or model budget, by bounded phase."
	taskMetricPlanningFailuresHelp = "Task plans that failed, by bounded classification reason."
	taskMetricQueueDepthHelp       = "Tasks waiting in the planning or ready state at the last scheduler pass."
	taskMetricDurationHelp         = "Wall time of a task from creation to a terminal state, in seconds."
	taskMetricPlanningHelp         = "Wall time of the background STRATA plan of a task, in seconds."
)

// taskDurationBuckets cover a fast 1-step task through a long 3-step task with network approvals.
var taskDurationBuckets = []float64{5, 15, 30, 60, 120, 300, 600, 1800, 3600}

// taskPlanningBuckets make the 120-second alert threshold visible as its own bucket: p95 above it is the
// signal that the planner is too slow for the asynchronous UX.
var taskPlanningBuckets = []float64{1, 2, 5, 10, 20, 30, 60, 120, 300, 600}

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

// taskTimeoutPhaseName maps the internal model-call phase to the bounded metric label. A step-level timeout
// is the tool/research phase by default; the final-answer and synthesis phases keep their own names.
func taskTimeoutPhaseName(phase string) string {
	switch phase {
	case taskPhaseSynthesis:
		return "synthesis"
	case taskPhaseFinal:
		return "final"
	default:
		return "tool"
	}
}

// countTaskStepTimeout records a task model call that failed because its deadline or model budget was spent.
// The label is a fixed phase name, never a task, chat or prompt identifier.
func (d *Dispatcher) countTaskStepTimeout(phase string) {
	if d == nil {
		return
	}
	if reg := d.metrics.Load(); reg != nil {
		reg.Counter(taskMetricStepTimeouts, taskMetricStepTimeoutsHelp).
			Add(map[string]string{"phase": taskTimeoutPhaseName(phase)}, 1)
	}
}

// taskPlanningFailureClass classifies a planning error into the bounded label set
// model|timeout|invalid|other. It never carries the planner prompt or the provider error text.
func taskPlanningFailureClass(cause error) string {
	switch {
	case cause == nil:
		return "other"
	case errors.Is(cause, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(cause, ErrTaskPlan):
		return "invalid"
	case errors.Is(cause, model.ErrEmptyAnswer):
		return "model"
	}
	var apiErr *model.APIError
	if errors.As(cause, &apiErr) || model.Retryable(cause) {
		return "model"
	}
	return "other"
}

// countTaskPlanningFailure records one failed plan by bounded class. A plan that the user is invited to
// retry after the model budget ran out is still a planning failure; only the stored failure is counted.
func (d *Dispatcher) countTaskPlanningFailure(cause error) {
	if d == nil {
		return
	}
	if reg := d.metrics.Load(); reg != nil {
		reg.Counter(taskMetricPlanningFailures, taskMetricPlanningFailuresHelp).
			Add(map[string]string{"reason": taskPlanningFailureClass(cause)}, 1)
	}
}

// observeTaskQueueDepth publishes how many tasks are still waiting for their plan or for a scheduler pass.
// The count is one indexed query on the scheduler pass, and the gauge is label-free on purpose.
func (d *Dispatcher) observeTaskQueueDepth(ctx context.Context) {
	if d == nil || d.deps.DB == nil {
		return
	}
	reg := d.metrics.Load()
	if reg == nil {
		return
	}
	var depth int64
	if err := d.deps.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE status IN (?, ?)`,
		store.TaskStatusPlanning, store.TaskStatusReady).Scan(&depth); err != nil {
		return
	}
	if depth < 0 {
		depth = 0
	}
	reg.Gauge(taskMetricQueueDepth, taskMetricQueueDepthHelp).Set(nil, float64(depth))
}

// observeTaskPlanning records one complete planning attempt (success or a stored failure). The startup
// recovery re-enqueue time is not part of it: the histogram measures the model call, not the crash gap.
func (d *Dispatcher) observeTaskPlanning(elapsed time.Duration) {
	if d == nil {
		return
	}
	if reg := d.metrics.Load(); reg != nil {
		seconds := elapsed.Seconds()
		if seconds < 0 {
			seconds = 0
		}
		reg.Histogram(taskMetricPlanning, taskMetricPlanningHelp, taskPlanningBuckets).Observe(nil, seconds)
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
