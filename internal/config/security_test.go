package config

import (
	"strings"
	"testing"
)

func TestStrataURLSecurityValidation(t *testing.T) {
	valid := []string{
		"http://127.0.0.1:8080",
		"http://localhost:8080/v1",
		"http://host.docker.internal:8080",
		"http://strata:8080",
		"http://strata:8080/gateway",
		"https://models.example.com/v1",
		"https://models.example.com/gateway",
	}
	for _, base := range valid {
		if problem := validateStrataBase(base); problem != "" {
			t.Errorf("valid local/HTTPS URL %q rejected: %s", base, problem)
		}
	}
	invalid := []string{
		"http://models.example.com",
		"https://user:password@models.example.com",
		"https://models.example.com/?api_key=secret",
		"https://models.example.com/#access_token=secret",
		"https://models.example.com/?",
		"https://models.example.com/#",
		"ftp://models.example.com",
		"://bad-url",
	}
	for _, base := range invalid {
		if problem := validateStrataBase(base); problem == "" {
			t.Errorf("unsafe URL %q was accepted", base)
		}
	}
}

func TestLoadFromRejectsUnsafeStrataURL(t *testing.T) {
	_, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "123456:fake",
		"STRATA_BASE_URL":    "http://models.example.com",
	})
	found := false
	for _, problem := range problems {
		if strings.Contains(problem, "HTTPS for a non-local endpoint") {
			found = true
		}
	}
	if !found {
		t.Fatalf("remote HTTP was accepted: %v", problems)
	}
}

func TestLoadFromRejectsURLCredentialsWithoutEchoingThem(t *testing.T) {
	for _, test := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "strata query", env: "STRATA_BASE_URL", value: "https://models.example.com/gateway?api_key=query-secret"},
		{name: "strata fragment", env: "STRATA_BASE_URL", value: "https://models.example.com/gateway#access_token=fragment-secret"},
		{name: "transcription query", env: "TRANSCRIPTION_BASE_URL", value: "https://models.example.com/gateway?api_key=query-secret"},
		{name: "transcription fragment", env: "TRANSCRIPTION_BASE_URL", value: "https://models.example.com/gateway#access_token=fragment-secret"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := map[string]string{
				"TELEGRAM_BOT_TOKEN": "123456:fake",
				"STRATA_BASE_URL":    "http://strata:8080/gateway",
				test.env:             test.value,
			}
			cfg, problems := LoadFrom(raw)
			found := false
			for _, problem := range problems {
				if strings.Contains(problem, test.env) && strings.Contains(problem, "query or fragment") {
					found = true
				}
				if strings.Contains(problem, test.value) || strings.Contains(problem, "secret") {
					t.Errorf("configuration problem echoes rejected URL data: %q", problem)
				}
			}
			if !found {
				t.Errorf("problems do not reject %s query/fragment: %v", test.env, problems)
			}
			for name, endpoint := range map[string]string{
				"StrataBase": cfg.StrataBase, "ChatURL": cfg.ChatURL, "ModelsURL": cfg.ModelsURL,
				"TranscriptionBase": cfg.TranscriptionBase, "TranscriptionURL": cfg.TranscriptionURL,
			} {
				if strings.Contains(endpoint, "secret") || strings.ContainsAny(endpoint, "?#") {
					t.Errorf("%s exposes rejected URL data: %q", name, endpoint)
				}
			}
			if test.env == "STRATA_BASE_URL" {
				if cfg.StrataBase != "https://models.example.com/gateway/v1" || cfg.TranscriptionBase != cfg.StrataBase {
					t.Errorf("safe path prefix was not preserved: strata=%q transcription=%q", cfg.StrataBase, cfg.TranscriptionBase)
				}
			} else if cfg.TranscriptionBase != "https://models.example.com/gateway/v1" {
				t.Errorf("safe transcription path prefix was not preserved: %q", cfg.TranscriptionBase)
			}
		})
	}
}
