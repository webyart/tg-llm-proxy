package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"bot/internal/model"
	"bot/internal/store"
)

const (
	TaskUsage            = "Использование: /task <цель>, /task для статуса или /task cancel. Задача содержит от 1 до 3 безопасных шагов."
	TaskMaxDescription   = 240
	TaskLifetime         = time.Hour
	taskPlannerMaxRunes  = 8192
	taskFinalResultRunes = store.TaskMaxStepResultRunes
)

var ErrTaskPlan = errors.New("task plan failed strict validation")

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
	if action == "menu:new" {
		return d.beginPendingInput(ctx, ev, CommandTask, "menu:task", "Отправь цель новой задачи одним сообщением. Будет создан новый безопасный план из 1–3 шагов. Ввод действует 2 минуты.")
	}
	if strings.EqualFold(action, "cancel") {
		return d.cancelTask(ctx, ev, false)
	}
	if len([]rune(action)) > store.TaskMaxGoalRunes || strings.ContainsRune(action, '\x00') {
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("Цель задачи должна быть непустой и не длиннее %d символов.", store.TaskMaxGoalRunes))
	}
	current, found, err := store.CurrentTask(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	if found && store.ActiveTaskStatus(current.Status) {
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("В чате уже выполняется задача #%d. Дождись завершения или используй /task cancel.", current.ID))
	}
	questionID, role, err := store.LastVisibleMessage(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	if questionID > 0 && role == store.RoleUser {
		if err := store.SetResumeSkip(ctx, d.deps.DB, ev.ChatID, questionID); err != nil {
			return err
		}
	}
	selectedModel, err := d.modelName(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	plan, err := d.planTask(ctx, ev.UserID, selectedModel, action)
	if err != nil {
		return d.reply(ctx, ev.ChatID, "Не удалось составить безопасный план. Активная задача не создана. Причина: "+brief(Sanitize(err, d.deps.Token), 300))
	}
	encodedPlan, err := taskPlanJSON(plan)
	if err != nil {
		return d.reply(ctx, ev.ChatID, "Не удалось проверить план. Активная задача не создана.")
	}
	if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
		return err
	}
	now := d.now().UTC()
	created, err := store.CreateTask(ctx, d.deps.DB, store.Task{
		ChatID: ev.ChatID, UserID: ev.UserID, Goal: action, Model: selectedModel, PlanJSON: encodedPlan,
		DueAt: now, ExpiresAt: now.Add(TaskLifetime), CreatedAt: now,
	})
	if errors.Is(err, store.ErrTaskActive) {
		return d.reply(ctx, ev.ChatID, "В чате уже есть активная задача. Дождись завершения или используй /task cancel.")
	}
	if err != nil {
		return err
	}
	d.wakeTasks()
	lines := []string{fmt.Sprintf("Задача #%d создана. План (%d шаг.):", created.ID, len(plan.Steps))}
	for index, step := range plan.Steps {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, step.Description))
	}
	lines = append(lines, "Шаги выполняются последовательно. /task — прогресс, /task cancel — отмена.")
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
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
	current, found, err := store.CurrentTask(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	cancelled, ok, err := store.CancelTask(ctx, d.deps.DB, ev.ChatID, d.now())
	if err != nil {
		return err
	}
	if ok && found {
		d.removeTaskApproval(ctx, current)
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
