package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type countingJSONValue struct {
	calls *int
}

func (v countingJSONValue) MarshalJSON() ([]byte, error) {
	*v.calls = *v.calls + 1
	return []byte(`"counted"`), nil
}

type capturedHTTPRequest struct {
	body          []byte
	contentType   string
	accept        string
	authorization string
	contentLength int64
	err           error
}

func captureHTTPRequest(r *http.Request) capturedHTTPRequest {
	body, err := io.ReadAll(r.Body)
	return capturedHTTPRequest{
		body:          body,
		contentType:   r.Header.Get("Content-Type"),
		accept:        r.Header.Get("Accept"),
		authorization: r.Header.Get("Authorization"),
		contentLength: r.ContentLength,
		err:           err,
	}
}

func TestChatRetryReusesRequestBody(t *testing.T) {
	for _, tt := range []struct {
		name        string
		stream      bool
		accept      string
		contentType string
		response    string
	}{
		{name: "complete", accept: "application/json", contentType: "application/json", response: `{"model":"qwen-test","choices":[{"message":{"content":"ok"}}]}`},
		{name: "stream", stream: true, accept: "text/event-stream", contentType: "text/event-stream", response: "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := make(chan capturedHTTPRequest, 2)
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- captureHTTPRequest(r)
				if attempts.Add(1) == 1 {
					http.Error(w, "retry", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", tt.contentType)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()
			client, err := New(Options{
				ChatURL:  server.URL + "/v1/chat/completions",
				APIKey:   "secret-key",
				Model:    "qwen-test",
				Attempts: 2,
				Backoff:  time.Millisecond,
				Timeout:  time.Second,
				Stream:   StreamSettings{Idle: time.Second, Total: time.Second},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			marshalCalls := 0
			req := Request{
				Messages: []Message{{Role: RoleUser, Content: "retry this request"}},
				Tools: []ToolDefinition{{Name: "lookup", Parameters: map[string]any{
					"value": countingJSONValue{calls: &marshalCalls},
				}}},
			}

			var response Response
			if tt.stream {
				response, err = client.Stream(context.Background(), req, StreamHandler{})
			} else {
				response, err = client.Complete(context.Background(), req)
			}
			if err != nil {
				t.Fatalf("chat request: %v", err)
			}
			if response.Content != "ok" || response.Attempts != 2 {
				t.Fatalf("response content=%q attempts=%d, want ok after two attempts", response.Content, response.Attempts)
			}
			if marshalCalls != 1 {
				t.Errorf("json.Marshal value calls = %d, want 1 for the retry sequence", marshalCalls)
			}
			if attempts.Load() != 2 {
				t.Fatalf("HTTP attempts = %d, want 2", attempts.Load())
			}
			first, second := <-requests, <-requests
			if !bytes.Equal(first.body, second.body) {
				t.Errorf("request bodies differ:\nfirst:  %q\nsecond: %q", first.body, second.body)
			}
			for index, captured := range []capturedHTTPRequest{first, second} {
				checkChatRetryRequest(t, captured, index+1, tt.stream, tt.accept)
			}
		})
	}
}

func checkChatRetryRequest(t *testing.T, captured capturedHTTPRequest, attempt int, stream bool, accept string) {
	t.Helper()
	if captured.err != nil {
		t.Fatalf("attempt %d read body: %v", attempt, captured.err)
	}
	if captured.contentType != "application/json" || captured.accept != accept || captured.authorization != "Bearer secret-key" {
		t.Errorf("attempt %d headers: Content-Type=%q Accept=%q Authorization=%q", attempt, captured.contentType, captured.accept, captured.authorization)
	}
	if captured.contentLength != int64(len(captured.body)) {
		t.Errorf("attempt %d Content-Length = %d, body length = %d", attempt, captured.contentLength, len(captured.body))
	}
	var body struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Parameters map[string]any `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(captured.body, &body); err != nil {
		t.Fatalf("attempt %d JSON body: %v", attempt, err)
	}
	if body.Model != "qwen-test" || body.Stream != stream {
		t.Errorf("attempt %d model=%q stream=%v, want qwen-test and %v", attempt, body.Model, body.Stream, stream)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != RoleUser || body.Messages[0].Content != "retry this request" {
		t.Errorf("attempt %d messages = %+v", attempt, body.Messages)
	}
	if len(body.Tools) != 1 || body.Tools[0].Function.Parameters["value"] != "counted" {
		t.Errorf("attempt %d tools = %+v", attempt, body.Tools)
	}
}

type capturedMultipartBody struct {
	model    string
	fileName string
	fileMIME string
	fileData []byte
}

func parseMultipartBody(contentType string, body []byte) (capturedMultipartBody, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return capturedMultipartBody{}, err
	}
	if mediaType != "multipart/form-data" || params["boundary"] == "" {
		return capturedMultipartBody{}, fmt.Errorf("unexpected multipart Content-Type %q", contentType)
	}
	var captured capturedMultipartBody
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return captured, nil
		}
		if err != nil {
			return capturedMultipartBody{}, err
		}
		data, readErr := io.ReadAll(part)
		_ = part.Close()
		if readErr != nil {
			return capturedMultipartBody{}, readErr
		}
		switch part.FormName() {
		case "model":
			captured.model = string(data)
		case "file":
			captured.fileName = part.FileName()
			captured.fileMIME = part.Header.Get("Content-Type")
			captured.fileData = data
		}
	}
}

func TestTranscribeRetryReusesMultipartBody(t *testing.T) {
	requests := make(chan capturedHTTPRequest, 2)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureHTTPRequest(r)
		if attempts.Add(1) == 1 {
			http.Error(w, "retry", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer server.Close()
	client, err := New(Options{
		ChatURL:  server.URL + "/v1/chat/completions",
		APIKey:   "secret-key",
		Model:    "chat-model",
		Attempts: 2,
		Backoff:  time.Millisecond,
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	audio := []byte{0, 'O', 'g', 'g', 'S', '\r', '\n', 0, 255}
	text, err := client.Transcribe(context.Background(), TranscriptionRequest{
		Model:    "voice-model",
		FileName: "clip.m4a",
		MIME:     "audio/mp4",
		Data:     audio,
	})
	if err != nil || text != "ok" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("HTTP attempts = %d, want 2", attempts.Load())
	}
	first, second := <-requests, <-requests
	if !bytes.Equal(first.body, second.body) {
		t.Error("multipart request bodies differ between attempts")
	}
	if first.contentType != second.contentType {
		t.Errorf("multipart Content-Type differs: %q != %q", first.contentType, second.contentType)
	}
	for index, captured := range []capturedHTTPRequest{first, second} {
		attempt := index + 1
		if captured.err != nil {
			t.Fatalf("attempt %d read body: %v", attempt, captured.err)
		}
		if captured.accept != "application/json" || captured.authorization != "Bearer secret-key" {
			t.Errorf("attempt %d headers: Accept=%q Authorization=%q", attempt, captured.accept, captured.authorization)
		}
		if captured.contentLength != int64(len(captured.body)) {
			t.Errorf("attempt %d Content-Length = %d, body length = %d", attempt, captured.contentLength, len(captured.body))
		}
		parsed, err := parseMultipartBody(captured.contentType, captured.body)
		if err != nil {
			t.Fatalf("attempt %d parse multipart: %v", attempt, err)
		}
		if parsed.model != "voice-model" || parsed.fileName != "clip.m4a" || parsed.fileMIME != "audio/mp4" || !bytes.Equal(parsed.fileData, audio) {
			t.Errorf("attempt %d multipart = model %q, file %q, MIME %q, data %v", attempt, parsed.model, parsed.fileName, parsed.fileMIME, parsed.fileData)
		}
	}
}
