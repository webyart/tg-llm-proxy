package bot

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bot/internal/metrics"
	"bot/internal/model"
	"bot/internal/store"
	"bot/internal/testutil"
)

// recordingPlanModel records the goal of every planning call (the user message of the plan prompt) while
// answering with one valid plan. Two models on two dispatchers therefore prove which chat each worker served,
// and one model on one dispatcher counts every chat of a fairness round.
type recordingPlanModel struct {
	mu    sync.Mutex
	plan  string
	delay time.Duration
	goals []string
}

func (m *recordingPlanModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	var goal string
	for _, message := range req.Messages {
		if message.Role == model.RoleUser {
			goal = message.Content
		}
	}
	m.mu.Lock()
	m.goals = append(m.goals, goal)
	m.mu.Unlock()
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
	return model.Response{Content: m.plan, Model: "selected-model", Attempts: 1}, nil
}

func (m *recordingPlanModel) Models(context.Context) ([]string, error) { return nil, nil }

func (m *recordingPlanModel) recordedGoals() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.goals...)
}

// planningTaskForChat creates one planning row with a goal that identifies the chat in the recorded plan
// calls. It does not wake a planner: the test starts the workers itself.
func planningTaskForChat(t *testing.T, db *sql.DB, chatID int64, goal string) store.Task {
	t.Helper()
	ctx := context.Background()
	if err := store.EnsureChat(ctx, db, chatID, 11, ""); err != nil {
		t.Fatalf("EnsureChat(%d): %v", chatID, err)
	}
	now := time.Now().UTC()
	created, err := store.CreatePlanningTask(ctx, db, store.Task{
		ChatID: chatID, UserID: 11, Goal: goal, Model: "m",
		DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreatePlanningTask(chat %d): %v", chatID, err)
	}
	return created
}

// waitTaskReady waits until the latest task of the chat has a stored plan and is ready.
func waitTaskReady(t *testing.T, db *sql.DB, chatID int64) store.Task {
	t.Helper()
	var ready store.Task
	testutil.Eventually(t, 5*time.Second, func() bool {
		task, found, err := store.CurrentTask(context.Background(), db, chatID)
		if err != nil || !found || task.Status != store.TaskStatusReady {
			return false
		}
		ready = task
		return true
	}, func() string { return fmt.Sprintf("chat %d did not reach a ready plan", chatID) })
	return ready
}

// TestTaskPlanningWorkerClaimsItsOwnChatFirst is the per-chat preference: the worker woken for the newer row
// of chat B claims it even though chat A's older row is still free. Before ClaimPlanningTaskByID the oldest
// row won and the woken chat waited behind another chat.
func TestTaskPlanningWorkerClaimsItsOwnChatFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "planning-preference.db")
	firstDB := openPlanningInstance(t, path)
	secondDB := openPlanningInstance(t, path)
	older := planningTaskForChat(t, firstDB, 100, "goal of chat A")
	newer := planningTaskForChat(t, firstDB, 101, "goal of chat B")
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	firstModel := &recordingPlanModel{plan: validTaskPlanOne}
	secondModel := &recordingPlanModel{plan: validTaskPlanOne}
	first := newTestDispatcher(t, cfg, firstModel, &fakeReplier{}, firstDB)
	second := newTestDispatcher(t, cfg, secondModel, &fakeReplier{}, secondDB)

	// The worker for the newer row starts first: strict FIFO would hand it chat A's older row.
	first.startTaskPlanning(newer)
	second.startTaskPlanning(older)
	waitTaskReady(t, firstDB, 101)
	waitTaskReady(t, firstDB, 100)
	waitTaskPlanningIdle(t, first)
	waitTaskPlanningIdle(t, second)

	if goals := firstModel.recordedGoals(); len(goals) != 1 || goals[0] != "goal of chat B" {
		t.Fatalf("the worker woken for chat B planned %v, want only goal of chat B", goals)
	}
	if goals := secondModel.recordedGoals(); len(goals) != 1 || goals[0] != "goal of chat A" {
		t.Fatalf("the worker woken for chat A planned %v, want only goal of chat A", goals)
	}
}

// TestTaskPlanningWorkerFallsBackWhenItsOwnRowIsLeased: when the preferred row is leased by another instance
// the worker does not idle — it serves the least-recently-updated free row of the queue. Once the preferred
// lease is released, a worker woken for it plans it.
func TestTaskPlanningWorkerFallsBackWhenItsOwnRowIsLeased(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	leased := planningTaskForChat(t, db, 100, "goal of leased chat")
	waiting := planningTaskForChat(t, db, 101, "goal of waiting chat")
	now := time.Now().UTC()
	if claimed, found, err := store.ClaimPlanningTaskByID(ctx, db, leased.ID, now, "instance-other", time.Minute); err != nil || !found || claimed.ID != leased.ID {
		t.Fatalf("lease the preferred row: claimed=%+v found=%v err=%v", claimed, found, err)
	}
	m := &recordingPlanModel{plan: validTaskPlanOne}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)

	d.startTaskPlanning(leased)
	waitTaskReady(t, db, 101)
	waitTaskPlanningIdle(t, d)
	if goals := m.recordedGoals(); len(goals) != 1 || goals[0] != "goal of waiting chat" {
		t.Fatalf("the fallback planned %v, want the only free row of chat 101", goals)
	}
	stillLeased, found, err := store.CurrentTask(ctx, db, 100)
	if err != nil || !found || stillLeased.Status != store.TaskStatusPlanning || stillLeased.PlanningOwner != "instance-other" {
		t.Fatalf("the preferred row=%+v found=%v err=%v, want it still leased by the other instance", stillLeased, found, err)
	}

	if released, err := store.ReleasePlanningLease(ctx, db, leased.ID, "instance-other"); err != nil || !released {
		t.Fatalf("release the preferred row changed=%v err=%v", released, err)
	}
	d.startTaskPlanning(leased)
	waitTaskReady(t, db, 100)
	waitTaskPlanningIdle(t, d)
	if goals := m.recordedGoals(); len(goals) != 2 || goals[0] != "goal of waiting chat" || goals[1] != "goal of leased chat" {
		t.Fatalf("planning calls=%v, want the waiting chat then the released preferred chat", goals)
	}
	if waiting.ID == 0 {
		t.Fatal("the waiting row was not created")
	}
}

// TestTaskPlanningFairFallbackDoesNotStarveChats runs the documented 4-workers/8-chats case in two rounds of
// one work cycle each. Every claim is a fair fallback (no preferred row), so the rows are served by
// updated_at; after the second round every chat has a plan and no chat was planned twice.
func TestTaskPlanningFairFallbackDoesNotStarveChats(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	const chats = 8
	goals := make(map[int64]string, chats)
	for index := 0; index < chats; index++ {
		chatID := int64(100 + index)
		goal := fmt.Sprintf("goal of chat %d", chatID)
		goals[chatID] = goal
		planningTaskForChat(t, db, chatID, goal)
	}
	m := &recordingPlanModel{plan: validTaskPlanOne}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)

	readyCount := func() int {
		var ready int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE status = 'ready'`).Scan(&ready); err != nil {
			t.Fatalf("count ready rows: %v", err)
		}
		return ready
	}
	for round := 0; round < 2; round++ {
		var workers sync.WaitGroup
		for worker := 0; worker < taskPlanningConcurrency; worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				// An empty hint claims no preferred row: this is the shared-queue fallback only.
				d.runTaskPlanning(store.Task{})
			}()
		}
		workers.Wait()
		waitTaskPlanningIdle(t, d)
		if got, want := readyCount(), (round+1)*taskPlanningConcurrency; got != want {
			t.Fatalf("round %d ready rows=%d, want %d", round+1, got, want)
		}
	}

	seen := map[string]int{}
	for _, goal := range m.recordedGoals() {
		seen[goal]++
	}
	if len(m.recordedGoals()) != chats {
		t.Fatalf("planning calls=%d, want one per chat (%d)", len(m.recordedGoals()), chats)
	}
	for chatID, goal := range goals {
		if seen[goal] != 1 {
			t.Fatalf("chat %d (goal %q) was planned %d times, want exactly once", chatID, goal, seen[goal])
		}
	}
	for chatID := range goals {
		if task := waitTaskReady(t, db, chatID); task.PlanJSON != validTaskPlanOne {
			t.Fatalf("chat %d plan=%q", chatID, task.PlanJSON)
		}
	}
}

// TestTaskPlanningQueueDepthAndWaitMetrics checks the two new label-free series: the planning backlog gauge
// and the created-to-claimed wait histogram. The gauge drops as rows are planned, and the histogram records
// one observation per claim; no chat, user or task identifier appears in the exposition.
func TestTaskPlanningQueueDepthAndWaitMetrics(t *testing.T) {
	ctx := context.Background()
	reg := metrics.NewRegistry()
	db := testStore(t)
	first := planningTaskForChat(t, db, 100, "goal of first chat")
	second := planningTaskForChat(t, db, 101, "goal of second chat")
	m := &recordingPlanModel{plan: validTaskPlanOne}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, &fakeReplier{}, db)
	d.SetMetrics(reg)

	d.observeTaskPlanningQueueDepth(ctx)
	if body := taskMetricsExposition(t, reg); !strings.Contains(body, taskMetricPlanningQueueDepth+" 2\n") {
		t.Fatalf("planning queue depth did not count both waiting rows:\n%s", body)
	}

	d.startTaskPlanning(first)
	waitTaskReady(t, db, 100)
	waitTaskPlanningIdle(t, d)
	body := taskMetricsExposition(t, reg)
	if !strings.Contains(body, taskMetricPlanningWait+"_count 1\n") {
		t.Errorf("the claim of the first row was not measured:\n%s", body)
	}
	d.observeTaskPlanningQueueDepth(ctx)
	if body := taskMetricsExposition(t, reg); !strings.Contains(body, taskMetricPlanningQueueDepth+" 1\n") {
		t.Errorf("planning queue depth did not drop after the first plan:\n%s", body)
	}

	d.startTaskPlanning(second)
	waitTaskReady(t, db, 101)
	waitTaskPlanningIdle(t, d)
	d.observeTaskPlanningQueueDepth(ctx)
	body = taskMetricsExposition(t, reg)
	for _, sample := range []string{
		taskMetricPlanningQueueDepth + " 0\n",
		taskMetricPlanningWait + "_count 2\n",
	} {
		if !strings.Contains(body, sample) {
			t.Errorf("metrics miss %q:\n%s", sample, body)
		}
	}
	for _, forbidden := range []string{"chat_id", "user_id", "task_id"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("task planning metrics expose %q:\n%s", forbidden, body)
		}
	}
}
