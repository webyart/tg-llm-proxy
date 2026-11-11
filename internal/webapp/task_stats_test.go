package webapp

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"bot/internal/store"
)

const taskStatsPlanJSON = `{"steps":[{"description":"gather notes"},{"description":"compare notes"},{"description":"summarize notes"}]}`

const taskStatsPrices = `[{"model":"task-model","input_per_mtok_usd":1,"output_per_mtok_usd":2},{"model":"step-model","input_per_mtok_usd":0.5,"output_per_mtok_usd":1.5}]`

func createTaskStatsTask(t *testing.T, app *App, createdAt time.Time, planJSON string) store.Task {
	t.Helper()
	ctx := context.Background()
	if err := store.EnsureChat(ctx, app.db, testChatID, testUserID, ""); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateTask(ctx, app.db, store.Task{
		ChatID: testChatID, UserID: testUserID, Goal: "task stats goal", Model: "task-model",
		PlanJSON: planJSON, DueAt: createdAt, ExpiresAt: createdAt.Add(time.Hour), CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func setTaskStatsState(t *testing.T, app *App, taskID int64, resultsJSON string, nextStep int, status string, finishedAt string) {
	t.Helper()
	if _, err := app.db.ExecContext(context.Background(),
		`UPDATE tasks SET results_json = ?, next_step = ?, status = ?, finished_at = ? WHERE id = ?`,
		resultsJSON, nextStep, status, finishedAt, taskID); err != nil {
		t.Fatal(err)
	}
}

func TestTaskListComputesUsageCostAndETA(t *testing.T) {
	type testCase struct {
		name           string
		resultsJSON    string
		nextStep       int
		wantTokensIn   int64
		wantTokensOut  int64
		wantCostMicros int64
		wantCompleted  int
		wantETASeconds int64
		wantETA        bool
	}
	cases := []testCase{
		{
			name: "zero steps", resultsJSON: `[]`, nextStep: 0,
			wantTokensIn: 0, wantTokensOut: 0, wantCostMicros: 0, wantCompleted: 0, wantETA: false,
		},
		{
			name:         "one measured step",
			resultsJSON:  `[{"content":"first result","tokens_in":6000,"tokens_out":2000,"model":"step-model","duration_ms":60000}]`,
			nextStep:     1,
			wantTokensIn: 6000, wantTokensOut: 2000, wantCostMicros: 6000, wantCompleted: 1,
			wantETASeconds: 120, wantETA: true,
		},
		{
			name: "many steps use the per-step model and fall back to the task model",
			resultsJSON: `[{"content":"first result","tokens_in":6000,"tokens_out":2000,"model":"step-model","duration_ms":60000},` +
				`{"content":"second result","tokens_in":10000,"tokens_out":5000,"duration_ms":120000}]`,
			nextStep:     2,
			wantTokensIn: 16000, wantTokensOut: 7000, wantCostMicros: 26000, wantCompleted: 2,
			wantETASeconds: 90, wantETA: true,
		},
		{
			name:         "legacy string results carry no usage",
			resultsJSON:  `["first result","second result"]`,
			nextStep:     2,
			wantTokensIn: 0, wantTokensOut: 0, wantCostMicros: 0, wantCompleted: 2, wantETA: false,
		},
		{
			name:         "null tokens count as zero",
			resultsJSON:  `[{"content":"first result","tokens_in":null,"tokens_out":null,"model":"step-model"}]`,
			nextStep:     1,
			wantTokensIn: 0, wantTokensOut: 0, wantCostMicros: 0, wantCompleted: 1, wantETA: false,
		},
		{
			name:         "cost keeps six decimals",
			resultsJSON:  `[{"content":"tiny result","tokens_in":1,"tokens_out":1,"model":"step-model"}]`,
			nextStep:     1,
			wantTokensIn: 1, wantTokensOut: 1, wantCostMicros: 2, wantCompleted: 1, wantETA: false,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			app, server := newTestAppWithRaw(t, map[string]string{"MODEL_PRICES": taskStatsPrices})
			bearer := launchSession(t, app, server)
			createdAt := time.Now().UTC().Add(-92 * time.Second)
			created := createTaskStatsTask(t, app, createdAt, taskStatsPlanJSON)
			setTaskStatsState(t, app, created.ID, test.resultsJSON, test.nextStep, store.TaskStatusRunning, "")

			status, body, _ := apiRequest(t, server, http.MethodGet, "/api/tasks?limit=1", bearer)
			if status != http.StatusOK {
				t.Fatalf("tasks status=%d body=%s", status, body)
			}
			var decoded struct {
				Items []taskItem `json:"items"`
			}
			if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.Items) != 1 {
				t.Fatalf("tasks body=%s err=%v", body, err)
			}
			item := decoded.Items[0]
			if item.TokensIn != test.wantTokensIn || item.TokensOut != test.wantTokensOut {
				t.Errorf("tokens=%d→%d, want %d→%d", item.TokensIn, item.TokensOut, test.wantTokensIn, test.wantTokensOut)
			}
			wantCost := float64(test.wantCostMicros) / 1_000_000
			if math.Abs(item.CostUSD-wantCost) > 1e-12 {
				t.Errorf("cost_usd=%v, want %v", item.CostUSD, wantCost)
			}
			if item.StepsCompleted != test.wantCompleted {
				t.Errorf("steps_completed=%d, want %d", item.StepsCompleted, test.wantCompleted)
			}
			if item.StepsTotal != 3 {
				t.Errorf("steps_total=%d, want 3", item.StepsTotal)
			}
			if test.wantETA {
				if item.ETASeconds == nil || *item.ETASeconds != test.wantETASeconds {
					t.Errorf("eta_seconds=%v, want %d", item.ETASeconds, test.wantETASeconds)
				}
			} else if item.ETASeconds != nil {
				t.Errorf("eta_seconds=%d, want null", *item.ETASeconds)
			}
			if item.ElapsedSeconds < 92 || item.ElapsedSeconds > 95 {
				t.Errorf("elapsed_seconds=%d, want about 92", item.ElapsedSeconds)
			}
			if item.HasPlan != true || item.DueAt == "" || item.ExpiresAt == "" {
				t.Errorf("existing task list fields changed: %+v", item)
			}
		})
	}
}

func TestTaskListPlanningTaskHasNoStepsOrETA(t *testing.T) {
	app, server := newTestAppWithRaw(t, map[string]string{"MODEL_PRICES": taskStatsPrices})
	bearer := launchSession(t, app, server)
	ctx := context.Background()
	if err := store.EnsureChat(ctx, app.db, testChatID, testUserID, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := store.CreatePlanningTask(ctx, app.db, store.Task{
		ChatID: testChatID, UserID: testUserID, Goal: "planning stats goal", Model: "task-model",
		DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/tasks?limit=1", bearer)
	if status != http.StatusOK {
		t.Fatalf("tasks status=%d body=%s", status, body)
	}
	var decoded struct {
		Items []taskItem `json:"items"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.Items) != 1 {
		t.Fatalf("tasks body=%s err=%v", body, err)
	}
	item := decoded.Items[0]
	if item.Status != store.TaskStatusPlanning || item.HasPlan {
		t.Fatalf("planning item=%+v", item)
	}
	if item.StepsTotal != 0 || item.StepsCompleted != 0 || item.ETASeconds != nil {
		t.Errorf("planning steps total=%d completed=%d eta=%v, want 0/0/null", item.StepsTotal, item.StepsCompleted, item.ETASeconds)
	}
	if item.TokensIn != 0 || item.TokensOut != 0 || item.CostUSD != 0 {
		t.Errorf("planning usage=%d→%d %v, want zero", item.TokensIn, item.TokensOut, item.CostUSD)
	}
}

func TestTaskListElapsedSecondsAreCappedAndNeverNegative(t *testing.T) {
	prices := map[string]string{"MODEL_PRICES": taskStatsPrices}
	for _, test := range []struct {
		name      string
		createdAt time.Time
		want      int64
	}{
		{name: "old task is capped", createdAt: time.Now().UTC().Add(-30 * 24 * time.Hour), want: maxTaskElapsedSeconds},
		{name: "future timestamp is zero", createdAt: time.Now().UTC().Add(time.Hour), want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, server := newTestAppWithRaw(t, prices)
			bearer := launchSession(t, app, server)
			created := createTaskStatsTask(t, app, test.createdAt, taskStatsPlanJSON)
			setTaskStatsState(t, app, created.ID, `[]`, 0, store.TaskStatusRunning, "")

			status, body, _ := apiRequest(t, server, http.MethodGet, "/api/tasks?limit=1", bearer)
			if status != http.StatusOK {
				t.Fatalf("tasks status=%d body=%s", status, body)
			}
			var decoded struct {
				Items []taskItem `json:"items"`
			}
			if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.Items) != 1 {
				t.Fatalf("tasks body=%s err=%v", body, err)
			}
			if got := decoded.Items[0].ElapsedSeconds; got != test.want {
				t.Errorf("elapsed_seconds=%d, want %d", got, test.want)
			}
		})
	}
}

func TestTaskListTerminalDurationAndCorruptResultsStaySafe(t *testing.T) {
	app, server := newTestAppWithRaw(t, map[string]string{"MODEL_PRICES": taskStatsPrices})
	bearer := launchSession(t, app, server)
	createdAt := time.Now().UTC().Add(-6*time.Minute - 12*time.Second)
	created := createTaskStatsTask(t, app, createdAt, taskStatsPlanJSON)
	finishedAt := createdAt.Add(6*time.Minute + 12*time.Second).UTC().Format("2006-01-02 15:04:05.999999999")
	setTaskStatsState(t, app, created.ID, `{"not":"an array"}`, 0, store.TaskStatusFailed, finishedAt)

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/tasks?limit=1", bearer)
	if status != http.StatusOK {
		t.Fatalf("tasks status=%d body=%s", status, body)
	}
	if got := string(body); !strings.Contains(got, `"duration_ms":372000`) ||
		!strings.Contains(got, `"tokens_in":0`) || !strings.Contains(got, `"tokens_out":0`) ||
		!strings.Contains(got, `"cost_usd":0`) || !strings.Contains(got, `"steps_completed":0`) ||
		!strings.Contains(got, `"eta_seconds":null`) {
		t.Fatalf("terminal task body=%s", body)
	}
}
