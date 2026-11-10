//  1. retries — AT MOST Attempts attempts (2 by default), exponential backoff, and ONLY for network
//     errors and 5xx. A 4xx, an empty answer or a cancelled context is returned immediately:
//     retries + timeout + the answer into Telegram is TG_REPLY_DEADLINE_SECONDS.
//  2. the time budget — every attempt shares ONE budget of STRATA_TIMEOUT_SECONDS, not one per
//     attempt. The deadline is taken from the caller's context when it is shorter, and the sleep
//     before a retry is skipped when the remaining budget cannot hold it.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
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
	// admissionTimeout bounds the wait for a free model slot. Without it the wait lasted until the caller's
	// whole reply budget expired, so a user whose request never reached the model was told the model timed
	// out — and ErrNoRoom, the error with its own message on the bot side, was unreachable code.
	admissionTimeout = 15 * time.Second
	// maxRetryWait caps one backoff pause; a provider asking for more than this gets the cap, and the
	// per-attempt budget check below refuses the retry instead of spending the user's whole deadline.
	maxRetryWait = 30 * time.Second
)

// the model, the budget, the retry shape and the GPU concurrency gate.
type Options struct {
	ChatURL             string
	ModelsURL           string
	APIKey              string
	Model               string
	TranscriptionModel  string
	TranscriptionURL    string
	TranscriptionNoAuth bool
	Timeout             time.Duration
	Attempts            int
	Backoff             time.Duration
	MaxConcurrent       int
	HTTPClient          *http.Client
	// Metrics is optional. nil keeps the client silent, which is what tests and short-lived helpers want.
	Metrics *Metrics
	// STREAM_IDLE_TIMEOUT_SECONDS, STREAM_TOTAL_DEADLINE_SECONDS and the progress cadence. A zero value means
	// the defaults of stream.go — a client built by a test streams with those defaults and no test has to
	Stream StreamSettings
}

type OpenAI struct {
	chatURL             string
	modelsURL           string
	apiKey              string
	model               string
	transcriptionModel  string
	transcriptionURL    string
	transcriptionNoAuth bool
	timeout             time.Duration
	attempts            int
	backoff             time.Duration
	client              *http.Client
	sem                 chan struct{}
	admitTimeout        time.Duration
	stream              StreamSettings
	metrics             *Metrics
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
		o.HTTPClient = defaultHTTPClient()
	}
	return &OpenAI{
		chatURL:             strings.TrimSpace(o.ChatURL),
		modelsURL:           strings.TrimSpace(o.ModelsURL),
		apiKey:              strings.TrimSpace(o.APIKey),
		model:               strings.TrimSpace(o.Model),
		transcriptionModel:  strings.TrimSpace(o.TranscriptionModel),
		transcriptionURL:    strings.TrimSpace(o.TranscriptionURL),
		transcriptionNoAuth: o.TranscriptionNoAuth,
		timeout:             o.Timeout,
		attempts:            o.Attempts,
		backoff:             o.Backoff,
		client:              o.HTTPClient,
		sem:                 make(chan struct{}, o.MaxConcurrent),
		admitTimeout:        admissionTimeout,
		stream:              withStreamDefaults(o.Stream),
		metrics:             o.Metrics,
	}, nil
}

// The returned Latency covers the whole call, retries and the wait for a free slot included,
func (c *OpenAI) Complete(ctx context.Context, req Request) (resp Response, err error) {
	var start = time.Now()
	var requested string
	defer func() {
		c.metrics.observe(err, time.Since(start))
		c.metrics.recordTokens(resp, requested)
	}()

	// The slot is the GPU: it stays busy for the whole call, and waiting for it is bounded by
	// admissionTimeout so a saturated endpoint says so instead of burning the reply budget.
	release, admitErr := c.acquire(ctx)
	if admitErr != nil {
		return Response{}, admitErr
	}
	defer release()

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
	body, model, err := c.marshalChatRequest(req, false, 1)
	if err != nil {
		return Response{}, err
	}
	requested = model

	var lastErr error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		var resp, err = c.attempt(attemptCtx, body, model, attempt)
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
		var wait = retryWait(lastErr, c.backoff, attempt)
		if deadline, ok := attemptCtx.Deadline(); ok && time.Until(deadline) <= wait {
			return Response{}, fmt.Errorf("%w (retry %d of %d skipped: %s)", lastErr, attempt+1, c.attempts, TelegramBudgetNotice)
		}
		c.metrics.retryStarted()
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
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolName   string         `json:"name,omitempty"`
}

type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

type chatToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	Arguments   string         `json:"arguments,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolDefinition struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatRequest struct {
	Model    string               `json:"model"`
	Messages []chatMessage        `json:"messages"`
	Stream   bool                 `json:"stream"`
	Tools    []chatToolDefinition `json:"tools,omitempty"`
	// StreamOptions asks the provider to put usage into the stream; without it a streaming provider that
	// follows the OpenAI API sends no usage at all and every streamed answer was accounted as "unknown".
	StreamOptions *chatStreamOptions `json:"stream_options,omitempty"`
}

type chatChoice struct {
	Message struct {
		Role      string         `json:"role"`
		Content   *string        `json:"content"` // a pointer: some providers answer null here
		ToolCalls []chatToolCall `json:"tool_calls"`
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

func (c *OpenAI) marshalChatRequest(req Request, stream bool, attempt int) ([]byte, string, error) {
	var model = req.Model
	if model == "" {
		model = c.model
	}
	if model == "" {
		return nil, "", fmt.Errorf("the model of (STRATA_MODEL) is not set, attempt %d", attempt)
	}

	var request = chatRequest{Model: model, Messages: toWire(req.Messages), Stream: stream, Tools: toWireTools(req.Tools)}
	if stream {
		request.StreamOptions = &chatStreamOptions{IncludeUsage: true}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, "", err
	}
	return body, model, nil
}

// defaultHTTPClient — a tuned transport plus one policy the default client does not have: a redirect is
// NOT followed. A 301/302 turns POST into GET and drops Authorization on a cross-host hop, so a misrouted
// STRATA_BASE_URL would answer with an HTML page instead of an error; refusing the redirect turns the 3xx
// into an APIError the caller can read.
func defaultHTTPClient() *http.Client {
	var transport = http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 8
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// retryWait — how long to wait before the next attempt: the provider's Retry-After when it named one,
// otherwise exponential backoff. The jitter keeps two chats that failed on the same 5xx from retrying in
// lockstep against an endpoint that is already struggling.
func retryWait(err error, base time.Duration, attempt int) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		if apiErr.RetryAfter > maxRetryWait {
			return maxRetryWait
		}
		return apiErr.RetryAfter
	}
	if base <= 0 {
		base = DefaultBackoff
	}
	var wait = base * time.Duration(1<<uint(attempt-1))
	if wait > maxRetryWait {
		wait = maxRetryWait
	}
	var jitterBound = int64(wait / 2)
	if jitterBound <= 0 {
		return wait
	}
	return wait + time.Duration(rand.Int64N(jitterBound))
}

// retryAfterHeader — seconds or an HTTP date, 0 when absent or unparseable.
func retryAfterHeader(value string) time.Duration {
	var trimmed = strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(trimmed); err == nil {
		if wait := time.Until(when); wait > 0 {
			return wait
		}
	}
	return 0
}

// acquire — one model slot. A saturated endpoint answers ErrNoRoom inside admissionTimeout instead of
// holding the caller's reply budget; a cancellation of the caller still wins.
func (c *OpenAI) acquire(ctx context.Context) (func(), error) {
	if cap(c.sem) == 0 {
		return func() {}, nil
	}
	var admitCtx, cancel = context.WithTimeout(ctx, c.admitTimeout)
	defer cancel()
	select {
	case c.sem <- struct{}{}:
		return func() { <-c.sem }, nil
	case <-admitCtx.Done():
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrNoRoom
	}
}

// attempt — one chat.completion request. Decode is separate so a unit test can feed it a payload.
func (c *OpenAI) attempt(ctx context.Context, body []byte, model string, attempt int) (Response, error) {
	var httpReq, buildErr = http.NewRequestWithContext(ctx, http.MethodPost, c.chatURL, bytes.NewReader(body))
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
		return Response{}, &APIError{
			Status:     res.StatusCode,
			Body:       Flatten(string(payload), BodyLimit),
			RetryAfter: retryAfterHeader(res.Header.Get("Retry-After")),
		}
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
	var choice = parsed.Choices[0]
	for _, candidate := range parsed.Choices {
		if candidate.Index == 0 {
			choice = candidate
			break
		}
	}
	var content = ""
	if choice.Message.Content != nil {
		content = *choice.Message.Content
	}
	toolCalls := fromWireToolCalls(choice.Message.ToolCalls)
	if strings.TrimSpace(content) == "" && len(toolCalls) == 0 {
		return Response{}, ErrEmptyAnswer // an empty answer without tool calls is an error, never a stored row
	}

	var out = Response{Content: content, Model: parsed.Model, Attempts: attempt, ToolCalls: toolCalls,
		FinishReason: choice.FinishReason}
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

// UseModel replaces the model name this client sends. It exists for startup model discovery on a single-model
// server (see DiscoverModelName) and must not be called while requests are in flight.
func (c *OpenAI) UseModel(name string) {
	var trimmed = strings.TrimSpace(name)
	if trimmed != "" {
		c.model = trimmed
	}
}

// `/model` slower than the answer it reports on.
func (c *OpenAI) Models(ctx context.Context) ([]string, error) {
	var modelCtx, cancel = context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var httpReq, err = http.NewRequestWithContext(modelCtx, http.MethodGet, c.modelsURL, nil)
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
		var content any = m.Content
		imageURLs := make([]string, 0, len(m.ImageURLs)+1)
		if m.ImageURL != "" && (len(m.ImageURLs) == 0 || m.ImageURLs[0] != m.ImageURL) {
			imageURLs = append(imageURLs, m.ImageURL)
		}
		for _, imageURL := range m.ImageURLs {
			if imageURL != "" {
				imageURLs = append(imageURLs, imageURL)
			}
		}
		if len(imageURLs) > 0 {
			parts := make([]chatContentPart, 0, len(imageURLs)+1)
			parts = append(parts, chatContentPart{Type: "text", Text: m.Content})
			for _, imageURL := range imageURLs {
				parts = append(parts, chatContentPart{Type: "image_url", ImageURL: &chatImageURL{URL: imageURL}})
			}
			content = parts
		}
		out = append(out, chatMessage{Role: m.Role, Content: content, ToolCalls: toWireToolCalls(m.ToolCalls), ToolCallID: m.ToolCallID, ToolName: m.ToolName})
	}
	return out
}

func toWireToolCalls(calls []ToolCall) []chatToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]chatToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, chatToolCall{ID: call.ID, Type: "function", Function: chatToolFunction{Name: call.Name, Arguments: call.Arguments}})
	}
	return out
}

func toWireTools(tools []ToolDefinition) []chatToolDefinition {
	if len(tools) == 0 {
		return nil
	}
	out := make([]chatToolDefinition, 0, len(tools))
	for _, tool := range tools {
		out = append(out, chatToolDefinition{Type: "function", Function: chatToolFunction{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}})
	}
	return out
}

func fromWireToolCalls(calls []chatToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
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
