package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bot/internal/i18n"
	"bot/internal/model"
	"bot/internal/store"
	"bot/internal/timefmt"
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

// taskStoredResults decodes the usage of the stored step results. A broken row is not rendered: the same
// progress line goes out without a footer instead of failing the status message.
func taskStoredResults(task store.Task) []store.TaskStepResult {
	results, err := store.ParseTaskResults(task.ResultsJSON)
	if err != nil {
		return nil
	}
	return results
}

func taskElapsed(task store.Task, now time.Time) time.Duration {
	if task.CreatedAt.IsZero() {
		return 0
	}
	elapsed := now.Sub(task.CreatedAt)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// formatTaskDuration renders elapsed/total task time through the shared formatter: m:ss under an hour (the
// «6:12» the user reads), a localized hours+minutes form above it, «~<1 мин» under a minute.
func formatTaskDuration(lang string, value time.Duration) string {
	return timefmt.Duration(value, lang)
}

// formatTaskETA renders the remaining-time estimate. The tilde is part of the text: an ETA is never a promise.
func formatTaskETA(lang string, value time.Duration) string {
	return timefmt.ETA(value, lang)
}

// formatTaskTokens shortens a token count to the form the footer uses: 950, 12.4k, 1.2M.
func formatTaskTokens(value int64) string {
	if value < 0 {
		value = 0
	}
	if value < 1000 {
		return strconv.FormatInt(value, 10)
	}
	if value < 1_000_000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(value)/1000), ".0") + "k"
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(value)/1_000_000), ".0") + "M"
}

// formatTaskCost renders the MODEL_PRICES estimate. Without a price table the cost is unknown, not zero.
func formatTaskCost(lang string, priced bool, micros int64) string {
	if !priced {
		return i18n.T(lang, "task.duration.none")
	}
	usd := float64(micros) / 1_000_000
	switch {
	case usd <= 0:
		return "$0.00"
	case usd < 0.01:
		return "<$0.01"
	default:
		return fmt.Sprintf("~$%.2f", usd)
	}
}

// taskUsageTotals sums tokens and cost over the stored steps plus an optional call that is not stored as a
// step (the final synthesis). A nil token count contributes zero, exactly like a NULL messages column.
func (d *Dispatcher) taskUsageTotals(task store.Task, results []store.TaskStepResult, extra *model.Response) (tokensIn int64, tokensOut int64, costMicros int64) {
	prices := d.deps.Cfg.ModelPrices
	add := func(modelName string, in *int64, out *int64) {
		var valueIn, valueOut int64
		if in != nil {
			valueIn = *in
		}
		if out != nil {
			valueOut = *out
		}
		tokensIn += valueIn
		tokensOut += valueOut
		costMicros += prices.CostMicros(modelName, valueIn, valueOut)
	}
	for _, result := range results {
		modelName := result.Model
		if modelName == "" {
			modelName = task.Model
		}
		add(modelName, result.TokensIn, result.TokensOut)
	}
	if extra != nil {
		add(replyModel(extra.Model, task.Model), extra.TokensIn, extra.TokensOut)
	}
	return tokensIn, tokensOut, costMicros
}

// taskAverageStepDuration is the average wall time of the already completed steps. Rows written before the
// usage was stored carry no duration; for them the elapsed time over the completed steps is the same average.
func taskAverageStepDuration(results []store.TaskStepResult, elapsed time.Duration) (time.Duration, bool) {
	if len(results) == 0 {
		return 0, false
	}
	var measured time.Duration
	var counted int
	for _, result := range results {
		if result.DurationMs > 0 {
			measured += time.Duration(result.DurationMs) * time.Millisecond
			counted++
		}
	}
	if counted > 0 {
		return measured / time.Duration(counted), true
	}
	if elapsed > 0 {
		return elapsed / time.Duration(len(results)), true
	}
	return 0, false
}

// taskStatusETA estimates the remaining time from the average completed-step duration. The bool is false
// while no step has completed and once every step is done: inventing an ETA from nothing would be a lie.
func taskStatusETA(average time.Duration, completed int, total int) (time.Duration, bool) {
	if average <= 0 || completed <= 0 || total <= 0 || completed >= total {
		return 0, false
	}
	return average * time.Duration(total-completed), true
}

// taskLiveFooter is the status-message footer: tokens, cost, elapsed time and the ETA of the current step.
func (d *Dispatcher) taskLiveFooter(lang string, task store.Task, results []store.TaskStepResult, completed int, total int, now time.Time) string {
	elapsed := taskElapsed(task, now)
	etaText := i18n.T(lang, "task.duration.none")
	if average, known := taskAverageStepDuration(results, elapsed); known {
		if eta, ok := taskStatusETA(average, completed, total); ok {
			etaText = formatTaskETA(lang, eta)
		}
	}
	tokensIn, tokensOut, costMicros := d.taskUsageTotals(task, results, nil)
	return i18n.T(lang, "task.live.footer",
		formatTaskDuration(lang, elapsed), etaText, formatTaskTokens(tokensIn), formatTaskTokens(tokensOut),
		formatTaskCost(lang, d.deps.Cfg.ModelPrices.HasPrices(), costMicros))
}

// taskFinalFooter is the terminal footer: the total time of the task and the usage of every call, including
// the final synthesis that is not stored as a step.
func (d *Dispatcher) taskFinalFooter(lang string, task store.Task, results []store.TaskStepResult, extra *model.Response, now time.Time) string {
	total := taskElapsed(task, now)
	if task.FinishedAt != nil && !task.FinishedAt.IsZero() && !task.CreatedAt.IsZero() {
		total = task.FinishedAt.Sub(task.CreatedAt)
		if total < 0 {
			total = 0
		}
	}
	tokensIn, tokensOut, costMicros := d.taskUsageTotals(task, results, extra)
	return i18n.T(lang, "task.live.footer_final",
		formatTaskDuration(lang, total), formatTaskTokens(tokensIn), formatTaskTokens(tokensOut),
		formatTaskCost(lang, d.deps.Cfg.ModelPrices.HasPrices(), costMicros))
}

// taskStepStatusText is the live line of one step: the header plus the footer with usage, elapsed and ETA.
func (d *Dispatcher) taskStepStatusText(lang string, task store.Task, plan TaskPlan, stepIndex int, completed int, now time.Time) string {
	total := len(plan.Steps)
	title := ""
	if stepIndex >= 0 && stepIndex < total {
		title = brief(plan.Steps[stepIndex].Description, 60)
	}
	header := i18n.T(lang, "task.live.step", task.ID, stepIndex+1, total, title)
	return header + "\n" + d.taskLiveFooter(lang, task, taskStoredResults(task), completed, total, now)
}

// taskApprovalStatusText is the same line while a network action waits for the user's confirmation.
func (d *Dispatcher) taskApprovalStatusText(lang string, task store.Task, plan TaskPlan, now time.Time) string {
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
	header := i18n.T(lang, "task.live.approval", task.ID, stepIndex+1, total, title)
	results := taskStoredResults(task)
	return header + "\n" + d.taskLiveFooter(lang, task, results, len(results), total, now)
}

// taskFinalStatusText keeps the last progress line and appends the terminal line requested by the user.
func (d *Dispatcher) taskFinalStatusText(lang string, task store.Task, plan TaskPlan, final string, now time.Time, extra *model.Response) string {
	total := len(plan.Steps)
	if total == 0 {
		return final
	}
	stepIndex := task.NextStep
	if task.Status == store.TaskStatusCompleted {
		stepIndex = total - 1
	}
	if stepIndex >= total {
		stepIndex = total - 1
	}
	if stepIndex < 0 {
		stepIndex = 0
	}
	title := ""
	if stepIndex < total {
		title = brief(plan.Steps[stepIndex].Description, 60)
	}
	header := i18n.T(lang, "task.live.step", task.ID, stepIndex+1, total, title)
	return header + "\n" + d.taskFinalFooter(lang, task, taskStoredResults(task), extra, now) + "\n" + final
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
	text := d.taskStepStatusText(lang, *task, plan, stepIndex, completed, now)
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
	text := d.taskApprovalStatusText(lang, *task, plan, now)
	if err := messenger.EditTaskStatus(ctx, task.ChatID, task.StatusMessageID, text); err != nil {
		d.logTaskStatusFailure("task approval status message was not edited", task, err)
		return
	}
	d.noteTaskStatusEdit(task.ChatID, now)
}

// finishTaskStatus appends the terminal line to the existing status message. It is a no-op when the task
// never had a status message (an old row, the Mini App, or a cancel before the first claim).
func (d *Dispatcher) finishTaskStatus(ctx context.Context, task store.Task, plan TaskPlan, final string, now time.Time) {
	d.finishTaskStatusWithUsage(ctx, task, plan, final, now, nil)
}

// finishTaskStatusWithUsage is finishTaskStatus for a call that carries usage not stored as a step result (the
// final synthesis). The extra response is only used by the footer.
func (d *Dispatcher) finishTaskStatusWithUsage(ctx context.Context, task store.Task, plan TaskPlan, final string, now time.Time, extra *model.Response) {
	if task.StatusMessageID <= 0 {
		return
	}
	messenger, ok := d.taskStatusMessenger()
	if !ok {
		return
	}
	lang := d.chatLanguage(ctx, task.ChatID)
	text := d.taskFinalStatusText(lang, task, plan, final, now, extra)
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
