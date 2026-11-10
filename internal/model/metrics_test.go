package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bot/internal/metrics"
)

func TestOutcomeOf(t *testing.T) {
	var cases = []struct {
		err  error
		want string
	}{
		{nil, "success"},
		{fmt.Errorf("wrapped: %w", ErrNoRoom), "no_room"},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "timeout"},
		{context.Canceled, "network_error"},
		{&APIError{Status: 500}, "api_error"},
		{ErrEmptyAnswer, "api_error"},
		{errors.New("connection refused"), "network_error"},
	}
	for _, testCase := range cases {
		if got := outcomeOf(testCase.err); got != testCase.want {
			t.Errorf("outcomeOf(%v) = %q, want %q", testCase.err, got, testCase.want)
		}
	}
}

func TestCompleteRecordsSuccessAndLatency(t *testing.T) {
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`))
	}))
	defer server.Close()

	var registry = metrics.NewRegistry()
	var client, err = New(Options{ChatURL: server.URL, ModelsURL: server.URL, Model: "m", Metrics: NewMetrics(registry)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, err := registry.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	var text = buf.String()
	if !strings.Contains(text, `model_requests_total{outcome="success"} 1`) {
		t.Errorf("success is not counted:\n%s", text)
	}
	if !strings.Contains(text, "model_request_duration_seconds_count 1") {
		t.Errorf("latency is not observed:\n%s", text)
	}
}

func TestCompleteRecordsTokensWithTheReportedModel(t *testing.T) {
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"reported-model","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":22}}`))
	}))
	defer server.Close()

	var registry = metrics.NewRegistry()
	var client, err = New(Options{ChatURL: server.URL, ModelsURL: server.URL, Model: "requested-model", Metrics: NewMetrics(registry)})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "reported-model" {
		t.Fatalf("Model = %q, want the model the provider reported", resp.Model)
	}

	var text = metricsText(t, registry)
	if !strings.Contains(text, `model_tokens_total{direction="in",model="reported-model"} 11`) {
		t.Errorf("input tokens are not counted under the reported model:\n%s", text)
	}
	if !strings.Contains(text, `model_tokens_total{direction="out",model="reported-model"} 22`) {
		t.Errorf("output tokens are not counted under the reported model:\n%s", text)
	}
}

func TestCompleteFallsBackToTheRequestedModelForTokens(t *testing.T) {
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],` +
			`"usage":{"prompt_tokens":3,"completion_tokens":4}}`))
	}))
	defer server.Close()

	var registry = metrics.NewRegistry()
	var client, err = New(Options{ChatURL: server.URL, ModelsURL: server.URL, Model: "requested-model", Metrics: NewMetrics(registry)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}

	var text = metricsText(t, registry)
	if !strings.Contains(text, `model_tokens_total{direction="in",model="requested-model"} 3`) {
		t.Errorf("tokens without a reported model are not attributed to the requested model:\n%s", text)
	}
}

func TestCompleteWithoutUsageRecordsNoTokens(t *testing.T) {
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`))
	}))
	defer server.Close()

	var registry = metrics.NewRegistry()
	var client, err = New(Options{ChatURL: server.URL, ModelsURL: server.URL, Model: "m", Metrics: NewMetrics(registry)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}

	if text := metricsText(t, registry); strings.Contains(text, "model_tokens_total{") {
		t.Errorf("a response without usage recorded tokens:\n%s", text)
	}
}

func TestStreamRecordsTokensWithTheModelOfTheChunks(t *testing.T) {
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"model\":\"stream-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"model\":\"stream-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":9}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer server.Close()

	var registry = metrics.NewRegistry()
	var client, err = New(Options{ChatURL: server.URL, ModelsURL: server.URL, Model: "requested-model", Metrics: NewMetrics(registry)})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Stream(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}, StreamHandler{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "stream-model" {
		t.Fatalf("Model = %q, want the model of the chunks", resp.Model)
	}

	var text = metricsText(t, registry)
	if !strings.Contains(text, `model_tokens_total{direction="in",model="stream-model"} 7`) {
		t.Errorf("streamed input tokens are not counted under the model of the chunks:\n%s", text)
	}
	if !strings.Contains(text, `model_tokens_total{direction="out",model="stream-model"} 9`) {
		t.Errorf("streamed output tokens are not counted under the model of the chunks:\n%s", text)
	}
}

func TestFallbackSuccessRecordsTokensUnderTheAnsweringModel(t *testing.T) {
	var primaryFake = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer primaryFake.Close()
	var fallbackFake = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"fallback-model","choices":[{"index":0,"message":{"role":"assistant","content":"reserve"}}],` +
			`"usage":{"prompt_tokens":5,"completion_tokens":6}}`))
	}))
	defer fallbackFake.Close()

	var registry = metrics.NewRegistry()
	var recorder = NewMetrics(registry)
	var primary, err = New(Options{ChatURL: primaryFake.URL, ModelsURL: primaryFake.URL, Model: "primary-model",
		Attempts: 1, Backoff: time.Millisecond, Metrics: recorder})
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := New(Options{ChatURL: fallbackFake.URL, ModelsURL: fallbackFake.URL, Model: "fallback-model",
		Attempts: 1, Backoff: time.Millisecond, Metrics: recorder})
	if err != nil {
		t.Fatal(err)
	}
	var client = NewFallback(primary, fallback, FallbackOptions{
		PrimaryModel: "primary-model", FallbackModel: "fallback-model", Metrics: recorder,
	})
	if _, err := client.Complete(context.Background(), Request{Model: "primary-model", Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}

	var text = metricsText(t, registry)
	if !strings.Contains(text, `model_tokens_total{direction="in",model="fallback-model"} 5`) {
		t.Errorf("the fallback answered but its input tokens are not counted:\n%s", text)
	}
	if !strings.Contains(text, `model_tokens_total{direction="out",model="fallback-model"} 6`) {
		t.Errorf("the fallback answered but its output tokens are not counted:\n%s", text)
	}
	if strings.Contains(text, `model="primary-model"`) {
		t.Errorf("tokens are attributed to the primary model that failed:\n%s", text)
	}
}

func TestCompleteRecordsAPIErrorAndRetries(t *testing.T) {
	var calls int
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	var registry = metrics.NewRegistry()
	var client, err = New(Options{
		ChatURL: server.URL, ModelsURL: server.URL, Model: "m",
		Attempts: 2, Backoff: time.Millisecond, Metrics: NewMetrics(registry),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err == nil {
		t.Fatal("a 500 was reported as success")
	}
	if calls != 2 {
		t.Fatalf("the server was called %d times, want 2 attempts", calls)
	}

	var buf bytes.Buffer
	if _, err := registry.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	var text = buf.String()
	if !strings.Contains(text, `model_requests_total{outcome="api_error"} 1`) {
		t.Errorf("api_error is not counted:\n%s", text)
	}
	if !strings.Contains(text, "model_retries_total 1") {
		t.Errorf("the retry is not counted:\n%s", text)
	}
}

// TestTokenModelLabelBoundsProviderControlledIDs — a model id from an endpoint must not become an unbounded label.
func TestTokenModelLabelBoundsProviderControlledIDs(t *testing.T) {
	var long = strings.Repeat("m", 200)
	for _, test := range []struct{ reported, requested, want string }{
		{reported: "kimi-k3.1-pro", want: "kimi-k3.1-pro"},
		{reported: "", requested: "loaded-model", want: "loaded-model"},
		{reported: "  ", requested: "", want: "unknown"},
		{reported: long, want: long[:64]},
		{reported: "bad id with spaces\tand\ncontrols", want: "badidwithspacesandcontrols"},
		{reported: "###", want: "unknown"},
	} {
		if got := tokenModelLabel(test.reported, test.requested); got != test.want {
			t.Fatalf("tokenModelLabel(%q, %q) = %q, want %q", test.reported, test.requested, got, test.want)
		}
	}
}
