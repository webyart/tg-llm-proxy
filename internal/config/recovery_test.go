package config

import (
	"strings"
	"testing"
)

func TestTranscriptionModelDefaultsToWhisper(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake", "STRATA_MODEL": "chat-model"})
	if len(problems) != 0 || cfg.TranscriptionModel != DefaultTranscriptionModel {
		t.Fatalf("transcription model=%q problems=%v", cfg.TranscriptionModel, problems)
	}
	cfg, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake", "STRATA_TRANSCRIPTION_MODEL": "voice-model"})
	if len(problems) != 0 || cfg.TranscriptionModel != "voice-model" {
		t.Fatalf("override=%q problems=%v", cfg.TranscriptionModel, problems)
	}
	if cfg.TranscriptionBase != cfg.StrataBase || cfg.TranscriptionURL != "http://host.docker.internal:8000/v1/audio/transcriptions" {
		t.Fatalf("default transcription endpoint=%q URL=%q", cfg.TranscriptionBase, cfg.TranscriptionURL)
	}
	cfg, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake", "TRANSCRIPTION_BASE_URL": "http://whisper:8000"})
	if len(problems) != 0 || cfg.TranscriptionURL != "http://whisper:8000/v1/audio/transcriptions" {
		t.Fatalf("sidecar transcription URL=%q problems=%v", cfg.TranscriptionURL, problems)
	}
	if !HasRow("STRATA_TRANSCRIPTION_MODEL") || !HasRow("TRANSCRIPTION_BASE_URL") {
		t.Fatal("transcription rows are missing from Env")
	}
}

func TestRecoveryMaxAgeConfiguration(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake"})
	if len(problems) != 0 || cfg.RecoveryMaxAgeHours != DefaultRecoveryMaxAgeHours {
		t.Fatalf("default=%d problems=%v", cfg.RecoveryMaxAgeHours, problems)
	}
	cfg, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake", "RECOVERY_MAX_AGE_HOURS": "0"})
	if len(problems) != 0 || cfg.RecoveryMaxAgeHours != 0 {
		t.Fatalf("disabled=%d problems=%v", cfg.RecoveryMaxAgeHours, problems)
	}
	_, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:fake", "RECOVERY_MAX_AGE_HOURS": "bad"})
	if len(problems) != 1 || !strings.Contains(problems[0], "RECOVERY_MAX_AGE_HOURS") {
		t.Fatalf("invalid row problems=%v", problems)
	}
	if !HasRow("RECOVERY_MAX_AGE_HOURS") {
		t.Fatal("RECOVERY_MAX_AGE_HOURS is missing from Env")
	}
}
