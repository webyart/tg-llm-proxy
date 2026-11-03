package model

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTranscribeSendsMultipartAudio(t *testing.T) {
	var requestBody string
	var contentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		contentType = r.Header.Get("Content-Type")
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read multipart: %v", err)
		}
		requestBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"распознанный текст"}`))
	}))
	defer server.Close()
	client, err := New(Options{ChatURL: server.URL + "/v1/chat/completions", APIKey: "key", Model: "chat-model", TranscriptionModel: "voice-model", Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	text, err := client.Transcribe(context.Background(), TranscriptionRequest{FileName: "voice.ogg", Data: []byte("OggS-audio")})
	if err != nil || text != "распознанный текст" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if !strings.HasPrefix(contentType, "multipart/form-data;") || !strings.Contains(requestBody, `name="model"`) || !strings.Contains(requestBody, "voice-model") || !strings.Contains(requestBody, `filename="voice.ogg"`) || !strings.Contains(requestBody, "OggS-audio") {
		t.Fatalf("multipart request is incomplete; content-type=%q", contentType)
	}
}

func TestTranscribeRejectsEmptyAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":" "}`))
	}))
	defer server.Close()
	client, err := New(Options{ChatURL: server.URL + "/v1/chat/completions", Attempts: 1, Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Transcribe(context.Background(), TranscriptionRequest{Data: []byte("OggS")})
	if err != ErrEmptyTranscription && !strings.Contains(err.Error(), ErrEmptyTranscription.Error()) {
		t.Fatalf("err=%v, want ErrEmptyTranscription", err)
	}
}
