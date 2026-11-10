package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"bot/internal/store"
)

func TestSettingsAcceptsProviderPrefixedModelIDs(t *testing.T) {
	var app, server = newTestAppWithRaw(t, map[string]string{
		"MODEL_PROVIDERS": `[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key":"sk-test","models":["kimi-k3.1-pro","kimi-k2.6"],"default_model":"kimi-k3.1-pro"}]`,
	})
	var bearer = launchSession(t, app, server)
	var client = app.model.(*fakeModelClient)
	client.mu.Lock()
	client.models = []string{app.cfg.StrataModel, "kimi:kimi-k3.1-pro", "kimi:kimi-k2.6"}
	client.mu.Unlock()

	var status, body = settingsRequest(t, server.URL, http.MethodGet, "/api/settings", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET settings status=%d body=%s", status, body)
	}
	var decoded settingsResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	var available = map[string]bool{}
	for _, name := range decoded.Model.Available {
		available[name] = true
	}
	for _, want := range []string{"kimi:kimi-k3.1-pro", "kimi:kimi-k2.6"} {
		if !available[want] {
			t.Fatalf("GET settings must list %q, got %v", want, decoded.Model.Available)
		}
	}

	status, body = putSettings(t, server.URL, "/api/settings/model", bearer, `{"model":"kimi:kimi-k2.6"}`)
	if status != http.StatusOK {
		t.Fatalf("provider-prefixed selection status=%d body=%s", status, body)
	}
	var selected, found, err = store.ChatModel(context.Background(), app.db, testChatID)
	if err != nil || !found || selected != "kimi:kimi-k2.6" {
		t.Fatalf("ChatModel=%q found=%v err=%v, want the provider-prefixed id", selected, found, err)
	}

	for _, refused := range []string{"kimi:kimi-k9", "nope:kimi-k2.6", "kimi:"} {
		status, body = putSettings(t, server.URL, "/api/settings/model", bearer, `{"model":"`+refused+`"}`)
		if status != http.StatusBadRequest {
			t.Fatalf("selection %q status=%d body=%s, want 400", refused, status, body)
		}
		selected, _, _ = store.ChatModel(context.Background(), app.db, testChatID)
		if selected != "kimi:kimi-k2.6" {
			t.Fatalf("refused selection %q changed the store to %q", refused, selected)
		}
	}

	status, body = settingsRequest(t, server.URL, http.MethodDelete, "/api/settings/model", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("reset provider selection status=%d body=%s", status, body)
	}
	if _, found, err = store.ChatModel(context.Background(), app.db, testChatID); err != nil || found {
		t.Fatalf("reset must clear the provider selection, found=%v err=%v", found, err)
	}
}
