// fallback.go — one second chance on a configured reserve model.
//
// The trigger is deliberately narrow: only an error Retryable already calls retryable (5xx, 429, a transient
// provider error inside a stream, or a network failure). A 400/401/404/parse error is a property of the
// request, not of the model, and another model would answer it with the same failure.
//
// A streamed answer is never replayed. The wrapper records whether any Delta reached the caller; once one
// has, the text is on the user's screen and a second stream would duplicate it, so the primary error is
// returned exactly as it arrived.
package model

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// FallbackOptions names the two models and carries the optional instruments. PrimaryModel and FallbackModel
// are used only in errors and the one warning of an attempt; they are never sent in a request body.
type FallbackOptions struct {
	PrimaryModel  string
	FallbackModel string
	Metrics       *Metrics
	Logger        *slog.Logger
}

// fallbackClient is the Client half of the wrapper. It is returned on its own when the primary is not a
// Streamer, so a client that cannot stream does not grow a Stream method that would fail at the call.
type fallbackClient struct {
	primary       Client
	fallback      Client
	primaryModel  string
	fallbackModel string
	metrics       *Metrics
	logger        *slog.Logger
}

// fallbackStreamerClient adds Stream to the wrapper. It exists as a distinct type because Go decides
// interface satisfaction statically: a struct with a Stream method always implements Streamer, even when
// the primary client cannot stream.
type fallbackStreamerClient struct {
	*fallbackClient
	primaryStreamer  Streamer
	fallbackStreamer Streamer
}

// NewFallback wraps primary with one retry on fallback after a retryable failure. An empty FallbackModel
// means the feature is off and the primary client is returned untouched — not wrapped in a no-op, so the
// existing tests and the byte-for-byte request behaviour stay exactly what they were.
//
// Transcription is deliberately not part of the returned client: the caller keeps using the primary for
// it, and a half-fallback that sends audio to a chat model would be worse than no fallback at all.
func NewFallback(primary, fallback Client, opts FallbackOptions) Client {
	if primary == nil {
		return nil
	}
	if opts.FallbackModel == "" || strings.TrimSpace(opts.FallbackModel) == "" || fallback == nil {
		return primary
	}
	var base = &fallbackClient{
		primary:       primary,
		fallback:      fallback,
		primaryModel:  opts.PrimaryModel,
		fallbackModel: strings.TrimSpace(opts.FallbackModel),
		metrics:       opts.Metrics,
		logger:        opts.Logger,
	}
	var primaryStreamer, streams = primary.(Streamer)
	if !streams {
		return base
	}
	// The wrapper must be a Streamer whenever the primary is one. A fallback that cannot stream still
	// leaves the wrapper a Streamer; Stream then returns the primary error instead of pretending a second
	// attempt exists.
	var fallbackStreamer, _ = fallback.(Streamer)
	return &fallbackStreamerClient{
		fallbackClient:   base,
		primaryStreamer:  primaryStreamer,
		fallbackStreamer: fallbackStreamer,
	}
}

// Complete tries the primary first and repeats exactly once on the fallback after a retryable error.
func (c *fallbackClient) Complete(ctx context.Context, req Request) (Response, error) {
	var response, err = c.primary.Complete(ctx, req)
	if err == nil {
		return response, nil
	}
	if !Retryable(err) {
		return response, err
	}

	c.warnFallback(err)
	var fallbackRequest = req
	fallbackRequest.Model = c.fallbackModel
	var fallbackResponse, fallbackErr = c.fallback.Complete(ctx, fallbackRequest)
	if fallbackErr != nil {
		c.metrics.fallbackAttempt("error")
		return fallbackResponse, fallbackFailed("Complete", c.primaryModel, c.fallbackModel, err, fallbackErr)
	}
	c.metrics.fallbackAttempt("ok")
	return fallbackResponse, nil
}

// Stream tries the primary and repeats on the fallback only while no text has reached the caller.
func (c *fallbackStreamerClient) Stream(ctx context.Context, req Request, h StreamHandler) (Response, error) {
	var delivered bool
	var primaryHandler = StreamHandler{
		Progress: h.Progress,
		Delta: func(text string) error {
			delivered = true
			if h.Delta == nil {
				return nil
			}
			return h.Delta(text)
		},
	}
	var response, err = c.primaryStreamer.Stream(ctx, req, primaryHandler)
	if err == nil {
		return response, nil
	}
	if delivered {
		// The user already reads part of the answer. A second stream would repeat that text, so the
		// primary error goes out as-is and the caller keeps what arrived.
		return response, err
	}
	if !Retryable(err) {
		return response, err
	}
	if c.fallbackStreamer == nil {
		// The configured fallback client cannot stream; there is no second attempt to make.
		return response, err
	}

	c.warnFallback(err)
	var fallbackRequest = req
	fallbackRequest.Model = c.fallbackModel
	var fallbackResponse, fallbackErr = c.fallbackStreamer.Stream(ctx, fallbackRequest, h)
	if fallbackErr != nil {
		c.metrics.fallbackAttempt("error")
		return fallbackResponse, fallbackFailed("Stream", c.primaryModel, c.fallbackModel, err, fallbackErr)
	}
	c.metrics.fallbackAttempt("ok")
	return fallbackResponse, nil
}

// Models keeps the list of the primary endpoint and asks the fallback only when the primary failed. That
// keeps /model pointed at the endpoint an operator configured; the fallback client is a second chance,
// not a second catalogue.
func (c *fallbackClient) Models(ctx context.Context) ([]string, error) {
	var models, err = c.primary.Models(ctx)
	if err == nil {
		return models, nil
	}

	c.warnFallback(err)
	var fallbackModels, fallbackErr = c.fallback.Models(ctx)
	if fallbackErr != nil {
		c.metrics.fallbackAttempt("error")
		return fallbackModels, fallbackFailed("Models", c.primaryModel, c.fallbackModel, err, fallbackErr)
	}
	c.metrics.fallbackAttempt("ok")
	return fallbackModels, nil
}

// warnFallback writes the ONE structured warning of an attempt. The two model names and a class of the
// failure are enough to operate the feature; the error itself is not logged because an endpoint error can
// echo the request body and the key never belongs in a log line.
func (c *fallbackClient) warnFallback(err error) {
	if c.logger == nil {
		return
	}
	c.logger.Warn("model fallback attempt",
		"primary_model", c.primaryModel,
		"fallback_model", c.fallbackModel,
		"reason", fallbackReason(err))
}

// fallbackReason maps the retryable error to a body-free class.
func fallbackReason(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Transient {
			return "transient"
		}
		return fmt.Sprintf("http_%d", apiErr.Status)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "context"
	}
	return "network"
}

// fallbackFailed names both models and attaches both causes. The error text of APIError carries no body,
// so the joined message stays free of prompts and keys.
func fallbackFailed(operation, primaryModel, fallbackModel string, primaryErr, fallbackErr error) error {
	return fmt.Errorf("model fallback %s: primary model %q failed, fallback model %q also failed: %w",
		operation, primaryModel, fallbackModel, errors.Join(primaryErr, fallbackErr))
}
