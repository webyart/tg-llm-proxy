package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseTaskResultsReadsLegacyStringsObjectsAndMixedArrays(t *testing.T) {
	for name, raw := range map[string]string{
		"legacy": `["first result","second result"]`,
		"object": `[{"content":"first result","tokens_in":10,"tokens_out":5,"model":"m","duration_ms":1200}]`,
		"mixed":  `["first result",{"content":"second result","tokens_in":3}]`,
	} {
		results, err := ParseTaskResults(raw)
		if err != nil {
			t.Fatalf("%s: ParseTaskResults(%q): %v", name, raw, err)
		}
		if len(results) == 0 || results[0].Content == "" {
			t.Fatalf("%s: results=%+v", name, results)
		}
	}
}

func TestParseTaskResultsRejectsCorruptRows(t *testing.T) {
	for name, raw := range map[string]string{
		"not an array":     `{"content":"x"}`,
		"empty content":    `[{"content":"  "}]`,
		"unknown field":    `[{"content":"x","prompt":"leak"}]`,
		"negative tokens":  `[{"content":"x","tokens_in":-1}]`,
		"negative elapsed": `[{"content":"x","duration_ms":-1}]`,
		"trailing value":   `[{"content":"x"} {"content":"y"}]`,
	} {
		if _, err := ParseTaskResults(raw); err == nil {
			t.Fatalf("%s: corrupted results %q were accepted", name, raw)
		}
	}
}

func TestCompleteTaskStepWithUsageStoresTheCallUsage(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTask(ctx, db, newStoreTask(100, now)); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	tokensIn, tokensOut := int64(11), int64(22)
	step := TaskStepResult{Content: "first result", TokensIn: &tokensIn, TokensOut: &tokensOut, Model: "selected-model", DurationMs: 1500}
	if ok, err := CompleteTaskStepWithUsage(ctx, db, claimed, step, now); err != nil || !ok {
		t.Fatalf("CompleteTaskStepWithUsage ok=%v err=%v", ok, err)
	}
	current, found, err := CurrentTask(ctx, db, 100)
	if err != nil || !found {
		t.Fatalf("CurrentTask found=%v err=%v", found, err)
	}
	results, err := ParseTaskResults(current.ResultsJSON)
	if err != nil || len(results) != 1 {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	stored := results[0]
	if stored.Content != "first result" || stored.TokensIn == nil || *stored.TokensIn != 11 ||
		stored.TokensOut == nil || *stored.TokensOut != 22 || stored.Model != "selected-model" || stored.DurationMs != 1500 {
		t.Fatalf("stored step=%+v", stored)
	}
	if !strings.Contains(current.ResultsJSON, `"tokens_in":11`) {
		t.Fatalf("results JSON does not keep the usage: %s", current.ResultsJSON)
	}

	negative := int64(-1)
	if _, err := CompleteTaskStepWithUsage(ctx, db, claimed, TaskStepResult{Content: "x", TokensIn: &negative}, now); err == nil {
		t.Fatal("a negative token count was accepted")
	}
}
