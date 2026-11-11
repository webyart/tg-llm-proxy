package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

type applyFakeClient struct {
	models []string
}

func (f *applyFakeClient) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}

func (f *applyFakeClient) Models(context.Context) ([]string, error) {
	return append([]string{}, f.models...), nil
}

// TestApplyProviderSettingsAtStartup — provider_settings rows are loaded after the store is open and applied to
// the router before serving. A row for a provider removed from MODEL_PROVIDERS and a leftover strata row are
// ignored and logged; the log names the provider and the model count, never a key or a model id.
func TestApplyProviderSettingsAtStartup(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx, "startup-provider-settings")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	if err := store.UpsertProviderSettings(ctx, db, store.ProviderSettings{
		Provider: "kimi", Models: []string{"new-a", "new-b"}, DefaultModel: "new-b", Enabled: false,
	}); err != nil {
		t.Fatalf("seed kimi override: %v", err)
	}
	if err := store.UpsertProviderSettings(ctx, db, store.ProviderSettings{
		Provider: "gone", Models: []string{"gone-model"}, DefaultModel: "gone-model", Enabled: true,
	}); err != nil {
		t.Fatalf("seed unknown provider override: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_settings (provider, models_json, default_model, enabled, updated_at)
		VALUES ('strata', '["primary-model"]', 'primary-model', 1, '2026-01-01 00:00:00')`); err != nil {
		t.Fatalf("seed strata row: %v", err)
	}

	def := &applyFakeClient{models: []string{"qwen"}}
	kimi := &applyFakeClient{}
	client := model.NewRouter(model.RouterOptions{
		Default: def, DefaultModel: "qwen",
		Providers: []model.Provider{{
			Name: "kimi", Models: []string{"kimi-k3"}, DefaultModel: "kimi-k3", Client: kimi,
		}},
	})
	overrides, ok := client.(model.ProviderOverrides)
	if !ok {
		t.Fatalf("router %T does not implement ProviderOverrides", client)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	applied, err := applyProviderSettings(ctx, db, client, logger)
	if err != nil || applied != 1 {
		t.Fatalf("applyProviderSettings applied=%d err=%v, want one row", applied, err)
	}
	catalog := overrides.ProviderCatalog()
	if len(catalog) != 1 || catalog[0].Source != model.ProviderSourceDB || catalog[0].Enabled ||
		catalog[0].DefaultModel != "new-b" || strings.Join(catalog[0].Models, ",") != "new-a,new-b" {
		t.Fatalf("applied catalogue=%+v", catalog)
	}
	if _, err := client.Complete(ctx, model.Request{Model: "kimi:new-a"}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("a disabled startup override was routable: %v", err)
	}

	logged := logs.String()
	for _, want := range []string{
		"provider model override applied", "provider=kimi", "models=2",
		"the provider is not configured", "provider=gone",
		"strata is reserved", "provider=strata",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("startup log misses %q: %s", want, logged)
		}
	}
	for _, secret := range []string{"new-a", "new-b", "gone-model", "primary-model"} {
		if strings.Contains(logged, secret) {
			t.Errorf("startup log leaked a model id %q: %s", secret, logged)
		}
	}
}

func TestApplyProviderSettingsWithoutNamedProviders(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx, "startup-provider-settings-none")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if err := store.UpsertProviderSettings(ctx, db, store.ProviderSettings{
		Provider: "kimi", Models: []string{"m"}, DefaultModel: "m", Enabled: true,
	}); err != nil {
		t.Fatalf("seed override: %v", err)
	}
	var logs bytes.Buffer
	applied, err := applyProviderSettings(ctx, db, &applyFakeClient{models: []string{"qwen"}},
		slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil || applied != 0 {
		t.Fatalf("applyProviderSettings applied=%d err=%v", applied, err)
	}
	if !strings.Contains(logs.String(), "no named provider is usable") {
		t.Fatalf("a stored row was silently ignored: %s", logs.String())
	}
}
