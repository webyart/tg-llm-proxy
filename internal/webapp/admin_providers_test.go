package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

// adminProvidersFixture builds a Mini App whose model client is a real router over one configured provider.
// The API tests therefore exercise the same catalogue/override path production uses, not a stub.
func adminProvidersFixture(t *testing.T) (*App, *httptest.Server, string, string, model.ProviderOverrides) {
	t.Helper()
	app, server := newTestAppWithRaw(t, map[string]string{
		"TELEGRAM_ADMIN_USERS": strconv.FormatInt(testUserID, 10),
		"MODEL_PROVIDERS": `[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key":"sk-secret-test",` +
			`"models":["kimi-k3","kimi-k2"],"default_model":"kimi-k3"}]`,
	})
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, otherTestUserID, otherTestUserID)
	def := &fakeModelClient{models: []string{"qwen", "qwen-alt"}}
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
	return app, server, bearer, otherBearer, overrides
}

// decodeAdminProviderList reads the GET envelope shared with the other admin list endpoints.
func decodeAdminProviderList(t *testing.T, body []byte) []adminProviderEntry {
	t.Helper()
	var response struct {
		Items []adminProviderEntry `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("admin providers JSON: %v body=%s", err, body)
	}
	return response.Items
}

func adminProviderTestPath(name string) string { return "/api/admin/providers/" + name }

func TestAdminProvidersListUpdateAndReset(t *testing.T) {
	app, server, bearer, _, overrides := adminProvidersFixture(t)
	ctx := context.Background()

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/admin/providers", bearer)
	if status != http.StatusOK {
		t.Fatalf("GET providers status=%d body=%s", status, body)
	}
	items := decodeAdminProviderList(t, body)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want one configured provider", items)
	}
	item := items[0]
	if item.Name != "kimi" || item.BaseURL != "api.moonshot.ai" || item.Default != "kimi-k3" ||
		!item.Enabled || item.Source != model.ProviderSourceEnv ||
		strings.Join(item.Models, ",") != "kimi-k3,kimi-k2" ||
		strings.Join(item.Configured, ",") != "kimi-k3,kimi-k2" {
		t.Fatalf("initial provider entry=%+v", item)
	}
	if strings.Contains(string(body), "sk-secret-test") || strings.Contains(string(body), "/v1") {
		t.Fatalf("the provider list leaked a key or a path: %s", body)
	}

	status, body = putSettings(t, server.URL, adminProviderTestPath("kimi"), bearer,
		`{"models":["new-a","new-b"],"default_model":"new-b","enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("PUT provider status=%d body=%s", status, body)
	}
	var updated adminProviderEntry
	if err := json.Unmarshal(body, &updated); err != nil {
		t.Fatalf("PUT provider JSON: %v body=%s", err, body)
	}
	if updated.Name != "kimi" || updated.Source != model.ProviderSourceDB || updated.Default != "new-b" ||
		!updated.Enabled || strings.Join(updated.Models, ",") != "new-a,new-b" {
		t.Fatalf("updated entry=%+v", updated)
	}
	catalog := overrides.ProviderCatalog()
	if catalog[0].Source != model.ProviderSourceDB || strings.Join(catalog[0].Models, ",") != "new-a,new-b" {
		t.Fatalf("router catalogue=%+v", catalog[0])
	}
	stored, found, err := store.GetProviderSettings(ctx, app.db, "kimi")
	if err != nil || !found || strings.Join(stored.Models, ",") != "new-a,new-b" || !stored.Enabled {
		t.Fatalf("stored provider settings=%+v found=%v err=%v", stored, found, err)
	}

	// /api/settings must see the effective list immediately, without the 45 s cache.
	status, settingsBody := settingsRequest(t, server.URL, http.MethodGet, "/api/settings", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET settings status=%d body=%s", status, settingsBody)
	}
	var settings settingsResponse
	if err := json.Unmarshal(settingsBody, &settings); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(settings.Model.Available, ",")
	if !strings.Contains(joined, "kimi:new-a") || !strings.Contains(joined, "kimi:new-b") ||
		strings.Contains(joined, "kimi:kimi-k3") {
		t.Fatalf("settings available models=%v", settings.Model.Available)
	}

	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/admin/providers", bearer)
	if status != http.StatusOK {
		t.Fatalf("GET providers after update status=%d body=%s", status, body)
	}
	items = decodeAdminProviderList(t, body)
	if items[0].Source != model.ProviderSourceDB || strings.Join(items[0].Models, ",") != "new-a,new-b" {
		t.Fatalf("list after update=%+v", items[0])
	}

	status, body = settingsRequest(t, server.URL, http.MethodDelete, adminProviderTestPath("kimi"), bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("DELETE provider status=%d body=%s", status, body)
	}
	var reset adminProviderEntry
	if err := json.Unmarshal(body, &reset); err != nil {
		t.Fatalf("DELETE provider JSON: %v body=%s", err, body)
	}
	if reset.Source != model.ProviderSourceEnv || strings.Join(reset.Models, ",") != "kimi-k3,kimi-k2" || !reset.Enabled {
		t.Fatalf("reset entry=%+v", reset)
	}
	if _, found, err := store.GetProviderSettings(ctx, app.db, "kimi"); err != nil || found {
		t.Fatalf("row survived reset: found=%v err=%v", found, err)
	}
	status, body = settingsRequest(t, server.URL, http.MethodDelete, adminProviderTestPath("kimi"), bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("second DELETE status=%d body=%s", status, body)
	}

	status, body = putSettings(t, server.URL, adminProviderTestPath("nope"), bearer,
		`{"models":["m"],"default_model":"m","enabled":true}`)
	if status != http.StatusNotFound || !strings.Contains(string(body), "unknown_provider") {
		t.Fatalf("unknown provider PUT status=%d body=%s, want 404 unknown_provider", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodDelete, adminProviderTestPath("nope"), bearer, "", "")
	if status != http.StatusNotFound || !strings.Contains(string(body), "unknown_provider") {
		t.Fatalf("unknown provider DELETE status=%d body=%s, want 404 unknown_provider", status, body)
	}
	status, body = putSettings(t, server.URL, adminProviderTestPath("strata"), bearer,
		`{"models":["m"],"default_model":"m","enabled":true}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "invalid_provider") {
		t.Fatalf("strata PUT status=%d body=%s, want 400 invalid_provider", status, body)
	}
}

func TestAdminProvidersStrictValidation(t *testing.T) {
	app, server, bearer, _, overrides := adminProvidersFixture(t)
	tooMany := make([]string, model.MaxProviderModels+1)
	for index := range tooMany {
		tooMany[index] = `"m` + strconv.Itoa(index) + `"`
	}
	valid := `{"models":["m"],"default_model":"m","enabled":true}`
	for name, test := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"empty list":         {`{"models":[],"default_model":"m","enabled":true}`, 400, "provider_validation"},
		"empty id":           {`{"models":["m","  "],"default_model":"m","enabled":true}`, 400, "provider_validation"},
		"duplicate":          {`{"models":["m","m"],"default_model":"m","enabled":true}`, 400, "provider_validation"},
		"default not listed": {`{"models":["m"],"default_model":"other","enabled":true}`, 400, "provider_validation"},
		"too many":           {`{"models":[` + strings.Join(tooMany, ",") + `],"default_model":"m0","enabled":true}`, 400, "provider_validation"},
		"missing enabled":    {`{"models":["m"],"default_model":"m"}`, 400, "provider_validation"},
		"missing models":     {`{"default_model":"m","enabled":true}`, 400, "provider_validation"},
		"unknown field":      {`{"models":["m"],"default_model":"m","enabled":true,"api_key":"x"}`, 400, "invalid_json"},
		"wrong type":         {`{"models":"m","default_model":"m","enabled":true}`, 400, "invalid_json"},
		"trailing content":   {valid + `{}`, 400, "invalid_json"},
	} {
		status, body := putSettings(t, server.URL, adminProviderTestPath("kimi"), bearer, test.body)
		if status != test.status || !strings.Contains(string(body), test.code) {
			t.Errorf("%s: status=%d body=%s, want %d %s", name, status, body, test.status, test.code)
		}
	}
	if catalog := overrides.ProviderCatalog(); catalog[0].Source != model.ProviderSourceEnv {
		t.Fatalf("a refused body changed the router: %+v", catalog[0])
	}
	if _, found, err := store.GetProviderSettings(context.Background(), app.db, "kimi"); err != nil || found {
		t.Fatalf("a refused body wrote a row: found=%v err=%v", found, err)
	}

	status, body := settingsRequest(t, server.URL, http.MethodPut, adminProviderTestPath("kimi"), bearer, valid, "text/plain")
	if status != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain status=%d body=%s, want 415", status, body)
	}
	status, body = putSettings(t, server.URL, adminProviderTestPath("kimi")+"?x=1", bearer, valid)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "invalid_query") {
		t.Fatalf("query parameter status=%d body=%s, want 400", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, adminProviderTestPath("kimi"), bearer, valid, "application/json")
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d body=%s, want 405", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodGet, "/api/admin/providers/kimi", bearer, "", "")
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("GET item status=%d body=%s, want 405", status, body)
	}
	status, body = putSettings(t, server.URL, adminProviderTestPath("Kimi"), bearer, valid)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "invalid_provider") {
		t.Fatalf("invalid provider name status=%d body=%s, want 400 invalid_provider", status, body)
	}
}

func TestAdminProvidersRequireAdminAndAreAudited(t *testing.T) {
	app, server, bearer, otherBearer, _ := adminProvidersFixture(t)
	valid := `{"models":["m"],"default_model":"m","enabled":false}`
	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/admin/providers", ""},
		{http.MethodPut, adminProviderTestPath("kimi"), valid},
		{http.MethodDelete, adminProviderTestPath("kimi"), ""},
	} {
		var status int
		var body []byte
		if request.method == http.MethodDelete {
			status, body = settingsRequest(t, server.URL, request.method, request.path, otherBearer, "", "")
		} else if request.method == http.MethodPut {
			status, body = putSettings(t, server.URL, request.path, otherBearer, request.body)
		} else {
			status, body, _ = apiRequest(t, server, request.method, request.path, otherBearer)
		}
		if status != http.StatusForbidden || !strings.Contains(string(body), "admin_required") {
			t.Fatalf("non-admin %s %s status=%d body=%s, want 403 admin_required", request.method, request.path, status, body)
		}
	}
	var refused int
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM events WHERE kind = 'miniapp_mutation'`).Scan(&refused); err != nil || refused != 0 {
		t.Fatalf("refused admin requests were audited: count=%d err=%v", refused, err)
	}
	if _, found, err := store.GetProviderSettings(context.Background(), app.db, "kimi"); err != nil || found {
		t.Fatalf("a refused PUT wrote a row: found=%v err=%v", found, err)
	}

	if status, body := putSettings(t, server.URL, adminProviderTestPath("kimi"), bearer, valid); status != http.StatusOK {
		t.Fatalf("admin PUT status=%d body=%s", status, body)
	}
	if status, body := settingsRequest(t, server.URL, http.MethodDelete, adminProviderTestPath("kimi"), bearer, "", ""); status != http.StatusOK {
		t.Fatalf("admin DELETE status=%d body=%s", status, body)
	}
	rows, err := app.db.QueryContext(context.Background(),
		`SELECT detail FROM events WHERE kind = 'miniapp_mutation' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var detail string
		if err := rows.Scan(&detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	joined := strings.Join(details, "\n")
	for _, want := range []string{"operation=admin.provider.update", "operation=admin.provider.reset", "provider=kimi"} {
		if !strings.Contains(joined, want) {
			t.Errorf("audit log misses %q: %s", want, joined)
		}
	}
	if len(details) != 2 {
		t.Fatalf("audit rows=%d, want exactly one per successful mutation: %s", len(details), joined)
	}
}

func TestSettingsForNonAdminDoesNotExposeProviderInternals(t *testing.T) {
	_, server, _, otherBearer, _ := adminProvidersFixture(t)
	status, body := settingsRequest(t, server.URL, http.MethodGet, "/api/settings", otherBearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET settings status=%d body=%s", status, body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"providers", "base_url", "api_key", "configured_models"} {
		if _, present := decoded[forbidden]; present {
			t.Errorf("/api/settings exposes %q to a non-admin: %s", forbidden, body)
		}
	}
	if decoded["is_admin"] != false {
		t.Fatalf("is_admin=%v, want false", decoded["is_admin"])
	}
}
