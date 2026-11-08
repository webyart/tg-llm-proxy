// Package embed is a small OpenAI-compatible embeddings client.
//
// It exists because the STRATA endpoint the bot talks to has no /v1/embeddings: a separate local server
// (the default is a llama.cpp/LM Studio style host at host.docker.internal:1234) answers POST {base}/embeddings
// with the usual {"data":[{"embedding":[...]}]} envelope. The client is deliberately silent: it never logs the
// API key or the texts it embeds, and an error names the endpoint and the status, not the input.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// MaxBatch is the largest number of inputs of one request. Some local servers cap a request at 64 inputs;
	// a document of a hundred chunks is therefore sent as two requests, and the caller never has to know.
	MaxBatch = 64
	// DefaultTimeout bounds one request when the caller passes no timeout.
	DefaultTimeout = 10 * time.Second
	// bodyLimit bounds the response of one batch: 64 vectors of 768 float JSON numbers fit well below it, and
	// a runaway error page must not sit in the memory of the bot.
	bodyLimit = 32 << 20
)

// ErrNoModel is returned by Embed when the client carries no model id: the request would be rejected by every
// server, so it is refused before the round trip.
var ErrNoModel = errors.New("embed: the model id is empty")

// APIError is a non-2xx answer of the endpoint. Body is a flattened, truncated snippet of the answer; the
// request (and therefore the input texts) is never part of it.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Sprintf("embed: the endpoint answered %d", e.Status)
	}
	return fmt.Sprintf("embed: the endpoint answered %d: %s", e.Status, e.Body)
}

// Client is an OpenAI-compatible embeddings client. It is safe for concurrent use.
type Client struct {
	endpoint string
	model    string
	apiKey   string
	http     *http.Client
	metrics  *Metrics
}

// New builds a client for an OpenAI-compatible base URL. baseURL is expected to carry the API prefix
// ("…/v1"); "/embeddings" is appended to it. A non-positive timeout falls back to DefaultTimeout.
func New(baseURL, model, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		endpoint: strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/embeddings",
		model:    strings.TrimSpace(model),
		apiKey:   strings.TrimSpace(apiKey),
		http: &http.Client{
			Timeout: timeout,
			// A redirect turns POST into GET and drops Authorization on a cross-host hop; refusing it turns a
			// misrouted base URL into a readable APIError instead of an HTML page.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// SetMetrics attaches the optional metric recorder. It must be called before the first Embed call.
func (c *Client) SetMetrics(m *Metrics) { c.metrics = m }

// Model returns the embedding model id the client sends. It is also the key the vectors are stored under.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

// Embed returns one vector per input text, in the order of the input. The call is split into batches of at
// most MaxBatch inputs; an empty input is not sent anywhere and returns an empty result. A response whose
// vectors differ in length is refused: the caller stores one dimension per model, so a mixed answer would
// corrupt the index.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if c == nil {
		return nil, errors.New("embed: the client is nil")
	}
	if c.model == "" {
		return nil, ErrNoModel
	}
	if strings.TrimSpace(c.endpoint) == "/embeddings" {
		return nil, errors.New("embed: the base URL is empty")
	}

	out := make([][]float32, len(texts))
	dimensions := 0
	for start := 0; start < len(texts); start += MaxBatch {
		end := start + MaxBatch
		if end > len(texts) {
			end = len(texts)
		}
		vectors, err := c.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		for _, vector := range vectors {
			if dimensions == 0 {
				dimensions = len(vector)
			} else if len(vector) != dimensions {
				return nil, fmt.Errorf("embed: the endpoint returned vectors of %d and %d dimensions, want one length", dimensions, len(vector))
			}
		}
		copy(out[start:end], vectors)
	}
	return out, nil
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingDatum struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

type embeddingResponse struct {
	Data  []embeddingDatum `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// embedBatch performs one request. The input texts stay out of every error string this function builds.
func (c *Client) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embeddingRequest{Model: c.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("embed: the request could not be encoded: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	res, err := c.http.Do(httpReq)
	if err != nil {
		// net/http wraps the URL, never the header, so the key cannot appear here; the texts are in the body,
		// which the error does not carry either.
		c.metrics.observe(err, 0)
		return nil, err
	}
	defer res.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(res.Body, bodyLimit))
	if err != nil {
		c.metrics.observe(err, 0)
		return nil, fmt.Errorf("embed: the answer of %s could not be read: %w", c.endpoint, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		apiErr := &APIError{Status: res.StatusCode, Body: redact(flatten(string(payload), 300), c.apiKey)}
		c.metrics.observe(apiErr, 0)
		return nil, apiErr
	}

	var parsed embeddingResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		c.metrics.observe(err, 0)
		return nil, fmt.Errorf("embed: %s answered %d with something that is not an embeddings response: %w", c.endpoint, res.StatusCode, err)
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		apiErr := &APIError{Status: res.StatusCode, Body: redact(flatten(parsed.Error.Message, 300), c.apiKey)}
		c.metrics.observe(apiErr, 0)
		return nil, apiErr
	}
	if len(parsed.Data) != len(texts) {
		err := fmt.Errorf("embed: %s returned %d vectors for %d inputs", c.endpoint, len(parsed.Data), len(texts))
		c.metrics.observe(err, 0)
		return nil, err
	}

	vectors := make([][]float32, len(texts))
	placed := make([]bool, len(texts))
	indexed := true
	seen := make(map[int]bool, len(parsed.Data))
	for _, datum := range parsed.Data {
		if datum.Index < 0 || datum.Index >= len(texts) || seen[datum.Index] {
			indexed = false
			break
		}
		seen[datum.Index] = true
	}
	for position, datum := range parsed.Data {
		if len(datum.Embedding) == 0 {
			err := fmt.Errorf("embed: %s returned an empty vector for input %d", c.endpoint, position+1)
			c.metrics.observe(err, 0)
			return nil, err
		}
		target := position
		if indexed {
			target = datum.Index
		}
		vectors[target] = datum.Embedding
		placed[target] = true
	}
	for index, ok := range placed {
		if !ok {
			err := fmt.Errorf("embed: %s returned no vector for input %d", c.endpoint, index+1)
			c.metrics.observe(err, 0)
			return nil, err
		}
	}
	if dimensions := len(vectors[0]); dimensions > 0 {
		for _, vector := range vectors {
			if len(vector) != dimensions {
				err := fmt.Errorf("embed: the endpoint returned vectors of %d and %d dimensions, want one length", dimensions, len(vector))
				c.metrics.observe(err, 0)
				return nil, err
			}
		}
	}
	c.metrics.observe(nil, len(vectors[0]))
	return vectors, nil
}

// flatten collapses a response body into one line of at most limit runes for an error text.
func flatten(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	var out strings.Builder
	count := 0
	for _, r := range text {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		out.WriteRune(r)
		count++
		if count >= limit {
			break
		}
	}
	return strings.TrimSpace(out.String())
}

// redact removes the API key from a server message: an endpoint that echoes the Authorization header must not
// put the secret into a log line that carries the error.
func redact(text, key string) string {
	if key == "" {
		return text
	}
	return strings.ReplaceAll(text, key, "[redacted]")
}
