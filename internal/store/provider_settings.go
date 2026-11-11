package store

// Runtime overrides of a configured model provider.
//
// MODEL_PROVIDERS is the base: a provider is an environment row, so name, base_url, api_key and
// timeout_seconds never enter this table. One row of provider_settings overrides exactly the values an
// administrator edits in the Mini App — models, default_model, enabled and (schema 28) the daily USD budget.
// The absence of a row is the default state of the feature: the env list applies, and a DELETE is the reset
// that brings it back (including "no budget").
//
// There is no foreign key because the parent of this row is the environment, not another table. A row for
// a provider that MODEL_PROVIDERS no longer lists is therefore possible; startup ignores it (and logs the
// name) instead of failing the start, and the admin API refuses it as an unknown provider.
//
// Validation lives here as well as in the router/API: the store is the last place that can refuse an
// override written by anything other than the Mini App, so a hand-edited row cannot silently empty a
// provider, point its default at a model that is not in the list, or store a negative budget.
//
// Every committed write also bumps ProviderSettingsVersion, the in-process signal the bot's 30-second
// provider budget cache uses to invalidate itself immediately after a Mini App save.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// MaxProviderModels is the upper bound of one provider's list. It matches config.MaxProviderModels and
// the /model keyboard limit: a longer list is not a useful catalogue, and the Mini App textarea caps at
// the same number.
const MaxProviderModels = 20

// MaxProviderDailyBudgetMicros is the upper bound of one provider's daily budget: 100000 USD. It matches the
// admin API range (0..100000 with six decimals) and keeps a mistyped amount from becoming an absurd limit.
// Zero is unlimited, so a provider without a budget is the default state.
const MaxProviderDailyBudgetMicros int64 = 100_000_000_000

const budgetMicrosPerUSD = 1_000_000.0

// Errors of this file. The admin API maps ErrInvalidProviderSettings to 400 and ErrUnknownProvider to
// 404; the startup apply only logs both.
var (
	ErrInvalidProviderSettings = errors.New("invalid provider settings")
	ErrUnknownProvider         = errors.New("unknown model provider")
)

// providerNamePattern is the same key rule MODEL_PROVIDERS uses: a chat types this name before the colon,
// so the comparison is literal and lower-case only.
var providerNamePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// providerSettingsVersion counts successful provider_settings mutations in THIS process. The bot's provider
// budget gate caches budgets and spend for 30 seconds; reading this counter on every check lets a Mini App save
// in the same process invalidate the cache immediately without the bot depending on webapp. A mutation made by
// another instance is still picked up by the 30-second TTL, which is the documented bound.
var providerSettingsVersion atomic.Uint64

// ProviderSettingsVersion returns the in-process counter of successful provider_settings writes. It never
// decreases and is cheap enough to read on the model-call hot path.
func ProviderSettingsVersion() uint64 {
	return providerSettingsVersion.Load()
}

// bumpProviderSettingsVersion records one committed write for the caches that derive from the table.
func bumpProviderSettingsVersion() {
	providerSettingsVersion.Add(1)
}

// ProviderSettings is one provider_settings row in its Go form. Models is the parsed, trimmed and
// deduplicated list; UpdatedAt is the stored timestamp of the last write. DailyBudgetMicros is the daily
// USD budget in millionths of a dollar, where 0 means unlimited.
type ProviderSettings struct {
	Provider          string
	Models            []string
	DefaultModel      string
	Enabled           bool
	DailyBudgetMicros int64
	UpdatedAt         string
}

// Clone returns a copy whose Models slice cannot be mutated through the receiver.
func (s ProviderSettings) Clone() ProviderSettings {
	s.Models = append([]string{}, s.Models...)
	return s
}

// NormalizeProviderSettings trims every value and rejects the structural problems an override may have:
// an empty or reserved provider name, an empty list, more than MaxProviderModels entries, an empty model
// id, a duplicate model id, and a default that is not in the list. It is the single normalizer used by
// Upsert and by ValidateProviderSettings, so the row written is exactly the row that was validated.
func NormalizeProviderSettings(settings ProviderSettings) (ProviderSettings, error) {
	settings.Provider = strings.TrimSpace(settings.Provider)
	if settings.Provider == "" {
		return ProviderSettings{}, fmt.Errorf("%w: provider is empty", ErrInvalidProviderSettings)
	}
	if settings.Provider == "strata" {
		return ProviderSettings{}, fmt.Errorf("%w: provider %q is reserved for the default endpoint", ErrInvalidProviderSettings, settings.Provider)
	}
	if !providerNamePattern.MatchString(settings.Provider) {
		return ProviderSettings{}, fmt.Errorf("%w: provider %q must match [a-z0-9_-]+", ErrInvalidProviderSettings, settings.Provider)
	}
	if settings.DailyBudgetMicros < 0 || settings.DailyBudgetMicros > MaxProviderDailyBudgetMicros {
		return ProviderSettings{}, fmt.Errorf("%w: daily budget of provider %q must be between 0 and %d micros (0..100000 USD)",
			ErrInvalidProviderSettings, settings.Provider, MaxProviderDailyBudgetMicros)
	}
	if len(settings.Models) == 0 {
		return ProviderSettings{}, fmt.Errorf("%w: provider %q has no models", ErrInvalidProviderSettings, settings.Provider)
	}
	if len(settings.Models) > MaxProviderModels {
		return ProviderSettings{}, fmt.Errorf("%w: provider %q lists %d models, at most %d are allowed",
			ErrInvalidProviderSettings, settings.Provider, len(settings.Models), MaxProviderModels)
	}
	var models = make([]string, 0, len(settings.Models))
	var seen = make(map[string]bool, len(settings.Models))
	for _, model := range settings.Models {
		var trimmed = strings.TrimSpace(model)
		if trimmed == "" {
			return ProviderSettings{}, fmt.Errorf("%w: provider %q has an empty model id", ErrInvalidProviderSettings, settings.Provider)
		}
		if seen[trimmed] {
			return ProviderSettings{}, fmt.Errorf("%w: provider %q lists model %q twice", ErrInvalidProviderSettings, settings.Provider, trimmed)
		}
		seen[trimmed] = true
		models = append(models, trimmed)
	}
	settings.Models = models
	settings.DefaultModel = strings.TrimSpace(settings.DefaultModel)
	if settings.DefaultModel == "" {
		return ProviderSettings{}, fmt.Errorf("%w: provider %q has no default_model", ErrInvalidProviderSettings, settings.Provider)
	}
	if !seen[settings.DefaultModel] {
		return ProviderSettings{}, fmt.Errorf("%w: default_model %q of provider %q is not one of its models",
			ErrInvalidProviderSettings, settings.DefaultModel, settings.Provider)
	}
	return settings, nil
}

// ValidateProviderSettings normalizes the row and, when known is non-empty, checks that the provider is
// one of the configured names. The admin API passes the router catalogue here; startup only needs the
// structural check and the unknown-row log, so it passes nil.
func ValidateProviderSettings(settings ProviderSettings, known []string) (ProviderSettings, error) {
	normalized, err := NormalizeProviderSettings(settings)
	if err != nil {
		return ProviderSettings{}, err
	}
	if len(known) == 0 {
		return normalized, nil
	}
	for _, name := range known {
		if name == normalized.Provider {
			return normalized, nil
		}
	}
	return ProviderSettings{}, fmt.Errorf("%w: %q (known: %s)", ErrUnknownProvider, normalized.Provider, strings.Join(known, ", "))
}

// ListProviderSettings returns every override ordered by provider name. An empty table is an empty
// slice, not an error: a deployment without overrides keeps the env values on every start.
func ListProviderSettings(ctx context.Context, db *sql.DB) ([]ProviderSettings, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT provider, models_json, default_model, enabled, daily_budget_micros, updated_at
		FROM provider_settings
		ORDER BY provider`)
	if err != nil {
		return nil, fmt.Errorf("list provider settings: %w", err)
	}
	defer rows.Close()

	var settings = []ProviderSettings{}
	for rows.Next() {
		var row ProviderSettings
		var modelsJSON string
		var enabled int
		if err := rows.Scan(&row.Provider, &modelsJSON, &row.DefaultModel, &enabled, &row.DailyBudgetMicros, &row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("list provider settings row: %w", err)
		}
		if err := json.Unmarshal([]byte(modelsJSON), &row.Models); err != nil {
			return nil, fmt.Errorf("provider settings %q: models_json: %w", row.Provider, err)
		}
		row.Models = append([]string{}, row.Models...)
		row.Enabled = enabled == 1
		settings = append(settings, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list provider settings rows: %w", err)
	}
	return settings, nil
}

// GetProviderSettings reads one override. A missing row is (zero value, false, nil): the caller keeps the
// env values instead of inventing an empty provider.
func GetProviderSettings(ctx context.Context, db *sql.DB, provider string) (ProviderSettings, bool, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ProviderSettings{}, false, fmt.Errorf("%w: provider is empty", ErrInvalidProviderSettings)
	}
	var row ProviderSettings
	var modelsJSON string
	var enabled int
	err := db.QueryRowContext(ctx, `
		SELECT provider, models_json, default_model, enabled, daily_budget_micros, updated_at
		FROM provider_settings WHERE provider = ?`, provider).
		Scan(&row.Provider, &modelsJSON, &row.DefaultModel, &enabled, &row.DailyBudgetMicros, &row.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderSettings{Provider: provider}, false, nil
	}
	if err != nil {
		return ProviderSettings{}, false, fmt.Errorf("read provider settings of %q: %w", provider, err)
	}
	if err := json.Unmarshal([]byte(modelsJSON), &row.Models); err != nil {
		return ProviderSettings{}, false, fmt.Errorf("provider settings %q: models_json: %w", provider, err)
	}
	row.Models = append([]string{}, row.Models...)
	row.Enabled = enabled == 1
	return row, true, nil
}

// UpsertProviderSettings validates and writes one row. updated_at is written on every call, so a row that
// was saved twice can be told apart by its timestamp.
func UpsertProviderSettings(ctx context.Context, db *sql.DB, settings ProviderSettings) error {
	normalized, err := NormalizeProviderSettings(settings)
	if err != nil {
		return err
	}
	modelsJSON, err := json.Marshal(normalized.Models)
	if err != nil {
		return fmt.Errorf("provider settings %q: models_json: %w", normalized.Provider, err)
	}
	var enabled = 0
	if normalized.Enabled {
		enabled = 1
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO provider_settings (provider, models_json, default_model, enabled, daily_budget_micros, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider) DO UPDATE SET
			models_json = excluded.models_json,
			default_model = excluded.default_model,
			enabled = excluded.enabled,
			daily_budget_micros = excluded.daily_budget_micros,
			updated_at = excluded.updated_at`,
		normalized.Provider, string(modelsJSON), normalized.DefaultModel, enabled,
		normalized.DailyBudgetMicros,
		time.Now().UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return fmt.Errorf("write provider settings of %q: %w", normalized.Provider, err)
	}
	bumpProviderSettingsVersion()
	return nil
}

// DeleteProviderSettings removes one override and reports whether a row existed. Deleting an override
// that is not there is not an error: the reset is idempotent and means "the env value applies now". The row
// carries the budget as well, so the reset also restores "no daily budget" (0 = unlimited).
func DeleteProviderSettings(ctx context.Context, db *sql.DB, provider string) (bool, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return false, fmt.Errorf("%w: provider is empty", ErrInvalidProviderSettings)
	}
	result, err := db.ExecContext(ctx, `DELETE FROM provider_settings WHERE provider = ?`, provider)
	if err != nil {
		return false, fmt.Errorf("delete provider settings of %q: %w", provider, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete provider settings of %q: rows affected: %w", provider, err)
	}
	if affected > 0 {
		bumpProviderSettingsVersion()
	}
	return affected > 0, nil
}

// ProviderBudgetMicros converts a USD amount to the stored millionths of a dollar. It refuses a non-finite
// or out-of-range amount; the value is rounded to the nearest micro, so a client that sends more than six
// decimals is stored at the precision the API reports. Zero is unlimited and always accepted.
func ProviderBudgetMicros(usd float64) (int64, error) {
	if math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0, fmt.Errorf("%w: the daily budget is not a finite number", ErrInvalidProviderSettings)
	}
	var micros = math.Round(usd * budgetMicrosPerUSD)
	if micros < 0 || micros > float64(MaxProviderDailyBudgetMicros) {
		return 0, fmt.Errorf("%w: the daily budget must be between 0 and %.0f USD",
			ErrInvalidProviderSettings, float64(MaxProviderDailyBudgetMicros)/budgetMicrosPerUSD)
	}
	return int64(micros), nil
}

// ProviderBudgetUSD is ProviderBudgetMicros inverted: the six decimals the admin API and the usage response
// report, where 0 means unlimited.
func ProviderBudgetUSD(micros int64) float64 {
	return float64(micros) / budgetMicrosPerUSD
}

// GetProviderBudget reads the daily budget of one provider in millionths of a dollar. A provider without a
// provider_settings row has no budget, so the missing row is (0, nil) rather than an error: "unlimited" is
// the default state of the feature.
func GetProviderBudget(ctx context.Context, db *sql.DB, provider string) (int64, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return 0, fmt.Errorf("%w: provider is empty", ErrInvalidProviderSettings)
	}
	var micros int64
	err := db.QueryRowContext(ctx,
		`SELECT daily_budget_micros FROM provider_settings WHERE provider = ?`, provider).Scan(&micros)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read provider budget of %q: %w", provider, err)
	}
	return micros, nil
}

// ProviderDailyBudgets returns the positive daily budgets of every provider in one query. A provider without
// a row or with 0 is absent from the map: the caller skips it, so a deployment without budgets never reads
// the spend table. The map is never nil, so a caller can range over it without a special case.
func ProviderDailyBudgets(ctx context.Context, db *sql.DB) (map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT provider, daily_budget_micros
		FROM provider_settings
		WHERE daily_budget_micros > 0
		ORDER BY provider`)
	if err != nil {
		return nil, fmt.Errorf("list provider budgets: %w", err)
	}
	defer rows.Close()
	var budgets = map[string]int64{}
	for rows.Next() {
		var provider string
		var micros int64
		if err := rows.Scan(&provider, &micros); err != nil {
			return nil, fmt.Errorf("list provider budgets row: %w", err)
		}
		budgets[provider] = micros
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list provider budgets rows: %w", err)
	}
	return budgets, nil
}

// SetProviderBudget stores only the daily budget of one provider. An existing row keeps its models, default
// and enabled flag: models_json and default_model are required by the schema, but a budget change is not a
// model change. A provider without a row gets one built from seed (the effective catalogue entry), so a
// budget can be set before the administrator ever edits the model list.
func SetProviderBudget(ctx context.Context, db *sql.DB, provider string, micros int64, seed ProviderSettings) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return fmt.Errorf("%w: provider is empty", ErrInvalidProviderSettings)
	}
	if micros < 0 || micros > MaxProviderDailyBudgetMicros {
		return fmt.Errorf("%w: daily budget of provider %q must be between 0 and %d micros (0..100000 USD)",
			ErrInvalidProviderSettings, provider, MaxProviderDailyBudgetMicros)
	}
	var updatedAt = time.Now().UTC().Format("2006-01-02 15:04:05")
	// The fast path keeps the model columns untouched, and the seed is only validated when a row really has
	// to be created.
	result, err := db.ExecContext(ctx,
		`UPDATE provider_settings SET daily_budget_micros = ?, updated_at = ? WHERE provider = ?`,
		micros, updatedAt, provider)
	if err != nil {
		return fmt.Errorf("write provider budget of %q: %w", provider, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("write provider budget of %q: rows affected: %w", provider, err)
	}
	if affected > 0 {
		bumpProviderSettingsVersion()
		return nil
	}
	seed.Provider = provider
	seed.DailyBudgetMicros = micros
	normalized, err := NormalizeProviderSettings(seed)
	if err != nil {
		return err
	}
	modelsJSON, err := json.Marshal(normalized.Models)
	if err != nil {
		return fmt.Errorf("provider budget of %q: models_json: %w", normalized.Provider, err)
	}
	var enabled = 0
	if normalized.Enabled {
		enabled = 1
	}
	// ON CONFLICT keeps the UPDATE/INSERT pair safe when two administrators save at the same moment: the
	// loser overwrites only the budget and the timestamp, never the other process's model list.
	_, err = db.ExecContext(ctx, `
		INSERT INTO provider_settings (provider, models_json, default_model, enabled, daily_budget_micros, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider) DO UPDATE SET
			daily_budget_micros = excluded.daily_budget_micros,
			updated_at = excluded.updated_at`,
		normalized.Provider, string(modelsJSON), normalized.DefaultModel, enabled, normalized.DailyBudgetMicros,
		updatedAt)
	if err != nil {
		return fmt.Errorf("write provider budget of %q: %w", normalized.Provider, err)
	}
	bumpProviderSettingsVersion()
	return nil
}
