package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func providerSettingsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenMemory(context.Background(), strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestProviderSettingsCRUD(t *testing.T) {
	ctx := context.Background()
	db := providerSettingsDB(t)

	rows, err := ListProviderSettings(ctx, db)
	if err != nil || len(rows) != 0 {
		t.Fatalf("fresh ListProviderSettings=%+v err=%v, want empty", rows, err)
	}
	if row, found, err := GetProviderSettings(ctx, db, "kimi"); err != nil || found || row.Provider != "kimi" || len(row.Models) != 0 {
		t.Fatalf("missing GetProviderSettings=%+v found=%v err=%v", row, found, err)
	}

	want := ProviderSettings{
		Provider: "kimi", Models: []string{" kimi-k3 ", "kimi-k2"},
		DefaultModel: "kimi-k2", Enabled: false,
	}
	if err := UpsertProviderSettings(ctx, db, want); err != nil {
		t.Fatalf("UpsertProviderSettings: %v", err)
	}
	got, found, err := GetProviderSettings(ctx, db, "kimi")
	if err != nil || !found {
		t.Fatalf("GetProviderSettings found=%v err=%v", found, err)
	}
	if got.Provider != "kimi" {
		t.Fatalf("provider=%q", got.Provider)
	}
	if strings.Join(got.Models, ",") != "kimi-k3,kimi-k2" {
		t.Fatalf("models=%v, want trimmed values", got.Models)
	}
	if got.DefaultModel != "kimi-k2" || got.Enabled {
		t.Fatalf("row=%+v", got)
	}
	if got.UpdatedAt == "" {
		t.Fatal("updated_at was not written")
	}

	// Replacing rewrites every field and keeps exactly one row.
	if err := UpsertProviderSettings(ctx, db, ProviderSettings{
		Provider: "kimi", Models: []string{"kimi-k4"}, DefaultModel: "kimi-k4", Enabled: true,
	}); err != nil {
		t.Fatalf("second UpsertProviderSettings: %v", err)
	}
	if err := UpsertProviderSettings(ctx, db, ProviderSettings{
		Provider: "deepseek", Models: []string{"deepseek-chat"}, DefaultModel: "deepseek-chat", Enabled: true,
	}); err != nil {
		t.Fatalf("second provider UpsertProviderSettings: %v", err)
	}
	rows, err = ListProviderSettings(ctx, db)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListProviderSettings=%+v err=%v, want two rows", rows, err)
	}
	if rows[0].Provider != "deepseek" || rows[1].Provider != "kimi" {
		t.Fatalf("rows are not ordered by provider: %+v", rows)
	}
	got, _, _ = GetProviderSettings(ctx, db, "kimi")
	if len(got.Models) != 1 || got.Models[0] != "kimi-k4" || got.DefaultModel != "kimi-k4" || !got.Enabled {
		t.Fatalf("replaced row=%+v", got)
	}

	deleted, err := DeleteProviderSettings(ctx, db, "kimi")
	if err != nil || !deleted {
		t.Fatalf("DeleteProviderSettings=%v err=%v", deleted, err)
	}
	if deleted, err := DeleteProviderSettings(ctx, db, "kimi"); err != nil || deleted {
		t.Fatalf("second DeleteProviderSettings=%v err=%v, want false", deleted, err)
	}
	if _, found, _ := GetProviderSettings(ctx, db, "kimi"); found {
		t.Fatal("a deleted provider still has a row")
	}
}

func TestProviderSettingsValidation(t *testing.T) {
	ctx := context.Background()
	db := providerSettingsDB(t)

	var tooMany = make([]string, MaxProviderModels+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("model-%d", index)
	}
	for name, settings := range map[string]ProviderSettings{
		"empty provider":     {Models: []string{"m"}, DefaultModel: "m"},
		"reserved strata":    {Provider: "strata", Models: []string{"m"}, DefaultModel: "m"},
		"bad provider name":  {Provider: "Kimi", Models: []string{"m"}, DefaultModel: "m"},
		"empty list":         {Provider: "kimi", DefaultModel: "m"},
		"too many models":    {Provider: "kimi", Models: tooMany, DefaultModel: tooMany[0]},
		"empty model":        {Provider: "kimi", Models: []string{"m", "  "}, DefaultModel: "m"},
		"duplicate model":    {Provider: "kimi", Models: []string{"m", "m"}, DefaultModel: "m"},
		"missing default":    {Provider: "kimi", Models: []string{"m"}},
		"default not listed": {Provider: "kimi", Models: []string{"m"}, DefaultModel: "other"},
	} {
		if err := UpsertProviderSettings(ctx, db, settings); !errors.Is(err, ErrInvalidProviderSettings) {
			t.Errorf("%s: err=%v, want ErrInvalidProviderSettings", name, err)
		}
	}
	if rows, err := ListProviderSettings(ctx, db); err != nil || len(rows) != 0 {
		t.Fatalf("a refused write left rows=%+v err=%v", rows, err)
	}

	if _, err := ValidateProviderSettings(ProviderSettings{
		Provider: "kimi", Models: []string{"m"}, DefaultModel: "m",
	}, []string{"deepseek"}); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown provider err=%v, want ErrUnknownProvider", err)
	}
	normalized, err := ValidateProviderSettings(ProviderSettings{
		Provider: " kimi ", Models: []string{" m ", "n"}, DefaultModel: " n ",
	}, []string{"kimi"})
	if err != nil {
		t.Fatalf("known provider: %v", err)
	}
	if normalized.Provider != "kimi" || strings.Join(normalized.Models, ",") != "m,n" || normalized.DefaultModel != "n" {
		t.Fatalf("normalized=%+v", normalized)
	}
}

func TestProviderSettingsDatabaseChecks(t *testing.T) {
	ctx := context.Background()
	db := providerSettingsDB(t)
	for name, statement := range map[string]string{
		"enabled outside 0..1": `INSERT INTO provider_settings (provider, models_json, default_model, enabled, updated_at)
			VALUES ('kimi', '["m"]', 'm', 2, '2026-01-01 00:00:00')`,
		"null models_json": `INSERT INTO provider_settings (provider, models_json, default_model, updated_at)
			VALUES ('kimi', NULL, 'm', '2026-01-01 00:00:00')`,
		"null default_model": `INSERT INTO provider_settings (provider, models_json, enabled, updated_at)
			VALUES ('kimi', '["m"]', 1, '2026-01-01 00:00:00')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err == nil {
			t.Errorf("%s was accepted by the schema", name)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_settings (provider, models_json, default_model, enabled, updated_at)
		VALUES ('kimi', '["m"]', 'm', 1, '2026-01-01 00:00:00')`); err != nil {
		t.Fatalf("valid row was refused: %v", err)
	}
	var enabled int
	if err := db.QueryRowContext(ctx, `SELECT enabled FROM provider_settings WHERE provider = 'kimi'`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("enabled=%d err=%v", enabled, err)
	}
}

func TestProviderSettingsCorruptModelsJSONIsAnError(t *testing.T) {
	ctx := context.Background()
	db := providerSettingsDB(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_settings (provider, models_json, default_model, enabled, updated_at)
		VALUES ('kimi', 'not-json', 'm', 1, '2026-01-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := GetProviderSettings(ctx, db, "kimi"); err == nil {
		t.Fatal("a corrupt models_json was accepted")
	}
	if _, err := ListProviderSettings(ctx, db); err == nil {
		t.Fatal("ListProviderSettings accepted a corrupt models_json")
	}
}

// TestMigration022PreservesTasks is the 21 -> 22 upgrade: existing tasks and their status_message_id must be
// untouched while the new provider_settings table appears at schema 22.
func TestMigration022PreservesTasks(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 21)
	if err := EnsureChat(ctx, db, 700, 11, "kept"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	task, err := CreateTask(ctx, db, Task{
		ChatID: 700, UserID: 11, Goal: "keep this task", Model: "model-a", PlanJSON: testTaskPlan,
		DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET status_message_id = 4242 WHERE id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}

	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("apply migration 022: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) || version < 22 {
		t.Fatalf("SchemaVersion=%d err=%v, want the newest migration (at least 22)", version, err)
	}
	var goal, status string
	var statusMessageID sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT goal, status, status_message_id FROM tasks WHERE id = ?`, task.ID).
		Scan(&goal, &status, &statusMessageID); err != nil {
		t.Fatalf("read preserved task: %v", err)
	}
	if goal != "keep this task" || status != TaskStatusReady || !statusMessageID.Valid || statusMessageID.Int64 != 4242 {
		t.Fatalf("task changed by the migration: goal=%q status=%q status_message_id=%v", goal, status, statusMessageID)
	}
	if _, err := db.ExecContext(ctx, `SELECT COUNT(*) FROM provider_settings`); err != nil {
		t.Fatalf("provider_settings is not queryable after the upgrade: %v", err)
	}
	if err := UpsertProviderSettings(ctx, db, ProviderSettings{
		Provider: "kimi", Models: []string{"m"}, DefaultModel: "m", Enabled: true,
	}); err != nil {
		t.Fatalf("write a provider override after the upgrade: %v", err)
	}
}
