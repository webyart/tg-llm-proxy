package model

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"bot/internal/metrics"
	"bot/internal/testutil"
)

// TestFallbackCompleteRetriesAnEmptyReserveOnce — a small local reserve can answer 200 with no content; one more
// attempt is cheap and saves the answer instead of failing the request.
func TestFallbackCompleteRetriesAnEmptyReserveOnce(t *testing.T) {
	var primaryFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{Status: http.StatusInternalServerError})
	var fallbackFake = testutil.NewStrata(t, "secret-key",
		testutil.StrataReply{Content: ""},
		testutil.StrataReply{Content: "второй ответ", Model: "reserve-model"},
	)
	var registry = metrics.NewRegistry()
	var recorder = NewMetrics(registry)
	var primary = newFallbackTestClient(t, primaryFake.ChatURL(), primaryFake.ModelsURL(), "primary-model", 1, recorder)
	var fallback = newFallbackTestClient(t, fallbackFake.ChatURL(), fallbackFake.ModelsURL(), "reserve-model", 1, recorder)
	var client = NewFallback(primary, fallback, FallbackOptions{PrimaryModel: "primary-model", FallbackModel: "reserve-model", Metrics: recorder})

	var response, err = client.Complete(context.Background(), Request{Model: "primary-model", Messages: []Message{{Role: RoleUser, Content: "вопрос"}}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Content != "второй ответ" {
		t.Fatalf("answer = %q, want the retried reserve answer", response.Content)
	}
	if calls := fallbackFake.ChatCalls(); calls != 2 {
		t.Fatalf("reserve was called %d times, want exactly 2 (empty + retry)", calls)
	}
	if !strings.Contains(metricsText(t, registry), `model_fallback_total{outcome="ok"} 1`) {
		t.Fatalf("the fallback outcome was not recorded as ok:\n%s", metricsText(t, registry))
	}
}

// TestFallbackStreamRetriesAnEmptyReserveOnce — same rule for a stream that failed before any delta.
func TestFallbackStreamRetriesAnEmptyReserveOnce(t *testing.T) {
	var primaryFake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{Status: http.StatusInternalServerError})
	var fallbackFake = testutil.NewStrata(t, "secret-key",
		testutil.StrataReply{StreamContent: []string{""}},
		testutil.StrataReply{StreamContent: []string{"го", "тово"}, Model: "reserve-model"},
	)
	var registry = metrics.NewRegistry()
	var recorder = NewMetrics(registry)
	var primary = newFallbackTestClient(t, primaryFake.ChatURL(), primaryFake.ModelsURL(), "primary-model", 1, recorder)
	var fallback = newFallbackTestClient(t, fallbackFake.ChatURL(), fallbackFake.ModelsURL(), "reserve-model", 1, recorder)
	var client = NewFallback(primary, fallback, FallbackOptions{PrimaryModel: "primary-model", FallbackModel: "reserve-model", Metrics: recorder})
	var streamer, ok = client.(Streamer)
	if !ok {
		t.Fatal("the wrapper is not a Streamer")
	}

	var text string
	var response, err = streamer.Stream(context.Background(), Request{Model: "primary-model", Messages: []Message{{Role: RoleUser, Content: "вопрос"}}}, StreamHandler{
		Delta: func(delta string) error { text += delta; return nil },
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text != "готово" || response.Content != "готово" {
		t.Fatalf("streamed %q response %q, want the retried answer", text, response.Content)
	}
	if calls := fallbackFake.ChatCalls(); calls != 2 {
		t.Fatalf("reserve was called %d times, want exactly 2 (empty + retry)", calls)
	}
}
