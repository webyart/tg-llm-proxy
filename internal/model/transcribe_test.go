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
	tests := []struct {
		name         string
		req          TranscriptionRequest
		wantFileName string
		wantMIME     string
	}{
		{name: "defaults", req: TranscriptionRequest{Data: []byte("OggS-audio")}, wantFileName: "voice.ogg", wantMIME: "audio/ogg"},
		{name: "requested metadata", req: TranscriptionRequest{FileName: "clip.m4a", MIME: "audio/mp4", Data: []byte("m4a-audio")}, wantFileName: "clip.m4a", wantMIME: "audio/mp4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var contentType, fileName, fileMIME, modelName, fileData string
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
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Errorf("parse multipart: %v", err)
					return
				}
				modelName = r.FormValue("model")
				file, header, err := r.FormFile("file")
				if err != nil {
					t.Errorf("file part: %v", err)
					return
				}
				data, err := io.ReadAll(file)
				_ = file.Close()
				if err != nil {
					t.Errorf("read file part: %v", err)
					return
				}
				fileData = string(data)
				fileName = header.Filename
				fileMIME = header.Header.Get("Content-Type")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"text":"распознанный текст"}`))
			}))
			defer server.Close()
			client, err := New(Options{ChatURL: server.URL + "/v1/chat/completions", APIKey: "key", Model: "chat-model", TranscriptionModel: "voice-model", Timeout: time.Second})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			text, err := client.Transcribe(context.Background(), tt.req)
			if err != nil || text != "распознанный текст" {
				t.Fatalf("text=%q err=%v", text, err)
			}
			if !strings.HasPrefix(contentType, "multipart/form-data;") || modelName != "voice-model" || fileData != string(tt.req.Data) {
				t.Fatalf("multipart request is incomplete; content-type=%q model=%q data=%q", contentType, modelName, fileData)
			}
			if fileName != tt.wantFileName || fileMIME != tt.wantMIME {
				t.Errorf("file metadata = %q, %q; want %q, %q", fileName, fileMIME, tt.wantFileName, tt.wantMIME)
			}
		})
	}
}

func TestTranscribeEscapesFileNameHeader(t *testing.T) {
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read multipart: %v", err)
			return
		}
		requestBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer server.Close()
	client, err := New(Options{ChatURL: server.URL + "/v1/chat/completions", Attempts: 1, Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Transcribe(context.Background(), TranscriptionRequest{FileName: "voice.ogg\r\nX-Injected: true", Data: []byte("OggS")})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if strings.Contains(requestBody, "\r\nX-Injected:") {
		t.Fatal("file name injected a multipart header")
	}
}

func TestTranscribeRejectsInvalidMIME(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()
	client, err := New(Options{ChatURL: server.URL + "/v1/chat/completions", Attempts: 1, Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Transcribe(context.Background(), TranscriptionRequest{MIME: "audio/ogg\r\nX-Injected: true", Data: []byte("OggS")})
	if err == nil {
		t.Fatal("Transcribe accepted an invalid MIME type")
	}
	if called {
		t.Fatal("request with an invalid MIME type reached the server")
	}
}

func TestTranscribeIncludesProviderErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"unsupported audio model"}`))
	}))
	defer server.Close()
	client, err := New(Options{ChatURL: server.URL + "/v1/chat/completions", Attempts: 1, Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Transcribe(context.Background(), TranscriptionRequest{Data: []byte("OggS")})
	if err == nil || !strings.Contains(err.Error(), "unsupported audio model") {
		t.Fatalf("err=%v, want provider detail", err)
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
