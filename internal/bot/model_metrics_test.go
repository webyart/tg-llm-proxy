package bot

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"bot/internal/metrics"
	"bot/internal/model"
)

// modelMetricStub answers per requested id, so one dispatcher can exercise the default path, a
// provider-prefixed id, an unconfigured id and a failure in the same registry.
type modelMetricStub struct {
	stream func(model.Request) (model.Response, error)
}

func (s *modelMetricStub) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	return s.stream(req)
}

func (s *modelMetricStub) Models(context.Context) ([]string, error) { return nil, nil }

func (s *modelMetricStub) Stream(ctx context.Context, req model.Request, _ model.StreamHandler) (model.Response, error) {
	return s.stream(req)
}

func metricsBody(t *testing.T, reg *metrics.Registry) string {
	t.Helper()
	var buf bytes.Buffer
	if _, err := reg.WriteTo(&buf); err != nil {
		t.Fatalf("registry.WriteTo: %v", err)
	}
	return buf.String()
}

func TestModelMetricsAreBoundedByTheConfiguredAllowlist(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11",
		"STRATA_MODEL":           "primary-model",
		"MODEL_PROVIDERS":        `[{"name":"kimi","base_url":"https://api.example.com/v1","api_key":"k","models":["k2"]}]`,
	})
	var stub = &modelMetricStub{}
	stub.stream = func(req model.Request) (model.Response, error) {
		switch req.Model {
		case "primary-model":
			return model.Response{Model: "primary-model", FirstTokenLatency: 250 * time.Millisecond}, nil
		case "kimi:k2":
			return model.Response{Model: "k2", FirstTokenLatency: 450 * time.Millisecond}, nil
		case "unconfigured-model":
			return model.Response{Model: "unconfigured-model", FirstTokenLatency: 100 * time.Millisecond}, nil
		case "failing-model":
			return model.Response{}, &model.APIError{Status: http.StatusInternalServerError}
		}
		return model.Response{}, errors.New("unexpected model")
	}
	var d = newTestDispatcher(t, cfg, stub, &fakeReplier{}, testStore(t))
	var reg = metrics.NewRegistry()
	d.SetMetrics(reg)
	var streamer, ok = d.deps.Model.(model.Streamer)
	if !ok {
		t.Fatal("the metrics wrapper lost the Streamer capability of the stub")
	}

	for _, request := range []string{"primary-model", "kimi:k2", "unconfigured-model", "failing-model"} {
		if _, err := streamer.Stream(context.Background(), model.Request{Model: request}, model.StreamHandler{}); err != nil && request != "failing-model" {
			t.Fatalf("Stream(%s): %v", request, err)
		}
		// A Complete must not record a TTFT even if a client wrongly carries the field.
		if _, err := d.deps.Model.Complete(context.Background(), model.Request{Model: request}); err != nil && request != "failing-model" {
			t.Fatalf("Complete(%s): %v", request, err)
		}
	}

	var body = metricsBody(t, reg)
	for _, want := range []string{
		`bot_model_ttft_seconds_count{model="primary-model",provider="strata"} 1`,
		`bot_model_ttft_seconds_count{model="k2",provider="kimi"} 1`,
		`bot_model_ttft_seconds_count{model="other",provider="other"} 1`,
		`bot_model_call_duration_seconds_count{model="primary-model",outcome="success",provider="strata"} 2`,
		`bot_model_call_duration_seconds_count{model="other",outcome="success",provider="other"} 2`,
		`bot_model_call_duration_seconds_count{model="other",outcome="api_error",provider="other"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics miss %q:\n%s", want, body)
		}
	}
	// The unconfigured id must be folded into "other": a provider that invents ids per response must
	// not create a new series, and the raw id must never appear as a label value.
	if strings.Contains(body, `model="unconfigured-model"`) {
		t.Errorf("an unconfigured model leaked into a label:\n%s", body)
	}
	for _, forbidden := range []string{"chat_id", "user_id"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("model metrics expose %q:\n%s", forbidden, body)
		}
	}
}

func TestModelMetricsAreRegisteredBeforeTheFirstCall(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "STRATA_MODEL": "primary-model"})
	var d = newTestDispatcher(t, cfg, &modelMetricStub{stream: func(model.Request) (model.Response, error) {
		return model.Response{}, errors.New("not called")
	}}, &fakeReplier{}, testStore(t))
	var reg = metrics.NewRegistry()
	d.SetMetrics(reg)
	var body = metricsBody(t, reg)
	for _, family := range []string{
		"# TYPE bot_model_ttft_seconds histogram",
		"# TYPE bot_model_call_duration_seconds histogram",
	} {
		if !strings.Contains(body, family) {
			t.Errorf("fresh registry misses %q:\n%s", family, body)
		}
	}
}
