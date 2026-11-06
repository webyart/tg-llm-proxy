package config

import (
	"strings"
	"testing"
)

func TestUserDailyLimitsDefaultToUnlimited(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "1:test"})
	if len(problems) != 0 {
		t.Fatalf("LoadFrom problems=%v", problems)
	}
	if cfg.UserDailyModelLimit != 0 || cfg.UserDailyNetworkToolLimit != 0 || len(cfg.UserLimits) != 0 {
		t.Fatalf("daily limits=%d/%d overrides=%v, want unlimited defaults", cfg.UserDailyModelLimit, cfg.UserDailyNetworkToolLimit, cfg.UserLimits)
	}
	if limits := cfg.DailyLimitsFor(42); limits.Model != 0 || limits.NetworkTools != 0 {
		t.Fatalf("DailyLimitsFor=%+v, want unlimited", limits)
	}
}

func TestUserDailyLimitOverrides(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":            "1:test",
		"USER_DAILY_MODEL_LIMIT":        "10",
		"USER_DAILY_NETWORK_TOOL_LIMIT": "20",
		"USER_LIMITS_JSON":              `{"11":{"model":3,"network_tools":4},"12":{"model":0},"13":{}}`,
	})
	if len(problems) != 0 {
		t.Fatalf("LoadFrom problems=%v", problems)
	}
	for _, test := range []struct {
		userID       int64
		model        int64
		networkTools int64
	}{
		{userID: 11, model: 3, networkTools: 4},
		{userID: 12, model: 0, networkTools: 20},
		{userID: 13, model: 10, networkTools: 20},
		{userID: 14, model: 10, networkTools: 20},
	} {
		limits := cfg.DailyLimitsFor(test.userID)
		if limits.Model != test.model || limits.NetworkTools != test.networkTools {
			t.Errorf("DailyLimitsFor(%d)=%+v, want %d/%d", test.userID, limits, test.model, test.networkTools)
		}
	}
}

func TestUserDailyLimitConfigRejectsInvalidValuesWithoutEchoingInput(t *testing.T) {
	const privateValue = "private-configuration-value"
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "negative global model", key: "USER_DAILY_MODEL_LIMIT", value: "-1"},
		{name: "global network above cap", key: "USER_DAILY_NETWORK_TOOL_LIMIT", value: "1000001"},
		{name: "malformed JSON", key: "USER_LIMITS_JSON", value: `{"11":`},
		{name: "top-level array", key: "USER_LIMITS_JSON", value: `[]`},
		{name: "top-level null", key: "USER_LIMITS_JSON", value: `null`},
		{name: "zero user", key: "USER_LIMITS_JSON", value: `{"0":{"model":1}}`},
		{name: "negative user", key: "USER_LIMITS_JSON", value: `{"-1":{"model":1}}`},
		{name: "noncanonical user", key: "USER_LIMITS_JSON", value: `{"01":{"model":1}}`},
		{name: "unknown field", key: "USER_LIMITS_JSON", value: `{"11":{"other":1}}`},
		{name: "string value", key: "USER_LIMITS_JSON", value: `{"11":{"model":"` + privateValue + `"}}`},
		{name: "null value", key: "USER_LIMITS_JSON", value: `{"11":{"model":null}}`},
		{name: "negative value", key: "USER_LIMITS_JSON", value: `{"11":{"model":-1}}`},
		{name: "fractional value", key: "USER_LIMITS_JSON", value: `{"11":{"model":1.5}}`},
		{name: "exponent value", key: "USER_LIMITS_JSON", value: `{"11":{"model":1e2}}`},
		{name: "above cap", key: "USER_LIMITS_JSON", value: `{"11":{"network_tools":1000001}}`},
		{name: "duplicate field", key: "USER_LIMITS_JSON", value: `{"11":{"model":1,"model":2}}`},
		{name: "duplicate user", key: "USER_LIMITS_JSON", value: `{"11":{"model":1},"11":{"model":2}}`},
		{name: "trailing JSON", key: "USER_LIMITS_JSON", value: `{}` + privateValue},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, problems := LoadFrom(map[string]string{
				"TELEGRAM_BOT_TOKEN": "1:test",
				test.key:             test.value,
			})
			if len(problems) != 1 || !strings.Contains(problems[0], test.key) {
				t.Fatalf("problems=%v, want one naming %s", problems, test.key)
			}
			joined := strings.Join(problems, "\n")
			if strings.Contains(joined, privateValue) {
				t.Fatalf("problem output contains configuration content: %q", joined)
			}
			if cfg.UserDailyModelLimit != 0 || cfg.UserDailyNetworkToolLimit != 0 || len(cfg.UserLimits) != 0 {
				t.Fatalf("invalid limits were applied: %+v overrides=%v", cfg, cfg.UserLimits)
			}
		})
	}
}
