package config

import "testing"

func TestToolsEnabledConfiguration(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake"})
	if len(problems) != 0 || !cfg.ToolsEnabled {
		t.Fatalf("default tools=%v problems=%v", cfg.ToolsEnabled, problems)
	}
	cfg, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake", "TOOLS_ENABLED": "false"})
	if len(problems) != 0 || cfg.ToolsEnabled {
		t.Fatalf("disabled tools=%v problems=%v", cfg.ToolsEnabled, problems)
	}
}
