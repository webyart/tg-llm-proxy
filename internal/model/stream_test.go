// answer, `delta.reasoning_content` for the thinking, keepalive comment lines, a `[DONE]` sentinel, `usage`
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"bot/internal/testutil"
)

type streamRecorder struct {
	deltas       []string
	progresses   int
	maxReasoning int // the largest thinking count Progress ever reported
	stopAfter    int // answer Delta with an error after this many deltas (0 = never)
}

func (r *streamRecorder) handler() StreamHandler {
	return StreamHandler{
		Progress: func(info StreamInfo) {
			r.progresses++
			if info.ReasoningRunes > r.maxReasoning {
				r.maxReasoning = info.ReasoningRunes
			}
		},
		Delta: func(text string) error {
			r.deltas = append(r.deltas, text)
			if r.stopAfter > 0 && len(r.deltas) >= r.stopAfter {
				return errors.New("the caller of stopped the stream (a delivery broken beyond repair)")
			}
			return nil
		},
	}
}

// silentHandler — a handler that watches and never stops the stream, for the tests that assert on the stream
// itself and not on what they were shown. Go will not take the address of a composite literal, hence a helper.
func silentHandler() StreamHandler { return (&streamRecorder{}).handler() }

// streams its thinking as `delta.reasoning_content`, and only `delta.content` is the answer. The answer of
// the chat are the content, in both the plain and the streamed path.
func TestStreamCollectsTheAnswerAndNotTheThinking(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{
		Model: "qwen-echo", TokensIn: 79, TokensOut: 167, StreamUsage: true,
		StreamReasoning: []string{"Thinking", ":", " 1.", " ", "анализ", " вопроса"},
		StreamContent:   []string{"Привет", " ", "мир"},
		StreamKeepalive: true, StreamGarbage: 1, StreamFinish: "stop",
		// 8 ms between the events: the fake produces ~11 of them in ~90 ms, so the Progress gate of 20 ms
		// below is visible as a count far below the number of events, and the thinking phase (6 events) is
		// long enough for one Progress to carry a non-zero thinking count.
		StreamGap: 8 * time.Millisecond,
	})
	var c = clientFor(t, fake, Options{Stream: StreamSettings{Idle: time.Second, Total: 5 * time.Second, Progress: 20 * time.Millisecond}})
	var rec = &streamRecorder{}

	var res, err = c.Stream(context.Background(), oneRequest("вопрос"), rec.handler())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if res.Content != "Привет мир" {
		t.Errorf("Content = %q, want only the delta.content pieces ", res.Content)
	}
	if res.ReasoningRunes != len([]rune("Thinking: 1. анализ вопроса")) {
		t.Errorf("ReasoningRunes = %d, want the thinking counted for the log line of", res.ReasoningRunes)
	}
	if res.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want the stop of the stream ", res.FinishReason)
	}
	if res.Model != "qwen-echo" {
		t.Errorf("Model = %q, want the name echoed by the chunks ", res.Model)
	}
	if res.TokensIn == nil || *res.TokensIn != 79 || res.TokensOut == nil || *res.TokensOut != 167 {
		t.Errorf("tokens = %v/%v, want 79/167 from the usage event of (it needs no stream_options)", res.TokensIn, res.TokensOut)
	}
	if res.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", res.Attempts)
	}
	if strings.Join(rec.deltas, "") != "Привет мир" {
		t.Errorf("deltas = %q, want the pieces in order and the reasoning NOT among them", rec.deltas)
	}
	if rec.progresses == 0 {
		t.Error("Progress never fired: UX «сообщение не зависает» needs the placeholder of a thinking model")
	}
	if rec.progresses > 6 {
		t.Errorf("Progress fired %d times for ~11 events with a 20 ms gate — the cadence of is what keeps the caller from rewriting the chat on every token", rec.progresses)
	}
	if rec.maxReasoning == 0 {
		t.Error("no Progress reported a thinking count: cannot say «модель думает» without it ")
	}
	if fake.ChatCalls() != 1 {
		t.Errorf("the fake got %d chat requests, want 1", fake.ChatCalls())
	}
	if !strings.Contains(fake.LastRequestBody(), `"stream":true`) {
		t.Error("the request did not ask for a stream on the wire ")
	}
}

// that rule: the retry is allowed while no text reached the caller yet.
func TestStreamRetriesOnlyWhileNothingHasBeenDelivered(t *testing.T) {
	var fake = testutil.NewStrata(t, "",
		testutil.StrataReply{Status: 500}, //: 500 is retryable
		testutil.StrataReply{Model: "qwen-echo", StreamContent: []string{"готово"}},
	)
	var c = clientFor(t, fake, Options{Attempts: 2, Stream: StreamSettings{Idle: time.Second, Total: 5 * time.Second}})

	var res, err = c.Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if err != nil {
		t.Fatalf("Stream after a 500: %v", err)
	}
	if res.Content != "готово" || res.Attempts != 2 {
		t.Errorf("Content=%q Attempts=%d, want the answer of the second attempt ", res.Content, res.Attempts)
	}
	if fake.ChatCalls() != 2 {
		t.Errorf("the fake got %d chat requests, want 2 (a retry before any text is allowed)", fake.ChatCalls())
	}
}

// TestStreamIsNotRetriedOnceTheUserSeesText — after the first delivered delta the answer is on the screen of
func TestStreamIsNotRetriedOnceTheUserSeesText(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{
		Model: "qwen-echo", StreamContent: []string{"уже", " видно"}, StreamStall: 80 * time.Millisecond,
	})
	var c = clientFor(t, fake, Options{Attempts: 2, Backoff: time.Millisecond,
		Stream: StreamSettings{Idle: 20 * time.Millisecond, Total: 5 * time.Second}})

	var res, err = c.Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if err == nil {
		t.Fatal("Stream ended with no error after a silent stall ")
	}
	if !errors.Is(err, ErrStreamStalled) {
		t.Errorf("err = %v, want it wrapped as ErrStreamStalled so can name the budget that expired", err)
	}
	if !strings.Contains(err.Error(), "already delivered") {
		t.Errorf("err = %v, want it to say that a retry is forbidden after delivery ", err)
	}
	if res.Content != "уже видно" {
		t.Errorf("Content = %q, want the PARTIAL answer beside the error ", res.Content)
	}
	if fake.ChatCalls() != 1 {
		t.Errorf("the fake got %d chat requests, want 1 (no retry once text was delivered)", fake.ChatCalls())
	}
}

// is not retried even before any text: repeating a model that stopped producing only takes the GPU slot of
func TestStreamStallIsNotANetworkError(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{
		Model: "qwen-echo", StreamReasoning: []string{"думаю"}, StreamStall: 80 * time.Millisecond,
	})
	var c = clientFor(t, fake, Options{Attempts: 2, Backoff: time.Millisecond,
		Stream: StreamSettings{Idle: 20 * time.Millisecond, Total: 5 * time.Second}})

	var _, err = c.Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if !errors.Is(err, ErrStreamStalled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want ErrStreamStalled wrapping context.DeadlineExceeded ", err)
	}
	if fake.ChatCalls() != 1 {
		t.Errorf("the fake got %d chat requests, want 1 (a silent model is not retried)", fake.ChatCalls())
	}
}

func TestStreamTotalCapEndsALongAnswer(t *testing.T) {
	var pieces = make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		pieces = append(pieces, "кусок ")
	}
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{
		Model: "qwen-echo", StreamContent: pieces, StreamGap: 20 * time.Millisecond,
	})
	var c = clientFor(t, fake, Options{Attempts: 1, Stream: StreamSettings{Idle: time.Second, Total: 100 * time.Millisecond}})

	var _, err = c.Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if !errors.Is(err, ErrStreamTooLong) {
		t.Errorf("err = %v, want ErrStreamTooLong ", err)
	}
	if errors.Is(err, ErrStreamStalled) {
		t.Errorf("err = %v, want the total cap and NOT a stall: the two budgets of answer different questions", err)
	}
}

func TestStreamCallerDeadlineIsNotTheStreamTotalBudget(t *testing.T) {
	var pieces = make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		pieces = append(pieces, "кусок ")
	}
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{
		Model: "qwen-echo", StreamContent: pieces, StreamGap: 20 * time.Millisecond,
	})
	var c = clientFor(t, fake, Options{Attempts: 1, Stream: StreamSettings{Idle: time.Second, Total: 5 * time.Second}})
	var ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var _, err = c.Stream(ctx, oneRequest("вопрос"), silentHandler())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}
	if errors.Is(err, ErrStreamTooLong) || errors.Is(err, ErrStreamStalled) {
		t.Errorf("err = %v, want the caller's deadline, not a stream budget that did not expire", err)
	}
}

// TestStreamRefusesAnEndpointThatIgnoredTheStream — a proxy that answers a stream request with plain JSON
// problem is transport, and the message has to say so.
func TestStreamRefusesAnEndpointThatIgnoredTheStream(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{Model: "qwen-echo", Content: "ответ без стрима"})
	var c = clientFor(t, fake, Options{Attempts: 2, Stream: StreamSettings{Idle: time.Second, Total: 5 * time.Second}})

	var _, err = c.Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if err == nil || !strings.Contains(err.Error(), "text/event-stream") {
		t.Errorf("err = %v, want the content-type of a stream that was ignored ", err)
	}
	if fake.ChatCalls() != 1 {
		t.Errorf("the fake got %d chat requests, want 1 (a 200 of the wrong shape is not a network error)", fake.ChatCalls())
	}
}

// A model that thinks a lot and says nothing is that case in its modern form.
func TestStreamOfOnlyThinkingIsAnEmptyAnswer(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{
		Model: "qwen-echo", StreamReasoning: []string{"Thinking", " very", " long"},
	})
	var c = clientFor(t, fake, Options{Stream: StreamSettings{Idle: time.Second, Total: 5 * time.Second}})

	var _, err = c.Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if !errors.Is(err, ErrEmptyAnswer) {
		t.Errorf("err = %v, want ErrEmptyAnswer — the thinking is not an answer ", err)
	}
}

// TestStreamWithoutTheDoneSentinel — the fake closes instead of sending `[DONE]`. A provider that ends the
func TestStreamWithoutTheDoneSentinel(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{
		Model: "qwen-echo", StreamContent: []string{"хвост"}, StreamNoDone: true,
	})
	var c = clientFor(t, fake, Options{Stream: StreamSettings{Idle: time.Second, Total: 5 * time.Second}})

	var res, err = c.Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if res.Content != "хвост" {
		t.Errorf("Content = %q, want the pieces that arrived before the close", res.Content)
	}
}

// user blocked the bot, say). The stream ends there, without a retry, and the half answer it managed to write
func TestStreamCallerMayStopIt(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{
		Model: "qwen-echo", StreamContent: []string{"раз ", "два ", "три "},
	})
	var c = clientFor(t, fake, Options{Attempts: 2, Backoff: time.Millisecond,
		Stream: StreamSettings{Idle: time.Second, Total: 5 * time.Second}})
	var rec = &streamRecorder{stopAfter: 2}

	var res, err = c.Stream(context.Background(), oneRequest("вопрос"), rec.handler())
	if err == nil || !strings.Contains(err.Error(), "stopped the stream") {
		t.Errorf("err = %v, want the error of the caller to come back unchanged", err)
	}
	if res.Content != "раз два " {
		t.Errorf("Content = %q, want the answer up to the point where the caller stopped", res.Content)
	}
	if fake.ChatCalls() != 1 {
		t.Errorf("the fake got %d chat requests, want 1 (the caller said stop — that is not a retry reason)", fake.ChatCalls())
	}
}

func streamTestEvent(t *testing.T, delta any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": delta}},
	})
	if err != nil {
		t.Fatalf("marshal stream event: %v", err)
	}
	return string(payload)
}

func toolStreamEvent(t *testing.T, index int, id string, arguments string) string {
	t.Helper()
	return streamTestEvent(t, map[string]any{
		"tool_calls": []any{map[string]any{
			"index": index,
			"id":    id,
			"function": map[string]any{
				"name":      "lookup",
				"arguments": arguments,
			},
		}},
	})
}

func serveTestStream(t *testing.T, events []string, continuous bool) (string, func() int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		write := func(event string) bool {
			if _, err := fmt.Fprintf(w, "data: %s\n\n", event); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}
		for {
			for _, event := range events {
				select {
				case <-r.Context().Done():
					return
				default:
				}
				if !write(event) {
					return
				}
			}
			if !continuous {
				write("[DONE]")
				return
			}
			// deliberate: a live SSE endpoint paces its events instead of sending them in one burst; the
			// continuous mode of this fake exists to make the reader meet events one at a time.
			time.Sleep(time.Millisecond)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL, requests.Load
}

func limitedStreamClient(t *testing.T, url string) *OpenAI {
	t.Helper()
	return clientForNoFake(t, url, Options{
		Attempts: 3,
		Stream:   StreamSettings{Idle: time.Second, Total: 5 * time.Second},
	})
}

func TestStreamAcceptsAnswerAtRuneLimit(t *testing.T) {
	content := strings.Repeat("界", StreamAnswerLimit)
	url, requests := serveTestStream(t, []string{streamTestEvent(t, map[string]any{"content": content})}, false)
	rec := &streamRecorder{}

	res, err := limitedStreamClient(t, url).Stream(context.Background(), oneRequest("вопрос"), rec.handler())
	if err != nil {
		t.Fatalf("Stream at the exact rune limit: %v", err)
	}
	if res.Content != content || strings.Join(rec.deltas, "") != content {
		t.Fatalf("exact-limit answer changed: response=%d runes deltas=%d runes", utf8.RuneCountInString(res.Content), utf8.RuneCountInString(strings.Join(rec.deltas, "")))
	}
	if requests() != 1 {
		t.Fatalf("requests = %d, want 1", requests())
	}
}

func TestStreamContinuousContentStopsAtRuneLimit(t *testing.T) {
	const deltaRunes = 1024
	event := streamTestEvent(t, map[string]any{"content": strings.Repeat("界", deltaRunes)})
	url, requests := serveTestStream(t, []string{event}, true)
	rec := &streamRecorder{}

	res, err := limitedStreamClient(t, url).Stream(context.Background(), oneRequest("вопрос"), rec.handler())
	if !errors.Is(err, ErrStreamLimit) {
		t.Fatalf("err = %v, want ErrStreamLimit", err)
	}
	gotRunes := utf8.RuneCountInString(res.Content)
	if gotRunes > StreamAnswerLimit || gotRunes <= StreamAnswerLimit-deltaRunes {
		t.Fatalf("partial answer = %d runes, want the accepted prefix within one delta of %d", gotRunes, StreamAnswerLimit)
	}
	if strings.Join(rec.deltas, "") != res.Content {
		t.Fatal("an overflowing delta reached the answer handler")
	}
	if len(err.Error()) > 256 {
		t.Fatalf("error is not bounded: %d bytes", len(err.Error()))
	}
	if requests() != 1 {
		t.Fatalf("a resource overflow was retried: %d requests", requests())
	}
}

func TestStreamAllowsEightDenseToolCallsAndFragmentedArguments(t *testing.T) {
	prefix := `{"value":"`
	suffix := `"}`
	longArguments := prefix + strings.Repeat("界", StreamToolArgumentLimit-utf8.RuneCountInString(prefix)-utf8.RuneCountInString(suffix)) + suffix
	events := make([]string, 0, 40)
	for _, index := range []int{7, 2, 5, 0, 6, 1, 4, 3} {
		events = append(events, toolStreamEvent(t, index, fmt.Sprintf("call-%d", index), ""))
		if index != 7 {
			events = append(events,
				toolStreamEvent(t, index, "", `{"value":`),
				toolStreamEvent(t, index, "", fmt.Sprintf("%d}", index)),
			)
			continue
		}
		runes := []rune(longArguments)
		for len(runes) > 0 {
			n := min(1024, len(runes))
			events = append(events, toolStreamEvent(t, index, "", string(runes[:n])))
			runes = runes[n:]
		}
	}
	url, requests := serveTestStream(t, events, false)

	res, err := limitedStreamClient(t, url).Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if err != nil {
		t.Fatalf("Stream with eight tool calls: %v", err)
	}
	if len(res.ToolCalls) != StreamToolCallLimit {
		t.Fatalf("tool calls = %d, want %d", len(res.ToolCalls), StreamToolCallLimit)
	}
	for index, call := range res.ToolCalls {
		if call.ID != fmt.Sprintf("call-%d", index) {
			t.Errorf("tool call %d has ID %q, want dense sorted output", index, call.ID)
		}
		if index == 7 && call.Arguments != longArguments {
			t.Errorf("fragmented arguments = %d runes, want exact %d-rune payload", utf8.RuneCountInString(call.Arguments), StreamToolArgumentLimit)
		}
	}
	if requests() != 1 {
		t.Fatalf("requests = %d, want 1", requests())
	}
}

func TestStreamContinuousToolArgumentStopsAtRuneLimit(t *testing.T) {
	event := toolStreamEvent(t, 3, "call-3", strings.Repeat("x", 64))
	url, requests := serveTestStream(t, []string{event}, true)

	res, err := limitedStreamClient(t, url).Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if !errors.Is(err, ErrStreamLimit) {
		t.Fatalf("err = %v, want ErrStreamLimit", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("partial tool calls = %d, want 1", len(res.ToolCalls))
	}
	if got := utf8.RuneCountInString(res.ToolCalls[0].Arguments); got != StreamToolArgumentLimit {
		t.Fatalf("partial arguments = %d runes, want exactly %d", got, StreamToolArgumentLimit)
	}
	if requests() != 1 {
		t.Fatalf("a resource overflow was retried: %d requests", requests())
	}
}

func TestStreamRejectsArbitraryToolCallIndexes(t *testing.T) {
	for _, index := range []int{-1, StreamToolCallLimit, 1_000_000} {
		t.Run(fmt.Sprintf("index-%d", index), func(t *testing.T) {
			url, requests := serveTestStream(t, []string{toolStreamEvent(t, index, "bad", "{}")}, true)
			res, err := limitedStreamClient(t, url).Stream(context.Background(), oneRequest("вопрос"), silentHandler())
			if !errors.Is(err, ErrStreamLimit) || !strings.Contains(err.Error(), "index") {
				t.Fatalf("err = %v, want a bounded tool-index error", err)
			}
			if len(res.ToolCalls) != 0 {
				t.Fatalf("an invalid index allocated tool calls: %+v", res.ToolCalls)
			}
			if requests() != 1 {
				t.Fatalf("an invalid index was retried: %d requests", requests())
			}
		})
	}
}
