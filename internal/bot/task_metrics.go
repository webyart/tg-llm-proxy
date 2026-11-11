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
	// taskMetricPlanningQueueDepth is the label-free count of rows still in the planning state. It answers
	// "how many chats are waiting for a planner" without naming a chat, user or task.
	taskMetricPlanningQueueDepth = "bot_task_planning_queue_depth"
	// taskMetricPlanningWait is the time between task creation and the claim of its planning row. It is the
	// queueing delay before the plan model call, not the model call itself (that is bot_tasks_planning_seconds).
	taskMetricPlanningWait = "bot_task_planning_wait_seconds"
)

const (
	taskMetricStartedHelp            = "Tasks created and persisted in this process."
	taskMetricCompletedHelp          = "Tasks that reached the completed state."
	taskMetricFailedHelp             = "Tasks that stopped with an error, by bounded reason."
	taskMetricStepRetriesHelp        = "Task steps retried once after a transient model failure."
	taskMetricStepTimeoutsHelp       = "Task model calls that ended with a spent deadline or model budget, by bounded phase."
	taskMetricPlanningFailuresHelp   = "Task plans that failed, by bounded classification reason."
	taskMetricQueueDepthHelp         = "Tasks waiting in the planning or ready state at the last scheduler pass."
	taskMetricDurationHelp           = "Wall time of a task from creation to a terminal state, in seconds."
	taskMetricPlanningHelp           = "Wall time of the background STRATA plan of a task, in seconds."
	taskMetricPlanningQueueDepthHelp = "Task rows in the planning state, including rows leased by a planner, at the last scheduler pass."
	taskMetricPlanningWaitHelp       = "Time from task creation to the claim of its planning row, in seconds."
)

// taskDurationBuckets cover a fast 1-step task through a long 3-step task with network approvals.
var taskDurationBuckets = []float64{5, 15, 30, 60, 120, 300, 600, 1800, 3600}

// taskPlanningBuckets make the 120-second alert threshold visible as its own bucket: p95 above it is the
// signal that the planner is too slow for the asynchronous UX.
var taskPlanningBuckets = []float64{1, 2, 5, 10, 20, 30, 60, 120, 300, 600}

// taskPlanningWaitBuckets cover an instant claim through a planner starved for half an hour. The 120-second
// bucket is the same alert threshold as taskPlanningBuckets: a wait above it means planning is not keeping
// up with task creation, independently of how long the model call itself takes.
var taskPlanningWaitBuckets = []float64{1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 1800}

// taskFailureReason classifies a task error into the bounded label set
// model|empty|invalid|timeout|budget|other. The classification never carries the error text.
func taskFailureReason(cause error) string {
	switch {
	case cause == nil:
		return "other"
	case errors.Is(cause, model.ErrEmptyAnswer):
		return "empty"
	case errors.Is(cause, ErrModelRateLimited), errors.Is(cause, ErrModelDailyLimited),
		errors.Is(cause, ErrChatModelDailyLimited), errors.Is(cause, ErrProviderBudgetExceeded):
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

// taskTimeoutPhaseName maps the internal model-call phase to the bounded metric label. The tool loop tags a
// tool-round failure and a final-answer failure itself, so both keep their own names; synthesis is passed by
// the scheduler. taskPhaseStep and any unexpected value are the bounded fallback "tool", never a raw label.
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

// observeTaskPlanningQueueDepth publishes how many rows are still waiting for a planner. It is a separate
// gauge from bot_task_queue_depth so an operator can tell planning backlog from execution backlog; both are
// label-free, so the number never names a chat. The count includes rows leased by a planner (a plan in
// progress is still not ready), and it is published on the same scheduler pass as the execution gauge.
func (d *Dispatcher) observeTaskPlanningQueueDepth(ctx context.Context) {
	if d == nil || d.deps.DB == nil {
		return
	}
	reg := d.metrics.Load()
	if reg == nil {
		return
	}
	var depth int64
	if err := d.deps.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE status = ?`,
		store.TaskStatusPlanning).Scan(&depth); err != nil {
		return
	}
	if depth < 0 {
		depth = 0
	}
	reg.Gauge(taskMetricPlanningQueueDepth, taskMetricPlanningQueueDepthHelp).Set(nil, float64(depth))
}

// observeTaskPlanningWait records how long a task waited between creation and the claim of its planning row.
// A zero CreatedAt means the row was built by a caller that did not set it; such a row is not measured
// rather than measured as an absurd age. A claim after crash recovery still measures the original creation:
// the wait is real, and the metric deliberately includes the downtime.
func (d *Dispatcher) observeTaskPlanningWait(task store.Task, claimedAt time.Time) {
	if d == nil || task.CreatedAt.IsZero() || claimedAt.IsZero() {
		return
	}
	if reg := d.metrics.Load(); reg != nil {
		seconds := claimedAt.Sub(task.CreatedAt).Seconds()
		if seconds < 0 {
			seconds = 0
		}
		reg.Histogram(taskMetricPlanningWait, taskMetricPlanningWaitHelp, taskPlanningWaitBuckets).Observe(nil, seconds)
	}
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
