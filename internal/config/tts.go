package config

import (
	"fmt"
	"strings"
)

func loadTTS(raw map[string]string, cfg *Config) []string {
	problems := []string{}
	enabled, problem := Flag(raw, "TTS_ENABLED", DefaultTTSEnabled)
	cfg.TTSEnabled = enabled
	problems = Push(problems, problem)

	maxChars, problem := Num(raw, "TTS_MAX_CHARS", DefaultTTSMaxChars, 1, 10000)
	cfg.TTSMaxChars = maxChars
	problems = Push(problems, problem)

	timeout, problem := Num(raw, "TTS_TIMEOUT_SECONDS", DefaultTTSTimeoutSeconds, 1, 300)
	cfg.TTSTimeoutSeconds = timeout
	problems = Push(problems, problem)

	cfg.TTSModel = strings.TrimSpace(raw["TTS_MODEL"])
	if cfg.TTSModel == "" {
		cfg.TTSModel = DefaultTTSModel
	}
	cfg.TTSVoice = strings.TrimSpace(raw["TTS_VOICE"])
	if cfg.TTSVoice == "" {
		cfg.TTSVoice = DefaultTTSVoice
	}
	cfg.TTSURL = JoinURL(cfg.TranscriptionBase, "/audio/speech")
	if !enabled {
		return problems
	}
	if !validTTSModel(cfg.TTSModel) {
		problems = Push(problems, "ENV TTS_MODEL must be 1-200 ASCII letters, digits, dots, underscores, hyphens or slashes")
	}
	if !validTTSVoice(cfg.TTSVoice) {
		problems = Push(problems, fmt.Sprintf("ENV TTS_VOICE must be 1-64 ASCII letters, digits, underscores or hyphens when TTS_ENABLED=true; %s is used by default", DefaultTTSVoice))
	}
	return problems
}

func validTTSModel(model string) bool {
	return validTTSIdentifier(model, 200, true)
}

func validTTSVoice(voice string) bool {
	return validTTSIdentifier(voice, 64, false)
}

func validTTSIdentifier(value string, maximum int, allowSlash bool) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, char := range []byte(value) {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-' || allowSlash && char == '/' {
			continue
		}
		return false
	}
	return true
}
