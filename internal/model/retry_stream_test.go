package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDecodeKeepsFinishReason(t *testing.T) {
	var payload = []byte(`{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"short"},"finish_reason":"length"}]}`)
	var response, err = Decode(http.StatusOK, payload, "m", 1)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if response.FinishReason != "length" {
		t.Fatalf("FinishReason = %q, want length: a truncated answer must not look complete", response.FinishReason)
	}
}

func TestDecodePrefersChoiceIndexOfZero(t *testing.T) {
	var payload = []byte(`{"choices":[
		{"index":1,"message":{"role":"assistant","content":"second"}},
		{"index":0,"message":{"role":"assistant","content":"first"}}
	]}`)
	var response, err = Decode(http.StatusOK, payload, "m", 1)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if response.Content != "first" {
		t.Fatalf("content = %q, want the choice with index 0", response.Content)
	}
}

// TestCompleteRetries429 is the policy change: a rate-limited provider used to end the call at once. The
// retry itself is bounded by Attempts and waited out with the provider's Retry-After (or the backoff).
func TestCompleteRetries429(t *testing.T) {
	var calls atomic.Int32
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer server.Close()

	var client, err = New(Options{ChatURL: server.URL, Attempts: 3, Backoff: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, completeErr := client.Complete(context.Background(), Request{Model: "m"})
	var apiErr *APIError
	if !errors.As(completeErr, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Fatalf("Complete = %v, want an APIError 429", completeErr)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3 (the configured attempts)", got)
	}
}

func TestRetryAfterHeaderParsing(t *testing.T) {
	var cases = []struct {
		header string
		want   time.Duration
	}{
		{"", 0},
		{"7", 7 * time.Second},
		{"0", 0},
		{"-3", 0},
		{"soon", 0},
	}
	for _, tc := range cases {
		if got := retryAfterHeader(tc.header); got != tc.want {
			t.Errorf("retryAfterHeader(%q) = %s, want %s", tc.header, got, tc.want)
		}
	}
	var future = time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
	if got := retryAfterHeader(future); got <= 0 || got > 6*time.Second {
		t.Errorf("retryAfterHeader(HTTP date) = %s, want about 5s", got)
	}
}

func TestRetryWaitHonoursProviderAndJittersBackoff(t *testing.T) {
	var apiErr = &APIError{Status: http.StatusTooManyRequests, RetryAfter: 5 * time.Second}
	if got := retryWait(apiErr, 250*time.Millisecond, 1); got != 5*time.Second {
		t.Fatalf("retryWait with Retry-After = %s, want 5s", got)
	}
	var huge = &APIError{Status: http.StatusTooManyRequests, RetryAfter: 10 * time.Minute}
	if got := retryWait(huge, 250*time.Millisecond, 1); got != maxRetryWait {
		t.Fatalf("retryWait with a huge Retry-After = %s, want the %s cap", got, maxRetryWait)
	}
	for i := 0; i < 20; i++ {
		var got = retryWait(&APIError{Status: http.StatusInternalServerError}, 100*time.Millisecond, 2)
		if got < 200*time.Millisecond || got > 300*time.Millisecond {
			t.Fatalf("jittered backoff = %s, want within [200ms, 300ms]", got)
		}
	}
}

func TestRetryableCoversRateLimitAndTransientProviderErrors(t *testing.T) {
	if !Retryable(&APIError{Status: http.StatusTooManyRequests}) {
		t.Fatal("429 must be retryable: the provider asked us to wait")
	}
	if !Retryable(&APIError{Status: http.StatusOK, Transient: true}) {
		t.Fatal("a provider error inside a stream must be retryable while nothing was delivered")
	}
	if Retryable(&APIError{Status: http.StatusBadRequest}) {
		t.Fatal("400 must not be retried")
	}
}

func TestCompleteRetriesAServerErrorAndCountsAttempts(t *testing.T) {
	var calls atomic.Int32
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	var client, err = New(Options{ChatURL: server.URL, Attempts: 2, Backoff: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var response, completeErr = client.Complete(context.Background(), Request{Model: "m"})
	if completeErr != nil {
		t.Fatalf("Complete: %v", completeErr)
	}
	if response.Content != "ok" || response.Attempts != 2 || calls.Load() != 2 {
		t.Fatalf("content=%q attempts=%d calls=%d", response.Content, response.Attempts, calls.Load())
	}
}

func TestCompleteReturnsErrNoRoomInsteadOfBurningTheBudget(t *testing.T) {
	var release = make(chan struct{})
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	var client, err = New(Options{ChatURL: server.URL, MaxConcurrent: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.admitTimeout = 50 * time.Millisecond

	var firstDone = make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = client.Complete(context.Background(), Request{Model: "m"})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(client.sem) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	var start = time.Now()
	_, noRoom := client.Complete(context.Background(), Request{Model: "m"})
	if !errors.Is(noRoom, ErrNoRoom) {
		t.Fatalf("second call = %v, want ErrNoRoom", noRoom)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the wait for a slot took %s: the admission timeout did not bound it", elapsed)
	}
	close(release)
	<-firstDone
}

func TestDefaultClientRefusesRedirects(t *testing.T) {
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v1/chat/completions" {
			http.Redirect(w, req, "/elsewhere", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("not the api"))
	}))
	defer server.Close()

	var client, err = New(Options{ChatURL: server.URL + "/v1/chat/completions"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, completeErr := client.Complete(context.Background(), Request{Model: "m"})
	var apiErr *APIError
	if !errors.As(completeErr, &apiErr) || apiErr.Status != http.StatusFound {
		t.Fatalf("a redirect = %v, want an APIError with status 302", completeErr)
	}
}

// sseStream builds a streaming client over a server that writes the given SSE body and records the request.
func sseStream(t *testing.T, body string) (*OpenAI, *[]byte) {
	t.Helper()
	var captured []byte
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var payload, _ = io.ReadAll(req.Body)
		captured = payload
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	var client, err = New(Options{ChatURL: server.URL, Stream: StreamSettings{Idle: time.Second, Total: 5 * time.Second}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, &captured
}

func TestStreamReportsAProviderErrorInsideA200Stream(t *testing.T) {
	var client, _ = sseStream(t, "data: {\"error\":{\"message\":\"model overloaded\",\"type\":\"server_error\"}}\n\n")
	_, err := client.Stream(context.Background(), Request{Model: "m"}, StreamHandler{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.Body, "model overloaded") {
		t.Fatalf("stream error = %v, want the provider message", err)
	}
	if errors.Is(err, ErrEmptyAnswer) {
		t.Fatal("a provider failure was reported as an empty answer")
	}
}

func TestStreamJoinsMultiLineDataEvents(t *testing.T) {
	// The SSE spec joins several data lines of one event with "\n"\u2014JSON tolerates the resulting
	// whitespace inside the object, so the event must be parsed, not dropped.
	var body = "data: {\"choices\":[{\"index\":0,\"delta\":\n" +
		"data: {\"content\":\"joined\"}}]}\n\n" +
		"data: [DONE]\n\n"
	var client, _ = sseStream(t, body)
	var got strings.Builder
	var response, err = client.Stream(context.Background(), Request{Model: "m"}, StreamHandler{
		Delta: func(text string) error { got.WriteString(text); return nil },
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got.String() != "joined" || response.Content != "joined" {
		t.Fatalf("delta=%q content=%q, want joined", got.String(), response.Content)
	}
}

func TestStreamIgnoresChoicesOtherThanTheFirst(t *testing.T) {
	var body = "data: {\"choices\":[{\"index\":1,\"delta\":{\"content\":\"second\"}},{\"index\":0,\"delta\":{\"content\":\"first\"}}]}\n\n" +
		"data: [DONE]\n\n"
	var client, _ = sseStream(t, body)
	var response, err = client.Stream(context.Background(), Request{Model: "m"}, StreamHandler{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if response.Content != "first" {
		t.Fatalf("content = %q, want only the choice with index 0", response.Content)
	}
}

func TestStreamRequestsUsageIncluded(t *testing.T) {
	var body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":9}}\n\n" +
		"data: [DONE]\n\n"
	var client, captured = sseStream(t, body)
	var response, err = client.Stream(context.Background(), Request{Model: "m"}, StreamHandler{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if response.TokensIn == nil || *response.TokensIn != 7 || response.TokensOut == nil || *response.TokensOut != 9 {
		t.Fatalf("usage = %v/%v, want 7/9", response.TokensIn, response.TokensOut)
	}
	var request struct {
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(*captured, &request); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if !request.Stream || !request.StreamOptions.IncludeUsage {
		t.Fatalf("stream_options.include_usage missing from %s", string(*captured))
	}
}

func TestStreamInfoCarriesElapsed(t *testing.T) {
	var body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n" +
		"data: [DONE]\n\n"
	var client, _ = sseStream(t, body)
	var sawElapsed bool
	_, err := client.Stream(context.Background(), Request{Model: "m"}, StreamHandler{
		Progress: func(info StreamInfo) {
			if info.Elapsed < 0 {
				t.Errorf("negative elapsed %s", info.Elapsed)
			}
			if info.ReasoningRunes > 0 {
				sawElapsed = true
			}
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !sawElapsed {
		t.Fatal("progress was never called with reasoning")
	}
}
