package store

// Runtime overrides of a configured model provider.
//
// MODEL_PROVIDERS is the base: a provider is an environment row, so name, base_url, api_key and
// timeout_seconds never enter this table. One row of provider_settings overrides exactly the three values
// an administrator edits in the Mini App — models, default_model and enabled. The absence of a row is the
// default state of the feature: the env list applies, and a DELETE is the reset that brings it back.
//
// There is no foreign key because the parent of this row is the environment, not another table. A row for
// a provider that MODEL_PROVIDERS no longer lists is therefore possible; startup ignores it (and logs the
// name) instead of failing the start, and the admin API refuses it as an unknown provider.
//
// Validation lives here as well as in the router/API: the store is the last place that can refuse an
// override written by anything other than the Mini App, so a hand-edited row cannot silently empty a
// provider or point its default at a model that is not in the list.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MaxProviderModels is the upper bound of one provider's list. It matches config.MaxProviderModels and
// the /model keyboard limit: a longer list is not a useful catalogue, and the Mini App textarea caps at
// the same number.
const MaxProviderModels = 20

// Errors of this file. The admin API maps ErrInvalidProviderSettings to 400 and ErrUnknownProvider to
// 404; the startup apply only logs both.
var (
	ErrInvalidProviderSettings = errors.New("invalid provider settings")
	ErrUnknownProvider         = errors.New("unknown model provider")
)

// providerNamePattern is the same key rule MODEL_PROVIDERS uses: a chat types this name before the colon,
// so the comparison is literal and lower-case only.
var providerNamePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// ProviderSettings is one provider_settings row in its Go form. Models is the parsed, trimmed and
// deduplicated list; UpdatedAt is the stored timestamp of the last write.
type ProviderSettings struct {
	Provider     string
	Models       []string
	DefaultModel string
	Enabled      bool
	UpdatedAt    string
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
		SELECT provider, models_json, default_model, enabled, updated_at
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
		if err := rows.Scan(&row.Provider, &modelsJSON, &row.DefaultModel, &enabled, &row.UpdatedAt); err != nil {
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
		SELECT provider, models_json, default_model, enabled, updated_at
		FROM provider_settings WHERE provider = ?`, provider).
		Scan(&row.Provider, &modelsJSON, &row.DefaultModel, &enabled, &row.UpdatedAt)
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
		INSERT INTO provider_settings (provider, models_json, default_model, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(provider) DO UPDATE SET
			models_json = excluded.models_json,
			default_model = excluded.default_model,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at`,
		normalized.Provider, string(modelsJSON), normalized.DefaultModel, enabled,
		time.Now().UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return fmt.Errorf("write provider settings of %q: %w", normalized.Provider, err)
	}
	return nil
}

// DeleteProviderSettings removes one override and reports whether a row existed. Deleting an override
// that is not there is not an error: the reset is idempotent and means "the env value applies now".
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
	return affected > 0, nil
}
