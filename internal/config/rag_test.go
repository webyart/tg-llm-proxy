package config

import (
	"strings"
	"testing"
)

func TestRAGLimits(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake"})
	if len(problems) != 0 || cfg.RAGMaxDocuments != 20 || cfg.RAGResultLimit != 3 || cfg.RAGMaxContextChars != 2000 {
		t.Fatalf("RAG defaults=%+v problems=%v", cfg, problems)
	}
	cfg, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:fake", "RAG_MAX_DOCUMENTS": "30", "RAG_RESULT_LIMIT": "5", "RAG_MAX_CONTEXT_CHARS": "4000",
	})
	if len(problems) != 0 || cfg.RAGMaxDocuments != 30 || cfg.RAGResultLimit != 5 || cfg.RAGMaxContextChars != 4000 {
		t.Fatalf("RAG overrides=%+v problems=%v", cfg, problems)
	}
}

// The hybrid layer is optional: the defaults must resolve without a problem and leave the feature off, so an
// existing .env keeps the lexical search it has today.
func TestRAGEmbeddingsDefaults(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake"})
	if len(problems) != 0 {
		t.Fatalf("embedding defaults reported problems: %v", problems)
	}
	if cfg.RAGEmbeddingsEnabled {
		t.Error("RAG_EMBEDDINGS_ENABLED must be off by default")
	}
	if cfg.RAGEmbeddingsBase != DefaultRAGEmbeddingsBaseURL {
		t.Errorf("base=%q, want %q", cfg.RAGEmbeddingsBase, DefaultRAGEmbeddingsBaseURL)
	}
	if cfg.RAGEmbeddingsURL != DefaultRAGEmbeddingsBaseURL+"/embeddings" {
		t.Errorf("URL=%q, want %q", cfg.RAGEmbeddingsURL, DefaultRAGEmbeddingsBaseURL+"/embeddings")
	}
	if cfg.RAGEmbeddingsModel != DefaultRAGEmbeddingsModel {
		t.Errorf("model=%q, want %q", cfg.RAGEmbeddingsModel, DefaultRAGEmbeddingsModel)
	}
	if cfg.RAGEmbeddingsTimeoutSeconds != DefaultRAGEmbeddingsTimeoutSeconds {
		t.Errorf("timeout=%d, want %d", cfg.RAGEmbeddingsTimeoutSeconds, DefaultRAGEmbeddingsTimeoutSeconds)
	}
}

func TestRAGEmbeddingsEnabledResolvesTheEndpoint(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":             "123:fake",
		"RAG_EMBEDDINGS_ENABLED":         "true",
		"RAG_EMBEDDINGS_BASE_URL":        "http://embeddings:8080/v1/",
		"RAG_EMBEDDINGS_MODEL":           "custom-embed",
		"RAG_EMBEDDINGS_TIMEOUT_SECONDS": "30",
	})
	if len(problems) != 0 {
		t.Fatalf("a valid embeddings configuration reported problems: %v", problems)
	}
	if !cfg.RAGEmbeddingsEnabled || cfg.RAGEmbeddingsBase != "http://embeddings:8080/v1" {
		t.Errorf("enabled=%v base=%q", cfg.RAGEmbeddingsEnabled, cfg.RAGEmbeddingsBase)
	}
	if cfg.RAGEmbeddingsURL != "http://embeddings:8080/v1/embeddings" || cfg.RAGEmbeddingsModel != "custom-embed" || cfg.RAGEmbeddingsTimeoutSeconds != 30 {
		t.Errorf("URL=%q model=%q timeout=%d", cfg.RAGEmbeddingsURL, cfg.RAGEmbeddingsModel, cfg.RAGEmbeddingsTimeoutSeconds)
	}
}

// A disabled row must not stop the start; an enabled one must name the broken URL.
func TestRAGEmbeddingsBaseURLValidation(t *testing.T) {
	_, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:fake", "RAG_EMBEDDINGS_BASE_URL": "localhost:1234/v1",
	})
	if len(problems) != 0 {
		t.Fatalf("a disabled feature validated its base URL: %v", problems)
	}
	_, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:fake", "RAG_EMBEDDINGS_ENABLED": "true", "RAG_EMBEDDINGS_BASE_URL": "localhost:1234/v1",
	})
	if len(problems) != 1 || !strings.Contains(problems[0], "RAG_EMBEDDINGS_BASE_URL") || !strings.Contains(problems[0], "absolute HTTP(S) URL") {
		t.Fatalf("problems=%v, want one problem naming RAG_EMBEDDINGS_BASE_URL", problems)
	}
	_, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:fake", "RAG_EMBEDDINGS_ENABLED": "true", "RAG_EMBEDDINGS_BASE_URL": "ftp://host/v1",
	})
	if len(problems) != 1 || !strings.Contains(problems[0], "absolute HTTP(S) URL") {
		t.Fatalf("problems=%v, want the scheme refusal", problems)
	}
	_, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:fake", "RAG_EMBEDDINGS_ENABLED": "true",
		"RAG_EMBEDDINGS_BASE_URL": "http://user:secret@host:1234/v1",
	})
	if len(problems) != 1 || !strings.Contains(problems[0], "userinfo") {
		t.Fatalf("problems=%v, want the userinfo refusal", problems)
	}
}

func TestRAGEmbeddingsTimeoutBounds(t *testing.T) {
	for _, value := range []string{"0", "121"} {
		cfg, problems := LoadFrom(map[string]string{
			"TELEGRAM_BOT_TOKEN": "123:fake", "RAG_EMBEDDINGS_TIMEOUT_SECONDS": value,
		})
		if len(problems) != 1 || cfg.RAGEmbeddingsTimeoutSeconds != DefaultRAGEmbeddingsTimeoutSeconds {
			t.Fatalf("timeout %s: value=%d problems=%v", value, cfg.RAGEmbeddingsTimeoutSeconds, problems)
		}
	}
}
