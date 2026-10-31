// stream_test.go — the §15 «стриминг ответов» at the model boundary (§16 change 34). Every shape replayed
// here is a shape MEASURED on the real endpoint of §14 with `zzscratch/streamprobe`: `delta.content` for the
// answer, `delta.reasoning_content` for the thinking, keepalive comment lines, a `[DONE]` sentinel, `usage`
// inside the stream. The budgets of §7 are set in milliseconds in these tests: a stall and a total cap have
// to be measured, not waited for (§9: a test that sleeps a minute proves nothing in that minute).
package model

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bot/internal/testutil"
)

// streamRecorder — what the handler of §15 was shown, plus the knob that makes Delta stop the stream.
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
				return errors.New("the caller of §15 stopped the stream (a delivery broken beyond repair)")
			}
			return nil
		},
	}
}

// silentHandler — a handler that watches and never stops the stream, for the tests that assert on the stream
// itself and not on what they were shown. Go will not take the address of a composite literal, hence a helper.
func silentHandler() StreamHandler { return (&streamRecorder{}).handler() }

// TestStreamCollectsTheAnswerAndNotTheThinking — the finding of §14 that decides what lands in §5: this model
// streams its thinking as `delta.reasoning_content`, and only `delta.content` is the answer. The answer of
// the two-word probe measured 10 runes of content behind 661 runes of reasoning; the row of §5 and the text in
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
		t.Errorf("Content = %q, want only the delta.content pieces (§5 keeps the answer, not the thinking)", res.Content)
	}
	if res.ReasoningRunes != len([]rune("Thinking: 1. анализ вопроса")) {
		t.Errorf("ReasoningRunes = %d, want the thinking counted for the log line of §10 День 7", res.ReasoningRunes)
	}
	if res.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want the stop of the stream (§13 risk «ответ обрывается» shows up as `length`)", res.FinishReason)
	}
	if res.Model != "qwen-echo" {
		t.Errorf("Model = %q, want the name echoed by the chunks (§6 /model)", res.Model)
	}
	if res.TokensIn == nil || *res.TokensIn != 79 || res.TokensOut == nil || *res.TokensOut != 167 {
		t.Errorf("tokens = %v/%v, want 79/167 from the usage event of §14 (it needs no stream_options)", res.TokensIn, res.TokensOut)
	}
	if res.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", res.Attempts)
	}
	if strings.Join(rec.deltas, "") != "Привет мир" {
		t.Errorf("deltas = %q, want the pieces in order and the reasoning NOT among them", rec.deltas)
	}
	if rec.progresses == 0 {
		t.Error("Progress never fired: §12 UX «сообщение не зависает» needs the placeholder of a thinking model")
	}
	if rec.progresses > 6 {
		t.Errorf("Progress fired %d times for ~11 events with a 20 ms gate — the cadence of §7 is what keeps the caller from rewriting the chat on every token", rec.progresses)
	}
	if rec.maxReasoning == 0 {
		t.Error("no Progress reported a thinking count: §15 cannot say «модель думает» without it (§12 UX)")
	}
	if fake.ChatCalls() != 1 {
		t.Errorf("the fake got %d chat requests, want 1", fake.ChatCalls())
	}
	if !strings.Contains(fake.LastRequestBody(), `"stream":true`) {
		t.Error("the request did not ask for a stream on the wire (§15)")
	}
}

// TestStreamRetriesOnlyWhileNothingHasBeenDelivered — §10 День 3 retries a 5xx, and §15 puts a line under
// that rule: the retry is allowed while no text reached the caller yet.
func TestStreamRetriesOnlyWhileNothingHasBeenDelivered(t *testing.T) {
	var fake = testutil.NewStrata(t, "",
		testutil.StrataReply{Status: 500}, // §10 День 3: 500 is retryable
		testutil.StrataReply{Model: "qwen-echo", StreamContent: []string{"готово"}},
	)
	var c = clientFor(t, fake, Options{Attempts: 2, Stream: StreamSettings{Idle: time.Second, Total: 5 * time.Second}})

	var res, err = c.Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if err != nil {
		t.Fatalf("Stream after a 500: %v", err)
	}
	if res.Content != "готово" || res.Attempts != 2 {
		t.Errorf("Content=%q Attempts=%d, want the answer of the second attempt (§10 День 3)", res.Content, res.Attempts)
	}
	if fake.ChatCalls() != 2 {
		t.Errorf("the fake got %d chat requests, want 2 (a retry before any text is allowed)", fake.ChatCalls())
	}
}

// TestStreamIsNotRetriedOnceTheUserSeesText — after the first delivered delta the answer is on the screen of
// the user. A second attempt would append the same text twice and put a second assistant row into §5: that is
// the invariant §10 День 3 was written for.
func TestStreamIsNotRetriedOnceTheUserSeesText(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{
		Model: "qwen-echo", StreamContent: []string{"уже", " видно"}, StreamStall: 80 * time.Millisecond,
	})
	var c = clientFor(t, fake, Options{Attempts: 2, Backoff: time.Millisecond,
		Stream: StreamSettings{Idle: 20 * time.Millisecond, Total: 5 * time.Second}})

	var res, err = c.Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if err == nil {
		t.Fatal("Stream ended with no error after a silent stall (§7 STREAM_IDLE_TIMEOUT_SECONDS)")
	}
	if !errors.Is(err, ErrStreamStalled) {
		t.Errorf("err = %v, want it wrapped as ErrStreamStalled so §12 can name the budget that expired", err)
	}
	if !strings.Contains(err.Error(), "already delivered") {
		t.Errorf("err = %v, want it to say that a retry is forbidden after delivery (§15)", err)
	}
	if res.Content != "уже видно" {
		t.Errorf("Content = %q, want the PARTIAL answer beside the error (§12: half an answer was really shown)", res.Content)
	}
	if fake.ChatCalls() != 1 {
		t.Errorf("the fake got %d chat requests, want 1 (no retry once text was delivered)", fake.ChatCalls())
	}
}

// TestStreamStallIsNotANetworkError — a stream that went silent is not the network class of §10 День 3, so it
// is not retried even before any text: repeating a model that stopped producing only takes the GPU slot of
// §12 again. The error still satisfies errors.Is(err, context.DeadlineExceeded), which is what the timeout
// branch of the ask handler and the events kind='timeout' of §5 test today.
func TestStreamStallIsNotANetworkError(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{
		Model: "qwen-echo", StreamReasoning: []string{"думаю"}, StreamStall: 80 * time.Millisecond,
	})
	var c = clientFor(t, fake, Options{Attempts: 2, Backoff: time.Millisecond,
		Stream: StreamSettings{Idle: 20 * time.Millisecond, Total: 5 * time.Second}})

	var _, err = c.Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if !errors.Is(err, ErrStreamStalled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want ErrStreamStalled wrapping context.DeadlineExceeded (§12 budget text, §5 timeout row)", err)
	}
	if fake.ChatCalls() != 1 {
		t.Errorf("the fake got %d chat requests, want 1 (a silent model is not retried)", fake.ChatCalls())
	}
}

// TestStreamTotalCapEndsALongAnswer — STREAM_TOTAL_DEADLINE_SECONDS of §7 is the ceiling of one answer: the
// GPU of §12 is bounded, so «an answer that never ends» is a failure mode, not a feature.
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
		t.Errorf("err = %v, want ErrStreamTooLong (§7 STREAM_TOTAL_DEADLINE_SECONDS)", err)
	}
	if errors.Is(err, ErrStreamStalled) {
		t.Errorf("err = %v, want the total cap and NOT a stall: the two budgets of §15 answer different questions", err)
	}
}

// TestStreamRefusesAnEndpointThatIgnoredTheStream — a proxy that answers a stream request with plain JSON
// (the OpenAI-compatible layer of §14 item 5 is exactly such a layer) must not end as ErrEmptyAnswer: the
// problem is transport, and the message has to say so.
func TestStreamRefusesAnEndpointThatIgnoredTheStream(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{Model: "qwen-echo", Content: "ответ без стрима"})
	var c = clientFor(t, fake, Options{Attempts: 2, Stream: StreamSettings{Idle: time.Second, Total: 5 * time.Second}})

	var _, err = c.Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if err == nil || !strings.Contains(err.Error(), "text/event-stream") {
		t.Errorf("err = %v, want the content-type of a stream that was ignored (§15)", err)
	}
	if fake.ChatCalls() != 1 {
		t.Errorf("the fake got %d chat requests, want 1 (a 200 of the wrong shape is not a network error)", fake.ChatCalls())
	}
}

// TestStreamOfOnlyThinkingIsAnEmptyAnswer — §10 День 3: an empty answer is an ERROR and never a stored row.
// A model that thinks a lot and says nothing is that case in its modern form.
func TestStreamOfOnlyThinkingIsAnEmptyAnswer(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{
		Model: "qwen-echo", StreamReasoning: []string{"Thinking", " very", " long"},
	})
	var c = clientFor(t, fake, Options{Stream: StreamSettings{Idle: time.Second, Total: 5 * time.Second}})

	var _, err = c.Stream(context.Background(), oneRequest("вопрос"), silentHandler())
	if !errors.Is(err, ErrEmptyAnswer) {
		t.Errorf("err = %v, want ErrEmptyAnswer — the thinking is not an answer (§10 День 3)", err)
	}
}

// TestStreamWithoutTheDoneSentinel — the fake closes instead of sending `[DONE]`. A provider that ends the
// stream by closing the connection still has to deliver the answer it already sent (§15).
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

// TestStreamCallerMayStopIt — Delta returns an error when the delivery of §15 is broken beyond repair (the
// user blocked the bot, say). The stream ends there, without a retry, and the half answer it managed to write
// travels to the handler so §5 and the log see exactly what the user saw.
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
