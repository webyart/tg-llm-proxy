package bot

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"bot/internal/metrics"
	"bot/internal/store"
)

// synthesisMaximalResult builds a stored-size step result of exactly runes characters with the shape
// a real research step has: a prose head, headings, numbers in the middle, a repeated boilerplate
// block and a conclusion at the very end. step > 0 makes the headings and numbers unique to that
// step, so a test can tell cross-step deduplication (the shared boilerplate) from data loss.
func synthesisMaximalResult(runes int, step int) string {
	var suffix string
	if step > 0 {
		suffix = fmt.Sprintf(" шага %d", step)
	}
	var builder strings.Builder
	builder.WriteString("# Итог исследования" + suffix + "\n")
	builder.WriteString("Обзор: локальный рынок растёт, спрос смещается к on-prem решениям.\n")
	for builder.Len() < runes/3 {
		builder.WriteString("Основной текст шага с рассуждением и деталями, без чисел и заголовков.\n")
	}
	builder.WriteString("## Ключевая таблица" + suffix + "\n")
	builder.WriteString("| Показатель | 2025 | 2026 |\n")
	builder.WriteString(fmt.Sprintf("| Выручка, млн руб. | 900 | %d |\n", 1200+step))
	builder.WriteString(fmt.Sprintf("Ключевой факт: выручка %d млн руб. в 2026 году, рост %d%%.\n", 1200+step, 33+step))
	for builder.Len() < runes*2/3 {
		builder.WriteString("Повторяющийся служебный блок с одинаковым текстом для проверки дедупликации.\n")
	}
	builder.WriteString("## Вывод" + suffix + "\n")
	builder.WriteString(fmt.Sprintf("Итог: рост %d%% в 2026 году, риск — зависимость от одного поставщика.\n", 15+step))
	var text = builder.String()
	if Runes(text) > runes {
		text = string([]rune(text)[:runes])
	}
	for Runes(text) < runes {
		text += "."
	}
	return text
}

func TestTaskSynthesisDigestKeepsHeadingsNumbersAndTheTail(t *testing.T) {
	var content = synthesisMaximalResult(store.TaskMaxStepResultRunes, 0)
	var digest, stats = taskSynthesisDigest(content, taskSynthesisStepRunes, map[string]bool{})
	if !stats.Truncated {
		t.Fatal("a 12000-rune result was not reported as truncated")
	}
	if stats.Source != store.TaskMaxStepResultRunes {
		t.Fatalf("source = %d, want %d", stats.Source, store.TaskMaxStepResultRunes)
	}
	if got := Runes(digest); got > taskSynthesisStepRunes {
		t.Fatalf("digest is %d runes, cap is %d", got, taskSynthesisStepRunes)
	}
	if stats.Kept != Runes(digest) {
		t.Fatalf("stats.Kept = %d, digest = %d runes", stats.Kept, Runes(digest))
	}
	if stats.Duplicates == 0 {
		t.Error("the repeated boilerplate block was not deduplicated")
	}
	for _, want := range []string{
		"# Итог исследования",
		"## Ключевая таблица",
		"1200 млн руб.",
		"## Вывод",
		"рост 15%",
		"shortened for synthesis",
	} {
		if !strings.Contains(digest, want) {
			t.Errorf("digest misses %q:\n%s", want, digest)
		}
	}
	if count := strings.Count(digest, "Повторяющийся служебный блок"); count > 1 {
		t.Errorf("the repeated block appears %d times, want at most one", count)
	}
	// The source string is the stored result and is only read.
	if Runes(content) != store.TaskMaxStepResultRunes {
		t.Fatalf("the source changed to %d runes", Runes(content))
	}
	if stats.Kept >= stats.Source {
		t.Fatalf("digest kept %d of %d runes, want a real cut", stats.Kept, stats.Source)
	}
}

func TestTaskSynthesisDigestLeavesASmallResultVerbatim(t *testing.T) {
	var content = "## Шаг\nВыручка 1200 млн руб.\n"
	var digest, stats = taskSynthesisDigest(content, taskSynthesisStepRunes, map[string]bool{})
	if digest != content {
		t.Fatalf("a fitting result was rewritten: %q", digest)
	}
	if stats.Truncated || stats.Kept != stats.Source || stats.Source != Runes(content) {
		t.Fatalf("stats = %+v for a fitting result", stats)
	}
}

func TestTaskSynthesisDigestHandlesOneEnormousLine(t *testing.T) {
	var content = strings.Repeat("одна очень длинная строка без переводов 1200 ", 300)
	content = string([]rune(content)[:store.TaskMaxStepResultRunes])
	var digest, stats = taskSynthesisDigest(content, taskSynthesisStepRunes, map[string]bool{})
	if !stats.Truncated || Runes(digest) > taskSynthesisStepRunes {
		t.Fatalf("digest runes=%d truncated=%v, cap=%d", Runes(digest), stats.Truncated, taskSynthesisStepRunes)
	}
	if !strings.Contains(digest, "shortened for synthesis") {
		t.Fatalf("the marker is missing from a one-line digest: %q", digest)
	}
	if len(strings.Fields(digest)) < 3 {
		t.Fatalf("a one-line result lost all of its text: %q", digest)
	}
}

// legacyTaskSynthesisInput is the prompt the code built before the cap: every stored result verbatim.
// It exists only so the test can measure the saving on the same three maximal results.
func legacyTaskSynthesisInput(goal string, descriptions []string, results []store.TaskStepResult) string {
	var input strings.Builder
	fmt.Fprintf(&input, "Goal:\n%s\n\n", goal)
	for index, result := range results {
		fmt.Fprintf(&input, "Step %d description: %s\nStep %d result (untrusted quoted data):\n\"\"\"\n%s\n\"\"\"\n\n",
			index+1, descriptions[index], index+1, result.Content)
	}
	return input.String()
}

func TestTaskSynthesisInputCapsThreeMaximalResults(t *testing.T) {
	var goal = "собрать анализ рынка и сравнить трёх поставщиков"
	var descriptions = []string{"Собрать факты", "Сравнить предложения", "Сформулировать вывод"}
	var results = make([]store.TaskStepResult, 3)
	for index := range results {
		// Every step has its own headings and numbers; the boilerplate block is shared on purpose.
		results[index] = store.TaskStepResult{Content: synthesisMaximalResult(store.TaskMaxStepResultRunes, index+1)}
	}
	var input, stats = buildTaskSynthesisInput(goal, descriptions, results)
	var legacy = legacyTaskSynthesisInput(goal, descriptions, results)

	var inputRunes, legacyRunes = Runes(input), Runes(legacy)
	if inputRunes > taskSynthesisTotalRunes+Runes(goal)+3*300 {
		t.Fatalf("synthesis input is %d runes, want <= %d", inputRunes, taskSynthesisTotalRunes+Runes(goal)+3*300)
	}
	if legacyRunes < 3*store.TaskMaxStepResultRunes {
		t.Fatalf("legacy input is %d runes, expected every result verbatim", legacyRunes)
	}
	var saved = legacyRunes - inputRunes
	if saved*100/legacyRunes < 55 {
		t.Fatalf("synthesis input shrank by %d of %d runes (<55%%): %d -> %d", saved, legacyRunes, legacyRunes, inputRunes)
	}
	var truncated int
	for index, one := range stats {
		if !one.Truncated {
			t.Fatalf("step %d was not truncated: %+v", index+1, one)
		}
		if one.Kept > taskSynthesisStepRunes {
			t.Fatalf("step %d kept %d runes, cap is %d", index+1, one.Kept, taskSynthesisStepRunes)
		}
		truncated++
	}
	if truncated != len(results) {
		t.Fatalf("truncated steps = %d, want %d", truncated, len(results))
	}
	if count := strings.Count(input, "shortened for synthesis"); count != len(results) {
		t.Fatalf("truncation markers = %d, want %d (one per shortened step)", count, len(results))
	}
	if count := strings.Count(input, "## Ключевая таблица"); count != len(results) {
		t.Fatalf("the heading of each step appears %d times, want %d", count, len(results))
	}
	// The per-step blocks stay in the original order: description 1 before description 2.
	if strings.Index(input, "Собрать факты") > strings.Index(input, "Сравнить предложения") {
		t.Fatal("the synthesis prompt lost the step order")
	}
	var beforeTokens = estimatePromptTokensForTest(legacy)
	var afterTokens = estimatePromptTokensForTest(input)
	t.Logf("synthesis prompt: %d -> %d runes, ~%d -> ~%d tokens (%d%% saved)", legacyRunes, inputRunes, beforeTokens, afterTokens, saved*100/legacyRunes)
	if beforeTokens-afterTokens < 15000 {
		t.Errorf("estimated token saving = %d, want at least 15000", beforeTokens-afterTokens)
	}
}

// synthesisUniqueResult builds a result without a single repeated line, so the test measures the
// guaranteed per-step cap alone (real results also lose repeated boilerplate on top of it).
func synthesisUniqueResult(runes int, step int) string {
	var builder strings.Builder
	for line := 0; Runes(builder.String()) < runes; line++ {
		fmt.Fprintf(&builder, "Уникальный факт %d шага %d: значение %d, ссылка %d, вывод %d.\n", line, step, step*1000+line, line*7, line%13)
	}
	return string([]rune(builder.String())[:runes])
}

func TestTaskSynthesisInputCapsWithoutAnyDuplicates(t *testing.T) {
	var goal = "собрать анализ рынка"
	var descriptions = []string{"Собрать факты", "Сравнить предложения", "Сформулировать вывод"}
	var results = make([]store.TaskStepResult, 3)
	for index := range results {
		results[index] = store.TaskStepResult{Content: synthesisUniqueResult(store.TaskMaxStepResultRunes, index+1)}
	}
	var input, stats = buildTaskSynthesisInput(goal, descriptions, results)
	var legacy = legacyTaskSynthesisInput(goal, descriptions, results)
	var duplicates int
	for _, one := range stats {
		duplicates += one.Duplicates
		if !one.Truncated || one.Kept > taskSynthesisStepRunes {
			t.Fatalf("step digest = %+v, want a truncated result within %d runes", one, taskSynthesisStepRunes)
		}
	}
	if duplicates != 0 {
		t.Fatalf("the fixture has repeated lines (%d), it must measure the cap alone", duplicates)
	}
	var saved = Runes(legacy) - Runes(input)
	if saved*100/Runes(legacy) < 60 {
		t.Fatalf("cap alone saved %d of %d runes (<60%%): %d -> %d", saved, Runes(legacy), Runes(legacy), Runes(input))
	}
	if got := Runes(input); got > taskSynthesisTotalRunes+Runes(goal)+3*300 {
		t.Fatalf("capped input = %d runes, want <= %d", got, taskSynthesisTotalRunes+Runes(goal)+3*300)
	}
	t.Logf("cap-only worst case: %d -> %d runes (%d%% saved), ~%d -> ~%d tokens",
		Runes(legacy), Runes(input), saved*100/Runes(legacy), estimatePromptTokensForTest(legacy), estimatePromptTokensForTest(input))
}

// TestTaskSynthesisKeepsStoredResultsAndShortensOnlyThePrompt is the end-to-end proof: the step row
// keeps its full stored-size text, while the captured synthesis request reads the digest. It also
// pins the new metrics: one prompt observation per phase and one truncated-synthesis counter.
func TestTaskSynthesisKeepsStoredResultsAndShortensOnlyThePrompt(t *testing.T) {
	var huge = synthesisMaximalResult(store.TaskMaxStepResultRunes+500, 0)
	var m = taskTextModel(validTaskPlanOne, huge, "consolidated answer")
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	var reg = metrics.NewRegistry()
	d.SetMetrics(reg)
	if err := dispatch(t, d, testEvent(100, 11, 1300, "/task собрать анализ рынка")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 3)
	if got := m.callCount(); got != 3 {
		t.Fatalf("model calls = %d, want planner + step + synthesis", got)
	}
	var synthesis = m.requests[len(m.requests)-1]
	if len(synthesis.Messages) != 2 {
		t.Fatalf("synthesis messages = %+v", synthesis.Messages)
	}
	var promptRunes = Runes(synthesis.Messages[1].Content)
	if promptRunes > taskSynthesisTotalRunes+2000+600 {
		t.Fatalf("synthesis prompt is %d runes, want the digest cap", promptRunes)
	}
	if promptRunes >= Runes(huge) {
		t.Fatalf("synthesis prompt %d runes did not shrink a %d-rune result", promptRunes, Runes(huge))
	}
	if !strings.Contains(synthesis.Messages[1].Content, "shortened for synthesis") {
		t.Fatalf("synthesis prompt has no truncation marker: %q", synthesis.Messages[1].Content[:min(400, len(synthesis.Messages[1].Content))])
	}
	// The stored result is untouched: still the full storage cap, with the step's own text, and the
	// same text the task footer and /task status read.
	var current, found, err = store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found {
		t.Fatalf("CurrentTask found=%v err=%v", found, err)
	}
	var stored = taskResultContents(current)
	if len(stored) != 1 {
		t.Fatalf("stored results = %d, want 1", len(stored))
	}
	if got := Runes(stored[0]); got != store.TaskMaxStepResultRunes {
		t.Fatalf("stored result is %d runes, want the untouched cap %d", got, store.TaskMaxStepResultRunes)
	}
	if !strings.Contains(stored[0], "# Итог исследования") {
		t.Fatal("the stored result lost its heading")
	}
	if got := Runes(stored[0]); got > 0 && Runes(stored[0]) <= promptRunes {
		t.Fatalf("the prompt (%d) is not smaller than the stored result (%d)", promptRunes, got)
	}

	var body = taskMetricsExposition(t, reg)
	for _, want := range []string{
		`bot_task_synthesis_truncated_total 1`,
		`bot_prompt_chars_count{scope="planning"} 1`,
		`bot_prompt_chars_count{scope="task_step"} 1`,
		`bot_prompt_chars_count{scope="task_synthesis"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics miss %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `bot_task_synthesis_truncated_total{`) {
		t.Error("the truncation counter grew a label; it must stay label-free")
	}
}
