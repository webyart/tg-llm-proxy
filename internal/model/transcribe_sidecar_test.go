package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTranscribeSidecarUsesSeparateURLWithoutStrataKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("STRATA key must not be forwarded to the transcription sidecar")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer server.Close()
	client, err := New(Options{
		ChatURL: "http://strata.invalid/v1/chat/completions", APIKey: "secret", TranscriptionURL: server.URL + "/v1/audio/transcriptions", TranscriptionNoAuth: true, Attempts: 1, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	text, err := client.Transcribe(context.Background(), TranscriptionRequest{Data: []byte("OggS")})
	if err != nil || text != "ok" {
		t.Fatalf("text=%q err=%v", text, err)
	}
}
