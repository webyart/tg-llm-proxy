package config

import (
	"strings"
	"testing"
)

func validWebhookRaw() map[string]string {
	return map[string]string{
		"TELEGRAM_BOT_TOKEN":   "123:fake",
		"WEBHOOK_ENABLED":      "true",
		"WEBHOOK_PUBLIC_URL":   "https://example.test/telegram/webhook",
		"WEBHOOK_SECRET_TOKEN": "1234567890abcdef",
	}
}

func TestWebhookDefaultsKeepLongPolling(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake"})
	if len(problems) != 0 {
		t.Fatalf("default config problems: %v", problems)
	}
	if cfg.WebhookEnabled || cfg.WebhookAddress() != "127.0.0.1:8082" || cfg.WebhookPath != DefaultWebhookPath {
		t.Fatalf("webhook defaults changed the transport: enabled=%v address=%q path=%q", cfg.WebhookEnabled, cfg.WebhookAddress(), cfg.WebhookPath)
	}
}

func TestWebhookValidConfiguration(t *testing.T) {
	cfg, problems := LoadFrom(validWebhookRaw())
	if len(problems) != 0 {
		t.Fatalf("valid webhook problems: %v", problems)
	}
	if !cfg.WebhookEnabled || cfg.WebhookPublicURL != "https://example.test/telegram/webhook" || cfg.WebhookSecretToken != "1234567890abcdef" {
		t.Fatalf("valid webhook was not applied: %+v", cfg)
	}
}

func TestWebhookValidationNamesEveryInvalidRow(t *testing.T) {
	raw := validWebhookRaw()
	raw["WEBHOOK_PUBLIC_URL"] = "http://example.test/wrong-path"
	raw["WEBHOOK_SECRET_TOKEN"] = "short"
	raw["WEBHOOK_LISTEN_HOST"] = "localhost"
	raw["WEBHOOK_PORT"] = "0"
	_, problems := LoadFrom(raw)
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"WEBHOOK_PUBLIC_URL", "WEBHOOK_SECRET_TOKEN", "WEBHOOK_LISTEN_HOST", "WEBHOOK_PORT"} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems must name %s: %v", want, problems)
		}
	}
}

func TestWebhookDisabledIgnoresUnusedEndpointSettings(t *testing.T) {
	raw := validWebhookRaw()
	raw["WEBHOOK_ENABLED"] = "false"
	raw["WEBHOOK_PUBLIC_URL"] = "not a URL"
	raw["WEBHOOK_SECRET_TOKEN"] = "short"
	_, problems := LoadFrom(raw)
	if len(problems) != 0 {
		t.Fatalf("disabled webhook must not require public endpoint settings: %v", problems)
	}
}

func TestWebhookSecretAlphabetAndPublicPath(t *testing.T) {
	raw := validWebhookRaw()
	raw["WEBHOOK_SECRET_TOKEN"] = "1234567890abcdef!"
	raw["WEBHOOK_PATH"] = "/other"
	_, problems := LoadFrom(raw)
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "WEBHOOK_SECRET_TOKEN") || !strings.Contains(joined, "WEBHOOK_PUBLIC_URL") {
		t.Fatalf("invalid secret alphabet and mismatched public path must be rejected: %v", problems)
	}
}
