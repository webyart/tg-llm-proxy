package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FetchModelIDs reads an OpenAI-compatible GET /v1/models endpoint. It is the one HTTP path used by both
// OpenAI.Models and the admin provider refresh, so the response shape and the 1 MiB body bound cannot
// drift between the runtime and the editor.
//
// The caller owns the URL and the credential (both come from the environment-only MODEL_PROVIDERS row),
// and a timeout <= 0 falls back to DefaultTimeout. Every error is wrapped without the API key.
func FetchModelIDs(ctx context.Context, modelsURL, apiKey string, timeout time.Duration, client *http.Client) ([]string, error) {
	modelsURL = strings.TrimSpace(modelsURL)
	if modelsURL == "" {
		return nil, fmt.Errorf("the model list URL is empty")
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if client == nil {
		client = defaultHTTPClient()
	}
	modelCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(modelCtx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &APIError{Status: res.StatusCode, Body: Flatten(string(payload), BodyLimit)}
	}
	var envelope modelsResponse
	if err := json.Unmarshal(payload, &envelope); err == nil && len(envelope.Data) > 0 {
		ids := make([]string, 0, len(envelope.Data))
		for _, item := range envelope.Data {
			if strings.TrimSpace(item.ID) != "" {
				ids = append(ids, item.ID)
			}
		}
		return ids, nil
	}
	var bare []string
	if err := json.Unmarshal(payload, &bare); err == nil {
		return bare, nil
	}
	return nil, fmt.Errorf("GET %s answered %d with something that is not a model list", modelsURL, res.StatusCode)
}
