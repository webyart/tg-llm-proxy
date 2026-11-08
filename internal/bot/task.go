package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"bot/internal/model"
	"bot/internal/store"
)

const (
	TaskUsage            = "Использование: /task <цель>, /task для статуса, /task cancel или /task resume <id>. Задача содержит от 1 до 3 безопасных шагов."
	TaskMaxDescription   = 240
	TaskLifetime         = time.Hour
	taskPlannerMaxRunes  = 8192
	taskFinalResultRunes = store.TaskMaxStepResultRunes
)

var ErrTaskPlan = errors.New("task plan failed strict validation")

// ErrTaskRateLimited — one of the two model budgets of the user is spent. Planning a task is a model call,
// so a task created from the Mini App must be refused with the same numbers the bot refuses /ask with.
var ErrTaskRateLimited = errors.New("the per-minute model budget of this user is spent")

type TaskPlanStep struct {
	Description string `json:"description"`
}

type TaskPlan struct {
	Steps []TaskPlanStep `json:"steps"`
}

func ParseTaskPlan(raw string) (TaskPlan, error) {
	if raw == "" || len(raw) > taskPlannerMaxRunes || !utf8.ValidString(raw) {
		return TaskPlan{}, ErrTaskPlan
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan TaskPlan
	if err := decoder.Decode(&plan); err != nil {
		return TaskPlan{}, fmt.Errorf("%w: %v", ErrTaskPlan, err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return TaskPlan{}, fmt.Errorf("%w: %v", ErrTaskPlan, err)
	}
	if len(plan.Steps) < 1 || len(plan.Steps) > store.TaskMaxSteps {
		return TaskPlan{}, fmt.Errorf("%w: plan must contain 1 to 3 steps", ErrTaskPlan)
	}
	for index := range plan.Steps {
		description := strings.TrimSpace(plan.Steps[index].Description)
		if description == "" || len([]rune(description)) > TaskMaxDescription || strings.ContainsRune(description, '\x00') {
			return TaskPlan{}, fmt.Errorf("%w: step %d has an invalid description length", ErrTaskPlan, index+1)
		}
		for _, char := range description {
			if unicode.IsControl(char) {
				return TaskPlan{}, fmt.Errorf("%w: step %d contains control characters", ErrTaskPlan, index+1)
			}
		}
		lower := strings.ToLower(description)
		for _, forbidden := range []string{
			"http:", "https:", "www.", "://", "read_url", "web_search", "calculator", "tool_call", "<tool",
			"download", "fetch ", "visit ", "open url", "search the web", "internet", "online", "network",
			"интернет", "скач", "загруз", "открой страниц", "bash", "shell", "powershell", "cmd.exe", "/bin/",
			"sudo ", "curl ", "wget ", "python", "javascript", "exec(", "eval(", "execute ", "command line",
			"terminal", "script", "file ", "filesystem", "embedding", "multi-agent", "subagent", "rm -rf", "${", "`", "../",
		} {
			if strings.Contains(lower, forbidden) {
				return TaskPlan{}, fmt.Errorf("%w: step %d contains a URL, network action, tool, code, or shell instruction", ErrTaskPlan, index+1)
			}
		}
		plan.Steps[index].Description = description
	}
	return plan, nil
}

func taskPlanJSON(plan TaskPlan) (string, error) {
	if _, err := ParseTaskPlan(mustTaskPlanJSON(plan)); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(plan)
	return string(encoded), err
}

func mustTaskPlanJSON(plan TaskPlan) string {
	encoded, _ := json.Marshal(plan)
	return string(encoded)
}

type taskActiveError struct {
	taskID int64
}

func (e *taskActiveError) Error() string { return store.ErrTaskActive.Error() }
func (e *taskActiveError) Unwrap() error { return store.ErrTaskActive }

type taskPlanningError struct {
	cause error
}

func (e *taskPlanningError) Error() string { return e.cause.Error() }
func (e *taskPlanningError) Unwrap() error { return e.cause }

func (d *Dispatcher) CreateTask(ctx context.Context, chatID int64, userID int64, goal string) (store.Task, error) {
	// This is the entry point of a second client (the Mini App), which does not pass through the command
	// middleware. Without this check, creating a task from there reached the model while /ask in the same
	// minute was refused. The bot's own /task handler comes through createTask directly and has already
	// been charged by the middleware, so the budget is checked exactly once per entry point.
	if !d.deps.Limit.Allow(userID) {
		return store.Task{}, ErrTaskRateLimited
	}
	var created store.Task
	err := d.deps.Locks.WithChat(ctx, chatID, func() error {
		var err error
		created, _, err = d.createTask(ctx, chatID, userID, "", goal)
		return err
	})
	if err != nil {
		return store.Task{}, err
	}
	return created, nil
}

func (d *Dispatcher) createTask(ctx context.Context, chatID int64, userID int64, chatTitle string, goal string) (store.Task, TaskPlan, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" || len([]rune(goal)) > store.TaskMaxGoalRunes || strings.ContainsRune(goal, '\x00') {
		return store.Task{}, TaskPlan{}, store.ErrTaskGoal
	}
	current, found, err := store.CurrentTask(ctx, d.deps.DB, chatID)
	if err != nil {
		return store.Task{}, TaskPlan{}, err
	}
	if found && store.ActiveTaskStatus(current.Status) {
		return store.Task{}, TaskPlan{}, &taskActiveError{taskID: current.ID}
	}
	questionID, role, err := store.LastVisibleMessage(ctx, d.deps.DB, chatID)
	if err != nil {
		return store.Task{}, TaskPlan{}, err
	}
	if questionID > 0 && role == store.RoleUser {
		if err := store.SetResumeSkip(ctx, d.deps.DB, chatID, questionID); err != nil {
			return store.Task{}, TaskPlan{}, err
		}
	}
	selectedModel, err := d.modelName(ctx, chatID)
	if err != nil {
		return store.Task{}, TaskPlan{}, err
	}
	plan, err := d.planTask(ctx, userID, selectedModel, goal)
	if err != nil {
		return store.Task{}, TaskPlan{}, &taskPlanningError{cause: err}
	}
	encodedPlan, err := taskPlanJSON(plan)
	if err != nil {
		return store.Task{}, TaskPlan{}, &taskPlanningError{cause: err}
	}
	if err := store.EnsureChat(ctx, d.deps.DB, chatID, userID, chatTitle); err != nil {
		return store.Task{}, TaskPlan{}, err
	}
	now := d.now().UTC()
	created, err := store.CreateTask(ctx, d.deps.DB, store.Task{
		ChatID: chatID, UserID: userID, Goal: goal, Model: selectedModel, PlanJSON: encodedPlan,
		DueAt: now, ExpiresAt: now.Add(TaskLifetime), CreatedAt: now,
	})
	if err != nil {
		return store.Task{}, TaskPlan{}, err
	}
	d.wakeTasks()
	return created, plan, nil
}

func (d *Dispatcher) task(ctx context.Context, ev Event, arg string) error {
	action := strings.TrimSpace(arg)
	if action == "" {
		return d.sendTaskMenu(ctx, ev)
	}
	if action == "menu" {
		return d.showTaskMenu(ctx, ev)
	}
	if action == "menu:cancel" {
		return d.cancelTask(ctx, ev, true)
	}
	if action == "menu:resume" {
		return d.resumeTask(ctx, ev, 0, true)
	}
	if action == "menu:clear" {
		return d.promptTaskClear(ctx, ev)
	}
	if action == "clear" {
		return d.promptTaskClear(ctx, ev)
	}
	if action == "clear confirm" {
		return d.clearTasks(ctx, ev)
	}
	if action == "menu:new" {
		return d.beginPendingInput(ctx, ev, CommandTask, "menu:task", "Отправь цель новой задачи одним сообщением. Будет создан новый безопасный план из 1–3 шагов. Ввод действует 2 минуты.")
	}
	if strings.EqualFold(action, "cancel") {
		return d.cancelTask(ctx, ev, false)
	}
	fields := strings.Fields(action)
	if len(fields) > 0 && strings.EqualFold(fields[0], "resume") {
		if len(fields) != 2 {
			return d.reply(ctx, ev.ChatID, TaskUsage)
		}
		taskID, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || taskID <= 0 {
			return d.reply(ctx, ev.ChatID, TaskUsage)
		}
		return d.resumeTask(ctx, ev, taskID, false)
	}
	created, plan, err := d.createTask(ctx, ev.ChatID, ev.UserID, ev.ChatTitle, action)
	if errors.Is(err, store.ErrTaskGoal) {
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("Цель задачи должна быть непустой и не длиннее %d символов.", store.TaskMaxGoalRunes))
	}
	if errors.Is(err, ErrTaskRateLimited) {
		return d.reply(ctx, ev.ChatID, modelWorkBudgetErrorText)
	}
	var active *taskActiveError
	if errors.As(err, &active) {
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("В чате уже выполняется задача #%d. Дождись завершения или используй /task cancel.", active.taskID))
	}
	var planning *taskPlanningError
	if errors.As(err, &planning) {
		reason := brief(Sanitize(planning.cause, d.deps.Token), 300)
		if notice, limited := modelLimitNotice(planning.cause); limited {
			reason = notice
		}
		return d.reply(ctx, ev.ChatID, "Не удалось составить безопасный план. Активная задача не создана. Причина: "+reason)
	}
	if errors.Is(err, store.ErrTaskActive) {
		return d.reply(ctx, ev.ChatID, "В чате уже есть активная задача. Дождись завершения или используй /task cancel.")
	}
	if err != nil {
		return err
	}
	lines := []string{fmt.Sprintf("Задача #%d создана. План (%d шаг.):", created.ID, len(plan.Steps))}
	for index, step := range plan.Steps {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, step.Description))
	}
	lines = append(lines, "Шаги выполняются последовательно. /task — прогресс, /task cancel — отмена.")
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

func (d *Dispatcher) promptTaskClear(ctx context.Context, ev Event) error {
	token, err := newDeleteConfirmationToken()
	if err != nil {
		return err
	}
	d.rememberDocumentConfirmation(token, ev, "task_clear", 0)
	markup := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		menuRow(menuButton("Подтвердить очистку", "menu:doc:confirm:"+token), menuButton("Отмена", "menu:doc:cancel:"+token)),
	}}
	return d.renderMenu(ctx, ev, "Удалить все задачи этого чата? Кнопки действуют 5 минут.", markup)
}

func (d *Dispatcher) clearTasks(ctx context.Context, ev Event) error {
	deleted, err := d.ClearTasks(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	return d.showTaskMenu(ctx, ev, fmt.Sprintf("Удалено задач: %d.", deleted))
}

func (d *Dispatcher) ClearTasks(ctx context.Context, chatID int64) (int64, error) {
	current, found, err := store.CurrentTask(ctx, d.deps.DB, chatID)
	if err != nil {
		return 0, err
	}
	deleted, err := store.ClearTasks(ctx, d.deps.DB, chatID)
	if err != nil {
		return 0, err
	}
	if found {
		d.removeTaskApproval(ctx, current)
	}
	return deleted, nil
}

func (d *Dispatcher) planTask(ctx context.Context, userID int64, selectedModel string, goal string) (TaskPlan, error) {
	replySeconds := d.deps.Cfg.ReplyDeadlineSeconds
	if replySeconds <= 0 {
		replySeconds = 30
	}
	planCtx, cancel := context.WithTimeout(ctx, time.Duration(replySeconds)*time.Second)
	defer cancel()
	response, err := d.callModel(planCtx, userID, model.Request{Model: selectedModel, Messages: []model.Message{
		{Role: model.RoleSystem, Content: "Create a SHORT plan for the user's goal. Return ONLY one JSON object with exactly this shape: {\"steps\":[{\"description\":\"...\"}]}. Use 1 to 3 steps. Each description is at most 240 characters and describes reasoning or information gathering in plain language. Do not include URLs, tool names, network actions, code, shell commands, file operations, agents, or any extra JSON fields. Do not use Markdown fences."},
		{Role: model.RoleUser, Content: goal},
	}}, nil, nil)
	if err != nil {
		return TaskPlan{}, err
	}
	return ParseTaskPlan(response.Content)
}

func (d *Dispatcher) wakeTasks() {
	select {
	case d.taskWake <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) cancelTask(ctx context.Context, ev Event, menu bool) error {
	cancelled, ok, err := d.CancelTask(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	if !ok {
		if menu {
			return d.showTaskMenu(ctx, ev, "Активной задачи нет или она уже завершена.")
		}
		return d.reply(ctx, ev.ChatID, "Активной задачи в этом чате нет.")
	}
	if menu {
		return d.showTaskMenu(ctx, ev, fmt.Sprintf("Задача #%d отменена. Будущие шаги и ожидающие подтверждения остановлены.", cancelled.ID))
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf("Задача #%d отменена. Будущие шаги и ожидающие подтверждения остановлены.", cancelled.ID))
}

func (d *Dispatcher) CancelTask(ctx context.Context, chatID int64) (store.Task, bool, error) {
	current, found, err := store.CurrentTask(ctx, d.deps.DB, chatID)
	if err != nil {
		return store.Task{}, false, err
	}
	cancelled, ok, err := store.CancelTask(ctx, d.deps.DB, chatID, d.now())
	if err != nil || !ok {
		return cancelled, ok, err
	}
	d.clearTaskNetworkScopes(chatID, cancelled.ID)
	if found && current.ID == cancelled.ID {
		d.removeTaskApproval(ctx, current)
	}
	return cancelled, true, nil
}

func (d *Dispatcher) resumeTask(ctx context.Context, ev Event, taskID int64, menu bool) error {
	if menu {
		current, found, err := store.CurrentTask(ctx, d.deps.DB, ev.ChatID)
		if err != nil {
			return err
		}
		if !found {
			return d.showTaskMenu(ctx, ev, "Возобновить можно только последнюю отменённую или остановленную с ошибкой задачу.")
		}
		taskID = current.ID
	}
	resumed, ok, err := d.ResumeTask(ctx, ev.ChatID, taskID)
	if err != nil {
		return err
	}
	if !ok {
		message := fmt.Sprintf("Задачу #%d нельзя возобновить: возобновляется только последняя отменённая или остановленная с ошибкой задача этого чата.", taskID)
		if menu {
			return d.showTaskMenu(ctx, ev, "Возобновить можно только последнюю отменённую или остановленную с ошибкой задачу.")
		}
		return d.reply(ctx, ev.ChatID, message)
	}
	message := fmt.Sprintf("Задача #%d возобновлена. Завершённые шаги и результаты сохранены; сетевые действия потребуют нового подтверждения.", resumed.ID)
	if menu {
		return d.showTaskMenu(ctx, ev, message)
	}
	return d.reply(ctx, ev.ChatID, message)
}

func (d *Dispatcher) ResumeTask(ctx context.Context, chatID int64, taskID int64) (store.Task, bool, error) {
	current, found, err := store.CurrentTask(ctx, d.deps.DB, chatID)
	if err != nil {
		return store.Task{}, false, err
	}
	resumable := found && current.ID == taskID && (current.Status == store.TaskStatusFailed || current.Status == store.TaskStatusCancelled)
	if resumable {
		d.clearTaskNetworkScopes(chatID, taskID)
	}
	resumed, ok, err := store.ResumeTask(ctx, d.deps.DB, chatID, taskID, d.now().UTC(), TaskLifetime)
	if err != nil || !ok {
		return resumed, ok, err
	}
	if resumable {
		d.removeTaskApproval(ctx, current)
	}
	d.wakeTasks()
	return resumed, true, nil
}

func (d *Dispatcher) removeTaskApproval(ctx context.Context, task store.Task) {
	d.clearTaskNetworkScopes(task.ChatID, task.ID)
	if task.ApprovalToken != "" {
		set := networkConfirmationsFor(d)
		set.mu.Lock()
		delete(set.pending, task.ApprovalToken)
		set.mu.Unlock()
	}
	if task.ApprovalMessageID > 0 {
		d.removeNetworkApprovalMenu(ctx, task.ChatID, task.ApprovalMessageID)
	}
}

func (d *Dispatcher) taskStatusText(ctx context.Context, ev Event) (string, store.Task, error) {
	task, found, err := store.CurrentTask(ctx, d.deps.DB, ev.ChatID)
	if err != nil || !found {
		return "Активной или завершённой задачи нет.\n" + TaskUsage, store.Task{}, err
	}
	plan, planErr := ParseTaskPlan(task.PlanJSON)
	status := map[string]string{
		store.TaskStatusReady: "ожидает выполнения", store.TaskStatusRunning: "выполняется",
		store.TaskStatusAwaitingApproval: "ждёт подтверждения сетевого действия", store.TaskStatusCompleted: "завершена",
		store.TaskStatusCancelled: "отменена", store.TaskStatusFailed: "остановлена с ошибкой",
	}[task.Status]
	if status == "" {
		status = "недействительное состояние"
	}
	lines := []string{fmt.Sprintf("Задача #%d: %s.", task.ID, status), "Цель: " + brief(task.Goal, 300)}
	if planErr == nil {
		progress := task.NextStep
		if progress > len(plan.Steps) {
			progress = len(plan.Steps)
		}
		lines = append(lines, fmt.Sprintf("Прогресс: %d/%d.", progress, len(plan.Steps)))
		for index, step := range plan.Steps {
			lines = append(lines, fmt.Sprintf("%d. %s", index+1, step.Description))
		}
	} else if store.ActiveTaskStatus(task.Status) {
		lines = append(lines, "Сохранённый план недействителен; выполнение будет остановлено без новых действий.")
	}
	if task.Error != "" {
		lines = append(lines, "Причина: "+brief(task.Error, 300))
	}
	if store.ActiveTaskStatus(task.Status) {
		lines = append(lines, "/task cancel — отменить.")
	} else {
		if task.Status == store.TaskStatusFailed || task.Status == store.TaskStatusCancelled {
			lines = append(lines, fmt.Sprintf("/task resume %d — возобновить с сохранением прогресса.", task.ID))
		}
		lines = append(lines, "/task <новая цель> — создать другую задачу.")
	}
	return strings.Join(lines, "\n"), task, nil
}

func taskMenuMarkup(task store.Task) models.InlineKeyboardMarkup {
	rows := [][]models.InlineKeyboardButton{menuRow(menuButton("Обновить", "menu:task"))}
	if task.ID > 0 && store.ActiveTaskStatus(task.Status) {
		rows = append(rows, menuRow(menuButton("Отменить задачу", "menu:task:cancel")))
	} else {
		rows = append(rows, menuRow(menuButton("Новая задача", "menu:task:new")))
	}
	if task.ID > 0 && (task.Status == store.TaskStatusFailed || task.Status == store.TaskStatusCancelled) {
		rows = append(rows, menuRow(menuButton("Возобновить задачу", "menu:task:resume")))
	}
	if task.ID > 0 {
		rows = append(rows, menuRow(menuButton("Очистить задачи", "menu:task:clear")))
	}
	rows = append(rows, menuRow(backButton("menu:more")))
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (d *Dispatcher) showTaskMenu(ctx context.Context, ev Event, note ...string) error {
	text, task, err := d.taskStatusText(ctx, ev)
	if err != nil {
		return err
	}
	return d.renderMenu(ctx, ev, menuText(text, note), taskMenuMarkup(task))
}

func (d *Dispatcher) sendTaskMenu(ctx context.Context, ev Event) error {
	text, task, err := d.taskStatusText(ctx, ev)
	if err != nil {
		return err
	}
	if sender, ok := d.deps.Replier.(interface {
		SendMenu(context.Context, int64, string, models.InlineKeyboardMarkup) error
	}); ok {
		return sender.SendMenu(ctx, ev.ChatID, Escape(text, d.deps.Cfg.ParseMode), taskMenuMarkup(task))
	}
	return d.reply(ctx, ev.ChatID, text)
}

func taskStepResults(task store.Task, expected int) ([]string, error) {
	var results []string
	if err := json.Unmarshal([]byte(task.ResultsJSON), &results); err != nil {
		return nil, fmt.Errorf("%w: stored results are not a string array", store.ErrTaskInvalid)
	}
	if len(results) != expected {
		return nil, fmt.Errorf("%w: stored results length %d, want %d", store.ErrTaskInvalid, len(results), expected)
	}
	for _, result := range results {
		if strings.TrimSpace(result) == "" || len([]rune(result)) > taskFinalResultRunes {
			return nil, fmt.Errorf("%w: stored result is empty or too long", store.ErrTaskInvalid)
		}
	}
	return results, nil
}
