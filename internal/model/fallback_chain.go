// fallback_chain.go — an ordered list of reserve providers instead of exactly one second chance.
//
// The trigger stays as narrow as the single-fallback wrapper: only a retryable error moves the request to the
// next reserve (5xx, 429, a transient stream error, a network failure). A 400/401/404/parse error is a property
// of the request, so the chain stops there instead of replaying it at every provider.
//
// A stream is never replayed after text reached the caller: once a delta is on screen, the chain returns the
// error instead of duplicating the answer from another provider.
package model

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Reserve is one entry of the chain: the model name to request and the client that serves it.
type Reserve struct {
	Model  string
	Client Client
}

// ChainOptions carries the primary model name and the optional instruments. Model names appear only in errors
// and warnings; they are never sent as anything but the request body's model field.
type ChainOptions struct {
	PrimaryModel string
	Metrics      *Metrics
	Logger       *slog.Logger
}

type chainClient struct {
	primary      Client
	reserves     []Reserve
	primaryModel string
	metrics      *Metrics
	logger       *slog.Logger
}

type chainStreamer struct {
	*chainClient
	primaryStreamer  Streamer
	reserveStreamers []Streamer
}

// NewFallbackChain wraps the primary with an ordered list of reserves. A reserve with an empty model or a nil
// client is ignored; when nothing is left the primary is returned untouched, not wrapped in a no-op.
func NewFallbackChain(primary Client, reserves []Reserve, opts ChainOptions) Client {
	if primary == nil {
		return nil
	}
	var usable []Reserve
	for _, reserve := range reserves {
		if reserve.Client == nil || strings.TrimSpace(reserve.Model) == "" {
			continue
		}
		usable = append(usable, Reserve{Model: strings.TrimSpace(reserve.Model), Client: reserve.Client})
	}
	if len(usable) == 0 {
		return primary
	}
	var base = &chainClient{
		primary:      primary,
		reserves:     usable,
		primaryModel: opts.PrimaryModel,
		metrics:      opts.Metrics,
		logger:       opts.Logger,
	}
	var primaryStreamer, streams = primary.(Streamer)
	if !streams {
		return base
	}
	var streamers = make([]Streamer, len(usable))
	for index, reserve := range usable {
		streamers[index], _ = reserve.Client.(Streamer)
	}
	return &chainStreamer{chainClient: base, primaryStreamer: primaryStreamer, reserveStreamers: streamers}
}

// reserveModels renders the chain for an error or a warning without ever including keys or prompts.
func (c *chainClient) reserveModels() []string {
	var names = make([]string, len(c.reserves))
	for index, reserve := range c.reserves {
		names[index] = reserve.Model
	}
	return names
}

// Complete walks the chain in order and returns the first successful answer.
func (c *chainClient) Complete(ctx context.Context, req Request) (Response, error) {
	var response, err = c.primary.Complete(ctx, req)
	if err == nil {
		return response, nil
	}
	if !Retryable(err) || ctx.Err() != nil {
		return response, err
	}
	var failures = []error{err}
	for _, reserve := range c.reserves {
		if ctx.Err() != nil {
			break
		}
		c.warnAttempt(reserve.Model, err)
		var reserveRequest = req
		reserveRequest.Model = reserve.Model
		response, err = reserve.Client.Complete(ctx, reserveRequest)
		if err != nil && errors.Is(err, ErrEmptyAnswer) {
			// A small reserve answered 200 with no content once (a known quirk of some models). One more
			// attempt is cheap next to handing the user a failed answer.
			response, err = reserve.Client.Complete(ctx, reserveRequest)
		}
		if err == nil {
			c.metrics.fallbackAttempt("ok")
			return response, nil
		}
		failures = append(failures, err)
		if !Retryable(err) {
			break
		}
	}
	c.metrics.fallbackAttempt("error")
	return response, chainFailed("Complete", c.primaryModel, c.reserveModels(), failures)
}

// Stream walks the chain only while no text has reached the caller.
func (c *chainStreamer) Stream(ctx context.Context, req Request, handler StreamHandler) (Response, error) {
	var delivered bool
	var primaryHandler = StreamHandler{
		Progress: handler.Progress,
		Delta: func(text string) error {
			delivered = true
			if handler.Delta == nil {
				return nil
			}
			return handler.Delta(text)
		},
	}
	var response, err = c.primaryStreamer.Stream(ctx, req, primaryHandler)
	if err == nil {
		return response, nil
	}
	if delivered || !Retryable(err) || ctx.Err() != nil {
		return response, err
	}
	var failures = []error{err}
	for index, reserve := range c.reserves {
		if ctx.Err() != nil {
			break
		}
		var streamer = c.reserveStreamers[index]
		if streamer == nil {
			// This reserve cannot stream; the next one may still be able to.
			continue
		}
		c.warnAttempt(reserve.Model, err)
		var reserveRequest = req
		reserveRequest.Model = reserve.Model
		var reserveDelivered bool
		var reserveHandler = StreamHandler{
			Progress: handler.Progress,
			Delta: func(text string) error {
				if strings.TrimSpace(text) != "" {
					reserveDelivered = true
				}
				if handler.Delta == nil {
					return nil
				}
				return handler.Delta(text)
			},
		}
		response, err = streamer.Stream(ctx, reserveRequest, reserveHandler)
		if err != nil && !reserveDelivered && errors.Is(err, ErrEmptyAnswer) {
			reserveDelivered = false
			response, err = streamer.Stream(ctx, reserveRequest, reserveHandler)
		}
		if err == nil {
			c.metrics.fallbackAttempt("ok")
			return response, nil
		}
		failures = append(failures, err)
		if reserveDelivered {
			// Part of this reserve's answer is on the user's screen; another provider would duplicate it.
			c.metrics.fallbackAttempt("error")
			return response, chainFailed("Stream", c.primaryModel, c.reserveModels(), failures)
		}
		if !Retryable(err) {
			break
		}
	}
	c.metrics.fallbackAttempt("error")
	return response, chainFailed("Stream", c.primaryModel, c.reserveModels(), failures)
}

// Models keeps the primary catalogue and asks reserves only when the primary listing failed.
func (c *chainClient) Models(ctx context.Context) ([]string, error) {
	var models, err = c.primary.Models(ctx)
	if err == nil {
		return models, nil
	}
	var failures = []error{err}
	for _, reserve := range c.reserves {
		c.warnAttempt(reserve.Model, err)
		var reserveModels, reserveErr = reserve.Client.Models(ctx)
		if reserveErr == nil {
			c.metrics.fallbackAttempt("ok")
			return reserveModels, nil
		}
		failures = append(failures, reserveErr)
	}
	c.metrics.fallbackAttempt("error")
	return nil, chainFailed("Models", c.primaryModel, c.reserveModels(), failures)
}

func (c *chainClient) warnAttempt(reserveModel string, err error) {
	if c.logger == nil {
		return
	}
	c.logger.Warn("model fallback attempt",
		"primary_model", c.primaryModel,
		"reserve_model", reserveModel,
		"reason", fallbackReason(err))
}

// chainFailed names the models and joins the causes. APIError text never carries a body, so the message stays
// free of prompts and keys.
func chainFailed(operation, primaryModel string, reserves []string, failures []error) error {
	return fmt.Errorf("model fallback chain %s: primary model %q and reserves %s failed: %w",
		operation, primaryModel, strings.Join(reserves, ", "), errors.Join(failures...))
}
