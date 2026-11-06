package config

import (
	"strings"
	"testing"
)

func TestTTSDefaultsRemainDisabled(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "1:test"})
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if cfg.TTSEnabled {
		t.Fatal("TTS must be disabled by default")
	}
	if cfg.TTSModel != DefaultTTSModel || cfg.TTSVoice != DefaultTTSVoice || cfg.TTSMaxChars != DefaultTTSMaxChars || cfg.TTSTimeoutSeconds != DefaultTTSTimeoutSeconds {
		t.Fatalf("unexpected TTS defaults: %+v", cfg)
	}
	if cfg.TTSURL != DefaultBaseURL+"/v1/audio/speech" {
		t.Fatalf("TTSURL = %q", cfg.TTSURL)
	}
}

func TestTTSUsesTranscriptionHostAndValidatedSettings(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":     "1:test",
		"TRANSCRIPTION_BASE_URL": "http://whisper:8000/v1/",
		"TTS_ENABLED":            "true",
		"TTS_MODEL":              "speaches-ai/piper-ru_RU-ruslan-medium",
		"TTS_VOICE":              "ruslan",
		"TTS_MAX_CHARS":          "500",
		"TTS_TIMEOUT_SECONDS":    "15",
	})
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if !cfg.TTSEnabled || cfg.TTSURL != "http://whisper:8000/v1/audio/speech" || cfg.TTSMaxChars != 500 || cfg.TTSTimeoutSeconds != 15 {
		t.Fatalf("unexpected TTS config: %+v", cfg)
	}
}

func TestTTSValidation(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
	}{
		{"TTS_ENABLED", "maybe"},
		{"TTS_MAX_CHARS", "0"},
		{"TTS_MAX_CHARS", "10001"},
		{"TTS_MAX_CHARS", "bad"},
		{"TTS_TIMEOUT_SECONDS", "0"},
		{"TTS_TIMEOUT_SECONDS", "301"},
		{"TTS_MODEL", "model with spaces"},
		{"TTS_MODEL", strings.Repeat("m", 201)},
		{"TTS_VOICE", "voice with spaces"},
		{"TTS_VOICE", "voice/name"},
	} {
		t.Run(test.name+"="+test.value, func(t *testing.T) {
			_, problems := LoadFrom(map[string]string{
				"TELEGRAM_BOT_TOKEN": "1:test",
				"TTS_ENABLED":        "true",
				test.name:            test.value,
			})
			if len(problems) != 1 || !strings.Contains(problems[0], test.name) {
				t.Fatalf("problems = %v, want one naming %s", problems, test.name)
			}
		})
	}
}

func TestTTSDisabledIgnoresModelSpecificVoice(t *testing.T) {
	_, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:test",
		"TTS_MODEL":          "not currently loaded",
		"TTS_VOICE":          "model/specific",
	})
	if len(problems) != 0 {
		t.Fatalf("disabled TTS must not require a loaded or model-specific voice: %v", problems)
	}
}
