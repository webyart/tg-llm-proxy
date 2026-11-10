package model

import (
	"context"
	"errors"
	"strings"
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
	fallback *metrics.Counter
	tokens   *metrics.Counter
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
		fallback: reg.Counter("model_fallback_total", "Fallback-model attempts by outcome."),
		tokens:   reg.Counter("model_tokens_total", "Tokens reported by the model, by direction and model id."),
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

// fallbackAttempt records one attempt on the fallback model. Every attempt ends with exactly one sample:
// ok when the fallback answered, error when it failed too.
func (m *Metrics) fallbackAttempt(outcome string) {
	if m == nil {
		return
	}
	m.fallback.Add(map[string]string{"outcome": outcome}, 1)
}

// recordTokens adds the usage of one finished response. The used model is the one the provider reported;
// when it is empty the model the request asked for stands in, and only a response with no name at all is
// attributed to "unknown". A response without usage (the provider omitted it) records nothing.
// tokenModelLabel bounds the provider-controlled model id: an endpoint that reports a different id per response
// would otherwise create unbounded Prometheus label cardinality. The id keeps its readable form up to 64 chars.
func tokenModelLabel(reported, requested string) string {
	var name = strings.TrimSpace(reported)
	if name == "" {
		name = strings.TrimSpace(requested)
	}
	if name == "" {
		return "unknown"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	var bounded strings.Builder
	for _, symbol := range name {
		switch {
		case symbol >= 'a' && symbol <= 'z', symbol >= 'A' && symbol <= 'Z', symbol >= '0' && symbol <= '9':
			bounded.WriteRune(symbol)
		case symbol == '.', symbol == '_', symbol == '-', symbol == '/', symbol == ':', symbol == '@':
			bounded.WriteRune(symbol)
		}
	}
	if bounded.Len() == 0 {
		return "unknown"
	}
	return bounded.String()
}

func (m *Metrics) recordTokens(resp Response, requested string) {
	if m == nil {
		return
	}
	if resp.TokensIn == nil && resp.TokensOut == nil {
		return
	}
	var model = tokenModelLabel(resp.Model, requested)
	if resp.TokensIn != nil {
		m.tokens.Add(map[string]string{"direction": "in", "model": model}, float64(*resp.TokensIn))
	}
	if resp.TokensOut != nil {
		m.tokens.Add(map[string]string{"direction": "out", "model": model}, float64(*resp.TokensOut))
	}
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
