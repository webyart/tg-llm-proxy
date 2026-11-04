package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

var ErrEmptyTranscription = errors.New("the transcription endpoint returned empty text")

type TranscriptionRequest struct {
	Model    string
	FileName string
	MIME     string
	Data     []byte
}

type Transcriber interface {
	Transcribe(ctx context.Context, req TranscriptionRequest) (string, error)
}

func (c *OpenAI) Transcribe(ctx context.Context, req TranscriptionRequest) (string, error) {
	if len(req.Data) == 0 {
		return "", errors.New("transcription audio is empty")
	}
	if cap(c.sem) > 0 {
		select {
		case c.sem <- struct{}{}:
			defer func() { <-c.sem }()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	budget := c.timeout
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < budget {
		budget = time.Until(deadline)
	}
	if budget <= 0 {
		return "", context.DeadlineExceeded
	}
	attemptCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var lastErr error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		text, err := c.transcribeAttempt(attemptCtx, req)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !Retryable(err) || attempt == c.attempts {
			break
		}
		wait := c.backoff * time.Duration(1<<(attempt-1))
		select {
		case <-attemptCtx.Done():
			return "", attemptCtx.Err()
		case <-time.After(wait):
		}
	}
	return "", fmt.Errorf("transcription failed: %w", lastErr)
}

func (c *OpenAI) transcribeAttempt(ctx context.Context, req TranscriptionRequest) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		modelName = c.transcriptionModel
	}
	if modelName == "" {
		modelName = c.model
	}
	if err := writer.WriteField("model", modelName); err != nil {
		return "", err
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = "voice.ogg"
	}
	contentDisposition := mime.FormatMediaType("form-data", map[string]string{
		"name":     "file",
		"filename": fileName,
	})
	if contentDisposition == "" {
		return "", errors.New("invalid transcription file name")
	}
	contentType := strings.TrimSpace(req.MIME)
	if contentType == "" {
		contentType = "audio/ogg"
	}
	contentType = mime.FormatMediaType(contentType, nil)
	if contentType == "" {
		return "", errors.New("invalid transcription MIME type")
	}
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {contentDisposition},
		"Content-Type":        {contentType},
	})
	if err != nil {
		return "", err
	}
	if _, err := part.Write(req.Data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	url := c.transcriptionURL
	if url == "" {
		url = strings.Replace(c.chatURL, "/chat/completions", "/audio/transcriptions", 1)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body.Bytes()))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	httpReq.Header.Set("Accept", "application/json")
	if c.apiKey != "" && !c.transcriptionNoAuth {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode, Body: Flatten(string(payload), BodyLimit)}
		var providerErr struct {
			Message string `json:"message"`
			Detail  string `json:"detail"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &providerErr) == nil {
			detail := strings.TrimSpace(providerErr.Message)
			if detail == "" {
				detail = strings.TrimSpace(providerErr.Error.Message)
			}
			if detail == "" {
				detail = strings.TrimSpace(providerErr.Detail)
			}
			if detail != "" {
				return "", fmt.Errorf("%w: %s", apiErr, Flatten(detail, BodyLimit))
			}
		}
		return "", apiErr
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("transcription response is not JSON: %w", err)
	}
	decoded.Text = strings.TrimSpace(decoded.Text)
	if decoded.Text == "" {
		return "", ErrEmptyTranscription
	}
	return decoded.Text, nil
}
