// STRATA is stopped, without a single line of the handler changing.
//
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

const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// CONTEXT_MESSAGES rows, bounded by MAX_CONTEXT_CHARS).
type Message struct {
	Role     string
	Content  string
	ImageURL string
}

type Request struct {
	Model    string
	Messages []Message
}

// Latency covers the WHOLE call including the retries, because messages.latency_ms and the
type Response struct {
	Content   string
	Model     string
	TokensIn  *int64
	TokensOut *int64
	Latency   time.Duration
	Attempts  int
	// «ответ обрывается») would otherwise be visible only as a suspiciously short answer.
	ReasoningRunes int
	FinishReason   string
}

// first delivered token) into places that have no tokens to deliver. The ask handler asks for this interface
// and falls back to Complete when the client does not implement it.
type Streamer interface {
	// Stream performs one chat request as a stream and returns the FINAL answer, so the caller still writes
	Stream(ctx context.Context, req Request, h StreamHandler) (Response, error)
}

type Client interface {
	// Complete performs one chat request, retries included, and returns the final answer.
	Complete(ctx context.Context, req Request) (Response, error)
	Models(ctx context.Context) ([]string, error)
}

// and an empty answer tells the user nothing) would be worse than saying "the model answered
// nothing".
var ErrEmptyAnswer = errors.New("the model returned a 200 without any content ")

// of the GPU) was full for the whole budget of the call.
var ErrNoRoom = errors.New("all model request slots are busy ")

// forbids putting the full texts of the model into the logs at LOG_LEVEL=info, and a provider error
// body can echo the prompt.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("strata answered %d: %s", e.Status, httpStatusClass(e.Status))
}

// «суммарный бюджет … не должен превышать»).
func (e *APIError) Retryable() bool { return e.Status >= 500 }

// stays free of secrets and of provider bodies at info level).
func httpStatusClass(status int) string {
	switch {
	case status >= 500:
		return "server error, retryable"
	case status == 401 || status == 403:
		return "the endpoint rejected the key"
	case status == 404:
		return "the endpoint path does not exist — check STRATA_BASE_URL "
	case status == 429:
		return "rate limited by the provider, not retried in v1"
	default:
		return "client error, not retried"
	}
}
