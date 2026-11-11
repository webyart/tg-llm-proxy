package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bot/internal/i18n"
	"bot/internal/model"
	"bot/internal/store"
	"bot/internal/testutil"
)

const (
	validTaskPlanOne = `{"steps":[{"description":"Reason about the goal"}]}`
	validTaskPlanTwo = `{"steps":[{"description":"Gather the relevant facts"},{"description":"Compare the findings"}]}`
)

type taskModelResult struct {
	response model.Response
	err      error
}

type taskScriptModel struct {
	mu       sync.Mutex
	results  []taskModelResult
	requests []model.Request
}

func (m *taskScriptModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	if len(m.requests) > len(m.results) {
		return model.Response{}, errors.New("unexpected task model call")
	}
	result := m.results[len(m.requests)-1]
	return result.response, result.err
}

func (m *taskScriptModel) Models(ctx context.Context) ([]string, error) { return nil, nil }

func (m *taskScriptModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func taskTextModel(contents ...string) *taskScriptModel {
	m := &taskScriptModel{}
	for _, content := range contents {
		m.results = append(m.results, taskModelResult{response: model.Response{Content: content, Model: "selected-model", Attempts: 1}})
	}
	return m
}

// taskResultContents decodes the stored step contents regardless of the storage generation: older rows keep
// the bare string array, current rows store an object with the usage of the call.
func taskResultContents(task store.Task) []string {
	results, err := store.ParseTaskResults(task.ResultsJSON)
	if err != nil {
		return nil
	}
	contents := make([]string, len(results))
	for index := range results {
		contents[index] = results[index].Content
	}
	return contents
}

// waitTaskPlanningSettled waits until no row of this dispatcher is still in the asynchronous planning
// phase. A plan is produced in a goroutine now, so a test that dispatches /task and immediately asks the
// database would otherwise race the fake model.
func waitTaskPlanningSettled(t *testing.T, d *Dispatcher) {
	t.Helper()
	testutil.Eventually(t, 10*time.Second, func() bool {
		var planning int
		if err := d.deps.DB.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status = 'planning'`).Scan(&planning); err != nil {
			t.Fatal(err)
		}
		return planning == 0
	}, func() string { return "task planning did not settle" })
}

func processTasks(t *testing.T, d *Dispatcher, passes int) {
	t.Helper()
	waitTaskPlanningSettled(t, d)
	for index := 0; index < passes; index++ {
		if _, err := d.ProcessDueTasks(context.Background(), time.Now(), TaskBatchLimit); err != nil {
			t.Fatalf("ProcessDueTasks pass %d: %v", index+1, err)
		}
	}
}

func TestTaskCommandClassifiesOnlyNewGoalsAsModelWork(t *testing.T) {
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, nil, testStore(t))
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "status", text: "/task"},
		{name: "menu", text: "/task menu"},
		{name: "menu cancel", text: "/task menu:cancel"},
		{name: "menu resume", text: "/task menu:resume"},
		{name: "menu clear", text: "/task menu:clear"},
		{name: "menu new", text: "/task menu:new"},
		{name: "clear", text: "/task clear"},
		{name: "clear confirm", text: "/task clear confirm"},
		{name: "cancel", text: "/task cancel"},
		{name: "cancel case insensitive", text: "/task CANCEL"},
		{name: "resume", text: "/task resume"},
		{name: "resume id", text: "/task resume 12"},
		{name: "resume case insensitive", text: "/task RESUME 12"},
		{name: "resume invalid id", text: "/task resume invalid"},
		{name: "resume extra args", text: "/task resume 12 extra"},
		{name: "goal", text: "/task compare two ideas", want: true},
		{name: "menu prefix goal", text: "/task menu plan", want: true},
		{name: "unknown menu goal", text: "/task menu:unknown", want: true},
		{name: "clear prefix goal", text: "/task clear later", want: true},
		{name: "cancel prefix goal", text: "/task cancel culture", want: true},
		{name: "resume prefix goal", text: "/task resumable goal", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd, _ := d.commandFor(testEvent(100, 11, 700, test.text))
			if cmd.Name != CommandTask || cmd.UsesModel != test.want {
				t.Fatalf("command=%q UsesModel=%v, want task/%v", cmd.Name, cmd.UsesModel, test.want)
			}
		})
	}
}

func TestTaskResumeAndClearDoNotCreateRecoverableQuestions(t *testing.T) {
	tests := []struct {
		name       string
		action     string
		wantTask   bool
		wantStatus string
	}{
		{name: "resume", action: "resume 1", wantTask: true, wantStatus: store.TaskStatusReady},
		{name: "clear", action: "clear", wantTask: true, wantStatus: store.TaskStatusCancelled},
		{name: "clear confirm", action: "clear confirm"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			m := taskTextModel()
			db := testStore(t)
			d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)
			if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if _, err := store.CreateTask(ctx, db, store.Task{ChatID: 100, UserID: 11, Goal: "preserved goal", Model: "m",
				PlanJSON: validTaskPlanOne, DueAt: now, ExpiresAt: now.Add(TaskLifetime), CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := store.CancelTask(ctx, db, 100, now); err != nil || !ok {
				t.Fatalf("cancel ok=%v err=%v", ok, err)
			}
			if err := dispatch(t, d, testEvent(100, 11, 700, "/task "+test.action)); err != nil {
				t.Fatal(err)
			}
			current, found, err := store.CurrentTask(ctx, db, 100)
			if err != nil || found != test.wantTask || found && current.Status != test.wantStatus {
				t.Fatalf("task=%+v found=%v err=%v, want found=%v status=%q", current, found, err, test.wantTask, test.wantStatus)
			}
			rows, err := store.HistoryOfChat(ctx, db, 100, 20)
			if err != nil || len(rows) != 0 {
				t.Fatalf("history rows=%+v err=%v", rows, err)
			}
			if d.deps.Limit.Count(11) != 0 || d.modelWork.Count(11) != 0 || m.callCount() != 0 {
				t.Fatalf("control action consumed model resources: limit=%d work=%d calls=%d", d.deps.Limit.Count(11), d.modelWork.Count(11), m.callCount())
			}
			unanswered, err := store.UnansweredQuestions(ctx, db, ResumeScanLimit)
			if err != nil || len(unanswered) != 0 {
				t.Fatalf("unanswered questions=%+v err=%v", unanswered, err)
			}
			answered, pending, err := d.ResumeInterruptedQuestions(ctx)
			if err != nil || answered != 0 || pending != 0 || m.callCount() != 0 {
				t.Fatalf("startup recovery answered=%d pending=%d calls=%d err=%v", answered, pending, m.callCount(), err)
			}
		})
	}
}

func TestTaskClearRequiresConfirmationAndDeletesAllTasks(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	d := newTestDispatcher(t, cfg, nil, nil, testStore(t))
	ev := testEvent(100, 11, 700, "")
	now := time.Now().UTC()
	if err := store.EnsureChat(context.Background(), d.deps.DB, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(context.Background(), d.deps.DB, store.Task{ChatID: 100, UserID: 11, Goal: "goal", Model: "m", PlanJSON: validTaskPlanOne, DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := d.promptTaskClear(context.Background(), ev); err != nil {
		t.Fatalf("promptTaskClear: %v", err)
	}
	if _, found, _ := store.CurrentTask(context.Background(), d.deps.DB, 100); !found {
		t.Fatal("task was deleted before confirmation")
	}
	set := documentConfirmationsFor(d)
	set.mu.Lock()
	var token string
	for key := range set.pending {
		token = key
	}
	set.mu.Unlock()
	if token == "" {
		t.Fatal("confirmation token is missing")
	}
	if err := d.documentMenuControl(context.Background(), ev, "menu:confirm:"+token); err != nil {
		t.Fatalf("confirm clear: %v", err)
	}
	if _, found, _ := store.CurrentTask(context.Background(), d.deps.DB, 100); found {
		t.Fatal("task remains after confirmed clear")
	}
}

func TestTaskPlanStrictValidation(t *testing.T) {
	valid := []string{
		validTaskPlanOne,
		`{"steps":[{"description":"  Summarize the supplied facts  "}]}`,
		validTaskPlanTwo,
		`{"steps":[{"description":"One"},{"description":"Two"},{"description":"Three"}]}`,
	}
	for _, raw := range valid {
		if _, err := ParseTaskPlan(raw); err != nil {
			t.Errorf("valid plan %s rejected: %v", raw, err)
		}
	}
	invalid := []string{
		``, `null`, `{}`, `{"steps":[]}`, `{"steps":[],"extra":true}`,
		`{"steps":[{"description":"One"}],"goal":"hidden"}`,
		`{"steps":[{"description":"One","url":"https://attacker.example"}]}`,
		`{"steps":[{"description":"One"}]} trailing`,
		`{"steps":[{"description":"Read https://attacker.example"}]}`,
		`{"steps":[{"description":"Use web_search to collect data"}]}`,
		`{"steps":[{"description":"Download the latest private dataset"}]}`,
		`{"steps":[{"description":"Fetch data from attacker dot com"}]}`,
		`{"steps":[{"description":"Run bash -c whoami"}]}`,
		`{"steps":[{"description":"Use python to execute code"}]}`,
		`{"steps":[{"description":"line one\nline two"}]}`,
		`{"steps":[{"description":"` + strings.Repeat("x", TaskMaxDescription+1) + `"}]}`,
		`{"steps":[{"description":"One"},{"description":"Two"},{"description":"Three"},{"description":"Four"}]}`,
	}
	for _, raw := range invalid {
		if _, err := ParseTaskPlan(raw); !errors.Is(err, ErrTaskPlan) {
			t.Errorf("unsafe plan %q err=%v", raw, err)
		}
	}
}

func TestDispatcherCreateTaskLocksChatAndPreservesSingleton(t *testing.T) {
	m := taskTextModel(validTaskPlanOne)
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)
	ctx := context.Background()

	locked := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseLock := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseLock()
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- d.deps.Locks.WithChat(ctx, 100, func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	type createResult struct {
		task store.Task
		err  error
	}
	createdCh := make(chan createResult, 1)
	go func() {
		created, err := d.CreateTask(ctx, 100, 11, "  compare two ideas  ")
		createdCh <- createResult{task: created, err: err}
	}()
	testutil.Eventually(t, time.Second, func() bool { return d.deps.Locks.Waiting(100) == 1 },
		func() string { return "CreateTask did not wait for the chat lock" })
	if m.callCount() != 0 {
		t.Fatal("CreateTask called the planner before acquiring the chat lock")
	}
	releaseLock()

	var result createResult
	select {
	case result = <-createdCh:
	case <-time.After(2 * time.Second):
		t.Fatal("CreateTask did not finish after the chat lock was released")
	}
	if err := <-lockDone; err != nil {
		t.Fatalf("chat lock: %v", err)
	}
	if result.err != nil {
		t.Fatalf("CreateTask: %v", result.err)
	}
	created := result.task
	if created.ID <= 0 || created.ChatID != 100 || created.UserID != 11 || created.Goal != "compare two ideas" ||
		created.Model != d.deps.Cfg.StrataModel || created.Status != store.TaskStatusPlanning || created.PlanJSON != "" {
		t.Fatalf("created task=%+v", created)
	}
	waitTaskPlanningSettled(t, d)
	if m.callCount() != 1 {
		t.Fatalf("planner calls=%d", m.callCount())
	}
	planned, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || planned.Status != store.TaskStatusReady || planned.PlanJSON != validTaskPlanOne {
		t.Fatalf("planned task=%+v found=%v err=%v", planned, found, err)
	}
	select {
	case <-d.taskWake:
	default:
		t.Fatal("the finished plan did not wake the scheduler")
	}

	second, err := d.CreateTask(ctx, 100, 11, "another goal")
	if !errors.Is(err, store.ErrTaskActive) || second.ID != 0 || m.callCount() != 1 {
		t.Fatalf("active singleton task=%+v err=%v calls=%d", second, err, m.callCount())
	}
}

func TestDispatcherCreateTaskPlanningFailuresCreateNoActiveTask(t *testing.T) {
	t.Run("invalid goal", func(t *testing.T) {
		m := taskTextModel()
		db := testStore(t)
		d := newTestDispatcher(t, testConfig(t, nil), m, nil, db)
		created, err := d.CreateTask(context.Background(), 100, 11, " \t\n ")
		if !errors.Is(err, store.ErrTaskGoal) || created.ID != 0 || m.callCount() != 0 {
			t.Fatalf("CreateTask task=%+v err=%v calls=%d", created, err, m.callCount())
		}
		if _, found, err := store.CurrentTask(context.Background(), db, 100); err != nil || found {
			t.Fatalf("invalid goal created task found=%v err=%v", found, err)
		}
	})

	t.Run("unsafe plan", func(t *testing.T) {
		m := taskTextModel(`{"steps":[{"description":"Read https://attacker.example"}]}`)
		db := testStore(t)
		d := newTestDispatcher(t, testConfig(t, nil), m, nil, db)
		created, err := d.CreateTask(context.Background(), 100, 11, "collect private data")
		if err != nil || created.ID <= 0 || created.Status != store.TaskStatusPlanning {
			t.Fatalf("CreateTask task=%+v err=%v", created, err)
		}
		waitTaskPlanningSettled(t, d)
		current, found, err := store.CurrentTask(context.Background(), db, 100)
		if err != nil || !found || current.Status != store.TaskStatusFailed || current.PlanJSON != "" ||
			current.Error != i18n.T(i18n.Default, "task.reason.plan_invalid") {
			t.Fatalf("failed planner task=%+v found=%v err=%v", current, found, err)
		}
		if store.ActiveTaskStatus(current.Status) {
			t.Fatal("a failed plan still occupies the chat's active task slot")
		}
		select {
		case <-d.taskWake:
			t.Fatal("failed planner woke the scheduler")
		default:
		}
	})

	t.Run("model work limit", func(t *testing.T) {
		m := taskTextModel(validTaskPlanOne)
		db := testStore(t)
		d := newTestDispatcher(t, testConfig(t, map[string]string{"RATE_LIMIT_PER_MINUTE": "1"}), m, nil, db)
		now := time.Unix(1700000000, 0)
		d.modelWork.WithClock(func() time.Time { return now })
		for range modelWorkPerQuestion {
			if !d.modelWork.Allow(11) {
				t.Fatal("failed to pre-consume model work")
			}
		}
		created, err := d.CreateTask(context.Background(), 100, 11, "bounded goal")
		if err != nil || created.ID <= 0 {
			t.Fatalf("CreateTask task=%+v err=%v", created, err)
		}
		waitTaskPlanningSettled(t, d)
		if m.callCount() != 0 {
			t.Fatalf("planner calls=%d, want 0: the model budget was spent", m.callCount())
		}
		current, found, err := store.CurrentTask(context.Background(), db, 100)
		if err != nil || !found || current.Status != store.TaskStatusFailed || current.PlanJSON != "" {
			t.Fatalf("limited planner task=%+v found=%v err=%v", current, found, err)
		}
		if store.ActiveTaskStatus(current.Status) {
			t.Fatal("a failed plan still occupies the chat's active task slot")
		}
	})
}

func TestTaskPlanningFailureCreatesNoActiveTask(t *testing.T) {
	m := taskTextModel(`{"steps":[{"description":"Read https://attacker.example"}]}`)
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	if err := dispatch(t, d, testEvent(100, 11, 1000, "/task collect private data")); err != nil {
		t.Fatal(err)
	}
	waitTaskPlanningSettled(t, d)
	current, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || current.Status != store.TaskStatusFailed || current.PlanJSON != "" {
		t.Fatalf("failed planner task=%+v found=%v err=%v", current, found, err)
	}
	if store.ActiveTaskStatus(current.Status) {
		t.Fatal("a failed plan still blocks the chat")
	}
	if m.callCount() != 1 {
		t.Fatalf("planner calls=%d", m.callCount())
	}
	if !strings.Contains(r.joined(), "не удалось составить план") {
		t.Fatalf("user did not receive the localized planning failure: %q", r.joined())
	}
}

func TestTaskSingletonSequentialProgressAndSingleFinalHistory(t *testing.T) {
	m := taskTextModel(validTaskPlanTwo, "first result", "second result", "consolidated answer")
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	if err := dispatch(t, d, testEvent(100, 11, 1001, "/task compare two ideas")); err != nil {
		t.Fatal(err)
	}
	waitTaskPlanningSettled(t, d)
	if err := dispatch(t, d, testEvent(100, 11, 1002, "/task another goal")); err != nil {
		t.Fatal(err)
	}
	if m.callCount() != 1 || !strings.Contains(r.joined(), "уже выполняется") {
		t.Fatalf("singleton calls=%d replies=%q", m.callCount(), r.joined())
	}
	processTasks(t, d, 3)
	joined := r.joined()
	for _, progress := range []string{"1/2", "2/2", "Задача #1 завершена (2/2)", "consolidated answer"} {
		if !strings.Contains(joined, progress) {
			t.Errorf("progress/final reply misses %q: %q", progress, joined)
		}
	}
	if strings.Index(joined, "1/2") > strings.Index(joined, "2/2") {
		t.Fatalf("steps were not sequential: %q", joined)
	}
	if m.callCount() != 4 {
		t.Fatalf("model calls=%d, want planner + two steps + synthesis", m.callCount())
	}
	rows, err := store.HistoryOfChat(context.Background(), db, 100, 20)
	if err != nil {
		t.Fatal(err)
	}
	assistants := 0
	for _, row := range rows {
		if row.Role == store.RoleAssistant {
			assistants++
			if row.Content != "consolidated answer" {
				t.Fatalf("assistant row=%+v", row)
			}
		}
	}
	if assistants != 1 {
		t.Fatalf("assistant rows=%d history=%+v", assistants, rows)
	}
	restarted := newTestDispatcher(t, d.deps.Cfg, m, r, db)
	processTasks(t, restarted, 2)
	if m.callCount() != 4 || strings.Count(r.joined(), "consolidated answer") != 1 {
		t.Fatalf("restart duplicated work calls=%d replies=%q", m.callCount(), r.joined())
	}
}

func TestTaskExecutionFailureIsTerminalAndClear(t *testing.T) {
	m := taskTextModel(validTaskPlanOne)
	m.results = append(m.results, taskModelResult{err: errors.New("execution failed safely")})
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	if err := dispatch(t, d, testEvent(100, 11, 1003, "/task execute one step")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 1)
	current, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || current.Status != store.TaskStatusFailed || !strings.Contains(r.joined(), "остановлена") {
		t.Fatalf("failed task=%+v found=%v err=%v replies=%q", current, found, err, r.joined())
	}
	processTasks(t, d, 2)
	if m.callCount() != 2 {
		t.Fatalf("failed task retried execution calls=%d", m.callCount())
	}
	questionID, _, err := store.LastVisibleMessage(context.Background(), db, 100)
	if err != nil {
		t.Fatal(err)
	}
	skipped, suppressed, err := store.ResumeSkip(context.Background(), db, 100)
	if err != nil || !suppressed || skipped != questionID {
		t.Fatalf("task goal generic recovery skip=%d suppressed=%v question=%d err=%v", skipped, suppressed, questionID, err)
	}
}

func TestTaskModelLimitExhaustionDoesNotCompleteStep(t *testing.T) {
	for _, tc := range []struct {
		name       string
		raw        map[string]string
		want       string
		preConsume bool
	}{
		{name: "per minute", raw: map[string]string{"RATE_LIMIT_PER_MINUTE": "1"}, want: modelWorkBudgetErrorText, preConsume: true},
		{name: "daily", raw: map[string]string{"USER_DAILY_MODEL_LIMIT": "2"}, want: modelDailyLimitErrorText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}
			for key, value := range tc.raw {
				raw[key] = value
			}
			m := taskTextModel(validTaskPlanTwo, "first result")
			r := &fakeReplier{}
			db := testStore(t)
			d := newTestDispatcher(t, testConfig(t, raw), m, r, db)
			now := time.Unix(1700000000, 0)
			d.modelWork.WithClock(func() time.Time { return now })
			if tc.preConsume && !d.modelWork.Allow(11) {
				t.Fatal("failed to pre-consume model work")
			}
			if err := dispatch(t, d, testEvent(100, 11, 1008, "/task compare two ideas")); err != nil {
				t.Fatal(err)
			}
			processTasks(t, d, 2)

			current, found, err := store.CurrentTask(context.Background(), db, 100)
			if err != nil || !found {
				t.Fatalf("CurrentTask found=%v err=%v", found, err)
			}
			if current.Status != store.TaskStatusFailed || current.NextStep != 1 || strings.Join(taskResultContents(current), "|") != "first result" {
				t.Fatalf("limit exhaustion changed step results: %+v", current)
			}
			if m.callCount() != 2 {
				t.Fatalf("provider calls=%d, want planner and first step only", m.callCount())
			}
			if !strings.Contains(r.joined(), tc.want) {
				t.Fatalf("user did not receive friendly limit notice %q: %q", tc.want, r.joined())
			}
			rows, err := store.HistoryOfChat(context.Background(), db, 100, 10)
			if err != nil || len(rows) != 1 || rows[0].Role != store.RoleUser {
				t.Fatalf("limit notice persisted in assistant history: rows=%+v err=%v", rows, err)
			}
		})
	}
}

func TestTaskNetworkApprovalResumeAfterRestartAndCancellation(t *testing.T) {
	t.Run("resume after restart", func(t *testing.T) {
		var providerCalls atomic.Int64
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			providerCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"title":"Result","url":"https://example.com","content":"Content"}]}`))
		}))
		defer provider.Close()
		call := model.ToolCall{ID: "task-search", Name: "web_search", Arguments: `{"query":"bounded task news"}`}
		m := &taskScriptModel{results: []taskModelResult{
			{response: model.Response{Content: validTaskPlanOne}},
			{response: model.Response{ToolCalls: []model.ToolCall{call}}},
			{response: model.Response{Content: "approved step result"}},
			{response: model.Response{Content: "approved consolidated answer"}},
		}}
		r := &networkMenuReplier{}
		db := testStore(t)
		cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": provider.URL})
		first := newTestDispatcher(t, cfg, m, r, db)
		if err := dispatch(t, first, testEvent(100, 11, 1004, "/task prepare a project overview")); err != nil {
			t.Fatal(err)
		}
		processTasks(t, first, 1)
		confirm, _ := latestNetworkCallbacks(t, r)
		if providerCalls.Load() != 0 {
			t.Fatal("network ran before approval")
		}
		second := newTestDispatcher(t, cfg, m, r, db)
		if err := dispatchMenuData(t, second, 100, 11, 700, confirm); err != nil {
			t.Fatal(err)
		}
		processTasks(t, second, 1)
		if providerCalls.Load() != 1 || m.callCount() != 4 || !strings.Contains(r.joined(), "approved consolidated answer") {
			t.Fatalf("resume provider=%d calls=%d replies=%q", providerCalls.Load(), m.callCount(), r.joined())
		}
		if err := dispatchMenuData(t, second, 100, 11, 700, confirm); err != nil {
			t.Fatal(err)
		}
		processTasks(t, second, 1)
		if providerCalls.Load() != 1 || m.callCount() != 4 {
			t.Fatalf("replayed approval duplicated work provider=%d calls=%d", providerCalls.Load(), m.callCount())
		}
		rows, err := store.HistoryOfChat(context.Background(), db, 100, 10)
		if err != nil || len(rows) != 2 || rows[1].Content != "approved consolidated answer" {
			t.Fatalf("history=%+v err=%v", rows, err)
		}
	})

	t.Run("cancel pending approval", func(t *testing.T) {
		call := model.ToolCall{ID: "cancel-task-search", Name: "web_search", Arguments: `{"query":"must not run"}`}
		m := &taskScriptModel{results: []taskModelResult{
			{response: model.Response{Content: validTaskPlanOne}},
			{response: model.Response{ToolCalls: []model.ToolCall{call}}},
		}}
		r := &networkMenuReplier{}
		db := testStore(t)
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[]}`))
		}))
		defer provider.Close()
		cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": provider.URL})
		d := newTestDispatcher(t, cfg, m, r, db)
		if err := dispatch(t, d, testEvent(100, 11, 1005, "/task wait for approval")); err != nil {
			t.Fatal(err)
		}
		processTasks(t, d, 1)
		confirm, _ := latestNetworkCallbacks(t, r)
		if err := dispatch(t, d, testEvent(100, 11, 1006, "/task cancel")); err != nil {
			t.Fatal(err)
		}
		if err := dispatchMenuData(t, d, 100, 11, 700, confirm); err != nil {
			t.Fatal(err)
		}
		processTasks(t, d, 2)
		current, _, _ := store.CurrentTask(context.Background(), db, 100)
		if current.Status != store.TaskStatusCancelled || m.callCount() != 2 || d.networkWork.Count(11) != 0 {
			t.Fatalf("cancel task=%+v calls=%d network=%d", current, m.callCount(), d.networkWork.Count(11))
		}
	})
}

func TestTaskMenuShowsProgressCancelAndBack(t *testing.T) {
	m := taskTextModel(validTaskPlanOne, validTaskPlanOne)
	r := &menuEditReplier{}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, testStore(t))
	if err := dispatch(t, d, testEvent(100, 11, 1007, "/task menu goal")); err != nil {
		t.Fatal(err)
	}
	waitTaskPlanningSettled(t, d)
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:task"); err != nil {
		t.Fatal(err)
	}
	edit := r.lastEdit()
	if !strings.Contains(edit.text, "0/1") || !hasMenuCallback(edit.markup, "menu:task:cancel") || hasMenuCallback(edit.markup, "menu:task:new") || !hasMenuCallback(edit.markup, "menu:more") {
		t.Fatalf("active task menu=%+v", edit)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:task:cancel"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "отменена") || hasMenuCallback(r.lastEdit().markup, "menu:task:cancel") || !hasMenuCallback(r.lastEdit().markup, "menu:task:new") || !hasMenuCallback(r.lastEdit().markup, "menu:more") {
		t.Fatalf("cancelled task menu=%+v", r.lastEdit())
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:task:new"); err != nil {
		t.Fatal(err)
	}
	pending, consumed := d.pendingEvent(testEvent(100, 11, 1009, "fresh menu goal"))
	if !consumed || pending.Text != "/task fresh menu goal" {
		t.Fatalf("new task input=%q consumed=%v", pending.Text, consumed)
	}
	if err := dispatch(t, d, pending); err != nil {
		t.Fatal(err)
	}
	waitTaskPlanningSettled(t, d)
	current, found, err := store.CurrentTask(context.Background(), d.deps.DB, 100)
	if err != nil || !found || current.Status != store.TaskStatusReady || current.Goal != "fresh menu goal" || current.ID == 1 {
		t.Fatalf("new menu task=%+v found=%v err=%v", current, found, err)
	}
}

func TestTaskTerminalMenusOfferNewTaskWithoutCancel(t *testing.T) {
	for _, status := range []string{store.TaskStatusCompleted, store.TaskStatusFailed, store.TaskStatusCancelled} {
		markup := taskMenuMarkup(store.Task{ID: 9, Status: status})
		if !hasMenuCallback(markup, "menu:task:new") || hasMenuCallback(markup, "menu:task:cancel") || !hasMenuCallback(markup, "menu:more") {
			t.Errorf("terminal %s markup=%+v", status, markup)
		}
	}
}

func TestTaskInvalidPersistedApprovalFailsClosed(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	r := &fakeReplier{}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, db)
	if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	created, err := store.CreateTask(ctx, db, store.Task{ChatID: 100, UserID: 11, Goal: "bounded goal", Model: d.deps.Cfg.StrataModel,
		PlanJSON: validTaskPlanOne, DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	token := "0123456789abcdef0123456789abcdef"
	saved, err := store.SaveTaskApproval(ctx, db, claimed, token, 700, now.Add(time.Minute), `{"request":{}}`, now)
	if err != nil || !saved {
		t.Fatalf("SaveTaskApproval saved=%v err=%v", saved, err)
	}
	processTasks(t, d, 1)
	current, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || current.ID != created.ID || current.Status != store.TaskStatusFailed || !strings.Contains(r.joined(), "остановлена") {
		t.Fatalf("invalid approval task=%+v found=%v err=%v replies=%q", current, found, err, r.joined())
	}
}

func TestTaskRestartAfterConsumedApprovalRequiresFreshExactApproval(t *testing.T) {
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		providerCalls.Add(1)
		if got := req.URL.Query().Get("q"); got != "never replay" {
			t.Errorf("provider query=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer provider.Close()
	call := model.ToolCall{ID: "consumed-before-crash", Name: "web_search", Arguments: `{"query":"never replay"}`}
	m := &taskScriptModel{results: []taskModelResult{
		{response: model.Response{Content: validTaskPlanOne}},
		{response: model.Response{ToolCalls: []model.ToolCall{call}}},
		{response: model.Response{Content: "freshly approved step"}},
		{response: model.Response{Content: "freshly approved final"}},
	}}
	r := &networkMenuReplier{}
	db := testStore(t)
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": provider.URL})
	first := newTestDispatcher(t, cfg, m, r, db)
	if err := dispatch(t, first, testEvent(100, 11, 1008, "/task consume then stop")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, first, 1)
	pending, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || pending.Status != store.TaskStatusAwaitingApproval {
		t.Fatalf("pending=%+v found=%v err=%v", pending, found, err)
	}
	oldToken := pending.ApprovalToken
	claimed, ok, err := store.ConsumeTaskApproval(context.Background(), db, pending.ID, 100, 11, pending.ApprovalMessageID, oldToken, time.Now(), first.taskLease())
	if err != nil || !ok {
		t.Fatalf("ConsumeTaskApproval ok=%v err=%v", ok, err)
	}
	second := newTestDispatcher(t, cfg, m, r, db)
	if _, err := second.ProcessDueTasks(context.Background(), time.Now().Add(first.taskLease()+time.Second), TaskBatchLimit); err != nil {
		t.Fatal(err)
	}
	current, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || current.Status != store.TaskStatusAwaitingApproval || current.ApprovalToken == "" || current.ApprovalToken == oldToken || providerCalls.Load() != 0 || m.callCount() != 2 {
		t.Fatalf("recovered task=%+v found=%v err=%v provider=%d calls=%d", current, found, err, providerCalls.Load(), m.callCount())
	}
	if claimed.ContinuationJSON == "" || !strings.Contains(r.joined(), "Требуется новое подтверждение") {
		t.Fatalf("missing continuation or fresh approval notice: %q", r.joined())
	}
	freshConfirm, _ := latestNetworkCallbacks(t, r)
	if strings.TrimPrefix(freshConfirm, "menu:network:confirm:") != current.ApprovalToken {
		t.Fatal("fresh prompt does not match the persisted exact-action token")
	}
	if err := dispatchMenuData(t, second, 100, 11, 700, "menu:network:confirm:"+oldToken); err != nil {
		t.Fatal(err)
	}
	if providerCalls.Load() != 0 || m.callCount() != 2 {
		t.Fatal("consumed pre-restart approval executed after recovery")
	}
	if err := dispatchMenuData(t, second, 100, 11, 700, freshConfirm); err != nil {
		t.Fatal(err)
	}
	processTasks(t, second, 1)
	if providerCalls.Load() != 1 || m.callCount() != 4 || !strings.Contains(r.joined(), "freshly approved final") {
		t.Fatalf("fresh approval did not resume provider=%d calls=%d replies=%q", providerCalls.Load(), m.callCount(), r.joined())
	}
	if err := dispatchMenuData(t, second, 100, 11, 700, freshConfirm); err != nil {
		t.Fatal(err)
	}
	if providerCalls.Load() != 1 || m.callCount() != 4 {
		t.Fatal("fresh approval was replayable")
	}
}

func TestTaskResumeCommandPreservesProgressAndRejectsWrongChatAndStatus(t *testing.T) {
	m := taskTextModel("second result", "consolidated answer")
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	ctx := context.Background()
	if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	created, err := store.CreateTask(ctx, db, store.Task{ChatID: 100, UserID: 11, Goal: "preserved goal", Model: "selected-model",
		PlanJSON: validTaskPlanTwo, DueAt: now, ExpiresAt: now.Add(TaskLifetime), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	resumeCommand := fmt.Sprintf("/task resume %d", created.ID)
	if err := dispatch(t, d, testEvent(100, 11, 1100, resumeCommand)); err != nil {
		t.Fatal(err)
	}
	current, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || current.Status != store.TaskStatusReady || m.callCount() != 0 {
		t.Fatalf("active task resume task=%+v found=%v err=%v calls=%d", current, found, err, m.callCount())
	}
	claimed, found, err := store.ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if ok, err := store.CompleteTaskStep(ctx, db, claimed, "first result", now); err != nil || !ok {
		t.Fatalf("first step ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.CancelTask(ctx, db, 100, now); err != nil || !ok {
		t.Fatalf("cancel ok=%v err=%v", ok, err)
	}
	if err := dispatch(t, d, testEvent(200, 11, 1101, resumeCommand)); err != nil {
		t.Fatal(err)
	}
	current, _, _ = store.CurrentTask(ctx, db, 100)
	if current.Status != store.TaskStatusCancelled {
		t.Fatalf("wrong-chat resume changed task: %+v", current)
	}
	if err := dispatch(t, d, testEvent(100, 11, 1102, resumeCommand)); err != nil {
		t.Fatal(err)
	}
	resumed, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || resumed.ID != created.ID || resumed.Status != store.TaskStatusReady || resumed.NextStep != 1 || strings.Join(taskResultContents(resumed), "|") != "first result" || resumed.Attempts != 0 {
		t.Fatalf("resumed=%+v found=%v err=%v", resumed, found, err)
	}
	processTasks(t, d, 2)
	current, found, err = store.CurrentTask(ctx, db, 100)
	if err != nil || !found || current.Status != store.TaskStatusCompleted || current.NextStep != 2 || strings.Join(taskResultContents(current), "|") != "first result|second result" {
		t.Fatalf("completed resumed task=%+v found=%v err=%v", current, found, err)
	}
	if m.callCount() != 2 || !strings.Contains(m.requests[0].Messages[len(m.requests[0].Messages)-1].Content, "Current step 2/2") {
		t.Fatalf("resume did not start at unfinished step: calls=%d requests=%+v", m.callCount(), m.requests)
	}
	if err := dispatch(t, d, testEvent(100, 11, 1103, resumeCommand)); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 2)
	if m.callCount() != 2 {
		t.Fatalf("completed task was resumed or duplicated: calls=%d", m.callCount())
	}
	rows, err := store.HistoryOfChat(ctx, db, 100, 20)
	if err != nil {
		t.Fatal(err)
	}
	assistants := 0
	for _, row := range rows {
		if row.Role == store.RoleAssistant {
			assistants++
			if row.Content != "consolidated answer" {
				t.Fatalf("assistant row=%+v", row)
			}
		}
	}
	if assistants != 1 {
		t.Fatalf("assistant rows=%d history=%+v", assistants, rows)
	}
}

func TestTaskResumeMenuIsLimitedToRetryableTerminalTasks(t *testing.T) {
	for _, status := range []string{store.TaskStatusCompleted, store.TaskStatusFailed, store.TaskStatusCancelled} {
		markup := taskMenuMarkup(store.Task{ID: 9, Status: status, PlanJSON: validTaskPlanOne})
		resumable := status != store.TaskStatusCompleted
		if hasMenuCallback(markup, "menu:task:resume") != resumable {
			t.Errorf("terminal %s resume markup=%+v", status, markup)
		}
	}
	// A failed PLANNING row has no plan to resume; its retry is the planner.
	planless := taskMenuMarkup(store.Task{ID: 9, Status: store.TaskStatusFailed})
	if hasMenuCallback(planless, "menu:task:resume") || !hasMenuCallback(planless, "menu:task:replan") {
		t.Fatalf("planless failed task markup=%+v", planless)
	}
	if text, ok := menuCallbackText("menu:task:replan"); !ok || text != "/task menu:replan" {
		t.Fatalf("replan callback text=%q ok=%v", text, ok)
	}
	if text, ok := menuCallbackText("menu:task:resume"); !ok || text != "/task menu:resume" {
		t.Fatalf("resume callback text=%q ok=%v", text, ok)
	}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, &menuEditReplier{}, testStore(t))
	if _, ok := d.callbackText("menu:task:resume:9"); ok {
		t.Fatal("callback with an arbitrary task id was accepted")
	}
	ctx := context.Background()
	if err := store.EnsureChat(ctx, d.deps.DB, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	created, err := store.CreateTask(ctx, d.deps.DB, store.Task{ChatID: 100, UserID: 11, Goal: "menu resume", Model: "m",
		PlanJSON: validTaskPlanOne, DueAt: now, ExpiresAt: now.Add(TaskLifetime), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.CancelTask(ctx, d.deps.DB, 100, now); err != nil || !ok {
		t.Fatalf("cancel ok=%v err=%v", ok, err)
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:task:resume"); err != nil {
		t.Fatal(err)
	}
	current, found, err := store.CurrentTask(ctx, d.deps.DB, 100)
	if err != nil || !found || current.ID != created.ID || current.Status != store.TaskStatusReady {
		t.Fatalf("menu resumed task=%+v found=%v err=%v", current, found, err)
	}
}

func TestTaskResumeRequiresFreshExactApproval(t *testing.T) {
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer provider.Close()
	call := model.ToolCall{ID: "resume-task-search", Name: "web_search", Arguments: `{"query":"fresh approval required"}`}
	m := &taskScriptModel{results: []taskModelResult{
		{response: model.Response{Content: validTaskPlanOne}},
		{response: model.Response{ToolCalls: []model.ToolCall{call}}},
		{response: model.Response{ToolCalls: []model.ToolCall{call}}},
		{response: model.Response{Content: "approved step result"}},
		{response: model.Response{Content: "approved final answer"}},
	}}
	r := &networkMenuReplier{}
	db := testStore(t)
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": provider.URL})
	d := newTestDispatcher(t, cfg, m, r, db)
	if err := dispatch(t, d, testEvent(100, 11, 1104, "/task network step needs approval")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 1)
	pending, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || pending.Status != store.TaskStatusAwaitingApproval {
		t.Fatalf("pending=%+v found=%v err=%v", pending, found, err)
	}
	oldToken := pending.ApprovalToken
	if err := dispatch(t, d, testEvent(100, 11, 1105, "/task cancel")); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 1106, fmt.Sprintf("/task resume %d", pending.ID))); err != nil {
		t.Fatal(err)
	}
	resumed, _, _ := store.CurrentTask(context.Background(), db, 100)
	if resumed.Status != store.TaskStatusReady || resumed.ContinuationJSON != "" || resumed.ApprovalToken != "" {
		t.Fatalf("resume retained approval state: %+v", resumed)
	}
	processTasks(t, d, 1)
	fresh, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || fresh.Status != store.TaskStatusAwaitingApproval || fresh.ApprovalToken == "" || fresh.ApprovalToken == oldToken || fresh.ContinuationJSON == "" {
		t.Fatalf("fresh approval task=%+v found=%v err=%v", fresh, found, err)
	}
	if providerCalls.Load() != 0 || m.callCount() != 3 {
		t.Fatalf("resume replayed network action provider=%d calls=%d", providerCalls.Load(), m.callCount())
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:network:confirm:"+oldToken); err != nil {
		t.Fatal(err)
	}
	if providerCalls.Load() != 0 || m.callCount() != 3 {
		t.Fatal("old approval executed after resume")
	}
	freshConfirm, _ := latestNetworkCallbacks(t, r)
	if strings.TrimPrefix(freshConfirm, "menu:network:confirm:") != fresh.ApprovalToken {
		t.Fatal("fresh prompt does not match the new exact-action token")
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, freshConfirm); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 2)
	if providerCalls.Load() != 1 || m.callCount() != 5 || !strings.Contains(r.joined(), "approved final answer") {
		t.Fatalf("fresh approval did not finish provider=%d calls=%d replies=%q", providerCalls.Load(), m.callCount(), r.joined())
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, freshConfirm); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 1)
	if providerCalls.Load() != 1 || m.callCount() != 5 {
		t.Fatal("fresh approval was replayable")
	}
}

type taskLifecycleFixture struct {
	d                  *Dispatcher
	r                  *networkMenuReplier
	task               store.Task
	other              store.Task
	state              *toolLoopContinuation
	call               model.ToolCall
	scope              networkApprovalScope
	otherScope         networkApprovalScope
	otherApprovalToken string
}

func newTaskLifecycleFixture(t *testing.T, awaitingApproval bool) taskLifecycleFixture {
	t.Helper()
	ctx := context.Background()
	r := &networkMenuReplier{}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, testStore(t))
	for _, chatID := range []int64{100, 200} {
		if err := store.EnsureChat(ctx, d.deps.DB, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	created, err := store.CreateTask(ctx, d.deps.DB, store.Task{ChatID: 100, UserID: 11, Goal: "lifecycle", Model: "m",
		PlanJSON: validTaskPlanOne, DueAt: now, ExpiresAt: now.Add(TaskLifetime), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateTask(ctx, d.deps.DB, store.Task{ChatID: 200, UserID: 11, Goal: "unrelated", Model: "m",
		PlanJSON: validTaskPlanOne, DueAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	call := model.ToolCall{ID: "lifecycle-search", Name: "web_search", Arguments: `{"query":"lifecycle"}`}
	state := &toolLoopContinuation{userID: 11, metadata: toolLoopMetadata{event: Event{ChatID: 100, UserID: 11}, taskID: created.ID}}
	otherState := &toolLoopContinuation{userID: 11, metadata: toolLoopMetadata{event: Event{ChatID: 200, UserID: 11}, taskID: other.ID}}
	if !d.grantNetworkScope(state, call) || !d.grantNetworkScope(otherState, call) {
		t.Fatal("failed to seed network scopes")
	}
	scope, _ := scopeForNetworkCall(state, call)
	otherScope, _ := scopeForNetworkCall(otherState, call)
	token := "0123456789abcdef0123456789abcdef"
	if awaitingApproval {
		claimed, found, err := store.ClaimNextTask(ctx, d.deps.DB, now, time.Minute)
		if err != nil || !found {
			t.Fatalf("ClaimNextTask found=%v err=%v", found, err)
		}
		if saved, err := store.SaveTaskApproval(ctx, d.deps.DB, claimed, token, 700, now.Add(time.Minute), `{"request":{}}`, now); err != nil || !saved {
			t.Fatalf("SaveTaskApproval saved=%v err=%v", saved, err)
		}
	} else {
		if _, ok, err := store.CancelTask(ctx, d.deps.DB, 100, now); err != nil || !ok {
			t.Fatalf("CancelTask ok=%v err=%v", ok, err)
		}
		if _, err := d.deps.DB.ExecContext(ctx, `UPDATE tasks SET approval_token = ?, approval_message_id = 700, continuation_json = '{"request":{}}' WHERE id = ?`, token, created.ID); err != nil {
			t.Fatal(err)
		}
	}
	current, found, err := store.CurrentTask(ctx, d.deps.DB, 100)
	if err != nil || !found {
		t.Fatalf("CurrentTask found=%v err=%v", found, err)
	}
	otherToken := "fedcba9876543210fedcba9876543210"
	set := networkConfirmationsFor(d)
	set.mu.Lock()
	set.pending[token] = networkConfirmation{ChatID: 100, UserID: 11, MessageID: 700, continuation: state}
	set.pending[otherToken] = networkConfirmation{ChatID: 200, UserID: 11, MessageID: 900, continuation: otherState}
	set.mu.Unlock()
	return taskLifecycleFixture{d: d, r: r, task: current, other: other, state: state, call: call, scope: scope, otherScope: otherScope, otherApprovalToken: otherToken}
}

func taskLifecycleScopePresent(d *Dispatcher, scope networkApprovalScope) bool {
	set := networkApprovalScopesFor(d)
	set.mu.Lock()
	defer set.mu.Unlock()
	return set.scopes[scope]
}

func taskLifecycleConfirmationPresent(d *Dispatcher, token string) bool {
	set := networkConfirmationsFor(d)
	set.mu.Lock()
	defer set.mu.Unlock()
	_, found := set.pending[token]
	return found
}

func TestDispatcherTaskLifecycleCleansOnlySuccessfulTransitions(t *testing.T) {
	t.Run("cancel resume and clear", func(t *testing.T) {
		fixture := newTaskLifecycleFixture(t, true)
		d := fixture.d
		cancelled, ok, err := d.CancelTask(context.Background(), 100)
		if err != nil || !ok || cancelled.Status != store.TaskStatusCancelled {
			t.Fatalf("CancelTask task=%+v ok=%v err=%v", cancelled, ok, err)
		}
		if taskLifecycleScopePresent(d, fixture.scope) || taskLifecycleConfirmationPresent(d, fixture.task.ApprovalToken) {
			t.Fatal("cancel retained the target scope or pending continuation")
		}
		if !taskLifecycleScopePresent(d, fixture.otherScope) || !taskLifecycleConfirmationPresent(d, fixture.otherApprovalToken) {
			t.Fatal("cancel cleared another chat")
		}
		if len(fixture.r.removed) != 1 || fixture.r.removed[0] != 700 {
			t.Fatalf("cancel removed keyboards=%v", fixture.r.removed)
		}
		if !d.grantNetworkScope(fixture.state, fixture.call) {
			t.Fatal("failed to seed a stale pre-resume grant")
		}
		resumed, ok, err := d.ResumeTask(context.Background(), 100, fixture.task.ID)
		if err != nil || !ok || resumed.Status != store.TaskStatusReady {
			t.Fatalf("ResumeTask task=%+v ok=%v err=%v", resumed, ok, err)
		}
		if taskLifecycleScopePresent(d, fixture.scope) {
			t.Fatal("resume reactivated the stale scoped grant")
		}
		select {
		case <-d.taskWake:
		default:
			t.Fatal("resume did not wake the task scheduler")
		}
	})

	t.Run("clear", func(t *testing.T) {
		fixture := newTaskLifecycleFixture(t, true)
		deleted, err := fixture.d.ClearTasks(context.Background(), 100)
		if err != nil || deleted != 1 {
			t.Fatalf("ClearTasks deleted=%d err=%v", deleted, err)
		}
		if _, found, err := store.CurrentTask(context.Background(), fixture.d.deps.DB, 100); err != nil || found {
			t.Fatalf("cleared task found=%v err=%v", found, err)
		}
		if taskLifecycleScopePresent(fixture.d, fixture.scope) || taskLifecycleConfirmationPresent(fixture.d, fixture.task.ApprovalToken) {
			t.Fatal("clear retained task approval state")
		}
		if !taskLifecycleScopePresent(fixture.d, fixture.otherScope) || !taskLifecycleConfirmationPresent(fixture.d, fixture.otherApprovalToken) {
			t.Fatal("clear changed another chat")
		}
		if len(fixture.r.removed) != 1 || fixture.r.removed[0] != 700 {
			t.Fatalf("clear removed keyboards=%v", fixture.r.removed)
		}
		if current, found, err := store.CurrentTask(context.Background(), fixture.d.deps.DB, 200); err != nil || !found || current.ID != fixture.other.ID {
			t.Fatalf("other task=%+v found=%v err=%v", current, found, err)
		}
	})
}

func TestDispatcherTaskLifecycleStoreFailuresPreserveUnrelatedState(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		fixture := newTaskLifecycleFixture(t, true)
		if _, err := fixture.d.deps.DB.ExecContext(context.Background(), fmt.Sprintf(`CREATE TRIGGER fail_task_cancel BEFORE UPDATE OF status ON tasks WHEN OLD.id = %d AND NEW.status = 'cancelled' BEGIN SELECT RAISE(FAIL, 'forced cancel failure'); END`, fixture.task.ID)); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := fixture.d.CancelTask(context.Background(), 100); err == nil || ok {
			t.Fatalf("CancelTask ok=%v err=%v", ok, err)
		}
		if !taskLifecycleScopePresent(fixture.d, fixture.scope) || !taskLifecycleScopePresent(fixture.d, fixture.otherScope) ||
			!taskLifecycleConfirmationPresent(fixture.d, fixture.task.ApprovalToken) || !taskLifecycleConfirmationPresent(fixture.d, fixture.otherApprovalToken) {
			t.Fatal("failed cancel cleared approval state")
		}
		if len(fixture.r.removed) != 0 {
			t.Fatalf("failed cancel removed keyboards=%v", fixture.r.removed)
		}
	})

	t.Run("resume clears only the target grant before the failed transition", func(t *testing.T) {
		fixture := newTaskLifecycleFixture(t, false)
		if _, err := fixture.d.deps.DB.ExecContext(context.Background(), fmt.Sprintf(`CREATE TRIGGER fail_task_resume BEFORE UPDATE OF status ON tasks WHEN OLD.id = %d AND NEW.status = 'ready' BEGIN SELECT RAISE(FAIL, 'forced resume failure'); END`, fixture.task.ID)); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := fixture.d.ResumeTask(context.Background(), 100, fixture.task.ID); err == nil || ok {
			t.Fatalf("ResumeTask ok=%v err=%v", ok, err)
		}
		if taskLifecycleScopePresent(fixture.d, fixture.scope) {
			t.Fatal("failed resume retained the target scoped grant")
		}
		if !taskLifecycleScopePresent(fixture.d, fixture.otherScope) || !taskLifecycleConfirmationPresent(fixture.d, fixture.otherApprovalToken) {
			t.Fatal("failed resume cleared another chat")
		}
		if !taskLifecycleConfirmationPresent(fixture.d, fixture.task.ApprovalToken) || len(fixture.r.removed) != 0 {
			t.Fatal("failed resume discarded pending approval data or keyboard")
		}
		select {
		case <-fixture.d.taskWake:
			t.Fatal("failed resume woke the scheduler")
		default:
		}
	})

	t.Run("clear", func(t *testing.T) {
		fixture := newTaskLifecycleFixture(t, true)
		if _, err := fixture.d.deps.DB.ExecContext(context.Background(), `CREATE TRIGGER fail_task_clear BEFORE DELETE ON tasks WHEN OLD.chat_id = 100 BEGIN SELECT RAISE(FAIL, 'forced clear failure'); END`); err != nil {
			t.Fatal(err)
		}
		if deleted, err := fixture.d.ClearTasks(context.Background(), 100); err == nil || deleted != 0 {
			t.Fatalf("ClearTasks deleted=%d err=%v", deleted, err)
		}
		if !taskLifecycleScopePresent(fixture.d, fixture.scope) || !taskLifecycleScopePresent(fixture.d, fixture.otherScope) ||
			!taskLifecycleConfirmationPresent(fixture.d, fixture.task.ApprovalToken) || !taskLifecycleConfirmationPresent(fixture.d, fixture.otherApprovalToken) {
			t.Fatal("failed clear removed approval state")
		}
		if len(fixture.r.removed) != 0 {
			t.Fatalf("failed clear removed keyboards=%v", fixture.r.removed)
		}
		if current, found, err := store.CurrentTask(context.Background(), fixture.d.deps.DB, 100); err != nil || !found || current.Status != store.TaskStatusAwaitingApproval {
			t.Fatalf("task after failed clear=%+v found=%v err=%v", current, found, err)
		}
	})
}
