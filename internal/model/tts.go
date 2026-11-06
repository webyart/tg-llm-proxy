package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const TTSMaxResponseBytes = 20 << 20

var (
	ErrTTSInput       = errors.New("speech synthesis input is empty or exceeds the character limit")
	ErrTTSSpeed       = errors.New("speech synthesis speed must be one of 0.75, 1, 1.25, 1.5")
	ErrTTSTimeout     = errors.New("speech synthesis timed out")
	ErrTTSUnavailable = errors.New("speech synthesis endpoint is unavailable")
	ErrTTSContentType = errors.New("speech synthesis endpoint did not return MP3 audio")
	ErrTTSTooLarge    = errors.New("speech synthesis response exceeds 20 MiB")
	ErrTTSEmpty       = errors.New("speech synthesis endpoint returned empty audio")
)

type TTSUpstreamError struct {
	Status int
}

func (e *TTSUpstreamError) Error() string {
	return fmt.Sprintf("speech synthesis endpoint returned HTTP %d", e.Status)
}

type SpeechSynthesizer interface {
	Synthesize(ctx context.Context, text string, speed float64) ([]byte, error)
}

type TTSOptions struct {
	URL      string
	Model    string
	Voice    string
	Timeout  time.Duration
	MaxChars int
}

type TTS struct {
	url      string
	model    string
	voice    string
	maxChars int
	client   *http.Client
}

func NewTTS(opts TTSOptions) (*TTS, error) {
	parsed, err := url.Parse(strings.TrimSpace(opts.URL))
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("speech synthesis URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		return nil, errors.New("speech synthesis model is empty")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	maxChars := opts.MaxChars
	if maxChars <= 0 {
		maxChars = 1000
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &TTS{url: parsed.String(), model: model, voice: strings.TrimSpace(opts.Voice), maxChars: maxChars, client: client}, nil
}

func (c *TTS) Synthesize(ctx context.Context, text string, speed float64) ([]byte, error) {
	if strings.TrimSpace(text) == "" || len([]rune(text)) > c.maxChars {
		return nil, ErrTTSInput
	}
	switch speed {
	case 0.75, 1, 1.25, 1.5:
	default:
		return nil, ErrTTSSpeed
	}
	payload, err := json.Marshal(struct {
		Model          string  `json:"model"`
		Input          string  `json:"input"`
		Voice          string  `json:"voice,omitempty"`
		ResponseFormat string  `json:"response_format"`
		Speed          float64 `json:"speed"`
		SampleRate     int     `json:"sample_rate"`
	}{Model: c.model, Input: text, Voice: c.voice, ResponseFormat: "mp3", Speed: speed, SampleRate: 22050})
	if err != nil {
		return nil, errors.New("speech synthesis request could not be encoded")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("speech synthesis request could not be created")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")
	resp, err := c.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrTTSTimeout
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return nil, context.Canceled
		}
		return nil, ErrTTSUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &TTSUpstreamError{Status: resp.StatusCode}
	}
	contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || contentType != "audio/mpeg" && contentType != "audio/mp3" {
		return nil, ErrTTSContentType
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, TTSMaxResponseBytes+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrTTSTimeout
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return nil, context.Canceled
		}
		return nil, ErrTTSUnavailable
	}
	if len(data) > TTSMaxResponseBytes {
		return nil, ErrTTSTooLarge
	}
	if len(data) == 0 {
		return nil, ErrTTSEmpty
	}
	return data, nil
}
