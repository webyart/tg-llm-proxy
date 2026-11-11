package model

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bot/internal/testutil"
)

// streamEvent writes one SSE event and flushes it, so the client can observe the delay before the
// next event instead of receiving the whole response at once.
func streamEvent(t *testing.T, w http.ResponseWriter, payload string) {
	t.Helper()
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func contentChunk(text string) string {
	return fmt.Sprintf(`{"model":"stream-model","choices":[{"index":0,"delta":{"content":%q}}]}`, text)
}

func TestStreamMeasuresTimeToFirstContentDelta(t *testing.T) {
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// An empty content chunk, a reasoning chunk and a tool-call chunk all arrive before the
		// answer: none of them is the first token the user can see.
		streamEvent(t, w, `{"model":"stream-model","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`)
		streamEvent(t, w, `{"model":"stream-model","choices":[{"index":0,"delta":{"reasoning_content":"thinking"}}]}`)
		streamEvent(t, w, `{"model":"stream-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{}"}}]}}]}`)
		time.Sleep(60 * time.Millisecond)
		streamEvent(t, w, contentChunk("При"))
		streamEvent(t, w, contentChunk("вет"))
		streamEvent(t, w, `{"model":"stream-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	var client, err = New(Options{
		ChatURL:  server.URL,
		Attempts: 1,
		Timeout:  5 * time.Second,
		Stream:   StreamSettings{Idle: time.Second, Total: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var deltas []string
	var response, streamErr = client.Stream(context.Background(), Request{Model: "stream-model", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, StreamHandler{
		Delta: func(text string) error {
			deltas = append(deltas, text)
			return nil
		},
	})
	if streamErr != nil {
		t.Fatalf("Stream: %v", streamErr)
	}
	if strings.Join(deltas, "") != "Привет" {
		t.Fatalf("deltas = %q, want the answer only", deltas)
	}
	if response.FirstTokenLatency <= 0 {
		t.Fatal("FirstTokenLatency was not measured")
	}
	if response.FirstTokenLatency < 40*time.Millisecond {
		t.Fatalf("FirstTokenLatency = %s, want at least the deliberate 60 ms before the first content delta", response.FirstTokenLatency)
	}
	if response.FirstTokenLatency >= 2*time.Second {
		t.Fatalf("FirstTokenLatency = %s, want the measurement of this attempt and not of the whole call", response.FirstTokenLatency)
	}
	if response.Latency < response.FirstTokenLatency {
		t.Fatalf("Latency = %s is shorter than FirstTokenLatency = %s", response.Latency, response.FirstTokenLatency)
	}
}

func TestStreamToolCallsAndEmptyChunksDoNotStartTTFT(t *testing.T) {
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		streamEvent(t, w, `{"model":"stream-model","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`)
		streamEvent(t, w, `{"model":"stream-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expression\":\"2+2\"}"}}]}}]}`)
		streamEvent(t, w, `{"model":"stream-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	var client, err = New(Options{
		ChatURL:  server.URL,
		Attempts: 1,
		Timeout:  5 * time.Second,
		Stream:   StreamSettings{Idle: time.Second, Total: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var response, streamErr = client.Stream(context.Background(), Request{Model: "stream-model"}, StreamHandler{})
	if streamErr != nil {
		t.Fatalf("Stream: %v", streamErr)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "calculator" {
		t.Fatalf("tool calls = %+v, want the calculator call", response.ToolCalls)
	}
	if response.FirstTokenLatency != 0 {
		t.Fatalf("FirstTokenLatency = %s, want zero for a tool-call-only stream", response.FirstTokenLatency)
	}
}

func TestStreamRetryStartsANewTTFTMeasurement(t *testing.T) {
	var calls atomic.Int32
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "try again", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		time.Sleep(60 * time.Millisecond)
		streamEvent(t, w, contentChunk("готово"))
		streamEvent(t, w, `{"model":"stream-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	var client, err = New(Options{
		ChatURL:  server.URL,
		Attempts: 2,
		Backoff:  500 * time.Millisecond,
		Timeout:  5 * time.Second,
		Stream:   StreamSettings{Idle: time.Second, Total: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var response, streamErr = client.Stream(context.Background(), Request{Model: "stream-model"}, StreamHandler{})
	if streamErr != nil {
		t.Fatalf("Stream: %v", streamErr)
	}
	if response.Attempts != 2 {
		t.Fatalf("Attempts = %d, want 2", response.Attempts)
	}
	// The retry waits at least 500 ms before the second request. A measurement that included that
	// wait could not be below 400 ms.
	if response.FirstTokenLatency >= 400*time.Millisecond {
		t.Fatalf("FirstTokenLatency = %s, want the second attempt measured from its own start", response.FirstTokenLatency)
	}
	if response.FirstTokenLatency < 30*time.Millisecond {
		t.Fatalf("FirstTokenLatency = %s, want the deliberate 60 ms delay of the successful attempt", response.FirstTokenLatency)
	}
}

func TestCompleteLeavesFirstTokenLatencyZero(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{Model: "plain-model", Content: "answer"})
	var client = clientFor(t, fake, Options{})
	var response, err = client.Complete(context.Background(), Request{Model: "plain-model"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.FirstTokenLatency != 0 {
		t.Fatalf("FirstTokenLatency = %s, want zero for a non-streamed answer", response.FirstTokenLatency)
	}
}

// ttftStubClient is a model.Client+Streamer with a fixed answer, used to prove the wrappers keep the
// TTFT of the attempt that actually answered.
type ttftStubClient struct {
	response Response
	err      error
}

func (c *ttftStubClient) Complete(context.Context, Request) (Response, error) {
	return c.response, c.err
}

func (c *ttftStubClient) Models(context.Context) ([]string, error) { return nil, nil }

func (c *ttftStubClient) Stream(context.Context, Request, StreamHandler) (Response, error) {
	return c.response, c.err
}

func TestFallbackAndRouterKeepTheSuccessfulResponseTTFT(t *testing.T) {
	var primary = &ttftStubClient{err: &APIError{Status: http.StatusInternalServerError}}
	var reserve = &ttftStubClient{response: Response{
		Content: "reserve answer", Model: "reserve-model", FirstTokenLatency: 750 * time.Millisecond, Attempts: 1,
	}}
	var fallback = NewFallback(primary, reserve, FallbackOptions{PrimaryModel: "primary-model", FallbackModel: "reserve-model"})
	var fallbackStreamer, ok = fallback.(Streamer)
	if !ok {
		t.Fatal("NewFallback did not keep the Streamer capability of the primary")
	}
	var response, err = fallbackStreamer.Stream(context.Background(), Request{Model: "primary-model"}, StreamHandler{})
	if err != nil {
		t.Fatalf("fallback Stream: %v", err)
	}
	if response.FirstTokenLatency != 750*time.Millisecond {
		t.Fatalf("fallback response FirstTokenLatency = %s, want the reserve's 750ms", response.FirstTokenLatency)
	}

	var router = NewRouter(RouterOptions{
		Default:      &ttftStubClient{err: &APIError{Status: http.StatusInternalServerError}},
		DefaultModel: "default-model",
		Providers: []Provider{{
			Name:         "kimi",
			Models:       []string{"kimi-k2"},
			DefaultModel: "kimi-k2",
			Client:       &ttftStubClient{response: Response{Content: "provider answer", Model: "kimi-k2", FirstTokenLatency: 320 * time.Millisecond, Attempts: 1}},
		}},
	})
	var routerStreamer, streams = router.(Streamer)
	if !streams {
		t.Fatal("NewRouter did not keep the Streamer capability of the default client")
	}
	response, err = routerStreamer.Stream(context.Background(), Request{Model: "kimi:kimi-k2"}, StreamHandler{})
	if err != nil {
		t.Fatalf("router Stream: %v", err)
	}
	if response.FirstTokenLatency != 320*time.Millisecond {
		t.Fatalf("router response FirstTokenLatency = %s, want the provider's 320ms", response.FirstTokenLatency)
	}
}
