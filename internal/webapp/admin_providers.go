package webapp

// Admin provider/model editor of the Mini App.
//
// MODEL_PROVIDERS stays environment-only for name, base_url, api_key and timeout_seconds. This API edits
// the runtime values an administrator may change — the closed model list, its default, the enabled flag and
// since schema 28 the provider's daily USD budget — stores them in provider_settings (migrations 022/028)
// and applies the model values to the running router without a restart. DELETE removes the row and restores
// the env values, including "no budget".
//
// Every endpoint is admin-only by TELEGRAM_ADMIN_USERS and answers 403 admin_required before reading
// anything. The payload is strict JSON: unknown fields are rejected by the shared decoder, the three model
// fields are required together, models is 1..20 non-empty unique ids, default_model must be in models, and
// "strata" is the reserved default endpoint and is never editable. daily_budget_usd is optional: its
// omission preserves the stored budget, and a body with only daily_budget_usd changes nothing but the
// budget. GET additionally reports the budget, today's spend and the 80% warning of every provider.
// Successful mutations are audited by the generic Mini App mutation log as admin.provider.update /
// admin.provider.reset, and an explicit budget change adds an admin.provider.budget row.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"bot/internal/model"
	"bot/internal/store"
)

const adminProvidersPath = "/api/admin/providers"

// adminProviderNamePattern is the same key rule MODEL_PROVIDERS uses. A name outside it is a bad request,
// not an unknown provider: no valid configuration could ever have created it.
var adminProviderNamePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// adminProviderEntry is one provider on the wire. BaseURL is the host of the env endpoint: no scheme path,
// query, userinfo or key is ever sent, so the browser cannot learn a credential or an internal path.
// DailyBudgetUSD is the stored budget (0 = unlimited); TodayCostUSD is today's spend attributed to this
// provider with MODEL_PRICES; Warning is the 80% flag of that budget.
type adminProviderEntry struct {
	Name           string   `json:"name"`
	BaseURL        string   `json:"base_url"`
	Models         []string `json:"models"`
	Default        string   `json:"default_model"`
	Enabled        bool     `json:"enabled"`
	Source         string   `json:"source"`
	Configured     []string `json:"configured_models,omitempty"`
	DailyBudgetUSD float64  `json:"daily_budget_usd"`
	TodayCostUSD   float64  `json:"today_cost_usd"`
	Warning        bool     `json:"warning"`
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
// standalone Mini App still shows the persisted state; the running router path already contains it. The
// budget and today's spend of every provider are filled from one grouped query, not one per card.
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
	if err := a.providerSpend(ctx, time.Now().UTC(), items); err != nil {
		return nil, err
	}
	return items, nil
}

// providerSpend fills daily_budget_usd, today_cost_usd and warning of the wire entries. It reads the stored
// budgets and prices today's assistant answers once for the whole list: the same grouped query and the same
// attribution rule the bot enforces, so the admin card cannot disagree with the refusal. A deployment
// without MODEL_PRICES has zero spend and skips the query entirely.
func (a *App) providerSpend(ctx context.Context, now time.Time, items []adminProviderEntry) error {
	budgets, err := store.ProviderDailyBudgets(ctx, a.db)
	if err != nil {
		return err
	}
	var costs map[string]int64
	if a.cfg.ModelPrices.HasPrices() {
		modelCosts, costErr := store.ModelDailyCostMicros(ctx, a.db, now, a.cfg.ModelPrices)
		if costErr != nil {
			return costErr
		}
		var catalog = make([]model.ProviderCatalogEntry, 0, len(items))
		for _, item := range items {
			catalog = append(catalog, model.ProviderCatalogEntry{
				Provider: item.Name, Models: item.Models, DefaultModel: item.Default,
				Enabled: item.Enabled, Source: item.Source,
			})
		}
		costs = model.NewProviderAttribution(catalog).ProviderCosts(modelCosts)
	}
	for index := range items {
		var budget = budgets[items[index].Name]
		var cost = costs[items[index].Name]
		items[index].DailyBudgetUSD = store.ProviderBudgetUSD(budget)
		items[index].TodayCostUSD = float64(cost) / 1_000_000
		if budget > 0 {
			var warningAt = (budget*store.ProviderDailyCostWarningPercent + 99) / 100
			items[index].Warning = cost >= warningAt
		}
	}
	return nil
}

// providerEntryWithSpend returns one wire entry with the budget fields filled. It is the response shape of
// PUT and DELETE, so the browser does not need a second GET to paint the card.
func (a *App) providerEntryWithSpend(ctx context.Context, entry model.ProviderCatalogEntry) (adminProviderEntry, error) {
	var items = []adminProviderEntry{a.providerEntryFor(entry)}
	if err := a.providerSpend(ctx, time.Now().UTC(), items); err != nil {
		return adminProviderEntry{}, err
	}
	return items[0], nil
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
		a.updateAdminProvider(w, req, name, current)
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

// providerBudgetMessage is the one 400 text of an invalid daily_budget_usd: it names the range and the six
// decimals the API stores, so a client can fix the value without guessing the unit.
const providerBudgetMessage = "daily_budget_usd must be between 0 and 100000 USD with at most six decimals."

func (a *App) updateAdminProvider(w http.ResponseWriter, req *http.Request, name string, current scope) {
	var input struct {
		Models         *[]string `json:"models"`
		DefaultModel   *string   `json:"default_model"`
		Enabled        *bool     `json:"enabled"`
		DailyBudgetUSD *float64  `json:"daily_budget_usd"`
	}
	if status, ok := decodeSettingsJSON(req, &input); !ok {
		settingsDecodeError(w, status)
		return
	}
	// The three model fields are still one unit: a request that changes any of them must carry all three.
	// daily_budget_usd is optional and its omission PRESERVES the stored budget, so editing a model list can
	// neither lift nor introduce a budget by accident. A body with only daily_budget_usd sets the budget and
	// leaves the model override untouched.
	if input.Models == nil && input.DefaultModel == nil && input.Enabled == nil {
		if input.DailyBudgetUSD == nil {
			writeError(w, http.StatusBadRequest, "provider_validation",
				"Provide models, default_model and enabled together, or daily_budget_usd alone.")
			return
		}
		a.updateAdminProviderBudget(w, req, name, current, *input.DailyBudgetUSD)
		return
	}
	if input.Models == nil || input.DefaultModel == nil || input.Enabled == nil {
		writeError(w, http.StatusBadRequest, "provider_validation", "All three fields are required: models, default_model, enabled.")
		return
	}
	var budgetMicros int64
	if input.DailyBudgetUSD != nil {
		parsed, budgetErr := store.ProviderBudgetMicros(*input.DailyBudgetUSD)
		if budgetErr != nil {
			writeError(w, http.StatusBadRequest, "provider_validation", providerBudgetMessage)
			return
		}
		budgetMicros = parsed
	} else if existing, found, readErr := store.GetProviderSettings(req.Context(), a.db, name); readErr != nil {
		a.queryFailed(w, "admin provider budget", readErr)
		return
	} else if found {
		budgetMicros = existing.DailyBudgetMicros
	}
	var known []string
	for _, entry := range a.providerCatalog() {
		known = append(known, entry.Provider)
	}
	settings, err := store.ValidateProviderSettings(store.ProviderSettings{
		Provider: name, Models: *input.Models, DefaultModel: *input.DefaultModel, Enabled: *input.Enabled,
		DailyBudgetMicros: budgetMicros,
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
	if input.DailyBudgetUSD != nil {
		a.auditProviderBudget(req.Context(), current, name, settings.DailyBudgetMicros)
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
	item, err := a.providerEntryWithSpend(req.Context(), model.ProviderCatalogEntry{
		Provider: name, Models: settings.Models, DefaultModel: settings.DefaultModel,
		Enabled: settings.Enabled, Source: model.ProviderSourceDB,
	})
	if err != nil {
		a.queryFailed(w, "admin provider update", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// updateAdminProviderBudget stores the daily budget of one provider and nothing else. Models, default_model
// and enabled stay exactly as they are (the stored row or the env base), so a budget can be changed without
// resubmitting a model list.
func (a *App) updateAdminProviderBudget(w http.ResponseWriter, req *http.Request, name string, current scope, usd float64) {
	micros, err := store.ProviderBudgetMicros(usd)
	if err != nil {
		writeError(w, http.StatusBadRequest, "provider_validation", providerBudgetMessage)
		return
	}
	var entry model.ProviderCatalogEntry
	for _, candidate := range a.providerCatalog() {
		if candidate.Provider == name {
			entry = candidate
			break
		}
	}
	if err := store.SetProviderBudget(req.Context(), a.db, name, micros, store.ProviderSettings{
		Provider: name, Models: entry.Models, DefaultModel: entry.DefaultModel, Enabled: entry.Enabled,
		DailyBudgetMicros: micros,
	}); err != nil {
		if errors.Is(err, store.ErrInvalidProviderSettings) {
			writeError(w, http.StatusBadRequest, "provider_validation", providerBudgetMessage)
			return
		}
		a.queryFailed(w, "admin provider budget", err)
		return
	}
	a.auditProviderBudget(req.Context(), current, name, micros)
	// The row now exists, so the router reports "db" as its source exactly as a restart would apply it:
	// the models themselves are unchanged (they came from the effective catalogue entry).
	if overrides := a.providerOverrides(); overrides != nil {
		if err := overrides.SetProviderModels(name, entry.Models, entry.DefaultModel, entry.Enabled); err != nil {
			a.logger.Warn("the provider row was stored but its model list was not re-applied",
				"provider", name, "error", err)
		}
	}
	var updated = entry
	updated.Source = model.ProviderSourceDB
	item, err := a.providerEntryWithSpend(req.Context(), updated)
	if err != nil {
		a.queryFailed(w, "admin provider budget", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// auditProviderBudget records an explicit budget change. The generic Mini App mutation log names the route
// admin.provider.update; a budget has its own operation, so every amount ever set can be found without
// parsing model lists out of a detail string.
func (a *App) auditProviderBudget(ctx context.Context, current scope, name string, micros int64) {
	var detail = fmt.Sprintf("operation=admin.provider.budget chat_id=%d provider=%s budget_micros=%d",
		current.ChatID, name, micros)
	if err := store.LogEvent(ctx, a.db, current.UserID, "miniapp_mutation", detail); err != nil {
		a.logger.Error("Mini App provider budget audit failed", "provider", name, "error", err)
	}
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
	item, err := a.providerEntryWithSpend(req.Context(), entry)
	if err != nil {
		a.queryFailed(w, "admin provider reset", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
