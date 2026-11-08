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
