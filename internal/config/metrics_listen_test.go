package config

import (
	"strings"
	"testing"
)

// TestMetricsListenHostIsValidated — the metrics bind address must be a concrete loopback/private IP. 0.0.0.0
// is the bug this row exists to prevent: it would put /metrics on every network of the container again.
func TestMetricsListenHostIsValidated(t *testing.T) {
	for _, test := range []struct {
		name      string
		metrics   string
		probe     string
		wantHost  string
		wantProbe int64
		bad       bool
	}{
		{name: "unset keeps the single listener", probe: "", wantHost: "", wantProbe: DefaultHealthPort},
		{name: "loopback", metrics: "127.0.0.1", probe: "8081", wantHost: "127.0.0.1", wantProbe: 8081},
		{name: "private network address", metrics: "172.24.0.250", probe: "8080", wantHost: "172.24.0.250", wantProbe: 8080},
		{name: "all interfaces refused", metrics: "0.0.0.0", probe: "8080", wantHost: "", wantProbe: 8080, bad: true},
		{name: "hostname refused", metrics: "metrics.internal", probe: "8080", wantHost: "", wantProbe: 8080, bad: true},
		{name: "probe port required", metrics: "172.24.0.250", probe: "", wantHost: "172.24.0.250", wantProbe: DefaultHealthPort, bad: true},
		{name: "same port on the same address refused", metrics: "127.0.0.1", probe: "8080", wantHost: "127.0.0.1", wantProbe: 8080, bad: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var raw = map[string]string{"TELEGRAM_BOT_TOKEN": "12345:test-token", "HEALTH_PORT": "8080"}
			if test.metrics != "" {
				raw["METRICS_LISTEN_HOST"] = test.metrics
			}
			if test.probe != "" {
				raw["HEALTH_PROBE_PORT"] = test.probe
			}
			var cfg, problems = LoadFrom(raw)
			if cfg.MetricsListenHost != test.wantHost {
				t.Errorf("MetricsListenHost = %q, want %q (problems: %v)", cfg.MetricsListenHost, test.wantHost, problems)
			}
			if cfg.HealthProbePort != test.wantProbe {
				t.Errorf("HealthProbePort = %d, want %d (problems: %v)", cfg.HealthProbePort, test.wantProbe, problems)
			}
			var named = false
			for _, problem := range problems {
				if strings.Contains(problem, "METRICS_LISTEN_HOST") || strings.Contains(problem, "HEALTH_PROBE_PORT") {
					named = true
				}
			}
			if named != test.bad {
				t.Fatalf("problems = %v, want a row named: %v", problems, test.bad)
			}
		})
	}
}
