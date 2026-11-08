package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"bot/internal/metrics"
)

type embeddingServer struct {
	mu       sync.Mutex
	requests []embeddingRequest
	auth     []string
	handler  func(w http.ResponseWriter, req *http.Request, body embeddingRequest)
}

func (s *embeddingServer) client(t *testing.T, timeout time.Duration) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body embeddingRequest
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request is not JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.requests = append(s.requests, body)
		s.auth = append(s.auth, req.Header.Get("Authorization"))
		s.mu.Unlock()
		s.handler(w, req, body)
	}))
	t.Cleanup(server.Close)
	return New(server.URL+"/v1", "text-embedding-test", "secret-key", timeout), server
}

func writeVectors(t *testing.T, w http.ResponseWriter, vectors [][]float32) {
	t.Helper()
	data := make([]map[string]any, 0, len(vectors))
	for index, vector := range vectors {
		data = append(data, map[string]any{"index": index, "embedding": vector})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func TestEmbedSuccess(t *testing.T) {
	s := &embeddingServer{handler: func(w http.ResponseWriter, _ *http.Request, body embeddingRequest) {
		vectors := make([][]float32, len(body.Input))
		for index := range body.Input {
			vectors[index] = []float32{float32(index), 0.5, 1}
		}
		writeVectors(t, w, vectors)
	}}
	client, _ := s.client(t, 2*time.Second)

	vectors, err := client.Embed(context.Background(), []string{"первый", "second"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != 2 || len(vectors[0]) != 3 || vectors[1][0] != 1 {
		t.Fatalf("vectors=%v, want two three-dimensional vectors in input order", vectors)
	}
	if len(s.requests) != 1 || s.requests[0].Model != "text-embedding-test" || len(s.requests[0].Input) != 2 {
		t.Fatalf("requests=%+v", s.requests)
	}
	if s.auth[0] != "Bearer secret-key" {
		t.Errorf("Authorization=%q, want the bearer key", s.auth[0])
	}
	if client.Model() != "text-embedding-test" {
		t.Errorf("Model()=%q", client.Model())
	}
}

func TestEmbedBatchesLargeInput(t *testing.T) {
	s := &embeddingServer{handler: func(w http.ResponseWriter, _ *http.Request, body embeddingRequest) {
		vectors := make([][]float32, len(body.Input))
		for index := range body.Input {
			vectors[index] = []float32{float32(len(body.Input))}
		}
		writeVectors(t, w, vectors)
	}}
	client, _ := s.client(t, 2*time.Second)
	texts := make([]string, MaxBatch+1)
	for index := range texts {
		texts[index] = "text"
	}
	vectors, err := client.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != len(texts) {
		t.Fatalf("got %d vectors, want %d", len(vectors), len(texts))
	}
	if len(s.requests) != 2 {
		t.Fatalf("requests=%d, want 2 batches of at most %d", len(s.requests), MaxBatch)
	}
	if len(s.requests[0].Input) != MaxBatch || len(s.requests[1].Input) != 1 {
		t.Errorf("batch sizes=%d/%d, want %d/1", len(s.requests[0].Input), len(s.requests[1].Input), MaxBatch)
	}
	if vectors[0][0] != MaxBatch || vectors[MaxBatch][0] != 1 {
		t.Errorf("vectors are not in input order: %v", vectors)
	}
}

func TestEmbedDimensionMismatch(t *testing.T) {
	s := &embeddingServer{handler: func(w http.ResponseWriter, _ *http.Request, _ embeddingRequest) {
		writeVectors(t, w, [][]float32{{1, 2, 3}, {1, 2}})
	}}
	client, _ := s.client(t, 2*time.Second)
	_, err := client.Embed(context.Background(), []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("Embed error=%v, want a dimension error", err)
	}
}

func TestEmbedServerError(t *testing.T) {
	s := &embeddingServer{handler: func(w http.ResponseWriter, _ *http.Request, _ embeddingRequest) {
		http.Error(w, "model not loaded", http.StatusInternalServerError)
	}}
	client, _ := s.client(t, 2*time.Second)
	_, err := client.Embed(context.Background(), []string{"secret text"})
	if err == nil {
		t.Fatal("Embed must fail on a 500")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("error=%v, want an APIError with status 500", err)
	}
	if strings.Contains(err.Error(), "secret text") || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("error leaks the input or the key: %q", err)
	}
}

func TestEmbedAPIKeyIsRedacted(t *testing.T) {
	s := &embeddingServer{handler: func(w http.ResponseWriter, _ *http.Request, _ embeddingRequest) {
		http.Error(w, "bad key secret-key in header", http.StatusUnauthorized)
	}}
	client, _ := s.client(t, 2*time.Second)
	_, err := client.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("Embed must fail on a 401")
	}
	if strings.Contains(err.Error(), "secret-key") && !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error leaks the API key: %q", err)
	}
}

func TestEmbedTimeout(t *testing.T) {
	s := &embeddingServer{handler: func(w http.ResponseWriter, _ *http.Request, _ embeddingRequest) {
		time.Sleep(250 * time.Millisecond)
		writeVectors(t, w, [][]float32{{1}})
	}}
	client, _ := s.client(t, 40*time.Millisecond)
	start := time.Now()
	_, err := client.Embed(context.Background(), []string{"slow"})
	if err == nil {
		t.Fatal("Embed must fail when the endpoint is slower than the timeout")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the timeout took %s", elapsed)
	}
}

func TestEmbedEmptyInputSendsNothing(t *testing.T) {
	s := &embeddingServer{handler: func(w http.ResponseWriter, _ *http.Request, _ embeddingRequest) {
		t.Error("an empty input must not reach the network")
	}}
	client, _ := s.client(t, time.Second)
	vectors, err := client.Embed(context.Background(), nil)
	if err != nil || len(vectors) != 0 {
		t.Fatalf("Embed(nil)=%v, %v; want no vectors and no error", vectors, err)
	}
	if len(s.requests) != 0 {
		t.Fatalf("requests=%d, want 0", len(s.requests))
	}
}

func TestEmbedRequiresModel(t *testing.T) {
	client := New("http://127.0.0.1:1/v1", "", "", time.Second)
	if _, err := client.Embed(context.Background(), []string{"a"}); !errors.Is(err, ErrNoModel) {
		t.Fatalf("Embed error=%v, want ErrNoModel", err)
	}
}

func TestEmbedCountsMetrics(t *testing.T) {
	s := &embeddingServer{handler: func(w http.ResponseWriter, _ *http.Request, body embeddingRequest) {
		if body.Input[0] == "fail" {
			http.Error(w, "nope", http.StatusBadRequest)
			return
		}
		writeVectors(t, w, [][]float32{{1, 2, 3, 4}})
	}}
	client, _ := s.client(t, 2*time.Second)
	registry := metrics.NewRegistry()
	client.SetMetrics(NewMetrics(registry))
	if _, err := client.Embed(context.Background(), []string{"ok"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	_, _ = client.Embed(context.Background(), []string{"fail"})

	var out bytes.Buffer
	if _, err := registry.WriteTo(&out); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		`rag_embeddings_total{outcome="ok"} 1`,
		`rag_embeddings_total{outcome="error"} 1`,
		"rag_embedding_dimensions 4",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics do not contain %q:\n%s", want, text)
		}
	}
}
