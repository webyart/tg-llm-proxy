// (HTTP 200, `text/event-stream`, one `data:` event per token, `data: [DONE]` at the end, keepalive comment
// lines starting with `:`, time to the first event 4–6 ms, cadence ~48 events/s, `usage` arrives inside the
// stream without `stream_options`). The finding that changed the design: this model streams
// `delta.reasoning_content` as a field of its own — the first ~200 events of a two-word answer were
// «Thinking: 1. **Analyze the Request…**» while `delta.content` carried the 10 runes of the real answer, and
// the non-streamed response separates them the same way (measured: content 10 runes, reasoning 661 runes).
// reaches the chat either.
//
// events of a healthy answer is ~20 ms, while the wall of an answer is a property of the answer, not of a
// exists for: the answer of `row=6` measured 1m56s and fitted no 60 s budget at all.
package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// The defaults of the streaming budgets, used when Options.Stream leaves a field zero. The bounds the loader
const (
	DefaultStreamIdle     = 30 * time.Second
	DefaultStreamTotal    = 900 * time.Second
	DefaultStreamProgress = time.Second
	// StreamLineLimit — one SSE line. An OpenAI-compatible chunk is a few hundred bytes; a megabyte is the
	// room of a provider that puts a whole answer into one line, and more than that is not an answer.
	StreamLineLimit         = 1 << 20
	StreamAnswerLimit       = 100_000
	StreamToolArgumentLimit = 16_384
	StreamToolCallLimit     = 8
)

// by two different numbers in the two places an operator compares»).
var ErrStreamStalled = fmt.Errorf("%w: the stream went silent longer than STREAM_IDLE_TIMEOUT_SECONDS ", context.DeadlineExceeded)

// ErrStreamTooLong — the answer is still arriving but already costs more than STREAM_TOTAL_DEADLINE_SECONDS
var ErrStreamTooLong = fmt.Errorf("%w: the streamed answer outlived STREAM_TOTAL_DEADLINE_SECONDS ", context.DeadlineExceeded)

var ErrStreamLimit = errors.New("the stream exceeded a resource limit")

const StreamNotice = "the stream was given up by its budget of (STREAM_IDLE_TIMEOUT_SECONDS / STREAM_TOTAL_DEADLINE_SECONDS)"

type StreamSettings struct {
	// Idle is the longest pause between two SSE events. A healthy answer produces one every ~20 ms
	// (measured), so this is a stall detector, not a wall on the length of an answer.
	Idle time.Duration
	// Total bounds the whole streamed answer, thinking included.
	Total time.Duration
	// Progress is how often Progress may fire while the model only thinks. The Telegram-side cadence
	// STREAM_EDIT_INTERVAL_MS is a different number and lives in internal/bot/stream.go.
	Progress time.Duration
}

// withStreamDefaults — the defaults above plus one invariant: a stall may not outlive the whole answer.
func withStreamDefaults(s StreamSettings) StreamSettings {
	if s.Idle <= 0 {
		s.Idle = DefaultStreamIdle
	}
	if s.Total <= 0 {
		s.Total = DefaultStreamTotal
	}
	if s.Progress <= 0 {
		s.Progress = DefaultStreamProgress
	}
	if s.Idle > s.Total {
		s.Idle = s.Total
	}
	return s
}

// StreamInfo — what the caller may show while the answer is still being written. The counters are runes
// at the end of the stream, so a placeholder may say «модель думает», never «осталось N символов».
type StreamInfo struct {
	Model          string
	ReasoningRunes int
	ContentRunes   int
	Elapsed        time.Duration
}

// StreamHandler — the callbacks of a stream. Both are optional and run on the goroutine that reads the
// stream: a callback that blocks starves the idle watchdog fed by that same goroutine, so a blocked caller
// looks to the stream exactly like a silent model.
type StreamHandler struct {
	// Progress fires as soon as the stream has begun and afterwards at most every StreamSettings.Progress
	// зависает» true for an answer whose visible part starts only after a minute of thinking. It carries no
	// model from finishing — only Delta may end the stream, by returning an error.
	Progress func(info StreamInfo)
	// Delta delivers the answer text as it arrives. Reasoning text is NEVER delivered here: the assistant
	Delta func(text string) error
}

// the first delivered delta a retry is forbidden: the user is already reading that text, and a second attempt
func (c *OpenAI) Stream(ctx context.Context, req Request, h StreamHandler) (Response, error) {
	var start = time.Now()

	// busy for all of it, and pretending otherwise would let a second answer start on the same GPU.
	release, admitErr := c.acquire(ctx)
	if admitErr != nil {
		return Response{}, admitErr
	}
	defer release()

	var total = c.stream.Total
	if deadline, ok := ctx.Deadline(); ok {
		var left = time.Until(deadline)
		if left < total {
			total = left
		}
	}
	if total <= 0 {
		return Response{}, fmt.Errorf("%w: %s", context.DeadlineExceeded, StreamNotice)
	}
	var answerCtx, cancelAnswer = context.WithTimeout(ctx, total)
	defer cancelAnswer()
	var body, model, err = c.marshalChatRequest(req, true, 1)
	if err != nil {
		return Response{}, err
	}

	var lastErr error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		var resp, delivered, err = c.streamAttempt(answerCtx, body, model, attempt, h)
		if err == nil {
			resp.Latency = time.Since(start)
			resp.Attempts = attempt
			return resp, nil
		}
		if ctx.Err() != nil {
			// The caller's deadline is not STREAM_TOTAL_DEADLINE_SECONDS. Keep its identity so the ask handler
			// names TG_REPLY_DEADLINE_SECONDS (or process cancellation) instead of blaming the stream budget.
			return resp, fmt.Errorf("%w (the caller's context ended the stream)", ctx.Err())
		}
		lastErr = err
		// exactly what arrived, because part of it is already on the user's screen. An error and a half
		if delivered {
			// to it by a second attempt; the caller finishes what arrived and says what broke.
			return resp, fmt.Errorf("%w (the stream already delivered text; a second attempt would repeat it)", err)
		}
		if !Retryable(err) {
			return resp, err
		}
		if attempt == c.attempts {
			break
		}
		var wait = retryWait(lastErr, c.backoff, attempt)
		if deadline, ok := answerCtx.Deadline(); ok && time.Until(deadline) <= wait {
			return resp, fmt.Errorf("%w (retry %d of %d skipped: %s)", lastErr, attempt+1, c.attempts, StreamNotice)
		}
		select {
		case <-answerCtx.Done():
			return resp, fmt.Errorf("%w: %s", answerCtx.Err(), StreamNotice)
		case <-time.After(wait):
		}
	}
	return Response{}, fmt.Errorf("%w (after %d attempts)", lastErr, c.attempts)
}

// `message` of the plain path; `reasoning_content` is the field this endpoint fills while it thinks, and
// `stream_options.include_usage`, so this client does not send that field).
type chatStreamToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id"`
	Function chatToolFunction `json:"function"`
}

type collectedStreamToolCall struct {
	call          ToolCall
	arguments     strings.Builder
	argumentRunes int
}

type chatChunk struct {
	Model string `json:"model"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	// Error — a failure a provider reports INSIDE a 200 stream. It used to be dropped with the rest of the
	// unknown fields, so a failed answer looked like an empty one or, worse, like a short answer.
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string               `json:"role"`
			Content          *string              `json:"content"`
			ReasoningContent *string              `json:"reasoning_content"`
			ToolCalls        []chatStreamToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// errStreamDone — the `[DONE]` sentinel and the caller-stopped delta both end the read loop through the
// same error channel of the event parser.
var errStreamDone = errors.New("stream sentinel received")

// deltaStopError — the caller's Delta callback refused the text; it is not a provider failure and must not
// be retried, so it keeps its own type on the way out of the parser.
type deltaStopError struct {
	cause error
}

func (e *deltaStopError) Error() string { return e.cause.Error() }
func (e *deltaStopError) Unwrap() error { return e.cause }

// beatStall tells the idle watchdog that another SSE line arrived. It never blocks: once the watchdog
// has given up (its timer fired and it left the select below), the channel has no reader left, and a
// plain send on a full buffer parks the reading goroutine forever — with the model slot and the chat
// lock it holds. A closed `stalled` is therefore checked first and reported as a stall.
func beatStall(beat chan<- struct{}, stalled <-chan struct{}) bool {
	select {
	case <-stalled:
		return true
	default:
	}
	select {
	case beat <- struct{}{}:
	default:
		// one beat is already queued: the watchdog will reset its timer when it takes it, and a
		// second one would add nothing it could act on.
	}
	return false
}

// streamAttempt — one SSE round trip. The bool it returns says whether any answer text reached the handler:
func (c *OpenAI) streamAttempt(ctx context.Context, body []byte, model string, attempt int, h StreamHandler) (Response, bool, error) {
	// this client may set, so the deadline lives in a context that a watchdog goroutine cancels, and every
	// fires only on a real stall — and then it costs the answer, not the budget of the next one.
	var attemptStart = time.Now()
	var stallCtx, cancelStall = context.WithCancel(ctx)
	defer cancelStall()
	var beat = make(chan struct{}, 1)
	var stalled = make(chan struct{})
	go func() {
		var timer = time.NewTimer(c.stream.Idle)
		defer timer.Stop()
		for {
			select {
			case <-stallCtx.Done():
				return
			case <-beat:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(c.stream.Idle)
			case <-timer.C:
				close(stalled)
				cancelStall()
				return
			}
		}
	}()

	var httpReq, buildErr = http.NewRequestWithContext(stallCtx, http.MethodPost, c.chatURL, bytes.NewReader(body))
	if buildErr != nil {
		return Response{}, false, buildErr
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	var res, doErr = c.client.Do(httpReq)
	if doErr != nil {
		if ctx.Err() != nil {
			return Response{}, false, fmt.Errorf("%w: %s", ctx.Err(), StreamNotice)
		}
		return Response{}, false, doErr
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var payload, _ = io.ReadAll(io.LimitReader(res.Body, 1<<16))
		return Response{}, false, &APIError{
			Status:     res.StatusCode,
			Body:       Flatten(string(payload), BodyLimit),
			RetryAfter: retryAfterHeader(res.Header.Get("Retry-After")),
		}
	}
	// endpoint behind an OpenAI-compatible layer once already). Reading it as SSE would end in ErrEmptyAnswer —
	// a text about an empty model answer for what is really a problem of transport.
	if !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		return Response{}, false, fmt.Errorf("the endpoint answered %d with content-type %q instead of text/event-stream ",
			res.StatusCode, Flatten(res.Header.Get("Content-Type"), 80))
	}

	var (
		answer      strings.Builder
		answerRunes int
		response    = Response{Model: model}
		delivered   bool
		reasoning   int
		finish      string
		toolCalls   = map[int]*collectedStreamToolCall{}
	)
	var lastProgress time.Time
	var progress = func() {
		if h.Progress == nil {
			return
		}
		if !lastProgress.IsZero() && time.Since(lastProgress) < c.stream.Progress {
			return
		}
		lastProgress = time.Now()
		h.Progress(StreamInfo{
			Model:          response.Model,
			ReasoningRunes: reasoning,
			ContentRunes:   answerRunes,
			Elapsed:        time.Since(attemptStart),
		})
	}

	var scanner = bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), StreamLineLimit)

	// One SSE event may span several `data:` lines (the spec joins them with "\n"), so the payload is
	// collected until the blank line that ends the event. A provider that reports a failure inside a 200
	// stream puts {"error":…} in the same place an answer would be — that used to be dropped silently.
	var pending []string
	var streamErr error
	var process = func(data string) error {
		if data == "[DONE]" {
			return errStreamDone
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil // one broken event must not throw away the answer next to it
		}
		if chunk.Error != nil && strings.TrimSpace(chunk.Error.Message) != "" {
			return &APIError{Status: http.StatusOK, Body: Flatten(chunk.Error.Message, BodyLimit), Transient: true}
		}
		if chunk.Model != "" {
			response.Model = chunk.Model
		}
		if chunk.Usage != nil {
			var in = int64(chunk.Usage.PromptTokens)
			var out = int64(chunk.Usage.CompletionTokens)
			response.TokensIn, response.TokensOut = &in, &out
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue // only the first completion is this request's answer
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = *choice.FinishReason // `length` here means the model ran out of context
			}
			for _, call := range choice.Delta.ToolCalls {
				if call.Index < 0 || call.Index >= StreamToolCallLimit {
					return fmt.Errorf("%w: tool call index %d is outside 0..%d", ErrStreamLimit, call.Index, StreamToolCallLimit-1)
				}
				current := toolCalls[call.Index]
				argumentRunes := utf8.RuneCountInString(call.Function.Arguments)
				usedRunes := 0
				if current != nil {
					usedRunes = current.argumentRunes
				}
				if argumentRunes > StreamToolArgumentLimit-usedRunes {
					return fmt.Errorf("%w: tool call %d arguments exceed %d runes", ErrStreamLimit, call.Index, StreamToolArgumentLimit)
				}
				if current == nil {
					current = &collectedStreamToolCall{}
					toolCalls[call.Index] = current
				}
				if call.ID != "" {
					current.call.ID = call.ID
				}
				if call.Function.Name != "" {
					current.call.Name = call.Function.Name
				}
				current.arguments.WriteString(call.Function.Arguments)
				current.argumentRunes += argumentRunes
			}
			if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
				reasoning += utf8.RuneCountInString(*choice.Delta.ReasoningContent)
				progress() // the placeholder of UX: the model thinks, the answer has not started
			}
			if choice.Delta.Content == nil || *choice.Delta.Content == "" {
				continue
			}
			var delta = *choice.Delta.Content
			deltaRunes := utf8.RuneCountInString(delta)
			if deltaRunes > StreamAnswerLimit-answerRunes {
				return fmt.Errorf("%w: answer content exceeds %d runes", ErrStreamLimit, StreamAnswerLimit)
			}
			answer.WriteString(delta)
			answerRunes += deltaRunes
			delivered = true
			if h.Delta != nil {
				if err := h.Delta(delta); err != nil {
					// the caller stopped the stream itself: no retry, and the text it already has stays
					return &deltaStopError{cause: err}
				}
			}
		}
		if lastProgress.IsZero() {
			progress() // the first event started the stream: the placeholder may be posted now
		}
		return nil
	}

scan:
	for scanner.Scan() {
		if beatStall(beat, stalled) {
			break scan // the watchdog gave up on this stream: stop reading a body nobody waits for
		}
		var line = strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if len(pending) == 0 {
				continue
			}
			var data = strings.Join(pending, "\n")
			pending = pending[:0]
			if err := process(data); err != nil {
				if errors.Is(err, errStreamDone) {
					break scan
				}
				streamErr = err
				break scan
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, ":"), strings.HasPrefix(line, "event:"):
			// a `:` line is a keepalive comment (measured: 2–3 per answer); `event:` names an event kind
			// this API does not use. None of them carries an answer.
			continue
		case strings.HasPrefix(line, "data:"):
			pending = append(pending, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		default:
			continue // anything else belongs to no event this client knows
		}
	}
	if streamErr == nil && len(pending) > 0 {
		// a provider that closes the stream without the trailing blank line still sent one event
		if err := process(strings.Join(pending, "\n")); err != nil && !errors.Is(err, errStreamDone) {
			streamErr = err
		}
	}
	var scanErr = scanner.Err()
	response.Content, response.ReasoningRunes, response.FinishReason = answer.String(), reasoning, finish
	if len(toolCalls) > 0 {
		indexes := make([]int, 0, len(toolCalls))
		for index := range toolCalls {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		response.ToolCalls = make([]ToolCall, 0, len(indexes))
		for _, index := range indexes {
			collected := toolCalls[index]
			collected.call.Arguments = collected.arguments.String()
			response.ToolCalls = append(response.ToolCalls, collected.call)
		}
	}
	var deltaStop *deltaStopError
	if errors.As(streamErr, &deltaStop) {
		return response, true, deltaStop.cause
	}
	if streamErr != nil {
		return response, delivered, streamErr
	}

	select {
	case <-stalled:
		return response, delivered, fmt.Errorf("%w: %s", ErrStreamStalled, StreamNotice)
	default:
	}
	if scanErr != nil {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return response, delivered, fmt.Errorf("%w: %s", ErrStreamTooLong, StreamNotice)
			}
			return response, delivered, ctx.Err() // the process is stopping
		}
		if delivered {
			return response, true, fmt.Errorf("the stream broke after delivering text: %w", scanErr)
		}
		return response, false, &url.Error{Op: "stream", URL: c.chatURL, Err: scanErr}
	}
	if strings.TrimSpace(answer.String()) == "" && len(response.ToolCalls) == 0 {
		return response, delivered, ErrEmptyAnswer
	}
	return response, delivered, nil
}
