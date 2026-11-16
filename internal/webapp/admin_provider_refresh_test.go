package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

// fakeProviderCatalog is a real HTTP server that answers the one endpoint the refresh uses. It records the
// Authorization header so the test can prove the env api_key is sent without ever exposing it in the API.
type fakeProviderCatalog struct {
	mu     sync.Mutex
	models []string
	status int
	hits   int
	auth   string
	path   string
}

func (f *fakeProviderCatalog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits++
	f.auth = r.Header.Get("Authorization")
	f.path = r.URL.Path
	models := append([]string{}, f.models...)
	status := f.status
	f.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	if status != http.StatusOK {
		http.Error(w, "upstream failure", status)
		return
	}
	data := make([]map[string]string, 0, len(models))
	for _, id := range models {
		data = append(data, map[string]string{"id": id})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (f *fakeProviderCatalog) snapshot() (int, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits, f.auth, f.path
}

func refreshProviderFixture(t *testing.T, providerModels []string) (*App, *httptest.Server, string, string, model.ProviderOverrides, *fakeProviderCatalog) {
	t.Helper()
	fake := &fakeProviderCatalog{models: providerModels}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)
	app, server := newTestAppWithRaw(t, map[string]string{
		"TELEGRAM_ADMIN_USERS": strconv.FormatInt(testUserID, 10),
		"MODEL_PROVIDERS": `[{"name":"kimi","base_url":"` + upstream.URL + `/v1","api_key":"sk-refresh-test",` +
			`"models":["kimi-k3","kimi-k2"],"default_model":"kimi-k3"}]`,
	})
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, otherTestUserID, otherTestUserID)
	def := &fakeModelClient{models: []string{"qwen"}}
	kimi := &fakeModelClient{models: []string{"kimi-k3", "kimi-k2"}}
	client := model.NewRouter(model.RouterOptions{
		Default: def, DefaultModel: "qwen",
		Providers: []model.Provider{{
			Name: "kimi", Models: []string{"kimi-k3", "kimi-k2"}, DefaultModel: "kimi-k3", Client: kimi,
		}},
	})
	overrides, ok := client.(model.ProviderOverrides)
	if !ok {
		t.Fatalf("router %T does not implement ProviderOverrides", client)
	}
	app.model = client
	app.modelCache = modelListCache{}
	return app, server, bearer, otherBearer, overrides, fake
}

func decodeRefreshResult(t *testing.T, body []byte) adminProviderRefreshResult {
	t.Helper()
	var result adminProviderRefreshResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("refresh JSON: %v body=%s", err, body)
	}
	return result
}

func TestAdminProviderRefreshRemovesStaleModelsAndIgnoresThirdParty(t *testing.T) {
	app, server, bearer, _, overrides, fake := refreshProviderFixture(t,
		[]string{"kimi-k3", "kimi-k4", "gpt-4"})
	status, body, _ := apiRequest(t, server, http.MethodPost, adminProviderTestPath("kimi")+"/refresh", bearer)
	if status != http.StatusOK {
		t.Fatalf("POST refresh status=%d body=%s", status, body)
	}
	result := decodeRefreshResult(t, body)
	if !result.Applied || strings.Join(result.Current, ",") != "kimi-k3" ||
		strings.Join(result.Added, ",") != "" || strings.Join(result.Removed, ",") != "kimi-k2" {
		t.Fatalf("refresh result=%+v", result)
	}
	if result.IgnoredCount != 2 || strings.Join(result.Ignored, ",") != "kimi-k4,gpt-4" || result.Fetched != 3 {
		t.Fatalf("ignored=%v count=%d fetched=%d, want kimi-k4,gpt-4/2/3", result.Ignored, result.IgnoredCount, result.Fetched)
	}
	if strings.Join(result.Provider.Models, ",") != "kimi-k3" || result.Provider.Default != "kimi-k3" ||
		result.Provider.Source != model.ProviderSourceDB || !result.Provider.Enabled {
		t.Fatalf("provider entry after refresh=%+v", result.Provider)
	}
	if result.Provider.Priced {
		t.Fatal("priced=true without MODEL_PRICES")
	}
	if hits, auth, path := fake.snapshot(); hits != 1 || auth != "Bearer sk-refresh-test" || path != "/v1/models" {
		t.Fatalf("upstream hits=%d auth=%q path=%q", hits, auth, path)
	}
	if catalog := overrides.ProviderCatalog(); strings.Join(catalog[0].Models, ",") != "kimi-k3" ||
		catalog[0].Source != model.ProviderSourceDB || catalog[0].DefaultModel != "kimi-k3" {
		t.Fatalf("router catalogue after refresh=%+v", catalog[0])
	}
	stored, found, err := store.GetProviderSettings(context.Background(), app.db, "kimi")
	if err != nil || !found || strings.Join(stored.Models, ",") != "kimi-k3" || stored.DefaultModel != "kimi-k3" {
		t.Fatalf("stored settings=%+v found=%v err=%v", stored, found, err)
	}
	var detail string
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT detail FROM events WHERE kind = 'miniapp_mutation' ORDER BY id DESC LIMIT 1`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"operation=admin.provider.refresh", "provider=kimi", "added=0", "removed=1", "current=1", "ignored=2"} {
		if !strings.Contains(detail, want) {
			t.Errorf("refresh audit %q misses %q", detail, want)
		}
	}
}

func TestAdminProviderRefreshAddsOnlyEnvAllowedModels(t *testing.T) {
	app, server, bearer, _, overrides, _ := refreshProviderFixture(t,
		[]string{"kimi-k3", "kimi-k2", "evil-model"})
	ctx := context.Background()
	if err := store.UpsertProviderSettings(ctx, app.db, store.ProviderSettings{
		Provider: "kimi", Models: []string{"kimi-k2"}, DefaultModel: "kimi-k2", Enabled: true,
	}); err != nil {
		t.Fatalf("seed override: %v", err)
	}
	if err := overrides.SetProviderModels("kimi", []string{"kimi-k2"}, "kimi-k2", true); err != nil {
		t.Fatalf("apply seed override: %v", err)
	}

	status, body, _ := apiRequest(t, server, http.MethodPost, adminProviderTestPath("kimi")+"/refresh", bearer)
	if status != http.StatusOK {
		t.Fatalf("POST refresh status=%d body=%s", status, body)
	}
	result := decodeRefreshResult(t, body)
	if !result.Applied || strings.Join(result.Added, ",") != "kimi-k3" ||
		strings.Join(result.Removed, ",") != "" ||
		strings.Join(result.Current, ",") != "kimi-k3,kimi-k2" {
		t.Fatalf("refresh result=%+v", result)
	}
	if strings.Contains(strings.Join(result.Current, ","), "evil-model") {
		t.Fatalf("refresh added a model outside MODEL_PROVIDERS: %+v", result)
	}
	if result.IgnoredCount != 1 || strings.Join(result.Ignored, ",") != "evil-model" {
		t.Fatalf("ignored=%v count=%d, want evil-model", result.Ignored, result.IgnoredCount)
	}
	if result.Provider.Default != "kimi-k2" {
		t.Fatalf("the surviving default model changed: %+v", result.Provider)
	}
	if catalog := overrides.ProviderCatalog(); strings.Join(catalog[0].Models, ",") != "kimi-k3,kimi-k2" {
		t.Fatalf("router catalogue=%+v", catalog[0])
	}
}

func TestAdminProviderRefreshPicksTheEnvDefaultWhenTheCurrentDefaultDisappears(t *testing.T) {
	app, server, bearer, _, _, _ := refreshProviderFixture(t, []string{"kimi-k3"})
	ctx := context.Background()
	if err := store.UpsertProviderSettings(ctx, app.db, store.ProviderSettings{
		Provider: "kimi", Models: []string{"kimi-k2"}, DefaultModel: "kimi-k2", Enabled: true,
	}); err != nil {
		t.Fatalf("seed override: %v", err)
	}
	status, body, _ := apiRequest(t, server, http.MethodPost, adminProviderTestPath("kimi")+"/refresh", bearer)
	if status != http.StatusOK {
		t.Fatalf("POST refresh status=%d body=%s", status, body)
	}
	result := decodeRefreshResult(t, body)
	if !result.Applied || result.Provider.Default != "kimi-k3" || strings.Join(result.Current, ",") != "kimi-k3" {
		t.Fatalf("refresh result=%+v, want env default kimi-k3", result)
	}
}

func TestAdminProviderRefreshRefusesUnsafeOrUnavailableAnswers(t *testing.T) {
	app, server, bearer, _, overrides, fake := refreshProviderFixture(t, []string{"evil-model"})
	before := overrides.ProviderCatalog()
	status, body, _ := apiRequest(t, server, http.MethodPost, adminProviderTestPath("kimi")+"/refresh", bearer)
	if status != http.StatusBadGateway || !strings.Contains(string(body), "provider_unavailable") {
		t.Fatalf("no-allowed-models status=%d body=%s, want 502 provider_unavailable", status, body)
	}
	if catalog := overrides.ProviderCatalog(); strings.Join(catalog[0].Models, ",") != strings.Join(before[0].Models, ",") {
		t.Fatalf("a refused refresh changed the router: before=%v after=%v", before[0].Models, catalog[0].Models)
	}
	if _, found, err := store.GetProviderSettings(context.Background(), app.db, "kimi"); err != nil || found {
		t.Fatalf("a refused refresh wrote a row: found=%v err=%v", found, err)
	}

	fake.mu.Lock()
	fake.status = http.StatusInternalServerError
	fake.mu.Unlock()
	status, body, _ = apiRequest(t, server, http.MethodPost, adminProviderTestPath("kimi")+"/refresh", bearer)
	if status != http.StatusBadGateway || !strings.Contains(string(body), "provider_unavailable") {
		t.Fatalf("upstream failure status=%d body=%s, want 502 provider_unavailable", status, body)
	}
}

func TestAdminProviderRefreshRequiresAdminPostAndKnownProvider(t *testing.T) {
	_, server, bearer, otherBearer, _, _ := refreshProviderFixture(t, []string{"kimi-k3"})
	cases := []struct {
		name   string
		method string
		path   string
		bearer string
		want   int
		code   string
	}{
		{name: "non-admin", method: http.MethodPost, path: adminProviderTestPath("kimi") + "/refresh", bearer: otherBearer, want: http.StatusForbidden, code: "admin_required"},
		{name: "wrong method", method: http.MethodGet, path: adminProviderTestPath("kimi") + "/refresh", bearer: bearer, want: http.StatusMethodNotAllowed, code: "method_not_allowed"},
		{name: "unknown provider", method: http.MethodPost, path: adminProviderTestPath("nope") + "/refresh", bearer: bearer, want: http.StatusNotFound, code: "unknown_provider"},
		{name: "query", method: http.MethodPost, path: adminProviderTestPath("kimi") + "/refresh?x=1", bearer: bearer, want: http.StatusBadRequest, code: "invalid_query"},
		{name: "trailing slash", method: http.MethodPost, path: adminProviderTestPath("kimi") + "/refresh/", bearer: bearer, want: http.StatusNotFound, code: "not_found"},
	}
	for _, test := range cases {
		status, body, _ := apiRequest(t, server, test.method, test.path, test.bearer)
		if status != test.want || !strings.Contains(string(body), test.code) {
			t.Errorf("%s: status=%d body=%s, want %d %s", test.name, status, body, test.want, test.code)
		}
	}
}

func TestAdminProviderRefreshReportsUnpricedAndKeepsBudget(t *testing.T) {
	app, server, bearer, _, _, _ := refreshProviderFixture(t, []string{"kimi-k3"})
	if err := store.SetProviderBudget(context.Background(), app.db, "kimi", 2_000_000, store.ProviderSettings{
		Provider: "kimi", Models: []string{"kimi-k3", "kimi-k2"}, DefaultModel: "kimi-k3", Enabled: true,
	}); err != nil {
		t.Fatalf("SetProviderBudget: %v", err)
	}
	status, body, _ := apiRequest(t, server, http.MethodPost, adminProviderTestPath("kimi")+"/refresh", bearer)
	if status != http.StatusOK {
		t.Fatalf("POST refresh status=%d body=%s", status, body)
	}
	result := decodeRefreshResult(t, body)
	if result.Provider.DailyBudgetUSD != 2 || result.Provider.Priced {
		t.Fatalf("provider after refresh=%+v, want budget 2 and priced=false", result.Provider)
	}
	if result.Provider.TodayCostUSD != 0 {
		t.Fatalf("unexpected spend: %+v", result.Provider)
	}
	if !strings.Contains(fmt.Sprint(result.Current), "kimi-k3") {
		t.Fatalf("current=%v", result.Current)
	}
}
