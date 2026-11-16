package bot

import (
	"bot/internal/model"
)

// Prompt-size instrumentation. TTFT is provider-bound, but the part the bot controls is the number of
// characters it asks the provider to prefill. bot_prompt_chars makes that weight visible per bounded
// call scope, so a prompt trim (the native tool instruction, the tool schemas, the task synthesis
// digest) can be proven in production instead of only in a test. The label set is fixed: no chat,
// user, task or model id ever reaches it.
const (
	promptCharsMetric = "bot_prompt_chars"
	promptCharsHelp   = "Characters of one model request: message text plus the JSON of the tool schemas. Provider prompt_tokens stays authoritative."
	// taskSynthesisTruncatedMetric counts final task syntheses whose prompt was shortened extractively
	// because the stored step results were larger than the synthesis budget. Stored results are never
	// changed; only what the synthesis call reads is capped.
	taskSynthesisTruncatedMetric = "bot_task_synthesis_truncated_total"
	taskSynthesisTruncatedHelp   = "Task syntheses whose prompt was extractively shortened to fit the synthesis budget."
)

// The scope values are the only labels bot_prompt_chars ever has. A new call site must use one of
// these names (or add a bounded one here), never a raw phase or model id from a request.
const (
	promptScopeChat      = "chat"
	promptScopeTaskStep  = "task_step"
	promptScopePlanning  = "planning"
	promptScopeSynthesis = "task_synthesis"
)

// promptCharsBuckets start below a bare system instruction and reach a three-step synthesis at the
// old 12000-rune cap: 3 x 12000 Cyrillic runes is roughly 43k provider tokens, so the top buckets are
// where the prefill cost this change removes used to sit.
var promptCharsBuckets = []float64{500, 1000, 2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000}

// observePromptChars records the character weight of one model request by bounded scope. A nil
// registry (tests, helpers) is a no-op.
func (d *Dispatcher) observePromptChars(scope string, messages []model.Message, tools []model.ToolDefinition) {
	if d == nil {
		return
	}
	reg := d.metrics.Load()
	if reg == nil {
		return
	}
	if scope != promptScopeChat && scope != promptScopeTaskStep && scope != promptScopePlanning && scope != promptScopeSynthesis {
		scope = promptScopeChat
	}
	reg.Histogram(promptCharsMetric, promptCharsHelp, promptCharsBuckets).
		Observe(map[string]string{"scope": scope}, float64(model.PromptChars(messages, tools)))
}

// countTaskSynthesisTruncated records one synthesis whose prompt was capped. It is label-free: how
// much was cut is visible in bot_prompt_chars{scope="task_synthesis"} and in the synthesis log line.
func (d *Dispatcher) countTaskSynthesisTruncated() {
	if d == nil {
		return
	}
	if reg := d.metrics.Load(); reg != nil {
		reg.Counter(taskSynthesisTruncatedMetric, taskSynthesisTruncatedHelp).Add(nil, 1)
	}
}
