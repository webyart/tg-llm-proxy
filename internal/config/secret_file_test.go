package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSecretFiles — a reader over an in-memory map, so the expansion is tested without touching the filesystem.
func fakeSecretFiles(files map[string]string, failures map[string]error) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if err, broken := failures[path]; broken {
			return nil, err
		}
		if content, found := files[path]; found {
			return []byte(content), nil
		}
		return nil, errors.New("no such file")
	}
}

// TestSecretFileRowsFillTheRows — the whole point of the feature: the token never appears in the process
// environment, only the path to it does.
func TestSecretFileRowsFillTheRows(t *testing.T) {
	var raw = map[string]string{
		"TELEGRAM_BOT_TOKEN_FILE": "/run/secrets/telegram_token",
		"STRATA_API_KEY_FILE":     "/run/secrets/strata_key",
	}
	var problems = ExpandFileRows(raw, fakeSecretFiles(map[string]string{
		"/run/secrets/telegram_token": "12345:telegram-secret\n",
		"/run/secrets/strata_key":     "sk-strata\n",
	}, nil))
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	if raw["TELEGRAM_BOT_TOKEN"] != "12345:telegram-secret" {
		t.Fatalf("token = %q, want the trimmed file content", raw["TELEGRAM_BOT_TOKEN"])
	}
	if raw["STRATA_API_KEY"] != "sk-strata" {
		t.Fatalf("key = %q", raw["STRATA_API_KEY"])
	}
	for _, name := range []string{"TELEGRAM_BOT_TOKEN_FILE", "STRATA_API_KEY_FILE"} {
		if _, left := raw[name]; left {
			t.Fatalf("%s is still in the map after expansion", name)
		}
	}
}

// TestSecretFileRowsConflictWithTheRow — both spellings at once is refused, not resolved by precedence: a bot
// that silently picked one of two secrets would be a mystery to debug.
func TestSecretFileRowsConflictWithTheRow(t *testing.T) {
	var raw = map[string]string{
		"TELEGRAM_BOT_TOKEN":      "111:from-env",
		"TELEGRAM_BOT_TOKEN_FILE": "/run/secrets/token",
	}
	var problems = ExpandFileRows(raw, fakeSecretFiles(map[string]string{"/run/secrets/token": "222:from-file"}, nil))
	if len(problems) != 1 || !strings.Contains(problems[0], "TELEGRAM_BOT_TOKEN_FILE") {
		t.Fatalf("problems = %v, want one conflict naming the file row", problems)
	}
	if raw["TELEGRAM_BOT_TOKEN"] != "111:from-env" {
		t.Fatalf("the environment value was overwritten: %q", raw["TELEGRAM_BOT_TOKEN"])
	}
}

// TestSecretFileRowsRefuseBadPathsAndContent keeps the fail-fast rule for every way a file can be wrong.
func TestSecretFileRowsRefuseBadPathsAndContent(t *testing.T) {
	var cases = []struct {
		name  string
		raw   map[string]string
		files map[string]string
		want  string
	}{
		{
			name: "relative path",
			raw:  map[string]string{"TELEGRAM_BOT_TOKEN_FILE": "secrets/token"},
			want: "absolute path",
		},
		{
			name:  "unreadable",
			raw:   map[string]string{"STRATA_API_KEY_FILE": "/run/secrets/key"},
			want:  "cannot be read",
			files: nil,
		},
		{
			name:  "empty",
			raw:   map[string]string{"STRATA_API_KEY_FILE": "/run/secrets/key"},
			files: map[string]string{"/run/secrets/key": "  \n"},
			want:  "empty file",
		},
		{
			name:  "too large",
			raw:   map[string]string{"STRATA_API_KEY_FILE": "/run/secrets/key"},
			files: map[string]string{"/run/secrets/key": strings.Repeat("x", secretFileMaxBytes+1)},
			want:  "larger than",
		},
		{
			name:  "nul byte",
			raw:   map[string]string{"WEBHOOK_SECRET_TOKEN_FILE": "/run/secrets/hook"},
			files: map[string]string{"/run/secrets/hook": "abc\x00def"},
			want:  "NUL bytes",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var problems = ExpandFileRows(test.raw, fakeSecretFiles(test.files, nil))
			if len(problems) != 1 || !strings.Contains(problems[0], test.want) {
				t.Fatalf("problems = %v, want one mentioning %q", problems, test.want)
			}
		})
	}
}

// TestSecretFileRowsDoNotTouchUnrelatedRows: the mechanism is limited to the three secrets, so an arbitrary row
// cannot be redirected to a file that shadows what an operator configured.
func TestSecretFileRowsDoNotTouchUnrelatedRows(t *testing.T) {
	var raw = map[string]string{
		"STRATA_MODEL_FILE": "/run/secrets/model",
		"STRATA_MODEL":      "qwen-test",
	}
	var problems = ExpandFileRows(raw, fakeSecretFiles(map[string]string{"/run/secrets/model": "other-model"}, nil))
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if raw["STRATA_MODEL"] != "qwen-test" {
		t.Fatalf("a non-secret row was redirected: %q", raw["STRATA_MODEL"])
	}
}

// TestLoadFromReadsSecretFiles is the end-to-end case: a configuration that only carries the path loads the
// secret and passes the token checks.
func TestLoadFromReadsSecretFiles(t *testing.T) {
	var path = filepath.Join(t.TempDir(), "telegram_token")
	if err := os.WriteFile(path, []byte("12345:from-file\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	var cfg, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN_FILE": path,
		"STRATA_MODEL":            "qwen-test",
	})
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	if cfg.BotToken != "12345:from-file" {
		t.Fatalf("token = %q", cfg.BotToken)
	}
}
