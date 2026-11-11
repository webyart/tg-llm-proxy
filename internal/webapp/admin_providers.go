package webapp

// Admin provider/model editor of the Mini App.
//
// MODEL_PROVIDERS stays environment-only for name, base_url, api_key and timeout_seconds. This API edits
// exactly the three runtime values an administrator may change — the closed model list, its default and the
// enabled flag — stores them in provider_settings (migration 022) and applies them to the running router
// without a restart. DELETE removes the row and restores the env values.
//
// Every endpoint is admin-only by TELEGRAM_ADMIN_USERS and answers 403 admin_required before reading
// anything. The payload is strict JSON: unknown fields are rejected by the shared decoder, all three fields
// are required, models is 1..20 non-empty unique ids, default_model must be in models, and "strata" is the
// reserved default endpoint and is never editable. Successful mutations are audited by the generic Mini App
// mutation log as admin.provider.update / admin.provider.reset.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"bot/internal/model"
	"bot/internal/store"
)

const adminProvidersPath = "/api/admin/providers"

// adminProviderNamePattern is the same key rule MODEL_PROVIDERS uses. A name outside it is a bad request,
// not an unknown provider: no valid configuration could ever have created it.
var adminProviderNamePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// adminProviderEntry is one provider on the wire. BaseURL is the host of the env endpoint: no scheme path,
// query, userinfo or key is ever sent, so the browser cannot learn a credential or an internal path.
type adminProviderEntry struct {
	Name       string   `json:"name"`
	BaseURL    string   `json:"base_url"`
	Models     []string `json:"models"`
	Default    string   `json:"default_model"`
	Enabled    bool     `json:"enabled"`
	Source     string   `json:"source"`
	Configured []string `json:"configured_models,omitempty"`
}

// adminProviderName extracts {name} from /api/admin/providers/{name}. Exactly one non-empty path segment is
// accepted; a trailing slash or a nested path is not this route.
func adminProviderName(path string) (string, bool) {
	const prefix = adminProvidersPath + "/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	raw := strings.TrimPrefix(path, prefix)
	if raw == "" || strings.Contains(raw, "/") {
		return "", false
	}
	return raw, true
}

// providerBaseHost is the only part of the env base_url the admin API exposes: the host (with its port).
func providerBaseHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return parsed.Host
}

// providerOverrides returns the runtime editor of the model client, or nil when this deployment has no
// usable named providers (the default client is returned untouched in that case).
func (a *App) providerOverrides() model.ProviderOverrides {
	if overrides, ok := a.model.(model.ProviderOverrides); ok {
		return overrides
	}
	return nil
}

// envProviderConfig returns the MODEL_PROVIDERS row of one name. The row is the source of base_url and the
// baseline the UI shows as "configured_models".
func (a *App) envProviderConfig(name string) (configProvider, bool) {
	for _, provider := range a.cfg.ModelProviders {
		if provider.Name == name {
			return configProvider{
				name: provider.Name, baseURL: provider.BaseURL,
				models: append([]string{}, provider.Models...),
			}, true
		}
	}
	return configProvider{}, false
}

// configProvider is the subset of config.ProviderConfig this file needs; the name keeps the import of the
// config package out of the response code.
type configProvider struct {
	name    string
	baseURL string
	models  []string
}

// envCatalog is the fallback catalogue used when the model client is not a router (no usable named provider,
// or a standalone Mini App built by a test). It lets GET answer with the configured providers and lets PUT
// persist a row instead of failing; in a real deployment with a usable provider the router catalogue is
// always the source.
func (a *App) envCatalog() []model.ProviderCatalogEntry {
	var entries = make([]model.ProviderCatalogEntry, 0, len(a.cfg.ModelProviders))
	for _, provider := range a.cfg.ModelProviders {
		var models = append([]string{}, provider.Models...)
		var defaultModel = strings.TrimSpace(provider.DefaultModel)
		if defaultModel == "" && len(models) > 0 {
			defaultModel = strings.TrimSpace(models[0])
		}
		entries = append(entries, model.ProviderCatalogEntry{
			Provider: provider.Name, Models: models, DefaultModel: defaultModel,
			Enabled: true, Source: model.ProviderSourceEnv,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Provider < entries[j].Provider })
	return entries
}

// providerCatalog is the effective catalogue. With a router it is the router's current state (env base plus
// any applied override); otherwise the env list is shown as read-only base.
func (a *App) providerCatalog() []model.ProviderCatalogEntry {
	if overrides := a.providerOverrides(); overrides != nil {
		return overrides.ProviderCatalog()
	}
	return a.envCatalog()
}

// providerEntryFor maps a catalogue entry to the wire shape. configured_models is the env baseline, so the
// admin can see what Reset returns even while a db override is active.
func (a *App) providerEntryFor(entry model.ProviderCatalogEntry) adminProviderEntry {
	var item = adminProviderEntry{
		Name: entry.Provider, Models: append([]string{}, entry.Models...),
		Default: entry.DefaultModel, Enabled: entry.Enabled, Source: entry.Source,
	}
	if configured, found := a.envProviderConfig(entry.Provider); found {
		item.BaseURL = providerBaseHost(configured.baseURL)
		item.Configured = append([]string{}, configured.models...)
	}
	if item.Models == nil {
		item.Models = []string{}
	}
	return item
}

// adminProviderEntries answers GET. With no router it overlays the env catalogue with any stored row, so a
// standalone Mini App still shows the persisted state; the running router path already contains it.
func (a *App) adminProviderEntries(ctx context.Context) ([]adminProviderEntry, error) {
	var catalog = a.providerCatalog()
	var overrides = a.providerOverrides()
	var items = make([]adminProviderEntry, 0, len(catalog))
	for _, entry := range catalog {
		if overrides == nil {
			row, found, err := store.GetProviderSettings(ctx, a.db, entry.Provider)
			if err != nil {
				return nil, err
			}
			if found {
				entry.Models = append([]string{}, row.Models...)
				entry.DefaultModel = row.DefaultModel
				entry.Enabled = row.Enabled
				entry.Source = model.ProviderSourceDB
			}
		}
		items = append(items, a.providerEntryFor(entry))
	}
	return items, nil
}

// adminProviders — GET /api/admin/providers.
func (a *App) adminProviders(w http.ResponseWriter, req *http.Request, current scope) {
	if !a.cfg.IsAdmin(current.UserID) {
		writeError(w, http.StatusForbidden, "admin_required", "Administrator access is required.")
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "Provider settings do not accept query parameters.")
		return
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		methodAllowed(w, http.MethodGet)
		return
	}
	items, err := a.adminProviderEntries(req.Context())
	if err != nil {
		a.queryFailed(w, "admin providers", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// adminProvider — PUT/DELETE /api/admin/providers/{name}.
func (a *App) adminProvider(w http.ResponseWriter, req *http.Request, current scope) {
	if !a.cfg.IsAdmin(current.UserID) {
		writeError(w, http.StatusForbidden, "admin_required", "Administrator access is required.")
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "Provider settings do not accept query parameters.")
		return
	}
	name, ok := adminProviderName(req.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "API endpoint not found.")
		return
	}
	if name == model.DefaultProviderName {
		writeError(w, http.StatusBadRequest, "invalid_provider", "The default endpoint strata is not an editable provider.")
		return
	}
	if !adminProviderNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid_provider", "The provider name must match [a-z0-9_-]+.")
		return
	}
	if !a.providerKnown(name) {
		writeError(w, http.StatusNotFound, "unknown_provider", "The provider is not configured.")
		return
	}
	switch req.Method {
	case http.MethodPut:
		a.updateAdminProvider(w, req, name)
	case http.MethodDelete:
		a.resetAdminProvider(w, req, name)
	default:
		methodAllowed(w, http.MethodPut, http.MethodDelete)
	}
}

// providerKnown reports whether the name is in the effective catalogue (the router, or the env list when the
// deployment has no usable named provider).
func (a *App) providerKnown(name string) bool {
	for _, entry := range a.providerCatalog() {
		if entry.Provider == name {
			return true
		}
	}
	return false
}

func (a *App) updateAdminProvider(w http.ResponseWriter, req *http.Request, name string) {
	var input struct {
		Models       *[]string `json:"models"`
		DefaultModel *string   `json:"default_model"`
		Enabled      *bool     `json:"enabled"`
	}
	if status, ok := decodeSettingsJSON(req, &input); !ok {
		settingsDecodeError(w, status)
		return
	}
	if input.Models == nil || input.DefaultModel == nil || input.Enabled == nil {
		writeError(w, http.StatusBadRequest, "provider_validation", "All three fields are required: models, default_model, enabled.")
		return
	}
	var known []string
	for _, entry := range a.providerCatalog() {
		known = append(known, entry.Provider)
	}
	settings, err := store.ValidateProviderSettings(store.ProviderSettings{
		Provider: name, Models: *input.Models, DefaultModel: *input.DefaultModel, Enabled: *input.Enabled,
	}, known)
	if err != nil {
		if errors.Is(err, store.ErrUnknownProvider) {
			writeError(w, http.StatusNotFound, "unknown_provider", "The provider is not configured.")
			return
		}
		writeError(w, http.StatusBadRequest, "provider_validation", "The provider model list is invalid: 1 to 20 non-empty unique models and a default model from that list.")
		return
	}
	if err := store.UpsertProviderSettings(req.Context(), a.db, settings); err != nil {
		if errors.Is(err, store.ErrInvalidProviderSettings) {
			writeError(w, http.StatusBadRequest, "provider_validation", "The provider model list is invalid.")
			return
		}
		a.queryFailed(w, "admin provider update", err)
		return
	}
	// Persist first, then apply: if the process dies between the two writes, the next start reads the row
	// and applies it; the reverse order would leave a running router that no restart can reproduce.
	if overrides := a.providerOverrides(); overrides != nil {
		if err := overrides.SetProviderModels(name, settings.Models, settings.DefaultModel, settings.Enabled); err != nil {
			if errors.Is(err, model.ErrUnknownProvider) {
				writeError(w, http.StatusNotFound, "unknown_provider", "The provider is not configured.")
				return
			}
			writeError(w, http.StatusBadRequest, "provider_validation", "The provider model list is invalid.")
			return
		}
	}
	a.invalidateModelCache()
	writeJSON(w, http.StatusOK, a.providerEntryFor(model.ProviderCatalogEntry{
		Provider: name, Models: settings.Models, DefaultModel: settings.DefaultModel,
		Enabled: settings.Enabled, Source: model.ProviderSourceDB,
	}))
}

func (a *App) resetAdminProvider(w http.ResponseWriter, req *http.Request, name string) {
	if status, ok := emptySettingsBody(req); !ok {
		settingsDecodeError(w, status)
		return
	}
	if _, err := store.DeleteProviderSettings(req.Context(), a.db, name); err != nil {
		if errors.Is(err, store.ErrInvalidProviderSettings) {
			writeError(w, http.StatusBadRequest, "invalid_provider", "The provider name is invalid.")
			return
		}
		a.queryFailed(w, "admin provider reset", err)
		return
	}
	var entry model.ProviderCatalogEntry
	if overrides := a.providerOverrides(); overrides != nil {
		if err := overrides.ResetProviderModels(name); err != nil {
			if errors.Is(err, model.ErrUnknownProvider) {
				writeError(w, http.StatusNotFound, "unknown_provider", "The provider is not configured.")
				return
			}
			writeError(w, http.StatusBadRequest, "provider_validation", "The provider could not be reset.")
			return
		}
		for _, candidate := range overrides.ProviderCatalog() {
			if candidate.Provider == name {
				entry = candidate
				break
			}
		}
	} else {
		for _, candidate := range a.envCatalog() {
			if candidate.Provider == name {
				entry = candidate
				break
			}
		}
	}
	a.invalidateModelCache()
	writeJSON(w, http.StatusOK, a.providerEntryFor(entry))
}
