package config

import "testing"

// TestStreamDraftsDefaultsToOff — draft previews keep the position where the answer started, so with a second
// question already in the chat the finished answer can appear above it. The edit path is the default; drafts
// are an explicit opt-in.
func TestStreamDraftsDefaultsToOff(t *testing.T) {
	var cfg, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "12345:test-token"})
	if len(problems) > 0 {
		t.Fatalf("config: %v", problems)
	}
	if cfg.StreamDrafts {
		t.Fatal("STREAM_DRAFTS_ENABLED is on by default")
	}

	cfg, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "12345:test-token", "STREAM_DRAFTS_ENABLED": "true"})
	if len(problems) > 0 {
		t.Fatalf("config: %v", problems)
	}
	if !cfg.StreamDrafts {
		t.Fatal("STREAM_DRAFTS_ENABLED=true was ignored")
	}

	if _, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "12345:test-token", "STREAM_DRAFTS_ENABLED": "maybe"}); len(problems) == 0 {
		t.Fatal("a non-boolean STREAM_DRAFTS_ENABLED was accepted")
	}
}
