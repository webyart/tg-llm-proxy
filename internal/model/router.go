// router.go — one chat can name a configured provider with a "provider:model" id.
//
// The default client stays the primary STRATA endpoint wrapped by the fallback chain. A request without a
// prefix, or with the reserved "strata:" prefix, takes exactly the old path with the model id unchanged.
// A configured provider gets its own client and one of ITS OWN models — an id that the provider does not list
// is refused, never forwarded blindly.
//
// Explicit provider selection is a preference: a retryable failure on the selected provider is tried once on
// the default chain, then reported. A stream that already delivered a delta is never replayed, so a partially
// shown answer cannot duplicate itself from another provider.
package model

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// DefaultProviderName is the reserved prefix of the primary endpoint. It is not a MODEL_PROVIDERS entry, so a
// configured provider can never shadow the default path.
const DefaultProviderName = "strata"

// Provider is one configured provider as the router sees it: the name a chat types before the colon, the closed
// list of models it may serve, its default model and the client that talks to it.
type Provider struct {
	Name         string
	Models       []string
	DefaultModel string
	Client       Client
}

// RouterOptions carries the default client (normally the fallback chain), the model of the primary endpoint
// used when a selected provider fails, the configured providers and the optional logger of warnings.
type RouterOptions struct {
	Default      Client
	DefaultModel string
	Providers    []Provider
	Logger       *slog.Logger
}

type providerRoute struct {
	name         string
	models       map[string]bool
	order        []string
	defaultModel string
	client       Client
	streamer     Streamer
}

type routerClient struct {
	def      Client
	defModel string
	routes   map[string]*providerRoute
	names    []string
	logger   *slog.Logger
}

// routerStreamer adds Stream to the router. It is a distinct type because a struct with a Stream method always
// satisfies Streamer; a default client that cannot stream must not turn the router into one.
type routerStreamer struct {
	*routerClient
	defaultStreamer Streamer
}

// NewRouter builds the routing client. An empty (or fully unusable) provider list returns the default client
// untouched, not wrapped in a no-op, so a deployment without MODEL_PROVIDERS keeps byte-identical behaviour.
func NewRouter(opts RouterOptions) Client {
	if opts.Default == nil {
		return nil
	}
	var routes = map[string]*providerRoute{}
	var names []string
	for _, provider := range opts.Providers {
		var name = strings.TrimSpace(provider.Name)
		if name == "" || provider.Client == nil || name == DefaultProviderName {
			continue
		}
		if _, exists := routes[name]; exists {
			continue
		}
		var order = normalizeProviderModels(provider.Models)
		if len(order) == 0 {
			continue
		}
		var allowed = make(map[string]bool, len(order))
		for _, model := range order {
			allowed[model] = true
		}
		var fallbackModel = strings.TrimSpace(provider.DefaultModel)
		if fallbackModel == "" || !allowed[fallbackModel] {
			fallbackModel = order[0]
		}
		var route = &providerRoute{name: name, models: allowed, order: order, defaultModel: fallbackModel, client: provider.Client}
		route.streamer, _ = provider.Client.(Streamer)
		routes[name] = route
		names = append(names, name)
	}
	if len(routes) == 0 {
		return opts.Default
	}
	sort.Strings(names)
	var base = &routerClient{
		def:      opts.Default,
		defModel: strings.TrimSpace(opts.DefaultModel),
		routes:   routes,
		names:    names,
		logger:   opts.Logger,
	}
	if streamer, streams := opts.Default.(Streamer); streams {
		return &routerStreamer{routerClient: base, defaultStreamer: streamer}
	}
	return base
}

func normalizeProviderModels(models []string) []string {
	var seen = map[string]bool{}
	var out = make([]string, 0, len(models))
	for _, model := range models {
		var trimmed = strings.TrimSpace(model)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

// providerNames renders the configured names for an error without including keys or prompts.
func (r *routerClient) providerNames() []string {
	if len(r.names) == 0 {
		return []string{"none"}
	}
	return r.names
}

// resolve splits a request model into the client that serves it and the model id to send. provider is empty when
// the request takes the default path.
func (r *routerClient) resolve(raw string) (Client, string, string, error) {
	var provider, rest, prefixed = strings.Cut(strings.TrimSpace(raw), ":")
	if !prefixed {
		return r.def, strings.TrimSpace(raw), "", nil
	}
	if provider == DefaultProviderName {
		return r.def, strings.TrimSpace(rest), "", nil
	}
	var route, known = r.routes[provider]
	if !known {
		return nil, "", provider, fmt.Errorf("unknown model provider %q in model id %q: configured providers are %s; use a plain model id or %s:<model> for the default endpoint",
			provider, raw, strings.Join(r.providerNames(), ", "), DefaultProviderName)
	}
	var model = strings.TrimSpace(rest)
	if model == "" {
		model = route.defaultModel
	}
	if !route.models[model] {
		return nil, "", provider, fmt.Errorf("model %q is not listed by provider %q: available models are %s",
			model, provider, strings.Join(route.order, ", "))
	}
	return route.client, model, provider, nil
}

// Complete routes one request. A retryable failure on an explicitly selected provider is retried once on the
// default chain: the selection is a preference, not a hard requirement.
func (r *routerClient) Complete(ctx context.Context, req Request) (Response, error) {
	var client, modelName, provider, err = r.resolve(req.Model)
	if err != nil {
		return Response{}, err
	}
	var mapped = req
	mapped.Model = modelName
	var response, callErr = client.Complete(ctx, mapped)
	if callErr == nil || provider == "" {
		return response, callErr
	}
	if !providerFallbackAllowed(callErr) || ctx.Err() != nil {
		return response, callErr
	}
	r.warnFallback(provider, modelName, callErr)
	var fallback = req
	fallback.Model = r.defModel
	var fallbackResponse, fallbackErr = r.def.Complete(ctx, fallback)
	if fallbackErr != nil {
		return fallbackResponse, fmt.Errorf("provider %q model %q failed (%v) and the default model also failed: %w",
			provider, modelName, callErr, fallbackErr)
	}
	return fallbackResponse, nil
}

// Models keeps the default catalogue and appends every configured "name:model" id, grouped by provider name in
// sorted order and in the configured order inside a provider. Duplicates are dropped once. A failed default
// listing does not hide the configured providers: their lists are configuration, not provider state.
func (r *routerClient) Models(ctx context.Context) ([]string, error) {
	var defaultModels, defaultErr = r.def.Models(ctx)
	if defaultErr != nil && r.logger != nil {
		r.logger.Warn("the default model catalogue is unavailable; listing the configured providers only",
			"reason", fallbackReason(defaultErr))
	}
	var out []string
	var seen = map[string]bool{}
	for _, model := range defaultModels {
		var trimmed = strings.TrimSpace(model)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	for _, name := range r.names {
		var route = r.routes[name]
		for _, model := range route.order {
			var id = name + ":" + model
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func (r *routerStreamer) Stream(ctx context.Context, req Request, handler StreamHandler) (Response, error) {
	var _, modelName, provider, err = r.resolve(req.Model)
	if err != nil {
		return Response{}, err
	}
	var mapped = req
	mapped.Model = modelName
	if provider == "" {
		return r.defaultStreamer.Stream(ctx, mapped, handler)
	}
	var route = r.routes[provider]
	if route.streamer == nil {
		return Response{}, fmt.Errorf("provider %q cannot stream: model %q is reachable only for non-streamed requests", provider, modelName)
	}
	var delivered bool
	var response, callErr = route.streamer.Stream(ctx, mapped, StreamHandler{
		Progress: handler.Progress,
		Delta: func(text string) error {
			delivered = true
			if handler.Delta == nil {
				return nil
			}
			return handler.Delta(text)
		},
	})
	if callErr == nil {
		return response, nil
	}
	if delivered || !providerFallbackAllowed(callErr) || ctx.Err() != nil {
		return response, callErr
	}
	r.warnFallback(provider, modelName, callErr)
	var fallback = req
	fallback.Model = r.defModel
	var fallbackResponse, fallbackErr = r.defaultStreamer.Stream(ctx, fallback, StreamHandler{
		Progress: handler.Progress,
		Delta: func(text string) error {
			if handler.Delta == nil {
				return nil
			}
			return handler.Delta(text)
		},
	})
	if fallbackErr != nil {
		return fallbackResponse, fmt.Errorf("provider %q model %q failed (%v) and the default model also failed: %w",
			provider, modelName, callErr, fallbackErr)
	}
	return fallbackResponse, nil
}

// providerFallbackAllowed is the narrow trigger of an explicit-selection fallback: a retryable provider error,
// or a 200 with no answer (a provider quirk the default chain already treats as worth another model). A spent
// context is never retried: the caller has no time left to read a second answer.
func providerFallbackAllowed(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return Retryable(err) || errors.Is(err, ErrEmptyAnswer)
}

func (r *routerClient) warnFallback(provider, modelName string, err error) {
	if r.logger == nil {
		return
	}
	r.logger.Warn("the selected provider failed; retrying on the default model",
		"provider", provider,
		"provider_model", modelName,
		"default_model", r.defModel,
		"reason", fallbackReason(err))
}
