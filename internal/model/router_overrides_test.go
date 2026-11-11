package model

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func overrideRouter(t *testing.T) (Client, ProviderOverrides, *routerFake, *routerFake) {
	t.Helper()
	var def = &routerFake{name: "default", models: []string{"qwen"}, content: "default answer"}
	var kimi = &routerFake{name: "kimi", content: "kimi answer"}
	var router = providerRouter(t, def, Provider{
		Name: "kimi", Models: []string{"kimi-k3", "kimi-k2"}, DefaultModel: "kimi-k3", Client: kimi,
	})
	overrides, ok := router.(ProviderOverrides)
	if !ok {
		t.Fatalf("router %T does not implement ProviderOverrides", router)
	}
	return router, overrides, def, kimi
}

func TestRouterOverrideRoutesAndListsImmediately(t *testing.T) {
	router, overrides, _, kimi := overrideRouter(t)
	initial := overrides.ProviderCatalog()
	if len(initial) != 1 || initial[0].Provider != "kimi" || initial[0].Source != ProviderSourceEnv || !initial[0].Enabled {
		t.Fatalf("initial catalogue=%+v", initial)
	}

	if err := overrides.SetProviderModels("kimi", []string{"new-a", "new-b"}, "new-b", true); err != nil {
		t.Fatalf("SetProviderModels: %v", err)
	}
	var response, err = router.Complete(context.Background(), Request{Model: "kimi:new-a"})
	if err != nil || response.Content != "kimi answer" {
		t.Fatalf("override model Complete response=%+v err=%v", response, err)
	}
	if len(kimi.seen) != 1 || kimi.seen[0] != "new-a" {
		t.Fatalf("provider saw models %v, want new-a", kimi.seen)
	}
	if _, err := router.Complete(context.Background(), Request{Model: "kimi:kimi-k3"}); err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Fatalf("the env model removed by the override was still routable: %v", err)
	}
	// An empty suffix uses the overridden default.
	if _, err := router.Complete(context.Background(), Request{Model: "kimi:"}); err != nil {
		t.Fatalf("overridden default model: %v", err)
	}
	if kimi.seen[len(kimi.seen)-1] != "new-b" {
		t.Fatalf("empty suffix sent %q, want the override default new-b", kimi.seen[len(kimi.seen)-1])
	}

	models, err := router.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if !reflect.DeepEqual(models, []string{"qwen", "kimi:new-a", "kimi:new-b"}) {
		t.Fatalf("Models=%v, want the override list immediately", models)
	}
	catalog := overrides.ProviderCatalog()
	if catalog[0].Source != ProviderSourceDB || catalog[0].DefaultModel != "new-b" || len(catalog[0].Models) != 2 {
		t.Fatalf("catalogue after override=%+v", catalog[0])
	}
}

func TestRouterDisabledProviderRejectsAndHidesModels(t *testing.T) {
	router, overrides, def, kimi := overrideRouter(t)
	if err := overrides.SetProviderModels("kimi", []string{"kimi-k3"}, "kimi-k3", false); err != nil {
		t.Fatalf("SetProviderModels: %v", err)
	}
	_, err := router.Complete(context.Background(), Request{Model: "kimi:kimi-k3"})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled provider error=%v, want a clear disabled error", err)
	}
	if kimi.calls != 0 || def.calls != 0 {
		t.Fatalf("a disabled provider was called: kimi=%d default=%d", kimi.calls, def.calls)
	}
	if _, err := router.Complete(context.Background(), Request{Model: "kimi:"}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled provider default error=%v", err)
	}
	models, err := router.Models(context.Background())
	if err != nil || !reflect.DeepEqual(models, []string{"qwen"}) {
		t.Fatalf("Models of a disabled provider=%v err=%v", models, err)
	}
	catalog := overrides.ProviderCatalog()
	if catalog[0].Enabled || catalog[0].Source != ProviderSourceDB {
		t.Fatalf("disabled catalogue=%+v", catalog[0])
	}
}

func TestRouterResetProviderReturnsToEnv(t *testing.T) {
	router, overrides, _, kimi := overrideRouter(t)
	if err := overrides.SetProviderModels("kimi", []string{"new-a"}, "new-a", false); err != nil {
		t.Fatalf("SetProviderModels: %v", err)
	}
	if err := overrides.ResetProviderModels("kimi"); err != nil {
		t.Fatalf("ResetProviderModels: %v", err)
	}
	catalog := overrides.ProviderCatalog()
	if catalog[0].Source != ProviderSourceEnv || !catalog[0].Enabled || catalog[0].DefaultModel != "kimi-k3" ||
		!reflect.DeepEqual(catalog[0].Models, []string{"kimi-k3", "kimi-k2"}) {
		t.Fatalf("catalogue after reset=%+v", catalog[0])
	}
	if _, err := router.Complete(context.Background(), Request{Model: "kimi:kimi-k2"}); err != nil {
		t.Fatalf("env model after reset: %v", err)
	}
	if len(kimi.seen) != 1 || kimi.seen[0] != "kimi-k2" {
		t.Fatalf("provider saw %v, want kimi-k2", kimi.seen)
	}
	// Reset is idempotent.
	if err := overrides.ResetProviderModels("kimi"); err != nil {
		t.Fatalf("second ResetProviderModels: %v", err)
	}
	models, _ := router.Models(context.Background())
	if !reflect.DeepEqual(models, []string{"qwen", "kimi:kimi-k3", "kimi:kimi-k2"}) {
		t.Fatalf("Models after reset=%v", models)
	}
}

func TestRouterOverrideRejectsUnknownAndInvalid(t *testing.T) {
	_, overrides, _, _ := overrideRouter(t)
	if err := overrides.SetProviderModels("nope", []string{"m"}, "m", true); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown provider err=%v, want ErrUnknownProvider", err)
	}
	if err := overrides.ResetProviderModels("nope"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("reset unknown provider err=%v, want ErrUnknownProvider", err)
	}
	if err := overrides.SetProviderModels(DefaultProviderName, []string{"m"}, "m", true); !errors.Is(err, ErrInvalidProviderModels) {
		t.Fatalf("strata override err=%v, want ErrInvalidProviderModels", err)
	}
	if err := overrides.ResetProviderModels(DefaultProviderName); !errors.Is(err, ErrInvalidProviderModels) {
		t.Fatalf("strata reset err=%v, want ErrInvalidProviderModels", err)
	}
	var tooMany = make([]string, MaxProviderModels+1)
	for index := range tooMany {
		tooMany[index] = "m" + strings.Repeat("x", index)
	}
	for name, test := range map[string]struct {
		models  []string
		def     string
		wantErr error
	}{
		"empty":               {nil, "", ErrInvalidProviderModels},
		"too many":            {tooMany, tooMany[0], ErrInvalidProviderModels},
		"empty id":            {[]string{"m", " "}, "m", ErrInvalidProviderModels},
		"duplicate":           {[]string{"m", "m"}, "m", ErrInvalidProviderModels},
		"default not in list": {[]string{"m"}, "other", ErrInvalidProviderModels},
	} {
		if err := overrides.SetProviderModels("kimi", test.models, test.def, true); !errors.Is(err, test.wantErr) {
			t.Errorf("%s: err=%v, want %v", name, err, test.wantErr)
		}
	}
	// The refused writes left the env list intact.
	catalog := overrides.ProviderCatalog()
	if catalog[0].Source != ProviderSourceEnv || !reflect.DeepEqual(catalog[0].Models, []string{"kimi-k3", "kimi-k2"}) {
		t.Fatalf("a refused override changed the catalogue: %+v", catalog[0])
	}
}

func TestRouterOverridesAreConcurrentSafe(t *testing.T) {
	// The env list also contains "stable": Set and Reset alternate while one reader routes that id, so a
	// reset in the middle of a request is a legitimate state rather than a test failure.
	var def = &routerFake{name: "default", models: []string{"qwen"}, content: "default answer"}
	var kimi = &routerFake{name: "kimi", content: "kimi answer"}
	var router = providerRouter(t, def, Provider{
		Name: "kimi", Models: []string{"stable", "env-a"}, DefaultModel: "stable", Client: kimi,
	})
	overrides, ok := router.(ProviderOverrides)
	if !ok {
		t.Fatalf("router %T does not implement ProviderOverrides", router)
	}
	var wg sync.WaitGroup
	var errorsSeen = make(chan error, 64)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			var list = []string{"stable", "worker-" + string(rune('a'+worker))}
			for round := 0; round < 100; round++ {
				if err := overrides.SetProviderModels("kimi", list, "stable", true); err != nil {
					errorsSeen <- err
					return
				}
				if round%3 == 0 {
					if err := overrides.ResetProviderModels("kimi"); err != nil {
						errorsSeen <- err
						return
					}
				}
				if worker == 0 && round%5 == 0 {
					if _, err := router.Complete(context.Background(), Request{Model: "kimi:stable"}); err != nil {
						errorsSeen <- err
						return
					}
					if _, err := router.Models(context.Background()); err != nil {
						errorsSeen <- err
						return
					}
					_ = overrides.ProviderCatalog()
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatalf("concurrent override failed: %v", err)
	}
	if err := overrides.ResetProviderModels("kimi"); err != nil {
		t.Fatalf("final reset: %v", err)
	}
	catalog := overrides.ProviderCatalog()
	if catalog[0].Source != ProviderSourceEnv || catalog[0].DefaultModel != "stable" {
		t.Fatalf("final catalogue=%+v", catalog[0])
	}
}
