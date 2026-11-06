package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bot/internal/model"
	"bot/internal/store"
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

func processTasks(t *testing.T, d *Dispatcher, passes int) {
	t.Helper()
	for index := 0; index < passes; index++ {
		if _, err := d.ProcessDueTasks(context.Background(), time.Now(), TaskBatchLimit); err != nil {
			t.Fatalf("ProcessDueTasks pass %d: %v", index+1, err)
		}
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

func TestTaskPlanningFailureCreatesNoActiveTask(t *testing.T) {
	m := taskTextModel(`{"steps":[{"description":"Read https://attacker.example"}]}`)
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)
	if err := dispatch(t, d, testEvent(100, 11, 1000, "/task collect private data")); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.CurrentTask(context.Background(), db, 100); err != nil || found {
		t.Fatalf("failed planner created task found=%v err=%v", found, err)
	}
	if m.callCount() != 1 {
		t.Fatalf("planner calls=%d", m.callCount())
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
	if _, err := second.ProcessDueTasks(context.Background(), time.Now().Add(2*time.Minute), TaskBatchLimit); err != nil {
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
