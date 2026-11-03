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
		"https://models.example.com/v1",
	}
	for _, base := range valid {
		if problem := validateStrataBase(base); problem != "" {
			t.Errorf("valid local/HTTPS URL %q rejected: %s", base, problem)
		}
	}
	invalid := []string{
		"http://models.example.com",
		"https://user:password@models.example.com",
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
