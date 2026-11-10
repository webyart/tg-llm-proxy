package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoadFromModelProviders(t *testing.T) {
	var cfg, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:secret",
		"MODEL_PROVIDERS": `[
			{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key":"sk-test","models":["kimi-k3.1-pro","kimi-k2.6"],"default_model":"kimi-k2.6","timeout_seconds":90},
			{"name":"local-lmstudio","base_url":"http://host.docker.internal:1234/v1","models":["qwen3-27b"]}
		]`,
	})
	if len(problems) > 0 {
		t.Fatalf("LOAD PROBLEMS: %v", problems)
	}
	if len(cfg.ModelProviders) != 2 {
		t.Fatalf("ModelProviders = %d, want 2", len(cfg.ModelProviders))
	}
	var kimi = cfg.ModelProviders[0]
	if kimi.Name != "kimi" || kimi.APIKey != "sk-test" || kimi.TimeoutSeconds != 90 {
		t.Fatalf("kimi provider = %+v", kimi)
	}
	if strings.Join(kimi.Models, ",") != "kimi-k3.1-pro,kimi-k2.6" || kimi.DefaultModel != "kimi-k2.6" {
		t.Fatalf("kimi models = %v default=%q", kimi.Models, kimi.DefaultModel)
	}
	if kimi.ChatURL != "https://api.moonshot.ai/v1/chat/completions" || kimi.ModelsURL != "https://api.moonshot.ai/v1/models" {
		t.Fatalf("kimi URLs = %q / %q", kimi.ChatURL, kimi.ModelsURL)
	}
	if strings.Count(kimi.ChatURL, "/v1") != 1 {
		t.Fatalf("kimi ChatURL carries %d /v1 segments: %q", strings.Count(kimi.ChatURL, "/v1"), kimi.ChatURL)
	}
	if kimi.IsLocal() {
		t.Fatal("api.moonshot.ai must not be treated as a local endpoint")
	}
	var local = cfg.ModelProviders[1]
	if local.DefaultModel != "qwen3-27b" || local.TimeoutSeconds != DefaultProviderTimeoutSeconds {
		t.Fatalf("local provider = %+v", local)
	}
	if !local.IsLocal() {
		t.Fatalf("host.docker.internal must be local: %+v", local)
	}
	if local.ChatURL != "http://host.docker.internal:1234/v1/chat/completions" {
		t.Fatalf("local ChatURL = %q", local.ChatURL)
	}
}

func TestLoadFromModelProvidersAbsentAndEmpty(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"missing": {"TELEGRAM_BOT_TOKEN": "1:secret"},
		"empty":   {"TELEGRAM_BOT_TOKEN": "1:secret", "MODEL_PROVIDERS": "[]"},
	} {
		var cfg, problems = LoadFrom(env)
		if len(problems) > 0 {
			t.Fatalf("%s: LOAD PROBLEMS: %v", name, problems)
		}
		if len(cfg.ModelProviders) != 0 {
			t.Fatalf("%s: ModelProviders = %+v, want none", name, cfg.ModelProviders)
		}
	}
}

func TestLoadFromModelProvidersAcceptsLegacyModel(t *testing.T) {
	var cfg, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:secret",
		"MODEL_PROVIDERS":    `[{"name":"deepseek","base_url":"https://api.deepseek.com/v1","model":"deepseek-chat"}]`,
	})
	if len(problems) > 0 {
		t.Fatalf("LOAD PROBLEMS: %v", problems)
	}
	var provider = cfg.ModelProviders[0]
	if strings.Join(provider.Models, ",") != "deepseek-chat" || provider.DefaultModel != "deepseek-chat" {
		t.Fatalf("legacy model = %+v", provider)
	}
	if provider.ChatURL != "https://api.deepseek.com/v1/chat/completions" {
		t.Fatalf("deepseek ChatURL = %q", provider.ChatURL)
	}
}

func TestLoadFromModelProvidersPrefersModelsOverLegacyModel(t *testing.T) {
	var cfg, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:secret",
		"MODEL_PROVIDERS":    `[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","model":"legacy","models":["new-a","new-b"]}]`,
	})
	if len(problems) > 0 {
		t.Fatalf("LOAD PROBLEMS: %v", problems)
	}
	var provider = cfg.ModelProviders[0]
	if strings.Join(provider.Models, ",") != "new-a,new-b" || provider.DefaultModel != "new-a" {
		t.Fatalf("provider = %+v", provider)
	}
}

func TestLoadFromModelProvidersRejectsBadRows(t *testing.T) {
	var seven = make([]string, 7)
	for index := range seven {
		seven[index] = fmt.Sprintf(`{"name":"p%d","base_url":"https://example.test/v1","models":["m"]}`, index)
	}
	var twentyOne = make([]string, MaxProviderModels+1)
	for index := range twentyOne {
		twentyOne[index] = fmt.Sprintf("%q", fmt.Sprintf("model-%d", index))
	}
	for name, row := range map[string]string{
		"bad json":        `[{"name":`,
		"too many":        "[" + strings.Join(seven, ",") + "]",
		"no name":         `[{"base_url":"https://example.test/v1","models":["m"]}]`,
		"reserved strata": `[{"name":"strata","base_url":"https://example.test/v1","models":["m"]}]`,
		"uppercase name":  `[{"name":"Kimi","base_url":"https://example.test/v1","models":["m"]}]`,
		"bad name char":   `[{"name":"kim i","base_url":"https://example.test/v1","models":["m"]}]`,
		"duplicate name":  `[{"name":"kimi","base_url":"https://example.test/v1","models":["m"]},{"name":"kimi","base_url":"https://example.test/v2","models":["n"]}]`,
		"no base":         `[{"name":"kimi","models":["m"]}]`,
		"relative base":   `[{"name":"kimi","base_url":"/v1","models":["m"]}]`,
		"userinfo base":   `[{"name":"kimi","base_url":"https://user:pass@example.test/v1","models":["m"]}]`,
		"ftp base":        `[{"name":"kimi","base_url":"ftp://example.test/v1","models":["m"]}]`,
		"no models":       `[{"name":"kimi","base_url":"https://example.test/v1"}]`,
		"empty model":     `[{"name":"kimi","base_url":"https://example.test/v1","models":[""]}]`,
		"duplicate model": `[{"name":"kimi","base_url":"https://example.test/v1","models":["m","m"]}]`,
		"too many models": `[{"name":"kimi","base_url":"https://example.test/v1","models":[` + strings.Join(twentyOne, ",") + `]}]`,
		"default missing": `[{"name":"kimi","base_url":"https://example.test/v1","models":["m"],"default_model":"other"}]`,
		"timeout low":     `[{"name":"kimi","base_url":"https://example.test/v1","models":["m"],"timeout_seconds":4}]`,
		"timeout high":    `[{"name":"kimi","base_url":"https://example.test/v1","models":["m"],"timeout_seconds":601}]`,
	} {
		var cfg, problems = LoadFrom(map[string]string{
			"TELEGRAM_BOT_TOKEN": "1:secret",
			"MODEL_PROVIDERS":    row,
		})
		if len(problems) == 0 {
			t.Errorf("%s: no problems reported", name)
			continue
		}
		var joined = strings.Join(problems, " ")
		if !strings.Contains(joined, "MODEL_PROVIDERS") {
			t.Errorf("%s: problems do not name MODEL_PROVIDERS: %v", name, problems)
		}
		if len(cfg.ModelProviders) != 0 {
			t.Errorf("%s: a broken list must not leave providers active: %+v", name, cfg.ModelProviders)
		}
	}
}

func TestProviderConfigIsLocal(t *testing.T) {
	for _, base := range []string{
		"http://localhost:1234/v1",
		"http://127.0.0.1:1234/v1",
		"http://192.168.1.10:1234/v1",
		"http://10.0.0.5:8000/v1",
		"http://host.docker.internal:1234/v1",
		"http://lmstudio.local:1234/v1",
		"http://strata:8080/v1",
	} {
		if !(ProviderConfig{BaseURL: base}).IsLocal() {
			t.Errorf("%s must be local", base)
		}
	}
	for _, base := range []string{
		"https://api.moonshot.ai/v1",
		"https://api.deepseek.com/v1",
		"https://8.8.8.8/v1",
		"not-a-url",
	} {
		if (ProviderConfig{BaseURL: base}).IsLocal() {
			t.Errorf("%s must not be local", base)
		}
	}
}
