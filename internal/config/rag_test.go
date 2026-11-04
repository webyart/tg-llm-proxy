package config

import "testing"

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
