package model

import (
	"context"
	"errors"
	"time"

	"bot/internal/metrics"
)

// requestBuckets cover a quick answer and a model that thinks for minutes; the buckets are seconds.
var requestBuckets = []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300}

// Metrics instruments model calls. A nil *Metrics is a no-op, so a client built without a registry and
// every existing test keep working unchanged.
type Metrics struct {
	requests *metrics.Counter
	latency  *metrics.Histogram
	retries  *metrics.Counter
}

// NewMetrics registers the model metric families on reg and returns the recorder for Options.Metrics.
func NewMetrics(reg *metrics.Registry) *Metrics {
	if reg == nil {
		return nil
	}
	return &Metrics{
		requests: reg.Counter("model_requests_total", "Model chat requests by outcome."),
		latency:  reg.Histogram("model_request_duration_seconds", "Wall time of one model chat request, retries included, in seconds.", requestBuckets),
		retries:  reg.Counter("model_retries_total", "Retries started after a retryable model failure."),
	}
}

// observe records one finished request and its wall time.
func (m *Metrics) observe(err error, elapsed time.Duration) {
	if m == nil {
		return
	}
	m.requests.Add(map[string]string{"outcome": outcomeOf(err)}, 1)
	m.latency.Observe(nil, elapsed.Seconds())
}

// retryStarted records one retry that is about to be attempted.
func (m *Metrics) retryStarted() {
	if m == nil {
		return
	}
	m.retries.Add(nil, 1)
}

// outcomeOf maps an error of a call to one of the five outcomes the metric documents. A caller's
// cancellation has no outcome of its own in that contract and is counted as a network error.
func outcomeOf(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrNoRoom):
		return "no_room"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "network_error"
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) || errors.Is(err, ErrEmptyAnswer) {
		return "api_error"
	}
	return "network_error"
}
