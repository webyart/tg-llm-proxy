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
	"sync"
)

// DefaultProviderName is the reserved prefix of the primary endpoint. It is not a MODEL_PROVIDERS entry, so a
// configured provider can never shadow the default path.
const DefaultProviderName = "strata"

// MaxProviderModels is the runtime bound of one provider's effective list. It matches the env loader and the
// admin API: the Mini App textarea and the /model keyboard are not meant to carry a longer catalogue.
const MaxProviderModels = 20

// Provider sources reported by ProviderCatalog. "env" means MODEL_PROVIDERS is in force for this provider;
// "db" means a provider_settings row overrides models/default_model/enabled at runtime.
const (
	ProviderSourceEnv = "env"
	ProviderSourceDB  = "db"
)

// Errors of the runtime override API. The admin API maps ErrUnknownProvider to 404 and
// ErrInvalidProviderModels to 400; a routing attempt on a disabled provider uses a plain error.
var (
	ErrUnknownProvider       = errors.New("unknown model provider")
	ErrInvalidProviderModels = errors.New("invalid provider models")
)

// ProviderCatalogEntry is one provider as the router currently sees it. It never contains a key, a base URL
// or a prompt: the admin API adds only the host of the env base_url on the webapp side.
type ProviderCatalogEntry struct {
	Provider     string
	Models       []string
	DefaultModel string
	Enabled      bool
	Source       string
}

// ProviderOverrides is the runtime editor of the provider catalogue. A router built by NewRouter implements
// it (both the plain and the streaming wrapper); the default client returned when no provider is usable does
// not, which is exactly the "no providers" path that must stay unchanged. The methods are safe for concurrent
// use with Complete, Stream, Models and each other.
type ProviderOverrides interface {
	SetProviderModels(provider string, models []string, defaultModel string, enabled bool) error
	ResetProviderModels(provider string) error
	ProviderCatalog() []ProviderCatalogEntry
}

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

// providerRoute keeps the env base and the effective values side by side. The env fields are immutable after
// NewRouter; the effective fields are replaced (never mutated in place) under routerClient.mu, so a reader may
// keep a snapshot of the slices and maps after releasing the read lock.
type providerRoute struct {
	name     string
	client   Client
	streamer Streamer

	envModels       []string
	envDefaultModel string

	models       []string
	allowed      map[string]bool
	defaultModel string
	enabled      bool
	source       string
}

// providerSnapshot is an immutable view of one route, copied by value under the read lock.
type providerSnapshot struct {
	name         string
	client       Client
	streamer     Streamer
	models       []string
	allowed      map[string]bool
	defaultModel string
	enabled      bool
}

func (r *providerRoute) snapshot() providerSnapshot {
	return providerSnapshot{
		name: r.name, client: r.client, streamer: r.streamer,
		models: r.models, allowed: r.allowed, defaultModel: r.defaultModel, enabled: r.enabled,
	}
}

// apply replaces the effective fields. The slices are copied and the map is rebuilt, so snapshots taken by a
// concurrent reader stay valid after the lock is released.
func (r *providerRoute) apply(models []string, defaultModel string, enabled bool, source string) {
	r.models = append([]string{}, models...)
	r.allowed = make(map[string]bool, len(models))
	for _, model := range models {
		r.allowed[model] = true
	}
	r.defaultModel = defaultModel
	r.enabled = enabled
	r.source = source
}

type routerClient struct {
	mu       sync.RWMutex
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

// routerTranscriber keeps the optional Transcriber of the default client behind the router. Without it, adding
// any MODEL_PROVIDERS silently removed voice transcription from the dispatcher: the concrete router type no
// longer satisfied model.Transcriber.
type routerTranscriber struct {
	*routerClient
	defaultTranscriber Transcriber
}

func (r *routerTranscriber) Transcribe(ctx context.Context, req TranscriptionRequest) (string, error) {
	return r.defaultTranscriber.Transcribe(ctx, req)
}

type routerStreamTranscriber struct {
	*routerStreamer
	defaultTranscriber Transcriber
}

func (r *routerStreamTranscriber) Transcribe(ctx context.Context, req TranscriptionRequest) (string, error) {
	return r.defaultTranscriber.Transcribe(ctx, req)
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
		var route = &providerRoute{name: name, client: provider.Client}
		route.streamer, _ = provider.Client.(Streamer)
		route.envModels = append([]string{}, order...)
		route.envDefaultModel = fallbackModel
		route.apply(order, fallbackModel, true, ProviderSourceEnv)
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
	streamer, streams := opts.Default.(Streamer)
	transcriber, transcribes := opts.Default.(Transcriber)
	switch {
	case streams && transcribes:
		return &routerStreamTranscriber{
			routerStreamer:     &routerStreamer{routerClient: base, defaultStreamer: streamer},
			defaultTranscriber: transcriber,
		}
	case streams:
		return &routerStreamer{routerClient: base, defaultStreamer: streamer}
	case transcribes:
		return &routerTranscriber{routerClient: base, defaultTranscriber: transcriber}
	default:
		return base
	}
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

// SetProviderModels replaces the effective list, default and enabled flag of one configured provider. The env
// base stays untouched, so ResetProviderModels can restore it without re-reading MODEL_PROVIDERS. The call is
// strict: an empty/oversized list, an empty or duplicate model id, a default outside the list, the reserved
// name and an unknown provider are errors instead of a silently reduced list.
func (r *routerClient) SetProviderModels(provider string, models []string, defaultModel string, enabled bool) error {
	provider = strings.TrimSpace(provider)
	if provider == DefaultProviderName {
		return fmt.Errorf("%w: %q is the reserved default endpoint", ErrInvalidProviderModels, provider)
	}
	normalized, err := runtimeProviderModels(models)
	if err != nil {
		return err
	}
	defaultModel = strings.TrimSpace(defaultModel)
	if defaultModel == "" {
		defaultModel = normalized[0]
	}
	var listed = false
	for _, model := range normalized {
		if model == defaultModel {
			listed = true
			break
		}
	}
	if !listed {
		return fmt.Errorf("%w: default model %q is not in the list of provider %q", ErrInvalidProviderModels, defaultModel, provider)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	route, known := r.routes[provider]
	if !known {
		return fmt.Errorf("%w: %q (configured: %s)", ErrUnknownProvider, provider, strings.Join(r.providerNames(), ", "))
	}
	route.apply(normalized, defaultModel, enabled, ProviderSourceDB)
	return nil
}

// ResetProviderModels restores the env list, default and enabled state of one provider. It is idempotent: a
// provider that was never overridden keeps its env values. An unknown name and the reserved "strata" are
// errors, so a typo cannot silently reset another provider.
func (r *routerClient) ResetProviderModels(provider string) error {
	provider = strings.TrimSpace(provider)
	if provider == DefaultProviderName {
		return fmt.Errorf("%w: %q is the reserved default endpoint", ErrInvalidProviderModels, provider)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	route, known := r.routes[provider]
	if !known {
		return fmt.Errorf("%w: %q (configured: %s)", ErrUnknownProvider, provider, strings.Join(r.providerNames(), ", "))
	}
	route.apply(route.envModels, route.envDefaultModel, true, ProviderSourceEnv)
	return nil
}

// ProviderCatalog returns a snapshot of every configured provider in sorted name order. It is a copy: a later
// override cannot change a slice the caller already holds. A disabled provider stays in the catalogue so the
// admin UI can enable it again.
func (r *routerClient) ProviderCatalog() []ProviderCatalogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out = make([]ProviderCatalogEntry, 0, len(r.names))
	for _, name := range r.names {
		var route = r.routes[name]
		out = append(out, ProviderCatalogEntry{
			Provider:     route.name,
			Models:       append([]string{}, route.models...),
			DefaultModel: route.defaultModel,
			Enabled:      route.enabled,
			Source:       route.source,
		})
	}
	return out
}

// runtimeProviderModels is the strict normalizer of the runtime editor: unlike the env loader it refuses a
// malformed list instead of dropping entries, because the admin typed it and expects the saved list to be
// exactly what runs.
func runtimeProviderModels(models []string) ([]string, error) {
	if len(models) == 0 {
		return nil, fmt.Errorf("%w: the model list is empty", ErrInvalidProviderModels)
	}
	if len(models) > MaxProviderModels {
		return nil, fmt.Errorf("%w: %d models listed, at most %d are allowed", ErrInvalidProviderModels, len(models), MaxProviderModels)
	}
	var seen = make(map[string]bool, len(models))
	var out = make([]string, 0, len(models))
	for _, model := range models {
		var trimmed = strings.TrimSpace(model)
		if trimmed == "" {
			return nil, fmt.Errorf("%w: an empty model id is not allowed", ErrInvalidProviderModels)
		}
		if seen[trimmed] {
			return nil, fmt.Errorf("%w: model %q is listed twice", ErrInvalidProviderModels, trimmed)
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out, nil
}

// resolve splits a request model into the client that serves it and the model id to send. provider is empty when
// the request takes the default path. The returned snapshot is immutable: an override that commits after the
// read lock is released cannot change what this request sends.
func (r *routerClient) resolve(raw string) (providerSnapshot, string, string, error) {
	var provider, rest, prefixed = strings.Cut(strings.TrimSpace(raw), ":")
	if !prefixed {
		return providerSnapshot{client: r.def}, strings.TrimSpace(raw), "", nil
	}
	if provider == DefaultProviderName {
		return providerSnapshot{client: r.def}, strings.TrimSpace(rest), "", nil
	}
	r.mu.RLock()
	var route, known = r.routes[provider]
	var snapshot providerSnapshot
	if known {
		snapshot = route.snapshot()
	}
	r.mu.RUnlock()
	if !known {
		return providerSnapshot{}, "", provider, fmt.Errorf("unknown model provider %q in model id %q: configured providers are %s; use a plain model id or %s:<model> for the default endpoint",
			provider, raw, strings.Join(r.providerNames(), ", "), DefaultProviderName)
	}
	var model = strings.TrimSpace(rest)
	if model == "" {
		model = snapshot.defaultModel
	}
	if !snapshot.enabled {
		return providerSnapshot{}, "", provider, fmt.Errorf("model provider %q is disabled: enable it in Settings, or use a plain model id or %s:<model> for the default endpoint",
			provider, DefaultProviderName)
	}
	if !snapshot.allowed[model] {
		return providerSnapshot{}, "", provider, fmt.Errorf("model %q is not listed by provider %q: available models are %s",
			model, provider, strings.Join(snapshot.models, ", "))
	}
	return snapshot, model, provider, nil
}

// Complete routes one request. A retryable failure on an explicitly selected provider is retried once on the
// default chain: the selection is a preference, not a hard requirement.
func (r *routerClient) Complete(ctx context.Context, req Request) (Response, error) {
	var route, modelName, provider, err = r.resolve(req.Model)
	if err != nil {
		return Response{}, err
	}
	var mapped = req
	mapped.Model = modelName
	var response, callErr = route.client.Complete(ctx, mapped)
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

// Models keeps the default catalogue and appends every enabled configured "name:model" id, grouped by provider
// name in sorted order and in the effective order inside a provider. Duplicates are dropped once. A failed
// default listing does not hide the configured providers: their lists are configuration, not provider state.
func (r *routerClient) Models(ctx context.Context) ([]string, error) {
	var defaultModels, defaultErr = r.def.Models(ctx)
	if defaultErr != nil && r.logger != nil {
		r.logger.Warn("the default model catalogue is unavailable; listing the configured providers only",
			"reason", fallbackReason(defaultErr))
	}
	var catalog = r.ProviderCatalog()
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
	for _, route := range catalog {
		if !route.Enabled {
			continue
		}
		for _, model := range route.Models {
			var id = route.Provider + ":" + model
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
	var route, modelName, provider, err = r.resolve(req.Model)
	if err != nil {
		return Response{}, err
	}
	var mapped = req
	mapped.Model = modelName
	if provider == "" {
		return r.defaultStreamer.Stream(ctx, mapped, handler)
	}
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
