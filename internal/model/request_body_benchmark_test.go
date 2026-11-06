package model

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type retryBodyTransport struct {
	attempt int
	stream  bool
}

func (t *retryBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	_, _ = io.Copy(io.Discard, req.Body)
	_ = req.Body.Close()
	t.attempt++
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("retry")),
		Request:    req,
	}
	if t.attempt == 1 {
		return resp, nil
	}
	resp.StatusCode = http.StatusOK
	if t.stream {
		resp.Header.Set("Content-Type", "text/event-stream")
		resp.Body = io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
		return resp, nil
	}
	resp.Header.Set("Content-Type", "application/json")
	if strings.HasSuffix(req.URL.Path, "/audio/transcriptions") {
		resp.Body = io.NopCloser(strings.NewReader("{\"text\":\"ok\"}"))
		return resp, nil
	}
	resp.Body = io.NopCloser(strings.NewReader("{\"model\":\"qwen-test\",\"choices\":[{\"message\":{\"content\":\"ok\"}}]}"))
	return resp, nil
}

func newRetryBodyBenchmarkClient(b *testing.B, transport http.RoundTripper) *OpenAI {
	b.Helper()
	client, err := New(Options{
		ChatURL:            "http://model.invalid/v1/chat/completions",
		Model:              "qwen-test",
		TranscriptionModel: "whisper-test",
		Attempts:           2,
		Backoff:            time.Nanosecond,
		Timeout:            time.Second,
		HTTPClient:         &http.Client{Transport: transport},
		Stream:             StreamSettings{Idle: time.Second, Total: time.Second},
	})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	return client
}

func BenchmarkCompleteRetryRequestBody(b *testing.B) {
	transport := &retryBodyTransport{}
	client := newRetryBodyBenchmarkClient(b, transport)
	req := Request{Messages: []Message{{Role: RoleUser, Content: strings.Repeat("request body ", 8192)}}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		transport.attempt = 0
		if _, err := client.Complete(context.Background(), req); err != nil {
			b.Fatalf("Complete: %v", err)
		}
	}
}

func BenchmarkStreamRetryRequestBody(b *testing.B) {
	transport := &retryBodyTransport{stream: true}
	client := newRetryBodyBenchmarkClient(b, transport)
	req := Request{Messages: []Message{{Role: RoleUser, Content: strings.Repeat("request body ", 8192)}}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		transport.attempt = 0
		if _, err := client.Stream(context.Background(), req, StreamHandler{}); err != nil {
			b.Fatalf("Stream: %v", err)
		}
	}
}

func BenchmarkTranscribeRetryRequestBody(b *testing.B) {
	transport := &retryBodyTransport{}
	client := newRetryBodyBenchmarkClient(b, transport)
	req := TranscriptionRequest{FileName: "voice.ogg", MIME: "audio/ogg", Data: []byte(strings.Repeat("audio", 32768))}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		transport.attempt = 0
		if _, err := client.Transcribe(context.Background(), req); err != nil {
			b.Fatalf("Transcribe: %v", err)
		}
	}
}
