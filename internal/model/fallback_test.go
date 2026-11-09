package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bot/internal/metrics"
	"bot/internal/testutil"
)

func newFallbackTestClient(t *testing.T, chatURL, modelsURL, model string, attempts int, recorder *Metrics) *OpenAI {
	t.Helper()
	var client, err = New(Options{
		ChatURL:   chatURL,
		ModelsURL: modelsURL,
		APIKey:    "secret-key",
		Model:     model,
		Attempts:  attempts,
		Backoff:   time.Millisecond,
		Timeout:   2 * time.Second,
		Metrics:   recorder,
		Stream:    StreamSettings{Idle: time.Second, Total: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("New(%s): %v", model, err)
	}
	return client
}

func metricsText(t *testing.T, registry *metrics.Registry) string {
	t.Helper()
	var buf bytes.Buffer
	if _, err := registry.WriteTo(&buf); err != nil {
		t.Fatalf("registry.WriteTo: %v", err)
	}
	return buf.String()
}

func requestedModel(t *testing.T, body string) string {
	t.Helper()
	var decoded struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	return decoded.Model
}

func TestFallbackCompleteUsesTheReserveOnARetryableError(t *testing.T) {
	var primaryFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{Status: http.StatusInternalServerError})
	var fallbackFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{Content: "fallback answer", Model: "fallback-model"})
	var registry = metrics.NewRegistry()
	var recorder = NewMetrics(registry)
	var primary = newFallbackTestClient(t, primaryFake.ChatURL(), primaryFake.ModelsURL(), "primary-model", 1, recorder)
	var fallback = newFallbackTestClient(t, fallbackFake.ChatURL(), fallbackFake.ModelsURL(), "fallback-model", 1, recorder)
	var client = NewFallback(primary, fallback, FallbackOptions{
		PrimaryModel: "primary-model", FallbackModel: "fallback-model", Metrics: recorder,
	})

	var response, err = client.Complete(context.Background(), Request{Model: "primary-model", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Content != "fallback answer" {
		t.Fatalf("Content = %q, want the body of the fallback", response.Content)
	}
	if primaryFake.ChatCalls() != 1 || fallbackFake.ChatCalls() != 1 {
		t.Fatalf("calls = %d/%d, want exactly one primary and one fallback attempt",
			primaryFake.ChatCalls(), fallbackFake.ChatCalls())
	}
	if got := requestedModel(t, fallbackFake.LastRequestBody()); got != "fallback-model" {
		t.Fatalf("the fallback request asked for %q, want fallback-model", got)
	}
	var text = metricsText(t, registry)
	if !strings.Contains(text, `model_fallback_total{outcome="ok"} 1`) {
		t.Fatalf("the fallback success is not counted:\n%s", text)
	}
}

func TestFallbackCompleteReturnsANonRetryableErrorAsIs(t *testing.T) {
	var primaryFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{Status: http.StatusBadRequest})
	var fallbackFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{Content: "must not be reached"})
	var registry = metrics.NewRegistry()
	var recorder = NewMetrics(registry)
	var primary = newFallbackTestClient(t, primaryFake.ChatURL(), primaryFake.ModelsURL(), "primary-model", 1, recorder)
	var fallback = newFallbackTestClient(t, fallbackFake.ChatURL(), fallbackFake.ModelsURL(), "fallback-model", 1, recorder)
	var client = NewFallback(primary, fallback, FallbackOptions{
		PrimaryModel: "primary-model", FallbackModel: "fallback-model", Metrics: recorder,
	})

	_, err := client.Complete(context.Background(), Request{Model: "primary-model"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("Complete = %v, want the primary APIError 400", err)
	}
	if fallbackFake.ChatCalls() != 0 {
		t.Fatalf("the fallback was called %d time(s) for a non-retryable error", fallbackFake.ChatCalls())
	}
	if strings.Contains(metricsText(t, registry), "model_fallback_total{") {
		t.Fatal("a non-retryable error was counted as a fallback attempt")
	}
}

func TestFallbackCompleteNamesBothModelsWhenTheReserveAlsoFails(t *testing.T) {
	var primaryFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{Status: http.StatusInternalServerError})
	var fallbackFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{Status: http.StatusBadGateway})
	var registry = metrics.NewRegistry()
	var recorder = NewMetrics(registry)
	var primary = newFallbackTestClient(t, primaryFake.ChatURL(), primaryFake.ModelsURL(), "primary-model", 1, recorder)
	var fallback = newFallbackTestClient(t, fallbackFake.ChatURL(), fallbackFake.ModelsURL(), "fallback-model", 1, recorder)
	var client = NewFallback(primary, fallback, FallbackOptions{
		PrimaryModel: "primary-model", FallbackModel: "fallback-model", Metrics: recorder,
	})

	_, err := client.Complete(context.Background(), Request{Model: "primary-model"})
	if err == nil {
		t.Fatal("Complete reported success while both models failed")
	}
	if !strings.Contains(err.Error(), "primary-model") || !strings.Contains(err.Error(), "fallback-model") {
		t.Fatalf("the error does not name both models: %v", err)
	}
	if primaryFake.ChatCalls() != 1 || fallbackFake.ChatCalls() != 1 {
		t.Fatalf("calls = %d/%d, want exactly one of each",
			primaryFake.ChatCalls(), fallbackFake.ChatCalls())
	}
	var text = metricsText(t, registry)
	if !strings.Contains(text, `model_fallback_total{outcome="error"} 1`) {
		t.Fatalf("the fallback failure is not counted:\n%s", text)
	}
}

func TestFallbackStreamRetriesBeforeTheFirstDelta(t *testing.T) {
	var primaryFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{Status: http.StatusInternalServerError})
	var fallbackFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{
		StreamContent: []string{"hel", "lo"},
	})
	var registry = metrics.NewRegistry()
	var recorder = NewMetrics(registry)
	var primary = newFallbackTestClient(t, primaryFake.ChatURL(), primaryFake.ModelsURL(), "primary-model", 1, recorder)
	var fallback = newFallbackTestClient(t, fallbackFake.ChatURL(), fallbackFake.ModelsURL(), "fallback-model", 1, recorder)
	var client = NewFallback(primary, fallback, FallbackOptions{
		PrimaryModel: "primary-model", FallbackModel: "fallback-model", Metrics: recorder,
	})
	var streamer, streams = client.(Streamer)
	if !streams {
		t.Fatal("the wrapper is not a Streamer although the primary is")
	}

	var got strings.Builder
	var response, err = streamer.Stream(context.Background(), Request{Model: "primary-model"}, StreamHandler{
		Delta: func(text string) error {
			got.WriteString(text)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got.String() != "hello" || response.Content != "hello" {
		t.Fatalf("deltas = %q, content = %q, want hello exactly once", got.String(), response.Content)
	}
	if fallbackFake.ChatCalls() != 1 {
		t.Fatalf("the fallback was called %d time(s), want 1", fallbackFake.ChatCalls())
	}
	if !strings.Contains(metricsText(t, registry), `model_fallback_total{outcome="ok"} 1`) {
		t.Fatal("the stream fallback success is not counted")
	}
}

func TestFallbackStreamNeverReplaysTextAfterADelta(t *testing.T) {
	var primaryCalls atomic.Int32
	var primaryServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		var flusher, _ = w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"model\":\"primary-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// A provider failure INSIDE a 200 stream is retryable, so only the delivered marker stops a replay.
		_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\"upstream overloaded\",\"type\":\"server_error\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer primaryServer.Close()
	var fallbackFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{StreamContent: []string{"replayed"}})

	var registry = metrics.NewRegistry()
	var recorder = NewMetrics(registry)
	var primary = newFallbackTestClient(t, primaryServer.URL+"/v1/chat/completions", primaryServer.URL+"/v1/models", "primary-model", 1, recorder)
	var fallback = newFallbackTestClient(t, fallbackFake.ChatURL(), fallbackFake.ModelsURL(), "fallback-model", 1, recorder)
	var client = NewFallback(primary, fallback, FallbackOptions{
		PrimaryModel: "primary-model", FallbackModel: "fallback-model", Metrics: recorder,
	})
	var streamer, streams = client.(Streamer)
	if !streams {
		t.Fatal("the wrapper is not a Streamer although the primary is")
	}

	var got strings.Builder
	var response, err = streamer.Stream(context.Background(), Request{Model: "primary-model"}, StreamHandler{
		Delta: func(text string) error {
			got.WriteString(text)
			return nil
		},
	})
	if err == nil {
		t.Fatal("Stream reported success after the provider error")
	}
	if got.String() != "partial" || response.Content != "partial" {
		t.Fatalf("deltas = %q, content = %q, want the delivered text kept and not replayed", got.String(), response.Content)
	}
	if fallbackFake.ChatCalls() != 0 {
		t.Fatalf("the fallback was called %d time(s) after a delta reached the caller", fallbackFake.ChatCalls())
	}
	if primaryCalls.Load() != 1 {
		t.Fatalf("the primary was called %d time(s), want 1", primaryCalls.Load())
	}
	if strings.Contains(metricsText(t, registry), "model_fallback_total{") {
		t.Fatal("a stream with a delivered delta was counted as a fallback attempt")
	}
}

func TestFallbackDisabledReturnsThePrimaryUnchanged(t *testing.T) {
	var primaryFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{Content: "primary answer"})
	var fallbackFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{Content: "fallback answer"})
	var registry = metrics.NewRegistry()
	var recorder = NewMetrics(registry)
	var primary = newFallbackTestClient(t, primaryFake.ChatURL(), primaryFake.ModelsURL(), "primary-model", 1, recorder)
	var fallback = newFallbackTestClient(t, fallbackFake.ChatURL(), fallbackFake.ModelsURL(), "fallback-model", 1, recorder)
	var client = NewFallback(primary, fallback, FallbackOptions{})

	if client != primary {
		t.Fatal("an empty FallbackModel must return the primary client itself, not a wrapper")
	}
	if _, err := client.Complete(context.Background(), Request{Model: "primary-model"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := client.Models(context.Background()); err != nil {
		t.Fatalf("Models: %v", err)
	}
	if fallbackFake.ChatCalls() != 0 || fallbackFake.ModelsCalls() != 0 {
		t.Fatalf("a disabled fallback sent %d chat and %d model request(s)",
			fallbackFake.ChatCalls(), fallbackFake.ModelsCalls())
	}
}

func TestFallbackKeepsStreamerAndDropsTranscriber(t *testing.T) {
	var primaryFake = testutil.NewStrata(t, "secret-key")
	var fallbackFake = testutil.NewStrata(t, "secret-key")
	var registry = metrics.NewRegistry()
	var recorder = NewMetrics(registry)
	var primary = newFallbackTestClient(t, primaryFake.ChatURL(), primaryFake.ModelsURL(), "primary-model", 1, recorder)
	var fallback = newFallbackTestClient(t, fallbackFake.ChatURL(), fallbackFake.ModelsURL(), "fallback-model", 1, recorder)
	var client = NewFallback(primary, fallback, FallbackOptions{
		PrimaryModel: "primary-model", FallbackModel: "fallback-model", Metrics: recorder,
	})

	if _, ok := client.(Streamer); !ok {
		t.Fatal("the wrapper does not implement Streamer although the primary does")
	}
	if _, ok := client.(Transcriber); ok {
		t.Fatal("the wrapper implements Transcriber: transcription must stay on the primary client")
	}
}

func TestFallbackModelsUsesTheReserveOnlyWhenThePrimaryFails(t *testing.T) {
	var primaryCalls atomic.Int32
	var primaryServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		http.Error(w, "models are down", http.StatusInternalServerError)
	}))
	defer primaryServer.Close()
	var fallbackFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{Models: []string{"fallback-model"}})

	var registry = metrics.NewRegistry()
	var recorder = NewMetrics(registry)
	var primary = newFallbackTestClient(t, primaryServer.URL, primaryServer.URL, "primary-model", 1, recorder)
	var fallback = newFallbackTestClient(t, fallbackFake.ChatURL(), fallbackFake.ModelsURL(), "fallback-model", 1, recorder)
	var client = NewFallback(primary, fallback, FallbackOptions{
		PrimaryModel: "primary-model", FallbackModel: "fallback-model", Metrics: recorder,
	})

	var models, err = client.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0] != "fallback-model" {
		t.Fatalf("Models = %v, want the reserve list", models)
	}
	if primaryCalls.Load() != 1 || fallbackFake.ModelsCalls() != 1 {
		t.Fatalf("calls = %d/%d, want one primary and one fallback", primaryCalls.Load(), fallbackFake.ModelsCalls())
	}
}

func TestFallbackErrorsAndLogsCarryNoKeyOrBody(t *testing.T) {
	var broken = func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the model says: secret-key prompt body", http.StatusInternalServerError)
	}
	var primaryServer = httptest.NewServer(http.HandlerFunc(broken))
	defer primaryServer.Close()
	var fallbackServer = httptest.NewServer(http.HandlerFunc(broken))
	defer fallbackServer.Close()

	var registry = metrics.NewRegistry()
	var recorder = NewMetrics(registry)
	var primary = newFallbackTestClient(t, primaryServer.URL, primaryServer.URL, "primary-model", 1, recorder)
	var fallback = newFallbackTestClient(t, fallbackServer.URL, fallbackServer.URL, "fallback-model", 1, recorder)
	var logs bytes.Buffer
	var client = NewFallback(primary, fallback, FallbackOptions{
		PrimaryModel:  "primary-model",
		FallbackModel: "fallback-model",
		Metrics:       recorder,
		Logger:        slog.New(slog.NewTextHandler(&logs, nil)),
	})

	_, err := client.Complete(context.Background(), Request{Model: "primary-model"})
	if err == nil {
		t.Fatal("Complete reported success while both models failed")
	}
	var output = err.Error() + "\n" + logs.String()
	for _, secret := range []string{"secret-key", "prompt body", "the model says"} {
		if strings.Contains(output, secret) {
			t.Fatalf("the error or the log carries %q:\n%s", secret, output)
		}
	}
	if !strings.Contains(err.Error(), "primary-model") || !strings.Contains(err.Error(), "fallback-model") {
		t.Fatalf("the error no longer names both models: %v", err)
	}
	var text = metricsText(t, registry)
	if !strings.Contains(text, `model_fallback_total{outcome="error"} 1`) {
		t.Fatalf("the failed fallback is not counted:\n%s", text)
	}
}
