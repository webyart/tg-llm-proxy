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

// adminProviderRefreshTimeout bounds one explicit catalogue fetch. It is intentionally shorter than a chat
// request: an administrator is waiting for a card result, and a stuck third-party /v1/models must not hold
// the Mini App request open for the provider's full model timeout.
const adminProviderRefreshTimeout = 8 * time.Second

// adminProviderRefreshLimit caps how many "not allowed by env" ids one response names. The count still
// reports the full number, so a provider with a huge catalogue cannot turn the card into a giant payload.
const adminProviderRefreshLimit = 50

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
	// Priced is the deployment-level MODEL_PRICES flag; without it today_cost_usd is unknown, not zero.
	Priced  bool `json:"priced"`
	Warning bool `json:"warning"`
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
				models: append([]string{}, provider.Models...), defaultModel: provider.DefaultModel,
				apiKey: provider.APIKey, modelsURL: provider.ModelsURL,
				timeoutSeconds: provider.TimeoutSeconds,
			}, true
		}
	}
	return configProvider{}, false
}

// configProvider is the subset of config.ProviderConfig this file needs; the name keeps the import of the
// config package out of the response code. apiKey/modelsURL/timeout stay server-side: they are used only by
// the explicit /refresh fetch and never appear in a JSON field.
type configProvider struct {
	name           string
	baseURL        string
	models         []string
	defaultModel   string
	apiKey         string
	modelsURL      string
	timeoutSeconds int64
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
		items[index].Priced = a.cfg.ModelPrices.HasPrices()
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

// adminProviderRefreshName extracts {name} from /api/admin/providers/{name}/refresh. The suffix is part of
// the route, not a provider name, so it is parsed before adminProvider sees the path (which would otherwise
// treat it as a nested invalid name).
func adminProviderRefreshName(path string) (string, bool) {
	const prefix = adminProvidersPath + "/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	raw := strings.TrimPrefix(path, prefix)
	name, action, found := strings.Cut(raw, "/")
	if !found || name == "" || action != "refresh" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

// adminProviderRefreshResult is the response of POST /api/admin/providers/{name}/refresh. current is the
// resulting effective list (the provider's /v1/models intersected with the env allowlist); added/removed are
// the differences from the list that was effective before the call. ignored names ids the provider serves
// that MODEL_PROVIDERS does not allow: they are reported but can never reach the router.
type adminProviderRefreshResult struct {
	Provider     adminProviderEntry `json:"provider"`
	Added        []string           `json:"added"`
	Removed      []string           `json:"removed"`
	Current      []string           `json:"current"`
	Ignored      []string           `json:"ignored"`
	IgnoredCount int                `json:"ignored_count"`
	Fetched      int                `json:"fetched"`
	Applied      bool               `json:"applied"`
}

// adminProviderRefresh — POST /api/admin/providers/{name}/refresh. It fetches the provider's OpenAI-compatible
// /v1/models and intersects the answer with the models MODEL_PROVIDERS allows for this provider. A model that
// is only in the provider's answer is reported under ignored and is never added: the env row remains the
// allowlist. The intersection is persisted/applied only when it actually differs, and the response is the
// same provider card the PUT path returns plus the diff.
func (a *App) adminProviderRefresh(w http.ResponseWriter, req *http.Request, current scope, name string) {
	if !a.cfg.IsAdmin(current.UserID) {
		writeError(w, http.StatusForbidden, "admin_required", "Administrator access is required.")
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "Provider refresh does not accept query parameters.")
		return
	}
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if status, ok := emptySettingsBody(req); !ok {
		settingsDecodeError(w, status)
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
	env, found := a.envProviderConfig(name)
	if !found {
		writeError(w, http.StatusNotFound, "unknown_provider", "The provider is not configured in MODEL_PROVIDERS.")
		return
	}
	if env.modelsURL == "" {
		writeError(w, http.StatusBadGateway, "provider_unavailable", "The provider model list URL is not configured.")
		return
	}
	var currentEntry model.ProviderCatalogEntry
	for _, candidate := range a.providerCatalog() {
		if candidate.Provider == name {
			currentEntry = candidate
			break
		}
	}

	ctx, cancel := context.WithTimeout(req.Context(), adminProviderRefreshTimeout)
	defer cancel()
	fetched, err := model.FetchModelIDs(ctx, env.modelsURL, env.apiKey, adminProviderRefreshTimeout, nil)
	if err != nil {
		a.logger.Warn("the admin provider catalogue refresh failed", "provider", name, "error", err)
		writeError(w, http.StatusBadGateway, "provider_unavailable", "The provider /v1/models endpoint could not be read.")
		return
	}

	var allowed = make(map[string]bool, len(env.models))
	var order []string
	for _, item := range env.models {
		var trimmed = strings.TrimSpace(item)
		if trimmed == "" || allowed[trimmed] {
			continue
		}
		allowed[trimmed] = true
		order = append(order, trimmed)
	}
	var seen = make(map[string]bool, len(fetched))
	var offered, ignored []string
	var ignoredCount int
	for _, item := range fetched {
		var trimmed = strings.TrimSpace(item)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		if !allowed[trimmed] {
			ignoredCount++
			if len(ignored) < adminProviderRefreshLimit {
				ignored = append(ignored, trimmed)
			}
			continue
		}
		offered = append(offered, trimmed)
	}
	// The env order is the catalogue order, so the list a refresh writes is stable across two identical
	// provider answers rather than following whatever order the endpoint happened to return.
	var ordered []string
	for _, item := range order {
		if seen[item] {
			ordered = append(ordered, item)
		}
	}
	offered = ordered
	if len(offered) == 0 {
		writeError(w, http.StatusBadGateway, "provider_unavailable",
			"The provider returned no model id that MODEL_PROVIDERS allows; the catalogue was not changed.")
		return
	}

	var added, removed []string
	for _, item := range offered {
		if !containsString(currentEntry.Models, item) {
			added = append(added, item)
		}
	}
	for _, item := range currentEntry.Models {
		if !containsString(offered, item) {
			removed = append(removed, item)
		}
	}
	var defaultModel = currentEntry.DefaultModel
	if !containsString(offered, defaultModel) {
		if env.defaultModel != "" && containsString(offered, env.defaultModel) {
			defaultModel = env.defaultModel
		} else {
			defaultModel = offered[0]
		}
	}
	var changed = len(added) > 0 || len(removed) > 0 || defaultModel != currentEntry.DefaultModel
	var result = adminProviderRefreshResult{
		Added: append([]string{}, added...), Removed: append([]string{}, removed...),
		Current: append([]string{}, offered...), Ignored: append([]string{}, ignored...),
		IgnoredCount: ignoredCount, Fetched: len(seen), Applied: changed,
	}
	if changed {
		var budgetMicros int64
		if existing, found, readErr := store.GetProviderSettings(ctx, a.db, name); readErr != nil {
			a.queryFailed(w, "admin provider refresh budget", readErr)
			return
		} else if found {
			budgetMicros = existing.DailyBudgetMicros
		}
		settings, validateErr := store.ValidateProviderSettings(store.ProviderSettings{
			Provider: name, Models: offered, DefaultModel: defaultModel, Enabled: currentEntry.Enabled,
			DailyBudgetMicros: budgetMicros,
		}, a.providerNames())
		if validateErr != nil {
			writeError(w, http.StatusBadRequest, "provider_validation", "The refreshed provider model list is invalid.")
			return
		}
		if err := store.UpsertProviderSettings(ctx, a.db, settings); err != nil {
			if errors.Is(err, store.ErrInvalidProviderSettings) {
				writeError(w, http.StatusBadRequest, "provider_validation", "The refreshed provider model list is invalid.")
				return
			}
			a.queryFailed(w, "admin provider refresh", err)
			return
		}
		// Persist first, then apply, exactly like PUT: a crash between the writes is repaired by startup.
		if overrides := a.providerOverrides(); overrides != nil {
			if err := overrides.SetProviderModels(name, settings.Models, settings.DefaultModel, settings.Enabled); err != nil {
				a.logger.Warn("the refreshed provider row was stored but not applied", "provider", name, "error", err)
			}
		}
		a.invalidateModelCache()
		currentEntry = model.ProviderCatalogEntry{
			Provider: name, Models: settings.Models, DefaultModel: settings.DefaultModel,
			Enabled: settings.Enabled, Source: model.ProviderSourceDB,
		}
	} else {
		currentEntry.Models = offered
		currentEntry.DefaultModel = defaultModel
	}
	a.auditProviderRefresh(ctx, current, name, len(added), len(removed), len(offered), ignoredCount)
	item, err := a.providerEntryWithSpend(ctx, currentEntry)
	if err != nil {
		a.queryFailed(w, "admin provider refresh", err)
		return
	}
	result.Provider = item
	writeJSON(w, http.StatusOK, result)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (a *App) providerNames() []string {
	var names []string
	for _, entry := range a.providerCatalog() {
		names = append(names, entry.Provider)
	}
	return names
}

// auditProviderRefresh records the diff counts, never the endpoint answer. It is a dedicated operation so a
// catalogue sync is distinguishable from a hand edit in the audit trail.
func (a *App) auditProviderRefresh(ctx context.Context, current scope, name string, added, removed, currentCount, ignored int) {
	var detail = fmt.Sprintf("operation=admin.provider.refresh chat_id=%d provider=%s added=%d removed=%d current=%d ignored=%d",
		current.ChatID, name, added, removed, currentCount, ignored)
	if err := store.LogEvent(ctx, a.db, current.UserID, "miniapp_mutation", detail); err != nil {
		a.logger.Error("Mini App provider refresh audit failed", "provider", name, "error", err)
	}
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
