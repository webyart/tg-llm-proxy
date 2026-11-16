package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func TestParseModelProvidersAPIKeyFile(t *testing.T) {
	var providers, problems = parseModelProviders(`[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key_file":"/run/secrets/kimi_api_key","models":["kimi-k3.1-pro"]}]`,
		fakeSecretFiles(map[string]string{"/run/secrets/kimi_api_key": "sk-from-file\n"}, nil))
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(providers))
	}
	if providers[0].APIKey != "sk-from-file" {
		t.Fatalf("APIKey = %q, want the trimmed file content", providers[0].APIKey)
	}
	if providers[0].APIKeyFile != "" {
		t.Fatalf("APIKeyFile = %q after resolution, want empty", providers[0].APIKeyFile)
	}
}

// TestLoadFromModelProvidersAPIKeyFile is the end-to-end case: a real file on disk, exactly what the compose
// secret mount provides. The path is absolute on both Windows and Linux.
func TestLoadFromModelProvidersAPIKeyFile(t *testing.T) {
	var path = filepath.Join(t.TempDir(), "kimi_api_key")
	if err := os.WriteFile(path, []byte("sk-real-file\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	var row, marshalErr = json.Marshal([]map[string]any{{
		"name":         "kimi",
		"base_url":     "https://api.moonshot.ai/v1",
		"api_key_file": path,
		"models":       []string{"kimi-k3.1-pro"},
	}})
	if marshalErr != nil {
		t.Fatalf("marshal: %v", marshalErr)
	}
	var cfg, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:secret",
		"MODEL_PROVIDERS":    string(row),
	})
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(cfg.ModelProviders) != 1 || cfg.ModelProviders[0].APIKey != "sk-real-file" {
		t.Fatalf("providers = %+v, want the key read from %s", cfg.ModelProviders, path)
	}
}

// TestParseModelProvidersRefusesBadKeyFiles keeps the fail-fast rule for every way a key file can be wrong.
// None of the messages may repeat the content of the file: a configuration error ends up in logs and /healthz.
func TestParseModelProvidersRefusesBadKeyFiles(t *testing.T) {
	var cases = []struct {
		name  string
		row   string
		files map[string]string
		want  string
	}{
		{
			name: "both spellings",
			row:  `[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key":"sk-json","api_key_file":"/run/secrets/kimi","models":["m"]}]`,
			want: "both api_key and api_key_file",
		},
		{
			name: "relative path",
			row:  `[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key_file":"secrets/kimi","models":["m"]}]`,
			want: "absolute path",
		},
		{
			name: "unreadable",
			row:  `[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key_file":"/run/secrets/missing","models":["m"]}]`,
			want: "cannot be read",
		},
		{
			name:  "empty",
			row:   `[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key_file":"/run/secrets/empty","models":["m"]}]`,
			files: map[string]string{"/run/secrets/empty": " \n\t"},
			want:  "empty file",
		},
		{
			name:  "too large",
			row:   `[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key_file":"/run/secrets/large","models":["m"]}]`,
			files: map[string]string{"/run/secrets/large": strings.Repeat("x", secretFileMaxBytes+1)},
			want:  "larger than",
		},
		{
			name:  "nul byte",
			row:   `[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key_file":"/run/secrets/nul","models":["m"]}]`,
			files: map[string]string{"/run/secrets/nul": "sk-must-not-leak\x00"},
			want:  "NUL bytes",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var providers, problems = parseModelProviders(test.row, fakeSecretFiles(test.files, nil))
			if len(problems) != 1 {
				t.Fatalf("problems = %v, want exactly one", problems)
			}
			if !strings.Contains(problems[0], "MODEL_PROVIDERS") || !strings.Contains(problems[0], test.want) {
				t.Fatalf("problem %q does not name MODEL_PROVIDERS and %q", problems[0], test.want)
			}
			if strings.Contains(problems[0], "sk-must-not-leak") || strings.Contains(problems[0], "sk-json") {
				t.Fatalf("problem message leaks a key value: %q", problems[0])
			}
			if len(providers) != 0 {
				t.Fatalf("a broken key file must not leave providers active: %+v", providers)
			}
		})
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
