package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTTSSpeechRequestAndMP3Response(t *testing.T) {
	const input = "Привет, это проверка."
	var request struct {
		Model          string  `json:"model"`
		Input          string  `json:"input"`
		Voice          string  `json:"voice"`
		ResponseFormat string  `json:"response_format"`
		Speed          float64 `json:"speed"`
		SampleRate     int     `json:"sample_rate"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/speech" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "audio/mpeg" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected headers: %+v", r.Header)
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "audio/mpeg; charset=binary")
		_, _ = w.Write([]byte("ID3-test-audio"))
	}))
	defer server.Close()
	client, err := NewTTS(TTSOptions{URL: server.URL + "/v1/audio/speech", Model: "speaches-ai/piper-ru_RU-ruslan-medium", Voice: "ruslan", Timeout: time.Second, MaxChars: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if client.client.Timeout != time.Second {
		t.Fatalf("dedicated HTTP timeout = %s", client.client.Timeout)
	}
	audio, err := client.Synthesize(context.Background(), input, 1.25)
	if err != nil || string(audio) != "ID3-test-audio" {
		t.Fatalf("audio = %q, err = %v", audio, err)
	}
	if request.Model != "speaches-ai/piper-ru_RU-ruslan-medium" || request.Input != input || request.Voice != "ruslan" || request.ResponseFormat != "mp3" || request.Speed != 1.25 || request.SampleRate != 22050 {
		t.Fatalf("request = %+v", request)
	}
}

func TestTTSBoundsInputBeforeHTTP(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client, err := NewTTS(TTSOptions{URL: server.URL, Model: "model", MaxChars: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"", "   ", "four", "🙂🙂🙂🙂"} {
		if _, err := client.Synthesize(context.Background(), input, 1); !errors.Is(err, ErrTTSInput) {
			t.Errorf("input %q: err = %v", input, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid input reached HTTP %d times", calls.Load())
	}
}

func TestTTSTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	client, err := NewTTS(TTSOptions{URL: server.URL, Model: "model", Timeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Synthesize(context.Background(), "text", 1)
	if !errors.Is(err, ErrTTSTimeout) {
		t.Fatalf("err = %v, want ErrTTSTimeout", err)
	}
}

func TestTTSRejectsWrongContentTypeOversizeAndRedactsFailures(t *testing.T) {
	const input = "private input that must not escape"
	const upstream = "private upstream body that must not escape"
	for _, test := range []struct {
		name        string
		status      int
		contentType string
		body        []byte
		want        error
	}{
		{name: "content type", status: http.StatusOK, contentType: "application/json", body: []byte(upstream), want: ErrTTSContentType},
		{name: "oversize", status: http.StatusOK, contentType: "audio/mpeg", body: make([]byte, TTSMaxResponseBytes+1), want: ErrTTSTooLarge},
		{name: "upstream", status: http.StatusInternalServerError, contentType: "text/plain", body: []byte(upstream), want: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.status)
				_, _ = w.Write(test.body)
			}))
			defer server.Close()
			client, err := NewTTS(TTSOptions{URL: server.URL + "/v1/audio/speech?private=url", Model: "model"})
			if err == nil {
				t.Fatal("query-bearing test URL must be rejected")
			}
			client, err = NewTTS(TTSOptions{URL: server.URL + "/v1/audio/speech", Model: "model"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Synthesize(context.Background(), input, 1)
			if err == nil {
				t.Fatal("expected an error")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if test.want == nil {
				var upstreamErr *TTSUpstreamError
				if !errors.As(err, &upstreamErr) || upstreamErr.Status != http.StatusInternalServerError {
					t.Fatalf("err = %#v", err)
				}
			}
			if len(err.Error()) > 160 || strings.Contains(err.Error(), input) || strings.Contains(err.Error(), upstream) || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("error leaked private data or was unbounded: %q", err)
			}
		})
	}
}

func TestTTSRejectsEmptyAudio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
	}))
	defer server.Close()
	client, err := NewTTS(TTSOptions{URL: server.URL, Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Synthesize(context.Background(), "text", 1); !errors.Is(err, ErrTTSEmpty) {
		t.Fatalf("err = %v", err)
	}
}

func TestTTSRejectsUnsupportedSpeedBeforeHTTP(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client, err := NewTTS(TTSOptions{URL: server.URL, Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	for _, speed := range []float64{0, 0.5, 1.1, 2, -1} {
		if _, err := client.Synthesize(context.Background(), "text", speed); !errors.Is(err, ErrTTSSpeed) {
			t.Errorf("speed %v: err = %v, want ErrTTSSpeed", speed, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid speed reached HTTP %d times", calls.Load())
	}
}
