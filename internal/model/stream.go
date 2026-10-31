// stream.go — the §15 «стриминг ответов» at the model boundary (§16 change 34). The question the plan left
// open in §14 item 6 («поддерживает ли streaming?») was measured on 2026-10-31 with `zzscratch/streamprobe`
// against the real endpoint of §14, and every branch below exists because of that measurement: SSE works
// (HTTP 200, `text/event-stream`, one `data:` event per token, `data: [DONE]` at the end, keepalive comment
// lines starting with `:`, time to the first event 4–6 ms, cadence ~48 events/s, `usage` arrives inside the
// stream without `stream_options`). The finding that changed the design: this model streams
// `delta.reasoning_content` as a field of its own — the first ~200 events of a two-word answer were
// «Thinking: 1. **Analyze the Request…**» while `delta.content` carried the 10 runes of the real answer, and
// the non-streamed response separates them the same way (measured: content 10 runes, reasoning 661 runes).
// So the text of the assistant row of §5 does not change when the stream is turned on, and reasoning never
// reaches the chat either.
//
// The two budgets below are separate rows of §7 by decision, and this is why: the measured pause between two
// events of a healthy answer is ~20 ms, while the wall of an answer is a property of the answer, not of a
// stall. One number cannot tell a stalled stream from a long one — and the long one is exactly what §15
// exists for: the answer of `row=6` measured 1m56s and fitted no 60 s budget at all.
package model

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The defaults of the streaming budgets, used when Options.Stream leaves a field zero. The bounds the loader
// of §7 puts on the ENV rows behind them are narrower, and config_test owns them.
const (
	DefaultStreamIdle     = 30 * time.Second
	DefaultStreamTotal    = 300 * time.Second
	DefaultStreamProgress = time.Second
	// StreamLineLimit — one SSE line. An OpenAI-compatible chunk is a few hundred bytes; a megabyte is the
	// room of a provider that puts a whole answer into one line, and more than that is not an answer.
	StreamLineLimit = 1 << 20
)

// ErrStreamStalled — the stream went silent longer than STREAM_IDLE_TIMEOUT_SECONDS of §7. It wraps
// context.DeadlineExceeded on purpose: a caller that only asks «did a budget expire?» (§12 UX, the events
// kind='timeout' of §5) keeps working, while the ask handler can name the budget that REALLY expired by
// testing this sentinel first — the same trap §12 measured for the two budgets of §7 («one failure described
// by two different numbers in the two places an operator compares»).
var ErrStreamStalled = fmt.Errorf("%w: the stream went silent longer than STREAM_IDLE_TIMEOUT_SECONDS (§15)", context.DeadlineExceeded)

// ErrStreamTooLong — the answer is still arriving but already costs more than STREAM_TOTAL_DEADLINE_SECONDS
// of §7. The GPU is bounded (§12 performance), so an answer that never ends is a failure mode, not a feature.
var ErrStreamTooLong = fmt.Errorf("%w: the streamed answer outlived STREAM_TOTAL_DEADLINE_SECONDS (§15)", context.DeadlineExceeded)

// StreamNotice — the line the stream errors share with TelegramBudgetNotice of §10 День 3.
const StreamNotice = "the stream was given up by its budget of §7 (STREAM_IDLE_TIMEOUT_SECONDS / STREAM_TOTAL_DEADLINE_SECONDS)"

// StreamSettings — the budgets of one streamed answer, all three from §7 (§16 change 34).
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
// because that is the unit Telegram measures (§12 UX) and because the provider's own usage counts arrive only
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
	// while the model thinks without producing answer text. This is what keeps §12 UX «сообщение не
	// зависает» true for an answer whose visible part starts only after a minute of thinking. It carries no
	// error on purpose: a Telegram edit that failed is skipped by the session of §15 and must never stop the
	// model from finishing — only Delta may end the stream, by returning an error.
	Progress func(info StreamInfo)
	// Delta delivers the answer text as it arrives. Reasoning text is NEVER delivered here: the assistant
	// row of §5 and the text in the chat are made of `content` in both the streamed and the plain path, so
	// turning the stream on cannot change what §5 remembers.
	Delta func(text string) error
}

// Stream — one chat request answered as a stream (§15). It satisfies the same write invariant as Complete:
// the caller gets ONE Response and writes ONE assistant row of §5.
//
// The retries of §10 День 3 apply only while NOTHING of the answer has been handed to the handler yet. After
// the first delivered delta a retry is forbidden: the user is already reading that text, and a second attempt
// would append the answer twice and put two assistant rows into §5.
func (c *OpenAI) Stream(ctx context.Context, req Request, h StreamHandler) (Response, error) {
	var start = time.Now()

	// §12 performance: the slot of MAX_CONCURRENT_MODEL_REQUESTS is held for the WHOLE stream — the GPU is
	// busy for all of it, and pretending otherwise would let a second answer start on the same GPU.
	if cap(c.sem) > 0 {
		select {
		case c.sem <- struct{}{}:
			defer func() { <-c.sem }()
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}

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

	var lastErr error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		var resp, delivered, err = c.streamAttempt(answerCtx, req, attempt, h)
		if err == nil {
			resp.Latency = time.Since(start)
			resp.Attempts = attempt
			return resp, nil
		}
		lastErr = err
		// The Response of a failed stream is PARTIAL on purpose: the handler of §15 has to store and log
		// exactly what arrived, because part of it is already on the user's screen. An error and a half
		// answer travel together here rather than the half answer being thrown away (§12 надёжность).
		if delivered {
			// §10 День 3 + §15: part of the answer is already on the user's screen. Nothing may be appended
			// to it by a second attempt; the caller finishes what arrived and says what broke.
			return resp, fmt.Errorf("%w (the stream already delivered text, a second attempt would repeat it — §15)", err)
		}
		if !Retryable(err) {
			return resp, err
		}
		if attempt == c.attempts {
			break
		}
		var wait = c.backoff * time.Duration(1<<(attempt-1))
		if deadline, ok := answerCtx.Deadline(); ok && time.Until(deadline) <= wait {
			return resp, fmt.Errorf("%w (retry %d of %d skipped: %s)", lastErr, attempt+1, c.attempts, StreamNotice)
		}
		select {
		case <-answerCtx.Done():
			return resp, fmt.Errorf("%w: %s", answerCtx.Err(), StreamNotice)
		case <-time.After(wait):
		}
	}
	return Response{}, fmt.Errorf("%w (after %d attempts, §10 День 3)", lastErr, c.attempts)
}

// chatChunk — one SSE event of a streamed chat.completion (§16 change 34). `delta` is what differs from the
// `message` of the plain path; `reasoning_content` is the field this endpoint fills while it thinks, and
// `usage` may come in a last event that carries no choices at all (measured on §14 without
// `stream_options.include_usage`, so this client does not send that field).
type chatChunk struct {
	Model string `json:"model"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string  `json:"role"`
			Content          *string `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// streamAttempt — one SSE round trip. The bool it returns says whether any answer text reached the handler:
// it is the line §15 must not cross twice, because text already shown to the user cannot be shown again by a
// retry (§10 День 3 keeps exactly one assistant row per request).
func (c *OpenAI) streamAttempt(ctx context.Context, req Request, attempt int, h StreamHandler) (Response, bool, error) {
	var model = req.Model
	if model == "" {
		model = c.model
	}
	if model == "" {
		return Response{}, false, fmt.Errorf("the model of §7 (STRATA_MODEL) is not set, attempt %d", attempt)
	}

	var body, err = json.Marshal(chatRequest{Model: model, Messages: toWire(req.Messages), Stream: true})
	if err != nil {
		return Response{}, false, err
	}

	// The idle watchdog of §7 (STREAM_IDLE_TIMEOUT_SECONDS). The body of a response carries no read deadline
	// this client may set, so the deadline lives in a context that a watchdog goroutine cancels, and every
	// SSE line feeds that watchdog. A healthy answer produces a line every ~20 ms (measured on §14), so this
	// fires only on a real stall — and then it costs the answer, not the budget of the next one.
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

	var httpReq, buildErr = http.NewRequestWithContext(stallCtx, http.MethodPost, c.chatURL, strings.NewReader(string(body)))
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
		return Response{}, false, &APIError{Status: res.StatusCode, Body: Flatten(string(payload), BodyLimit)}
	}
	// A 200 that is NOT an event stream is a proxy that quietly ignored `"stream": true` (§14 item 5 put the
	// endpoint behind an OpenAI-compatible layer once already). Reading it as SSE would end in ErrEmptyAnswer —
	// a text about an empty model answer for what is really a problem of transport.
	if !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		return Response{}, false, fmt.Errorf("the endpoint answered %d with content-type %q instead of text/event-stream (§15: it ignored the stream)",
			res.StatusCode, Flatten(res.Header.Get("Content-Type"), 80))
	}

	var (
		answer    strings.Builder
		response  = Response{Model: model}
		delivered bool
		reasoning int
		finish    string
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
			ContentRunes:   len([]rune(answer.String())),
		})
	}

	var scanner = bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), StreamLineLimit)
	for scanner.Scan() {
		beat <- struct{}{} // the watchdog: something arrived, the stall clock restarts
		var line = strings.TrimRight(scanner.Text(), "\r")
		switch {
		case line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:"):
			// SSE keeps events apart with an empty line; a `:` line is a keepalive comment (measured: 2–3 per
			// answer); `event:` names an event kind this API does not use. None of them carries an answer.
			continue
		case strings.HasPrefix(line, "data:"):
			// the one line shape of the OpenAI-compatible stream
		default:
			continue // anything else belongs to no event this client knows
		}

		var data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break // the sentinel of the stream (measured on §14)
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // one broken line of a provider must not throw away the answer next to it
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
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = *choice.FinishReason // `length` here means the model ran out of context (§13 risk)
			}
			if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
				reasoning += len([]rune(*choice.Delta.ReasoningContent))
				progress() // the placeholder of §12 UX: the model thinks, the answer has not started
				continue
			}
			if choice.Delta.Content == nil || *choice.Delta.Content == "" {
				continue
			}
			var delta = *choice.Delta.Content
			answer.WriteString(delta)
			delivered = true
			if h.Delta != nil {
				if err := h.Delta(delta); err != nil {
					response.Content, response.ReasoningRunes, response.FinishReason = answer.String(), reasoning, finish
					return response, true, err // the caller stopped the stream itself: no retry, §15
				}
			}
		}
		if lastProgress.IsZero() {
			progress() // the first event started the stream: the placeholder may be posted now
		}
	}
	var scanErr = scanner.Err()
	response.Content, response.ReasoningRunes, response.FinishReason = answer.String(), reasoning, finish

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
			return response, delivered, ctx.Err() // the process is stopping (the signal ctx of §10 День 1)
		}
		if delivered {
			return response, true, fmt.Errorf("the stream broke after delivering text: %w", scanErr)
		}
		// Nothing reached the user yet, so this is the network class of §10 День 3 and Retryable says yes.
		return response, false, &url.Error{Op: "stream", URL: c.chatURL, Err: scanErr}
	}
	if strings.TrimSpace(answer.String()) == "" {
		// The rule of §10 День 3 survives the stream: an empty answer is an error, never a stored row.
		return response, delivered, ErrEmptyAnswer
	}
	return response, delivered, nil
}
