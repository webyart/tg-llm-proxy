package config

import (
	"strings"
	"testing"
)

func validMiniAppRaw() map[string]string {
	return map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:fake",
		"MINIAPP_ENABLED":    "true",
		"MINIAPP_PUBLIC_URL": "https://example.test/miniapp",
	}
}

func TestMiniAppDefaultsAreDisabledAndLoopback(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake"})
	if len(problems) != 0 {
		t.Fatalf("default config problems: %v", problems)
	}
	if cfg.MiniAppEnabled || cfg.MiniAppAddress() != "127.0.0.1:8084" || cfg.MiniAppPublicURL != "" {
		t.Fatalf("unexpected Mini App defaults: enabled=%v address=%q public=%q", cfg.MiniAppEnabled, cfg.MiniAppAddress(), cfg.MiniAppPublicURL)
	}
}

func TestMiniAppValidConfiguration(t *testing.T) {
	cfg, problems := LoadFrom(validMiniAppRaw())
	if len(problems) != 0 {
		t.Fatalf("valid Mini App config problems: %v", problems)
	}
	if !cfg.MiniAppEnabled || cfg.MiniAppPublicURL != "https://example.test/miniapp" {
		t.Fatalf("valid Mini App config was not applied: %+v", cfg)
	}
}

func TestMiniAppRejectsInvalidPublicURLs(t *testing.T) {
	for _, value := range []string{
		"", "http://example.test/app", "example.test/app", "https:///app",
		"https://user:pass@example.test/app", "https://example.test/app?x=1",
		"https://example.test/app?", "https://example.test/app#fragment",
	} {
		t.Run(value, func(t *testing.T) {
			raw := validMiniAppRaw()
			raw["MINIAPP_PUBLIC_URL"] = value
			_, problems := LoadFrom(raw)
			if !hasProblemNamed(problems, "MINIAPP_PUBLIC_URL") {
				t.Fatalf("public URL %q was accepted: %v", value, problems)
			}
		})
	}
}

func TestMiniAppListenerAndPortValidation(t *testing.T) {
	for _, host := range []string{"localhost", "192.0.2.1"} {
		raw := validMiniAppRaw()
		raw["MINIAPP_LISTEN_HOST"] = host
		_, problems := LoadFrom(raw)
		if !hasProblemNamed(problems, "MINIAPP_LISTEN_HOST") {
			t.Errorf("non-loopback host %q was accepted: %v", host, problems)
		}
	}
	raw := validMiniAppRaw()
	raw["MINIAPP_PORT"] = "0"
	_, problems := LoadFrom(raw)
	if !hasProblemNamed(problems, "MINIAPP_PORT") {
		t.Fatalf("invalid port was accepted: %v", problems)
	}
}

func TestMiniAppDisabledIgnoresPublicEndpointButKeepsSafeDefaults(t *testing.T) {
	raw := validMiniAppRaw()
	raw["MINIAPP_ENABLED"] = "false"
	raw["MINIAPP_PUBLIC_URL"] = "not a URL"
	raw["MINIAPP_LISTEN_HOST"] = "0.0.0.0"
	cfg, problems := LoadFrom(raw)
	if len(problems) != 0 {
		t.Fatalf("disabled Mini App must not require endpoint settings: %v", problems)
	}
	if cfg.MiniAppEnabled {
		t.Fatal("Mini App unexpectedly enabled")
	}
}

func TestMiniAppRowsAreKnown(t *testing.T) {
	for _, name := range []string{"MINIAPP_ENABLED", "MINIAPP_LISTEN_HOST", "MINIAPP_PORT", "MINIAPP_PUBLIC_URL"} {
		if !HasRow(name) {
			t.Errorf("%s is missing from Env", name)
		}
	}
	if strings.Contains(strings.Join(Env, ","), "MINIAPP_SECRET") {
		t.Fatal("unexpected Mini App secret configuration row")
	}
}
