package bot

import (
	"context"
	"errors"
	"strings"
	"time"

	"bot/internal/config"
	"bot/internal/metrics"
	"bot/internal/model"
)

// The buckets of the two model-call families. TTFT starts below the 100 ms of a local model and
// still covers a provider that spends a minute in its queue before the first token; the call
// duration covers a fast answer and a thinking model with tools.
var (
	modelTTFTBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30, 60}
	modelCallBuckets = []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300}
)

// botModelMetrics instruments model calls at the dispatcher boundary, i.e. ONCE per user-visible
// call after the fallback chain and the provider router have chosen an answer. It therefore sees the
// Response that actually answered: its TTFT belongs to the successful attempt, not to a failed one.
type botModelMetrics struct {
	ttft     *metrics.Histogram
	duration *metrics.Histogram
	labels   botModelLabels
}

// newBotModelMetrics registers both families eagerly, so a fresh /metrics lists them before the
// first model call and an operator can see that the instrumentation is wired.
func newBotModelMetrics(reg *metrics.Registry, cfg config.Config) *botModelMetrics {
	if reg == nil {
		return nil
	}
	return &botModelMetrics{
		ttft:     reg.Histogram("bot_model_ttft_seconds", "Time from the start of a streaming model call to its first non-empty content delta, in seconds.", modelTTFTBuckets),
		duration: reg.Histogram("bot_model_call_duration_seconds", "Wall time of one model call at the dispatcher boundary, in seconds.", modelCallBuckets),
		labels:   configuredModelLabels(cfg),
	}
}

// observe records one finished call. The duration is recorded for failures too, because a provider
// that times out is exactly the latency an operator is hunting; TTFT exists only for a successful
// streamed answer, so a Complete response (which must leave the field zero) never records it.
func (m *botModelMetrics) observe(req model.Request, resp model.Response, err error, elapsed time.Duration, streamed bool) {
	if m == nil {
		return
	}
	label := m.labels.forCall(req.Model, resp.Model)
	m.duration.Observe(map[string]string{
		"provider": label.provider,
		"model":    label.model,
		"outcome":  modelCallOutcome(err),
	}, elapsed.Seconds())
	if streamed && err == nil && resp.FirstTokenLatency > 0 {
		m.ttft.Observe(map[string]string{
			"provider": label.provider,
			"model":    label.model,
		}, resp.FirstTokenLatency.Seconds())
	}
}

// botModelLabel is one bounded (provider, model) pair. Both values come from configuration, never
// from a response, so an endpoint cannot create an unbounded number of series.
type botModelLabel struct {
	provider string
	model    string
}

// botModelLabels is the allowlist of configured model ids plus the single "other" fallback. A model
// id may be spelled bare (the default path) or as "provider:model" (MODEL_PROVIDERS).
type botModelLabels struct {
	bare     map[string]botModelLabel
	prefixed map[string]botModelLabel
}

var otherModelLabel = botModelLabel{provider: "other", model: "other"}

// configuredModelLabels builds the allowlist from the models the process was configured with. Any
// model the model client reports that is not in this set is folded into "other", so a provider that
// invents a new id per response cannot create a new Prometheus series per call.
func configuredModelLabels(cfg config.Config) botModelLabels {
	labels := botModelLabels{
		bare:     map[string]botModelLabel{},
		prefixed: map[string]botModelLabel{},
	}
	add := func(key, provider, name string) {
		key = strings.TrimSpace(key)
		if key == "" || provider == "" || name == "" {
			return
		}
		if _, exists := labels.prefixed[key]; !exists {
			labels.prefixed[key] = botModelLabel{provider: provider, model: name}
		}
	}
	addBare := func(key, provider, name string) {
		key = strings.TrimSpace(key)
		if key == "" || provider == "" || name == "" {
			return
		}
		if _, exists := labels.bare[key]; !exists {
			labels.bare[key] = botModelLabel{provider: provider, model: name}
		}
	}

	primary := strings.TrimSpace(cfg.StrataModel)
	if primary != "" {
		add(model.DefaultProviderName+":"+primary, "strata", primary)
		addBare(primary, "strata", primary)
	}
	if reserve := strings.TrimSpace(cfg.StrataFallbackModel); reserve != "" {
		addBare(reserve, "fallback", reserve)
	}
	for _, reserve := range cfg.FallbackProviders {
		name := strings.TrimSpace(reserve.Model)
		if name == "" {
			continue
		}
		addBare(name, "fallback", name)
	}
	for _, provider := range cfg.ModelProviders {
		name := strings.TrimSpace(provider.Name)
		models := provider.Models
		if len(models) == 0 && strings.TrimSpace(provider.Model) != "" {
			models = []string{provider.Model}
		}
		for _, providerModel := range models {
			providerModel = strings.TrimSpace(providerModel)
			if name == "" || providerModel == "" {
				continue
			}
			add(name+":"+providerModel, name, providerModel)
			addBare(providerModel, name, providerModel)
		}
	}
	return labels
}

// forCall chooses the label set of one call. The model a provider reports wins, because that is the
// one that answered; the request id is the fallback for an error response that carries no model. A
// provider-prefixed request keeps its provider when the response echoes the same bare model id.
func (l botModelLabels) forCall(requested, reported string) botModelLabel {
	requested = strings.TrimSpace(requested)
	reported = strings.TrimSpace(reported)
	_, bare, prefixed := strings.Cut(requested, ":")

	if reported != "" {
		if prefixed && reported == strings.TrimSpace(bare) {
			if label, ok := l.prefixed[requested]; ok {
				return label
			}
		}
		if label, ok := l.prefixed[reported]; ok {
			return label
		}
		if label, ok := l.bare[reported]; ok {
			return label
		}
	}
	if label, ok := l.prefixed[requested]; ok {
		return label
	}
	if label, ok := l.bare[requested]; ok {
		return label
	}
	return otherModelLabel
}

// modelCallOutcome mirrors the outcome classes of the model package so both metric families of one
// call agree on the same five values.
func modelCallOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, model.ErrNoRoom):
		return "no_room"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "network_error"
	}
	var apiErr *model.APIError
	if errors.As(err, &apiErr) || errors.Is(err, model.ErrEmptyAnswer) {
		return "api_error"
	}
	return "network_error"
}
