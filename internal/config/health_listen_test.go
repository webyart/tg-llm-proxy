package config

import (
	"strings"
	"testing"
)

// TestHealthListenHostIsValidated — the listener address is a closed list: loopback or all interfaces of the
// container, nothing else. A typo must be named at start instead of binding an address nobody documented.
func TestHealthListenHostIsValidated(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
		bad  bool
	}{
		{name: "default", raw: "", want: DefaultHealthListenHost},
		{name: "loopback", raw: "127.0.0.1", want: "127.0.0.1"},
		{name: "ipv6 loopback", raw: "::1", want: "::1"},
		{name: "container", raw: "0.0.0.0", want: "0.0.0.0"},
		{name: "hostname", raw: "health.internal", want: DefaultHealthListenHost, bad: true},
		{name: "public", raw: "192.168.1.10", want: DefaultHealthListenHost, bad: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var raw = map[string]string{"TELEGRAM_BOT_TOKEN": "12345:test-token"}
			if test.raw != "" {
				raw["HEALTH_LISTEN_HOST"] = test.raw
			}
			var cfg, problems = LoadFrom(raw)
			if cfg.HealthListenHost != test.want {
				t.Fatalf("HealthListenHost = %q, want %q", cfg.HealthListenHost, test.want)
			}
			var named = false
			for _, problem := range problems {
				if strings.Contains(problem, "HEALTH_LISTEN_HOST") {
					named = true
				}
			}
			if named != test.bad {
				t.Fatalf("problems = %v, want the row named: %v", problems, test.bad)
			}
		})
	}
}
