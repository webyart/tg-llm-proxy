package config

import "testing"

func TestURLReaderIsOptIn(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake"})
	if len(problems) != 0 || cfg.URLReaderEnabled {
		t.Fatalf("URL reader default=%v problems=%v", cfg.URLReaderEnabled, problems)
	}
	cfg, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake", "URL_READER_ENABLED": "true"})
	if len(problems) != 0 || !cfg.URLReaderEnabled {
		t.Fatalf("URL reader override=%v problems=%v", cfg.URLReaderEnabled, problems)
	}
}
