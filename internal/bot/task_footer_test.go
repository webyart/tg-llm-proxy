package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot/internal/i18n"
	"bot/internal/model"
	"bot/internal/store"
)

func TestTaskStatusETAWithZeroOneAndManyCompletedSteps(t *testing.T) {
	var average = 2 * time.Minute
	if _, ok := taskStatusETA(average, 0, 3); ok {
		t.Fatal("an ETA was invented before the first completed step")
	}
	if eta, ok := taskStatusETA(average, 1, 3); !ok || eta != 4*time.Minute {
		t.Fatalf("ETA after one of three steps = %s known=%v, want 4m", eta, ok)
	}
	if eta, ok := taskStatusETA(average, 2, 3); !ok || eta != 2*time.Minute {
		t.Fatalf("ETA after two of three steps = %s known=%v, want 2m", eta, ok)
	}
	if _, ok := taskStatusETA(average, 3, 3); ok {
		t.Fatal("an ETA was shown after the last step")
	}
	if _, ok := taskStatusETA(0, 1, 3); ok {
		t.Fatal("an ETA was invented from a zero average")
	}
}

func TestTaskAverageStepDurationPrefersStoredDurationsAndFallsBackToElapsed(t *testing.T) {
	var measured = []store.TaskStepResult{{DurationMs: 1000}, {DurationMs: 3000}}
	if got, ok := taskAverageStepDuration(measured, time.Minute); !ok || got != 2*time.Second {
		t.Fatalf("average of stored durations = %s known=%v, want 2s", got, ok)
	}
	var legacy = []store.TaskStepResult{{Content: "first"}, {Content: "second"}}
	if got, ok := taskAverageStepDuration(legacy, 10*time.Second); !ok || got != 5*time.Second {
		t.Fatalf("legacy average from elapsed = %s known=%v, want 5s", got, ok)
	}
	if _, ok := taskAverageStepDuration(nil, time.Minute); ok {
		t.Fatal("an average was reported without completed steps")
	}
}

func TestTaskFooterSumsTokensAndCostFromModelPrices(t *testing.T) {
	var cfg = testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11",
		"MODEL_PRICES":           `[{"model":"selected-model","input_per_mtok_usd":1,"output_per_mtok_usd":2}]`,
	})
	var d = newTestDispatcher(t, cfg, nil, &fakeReplier{}, nil)
	var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	var task = store.Task{ID: 7, ChatID: 100, Model: "selected-model", CreatedAt: now.Add(-6*time.Minute - 12*time.Second)}
	var results = []store.TaskStepResult{
		{Content: "first", TokensIn: ptr(6000), TokensOut: ptr(2000), Model: "selected-model", DurationMs: 60_000},
		{Content: "second", TokensIn: ptr(10000), TokensOut: ptr(5000), Model: "selected-model", DurationMs: 120_000},
	}

	var live = d.taskLiveFooter(i18n.RU, task, results, 2, 3, now)
	for _, want := range []string{"идёт 6:12", "токены 16k→7k", "~$0.03", "осталось ~2 мин"} {
		if !strings.Contains(live, want) {
			t.Errorf("RU live footer misses %q: %q", want, live)
		}
	}

	var finished = task
	finished.FinishedAt = &now
	var extra = model.Response{TokensIn: ptr(4000), TokensOut: ptr(3000), Model: "selected-model"}
	var final = d.taskFinalFooter(i18n.RU, finished, results, &extra, now)
	for _, want := range []string{"всего 6:12", "токены 20k→10k", "~$0.04"} {
		if !strings.Contains(final, want) {
			t.Errorf("RU final footer misses %q: %q", want, final)
		}
	}

	var english = d.taskFinalFooter(i18n.EN, finished, results, &extra, now)
	for _, want := range []string{"total 6:12", "tokens 20k→10k", "~$0.04"} {
		if !strings.Contains(english, want) {
			t.Errorf("EN final footer misses %q: %q", want, english)
		}
	}
}

func TestTaskFooterWithoutPricesShowsAnUnknownCost(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var d = newTestDispatcher(t, cfg, nil, &fakeReplier{}, nil)
	var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	var task = store.Task{ID: 7, ChatID: 100, Model: "selected-model", CreatedAt: now.Add(-time.Minute)}
	var results = []store.TaskStepResult{{Content: "first", TokensIn: ptr(100), TokensOut: ptr(50)}}
	var footer = d.taskLiveFooter(i18n.RU, task, results, 1, 2, now)
	if !strings.Contains(footer, "токены 100→50") || !strings.Contains(footer, "—") {
		t.Fatalf("unpriced footer = %q, want the token counts and an unknown cost", footer)
	}
}

func TestTaskFooterHumanReadableDurationsRUAndEN(t *testing.T) {
	var cases = []struct {
		value time.Duration
		ru    string
		en    string
	}{
		{value: 6*time.Minute + 12*time.Second, ru: "6:12", en: "6:12"},
		{value: 2*time.Hour + 5*time.Minute, ru: "2 ч 5 мин", en: "2 h 5 min"},
		{value: time.Hour, ru: "1 ч", en: "1 h"},
		{value: 59 * time.Second, ru: "0:59", en: "0:59"},
	}
	for _, test := range cases {
		if got := formatTaskDuration(i18n.RU, test.value); got != test.ru {
			t.Errorf("RU duration(%s)=%q, want %q", test.value, got, test.ru)
		}
		if got := formatTaskDuration(i18n.EN, test.value); got != test.en {
			t.Errorf("EN duration(%s)=%q, want %q", test.value, got, test.en)
		}
	}
	if got := formatTaskETA(i18n.RU, 7*time.Minute); got != "~7 мин" {
		t.Errorf("RU ETA = %q", got)
	}
	if got := formatTaskETA(i18n.EN, 7*time.Minute); got != "~7 min" {
		t.Errorf("EN ETA = %q", got)
	}
	if got := formatTaskETA(i18n.RU, 30*time.Second); got != "~<1 мин" {
		t.Errorf("RU sub-minute ETA = %q", got)
	}
	if got := formatTaskETA(i18n.RU, 2*time.Hour+5*time.Minute); got != "~2 ч 5 мин" {
		t.Errorf("RU hour ETA = %q", got)
	}
	if got := formatTaskETA(i18n.RU, time.Hour+59*time.Minute+40*time.Second); got != "~2 ч" {
		t.Errorf("RU rounded hour ETA = %q, want ~2 ч (60 minutes must not appear)", got)
	}
	if got := formatTaskETA(i18n.RU, 0); got != "—" {
		t.Errorf("RU empty ETA = %q", got)
	}
}

func TestTaskFinalMessageAndStatusCarryTheFooter(t *testing.T) {
	var in1, out1 = int64(10), int64(5)
	var in2, out2 = int64(20), int64(5)
	var in3, out3 = int64(30), int64(10)
	var m = &taskScriptModel{results: []taskModelResult{
		{response: model.Response{Content: validTaskPlanTwo, Model: "selected-model", Attempts: 1}},
		{response: model.Response{Content: "first result", Model: "selected-model", Attempts: 1, TokensIn: &in1, TokensOut: &out1}},
		{response: model.Response{Content: "second result", Model: "selected-model", Attempts: 1, TokensIn: &in2, TokensOut: &out2}},
		{response: model.Response{Content: "final answer", Model: "selected-model", Attempts: 1, TokensIn: &in3, TokensOut: &out3}},
	}}
	var r = newTaskStatusReplier()
	var db = testStore(t)
	var cfg = testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11",
		"MODEL_PRICES":           `[{"model":"selected-model","input_per_mtok_usd":1000,"output_per_mtok_usd":2000}]`,
	})
	var d = newTestDispatcher(t, cfg, m, r, db)
	if err := dispatch(t, d, testEvent(100, 11, 2201, "/task two steps with usage")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 3)
	current, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || current.Status != store.TaskStatusCompleted {
		t.Fatalf("task=%+v found=%v err=%v", current, found, err)
	}

	var edits = r.editMessages()
	if len(edits) == 0 {
		t.Fatal("no status edit was made")
	}
	var finalStatus = edits[len(edits)-1].text
	for _, want := range []string{"Задача #1 завершена (2/2)", "всего", "токены 60→20", "~$0.10"} {
		if !strings.Contains(finalStatus, want) {
			t.Errorf("final status misses %q: %q", want, finalStatus)
		}
	}

	var reply = r.joined()
	for _, want := range []string{"final answer", "токены 60→20", "~$0.10", "всего"} {
		if !strings.Contains(reply, want) {
			t.Errorf("final message misses %q: %q", want, reply)
		}
	}
}
