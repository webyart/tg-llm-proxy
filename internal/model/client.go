// client.go — the interface of §10 День 3: `model.Client { Complete(ctx, req) (Resp, error) }`.
// The bot layer of День 4 depends on this interface only, which is what lets §11 run the whole
// /ask scenario against the httptest fake of internal/testutil and §10 День 6 degrade the bot when
// STRATA is stopped, without a single line of the handler changing.
//
// The write invariant of §10 День 3 lives on the CALLER side and is stated here because this is the
// boundary it crosses: an assistant message is written to SQLite EXACTLY ONCE, after the final
// successful Response. A retry inside Complete must not produce two rows, and when every attempt
// failed the caller writes only events(kind='model_error') — never a half answer.
package model

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The roles of §5 (the same three values the messages.role CHECK of SQLite accepts). The system
// role is used for SYSTEM_PROMPT of §7, which §5 keeps out of the history table.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message — one turn of the glued context of §5 («Правила контекста (v1)»: SYSTEM_PROMPT + the last
// CONTEXT_MESSAGES rows, bounded by MAX_CONTEXT_CHARS).
type Message struct {
	Role    string
	Content string
}

// Request — one completion call. Model is optional: an empty value means "the STRATA_MODEL of §7",
// which the client fills from its options (§6 `/model` reports that same resolution).
type Request struct {
	Model    string
	Messages []Message
}

// Response — what a successful attempt returned, ready to be stored in the §5 columns.
// TokensIn/TokensOut stay nil when the provider sent no `usage` (§5 keeps those columns NULL), and
// Latency covers the WHOLE call including the retries, because messages.latency_ms and the
// «⏱ latency» line of §6 measure what the user waited for (§12 UX).
type Response struct {
	Content   string
	Model     string
	TokensIn  *int64
	TokensOut *int64
	Latency   time.Duration
	Attempts  int
	// ReasoningRunes and FinishReason are filled only by the streamed path of §15 (§16 change 34). They exist
	// for the log line of §10 День 7 and for nothing else: no column of §5 holds them, because §5 keeps the
	// answer text, not the thinking, and `finish_reason == "length"` (the model ran out of context, §13 risk
	// «ответ обрывается») would otherwise be visible only as a suspiciously short answer.
	ReasoningRunes int
	FinishReason   string
}

// Streamer — the optional interface of §15 «стриминг ответов» (§16 change 34). It is deliberately NOT part of
// Client: the fakes of §11 and the health probe of §4 answer one question with one answer, and making every
// implementation produce a stream would push the streaming rules of §10 День 3 (one row, no retry after the
// first delivered token) into places that have no tokens to deliver. The ask handler asks for this interface
// and falls back to Complete when the client does not implement it.
type Streamer interface {
	// Stream performs one chat request as a stream and returns the FINAL answer, so the caller still writes
	// exactly one assistant row of §5.
	Stream(ctx context.Context, req Request, h StreamHandler) (Response, error)
}

// Client — the contract of §4 (`internal/model/client.go`).
type Client interface {
	// Complete performs one chat request, retries included, and returns the final answer.
	Complete(ctx context.Context, req Request) (Response, error)
	// Models lists the models of the endpoint (§6 `/model check`, §4 internal/health probe).
	Models(ctx context.Context) ([]string, error)
}

// ErrEmptyAnswer — a 200 with no content. It is an ERROR, not an empty reply: §10 День 3 lists it
// as one of the cases the caller must handle, and storing "" into messages.content of §5 (NOT NULL,
// and an empty answer tells the user nothing) would be worse than saying "the model answered
// nothing".
var ErrEmptyAnswer = errors.New("the model returned a 200 without any content (§10 День 3: an empty answer is an error)")

// ErrNoRoom — the concurrency gate of §12 (MAX_CONCURRENT_MODEL_REQUESTS limits the parallel load
// of the GPU) was full for the whole budget of the call.
var ErrNoRoom = errors.New("all model request slots are busy (§7 MAX_CONCURRENT_MODEL_REQUESTS, §12 performance)")

// APIError — a non-2xx answer of the endpoint. The body is truncated by the client: §12 security
// forbids putting the full texts of the model into the logs at LOG_LEVEL=info, and a provider error
// body can echo the prompt.
type APIError struct {
	Status int
	Body   string
}

// Error names the status and the class, never the token (§12 security).
func (e *APIError) Error() string {
	return fmt.Sprintf("strata answered %d (§10 День 3: %s)", e.Status, httpStatusClass(e.Status))
}

// Retryable — §10 День 3 retries ONLY network errors and 5xx. A 4xx is a wrong request or a wrong
// key, and repeating it only wastes the budget of TG_REPLY_DEADLINE_SECONDS (§7 row, §10 День 3
// «суммарный бюджет … не должен превышать»).
func (e *APIError) Retryable() bool { return e.Status >= 500 }

// httpStatusClass — a short class for the error text and for events.detail (§12 security: the log
// stays free of secrets and of provider bodies at info level).
func httpStatusClass(status int) string {
	switch {
	case status >= 500:
		return "server error, retryable"
	case status == 401 || status == 403:
		return "the endpoint rejected the key"
	case status == 404:
		return "the endpoint path does not exist — check STRATA_BASE_URL (§16 change 26)"
	case status == 429:
		return "rate limited by the provider, not retried in v1"
	default:
		return "client error, not retried"
	}
}
