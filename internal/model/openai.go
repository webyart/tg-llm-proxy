//  1. retries — AT MOST Attempts attempts (2 by default), exponential backoff, and ONLY for network
//     errors and 5xx. A 4xx, an empty answer or a cancelled context is returned immediately:
//     retries + timeout + the answer into Telegram is TG_REPLY_DEADLINE_SECONDS.
//  2. the time budget — every attempt shares ONE budget of STRATA_TIMEOUT_SECONDS, not one per
//     attempt. The deadline is taken from the caller's context when it is shorter, and the sleep
//     before a retry is skipped when the remaining budget cannot hold it.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	DefaultAttempts      = 2
	DefaultBackoff       = 250 * time.Millisecond
	DefaultTimeout       = 60 * time.Second
	BodyLimit            = 300
	TelegramBudgetNotice = "the budget of STRATA_TIMEOUT_SECONDS is spent — the answer is not retried again "
)

// the model, the budget, the retry shape and the GPU concurrency gate.
type Options struct {
	ChatURL       string
	ModelsURL     string
	APIKey        string
	Model         string
	Timeout       time.Duration
	Attempts      int
	Backoff       time.Duration
	MaxConcurrent int
	HTTPClient    *http.Client
	// STREAM_IDLE_TIMEOUT_SECONDS, STREAM_TOTAL_DEADLINE_SECONDS and the progress cadence. A zero value means
	// the defaults of stream.go — a client built by a test streams with those defaults and no test has to
	Stream StreamSettings
}

type OpenAI struct {
	chatURL   string
	modelsURL string
	apiKey    string
	model     string
	timeout   time.Duration
	attempts  int
	backoff   time.Duration
	client    *http.Client
	sem       chan struct{}
	stream    StreamSettings
}

// problem and is reported instead of producing a client that would fail on the first /ask.
func New(o Options) (*OpenAI, error) {
	if strings.TrimSpace(o.ChatURL) == "" {
		return nil, fmt.Errorf("the model client needs the chat URL of (STRATA_BASE_URL), it is empty")
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Attempts <= 0 {
		o.Attempts = DefaultAttempts
	}
	if o.Backoff <= 0 {
		o.Backoff = DefaultBackoff
	}
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = 1
	}
	if o.ModelsURL == "" {
		o.ModelsURL = strings.Replace(strings.TrimSpace(o.ChatURL), "/chat/completions", "/models", 1)
	}
	if o.HTTPClient == nil {
		// The per-attempt deadline comes from the context budget below, so the transport itself
		// stays open-ended; a client-side http.Client timeout would fight with the retry budget.
		o.HTTPClient = http.DefaultClient
	}
	return &OpenAI{
		chatURL:   strings.TrimSpace(o.ChatURL),
		modelsURL: strings.TrimSpace(o.ModelsURL),
		apiKey:    strings.TrimSpace(o.APIKey),
		model:     strings.TrimSpace(o.Model),
		timeout:   o.Timeout,
		attempts:  o.Attempts,
		backoff:   o.Backoff,
		client:    o.HTTPClient,
		sem:       make(chan struct{}, o.MaxConcurrent),
		stream:    withStreamDefaults(o.Stream),
	}, nil
}

// The returned Latency covers the whole call, retries and the wait for a free slot included,
func (c *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	var start = time.Now()

	// for a slot consumes the budget too, so the wait is interruptible by the caller's context.
	if cap(c.sem) > 0 {
		select {
		case c.sem <- struct{}{}:
			defer func() { <-c.sem }()
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}

	var budget = c.timeout
	if deadline, ok := ctx.Deadline(); ok {
		var left = time.Until(deadline)
		if left < budget {
			budget = left
		}
	}
	if budget <= 0 {
		return Response{}, fmt.Errorf("%w: %s", context.DeadlineExceeded, TelegramBudgetNotice)
	}
	var attemptCtx, cancel = context.WithTimeout(ctx, budget)
	defer cancel()

	var lastErr error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		var resp, err = c.attempt(attemptCtx, req, attempt)
		if err == nil {
			resp.Latency = time.Since(start)
			resp.Attempts = attempt
			return resp, nil
		}
		lastErr = err
		if !Retryable(err) {
			return Response{}, err
		}
		if attempt == c.attempts {
			break
		}
		var wait = c.backoff * time.Duration(1<<(attempt-1)) // exponential,
		if deadline, ok := attemptCtx.Deadline(); ok && time.Until(deadline) <= wait {
			return Response{}, fmt.Errorf("%w (retry %d of %d skipped: %s)", lastErr, attempt+1, c.attempts, TelegramBudgetNotice)
		}
		select {
		case <-attemptCtx.Done():
			return Response{}, fmt.Errorf("%w: %s", attemptCtx.Err(), TelegramBudgetNotice)
		case <-time.After(wait):
		}
	}
	return Response{}, fmt.Errorf("%w (after %d attempts)", lastErr, c.attempts)
}

// false), an empty answer and a cancelled context all end the call at once.
func Retryable(err error) bool {
	// A spent budget is not the network class, and this check has to come FIRST: context.DeadlineExceeded
	// implements Timeout() and Is(), so it satisfies net.Error and the branch below would retry an answer
	// retried for exactly this reason — a second wall for the user and a second slot of
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true // connection refused, DNS, TLS — the network class of
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

// because a streamed event carries a `delta`, not a `message`.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatChoice struct {
	Message struct {
		Role    string  `json:"role"`
		Content *string `json:"content"` // a pointer: some providers answer null here
	} `json:"message"`
	Index        int    `json:"index"`
	FinishReason string `json:"finish_reason"`
}

type chatResponse struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// modelsResponse — the OpenAI envelope {"data":[{"id":…}]}; GET /models also answers a bare array
// of strings on some local servers, which Models tries second.
type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (c *OpenAI) attempt(ctx context.Context, req Request, attempt int) (Response, error) {
	var model = req.Model
	if model == "" {
		model = c.model
	}
	if model == "" {
		return Response{}, fmt.Errorf("the model of (STRATA_MODEL) is not set, attempt %d", attempt)
	}

	var body, err = json.Marshal(chatRequest{Model: model, Messages: toWire(req.Messages), Stream: false})
	if err != nil {
		return Response{}, err
	}

	var httpReq, buildErr = http.NewRequestWithContext(ctx, http.MethodPost, c.chatURL, strings.NewReader(string(body)))
	if buildErr != nil {
		return Response{}, buildErr
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	var res, doErr = c.client.Do(httpReq)
	if doErr != nil {
		// A deadline of the budget is not a retryable network failure: the budget is already spent,
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("%w: %s", ctx.Err(), TelegramBudgetNotice)
		}
		return Response{}, doErr
	}
	defer res.Body.Close()

	// 1 MiB: a chat.completion fits; a runaway error page must not sit in the memory of the bot.
	var payload, readErr = io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if readErr != nil {
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("%w: %s", ctx.Err(), TelegramBudgetNotice)
		}
		return Response{}, readErr
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Response{}, &APIError{Status: res.StatusCode, Body: Flatten(string(payload), BodyLimit)}
	}
	return Decode(res.StatusCode, payload, model, attempt)
}

func Decode(status int, payload []byte, model string, attempt int) (Response, error) {
	var parsed chatResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return Response{}, fmt.Errorf("the endpoint answered %d with something that is not a chat.completion : %w", status, err)
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return Response{}, &APIError{Status: status, Body: Flatten(parsed.Error.Message, BodyLimit)}
	}
	if len(parsed.Choices) == 0 {
		return Response{}, ErrEmptyAnswer
	}
	var content = ""
	if parsed.Choices[0].Message.Content != nil {
		content = *parsed.Choices[0].Message.Content
	}
	if strings.TrimSpace(content) == "" {
		return Response{}, ErrEmptyAnswer // an empty answer is an error, never a stored row
	}

	var out = Response{Content: content, Model: parsed.Model, Attempts: attempt}
	if out.Model == "" {
		out.Model = model
	}
	if parsed.Usage != nil {
		var in = int64(parsed.Usage.PromptTokens)
		var answered = int64(parsed.Usage.CompletionTokens)
		out.TokensIn = &in
		out.TokensOut = &answered
	}
	return out, nil
}

// `/model` slower than the answer it reports on.
func (c *OpenAI) Models(ctx context.Context) ([]string, error) {
	var httpReq, err = http.NewRequestWithContext(ctx, http.MethodGet, c.modelsURL, nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	var res, doErr = c.client.Do(httpReq)
	if doErr != nil {
		return nil, doErr
	}
	defer res.Body.Close()

	var payload, readErr = io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if readErr != nil {
		return nil, readErr
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &APIError{Status: res.StatusCode, Body: Flatten(string(payload), BodyLimit)}
	}

	var envelope modelsResponse
	if err := json.Unmarshal(payload, &envelope); err == nil && len(envelope.Data) > 0 {
		var ids = make([]string, 0, len(envelope.Data))
		for _, m := range envelope.Data {
			if strings.TrimSpace(m.ID) != "" {
				ids = append(ids, m.ID)
			}
		}
		return ids, nil
	}
	var bare []string
	if err := json.Unmarshal(payload, &bare); err == nil {
		return bare, nil
	}
	return nil, fmt.Errorf("GET %s answered %d with something that is not a model list ", c.modelsURL, res.StatusCode)
}

func toWire(messages []Message) []chatMessage {
	var out = make([]chatMessage, 0, len(messages))
	for _, m := range messages {
		out = append(out, chatMessage{Role: m.Role, Content: m.Content})
	}
	return out
}

// Flatten — one line of at most limit runes for an error text. The body of a provider can be a full
func Flatten(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	var runes = []rune{}
	for _, r := range text {
		if unicode.IsSpace(r) {
			r = ' '
		}
		runes = append(runes, r)
		if len(runes) >= limit {
			break
		}
	}
	return strings.TrimSpace(string(runes))
}
