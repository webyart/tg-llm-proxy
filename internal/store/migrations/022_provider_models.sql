-- 022_provider_models.sql — runtime override of the per-provider model list (schema 22).
--
-- MODEL_PROVIDERS stays the base: name, base_url, api_key and timeout_seconds are environment-only and
-- never leave the process. A row of this table overrides exactly the three values an administrator may
-- change in the Mini App — the closed model list, its default and the enabled flag. The absent row means
-- "use the env value", so DELETE of this table is the reset that keeps base_url/api_key untouched.
--
-- models_json is a JSON array of 1..20 non-empty unique strings; validation lives in store and in the
-- admin API, and the CHECKs below are the last line of defence for a hand-edited database. There is no
-- foreign key: a configured provider is an environment row, not a database row, so a row for a provider
-- removed from MODEL_PROVIDERS is ignored (and logged) at startup instead of failing the start.
CREATE TABLE IF NOT EXISTS provider_settings (
    provider      TEXT PRIMARY KEY,
    models_json   TEXT NOT NULL,
    default_model TEXT NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    updated_at    TEXT NOT NULL
);
