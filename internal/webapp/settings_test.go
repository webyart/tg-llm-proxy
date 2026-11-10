package webapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bot/internal/bot"
	"bot/internal/store"
	"bot/internal/testutil"
)

func settingsRequest(t *testing.T, serverURL string, method string, path string, bearer string, body string, contentType string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, serverURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, responseBody
}

func putSettings(t *testing.T, serverURL string, path string, bearer string, body string) (int, []byte) {
	t.Helper()
	return settingsRequest(t, serverURL, http.MethodPut, path, bearer, body, "application/json")
}

func TestSettingsReadAndModelMutations(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	status, body := settingsRequest(t, server.URL, http.MethodGet, "/api/settings", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET settings status=%d body=%s", status, body)
	}
	var decoded settingsResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Model.Current != app.cfg.StrataModel || decoded.Model.Default != app.cfg.StrataModel || decoded.Model.Source != "global" || len(decoded.Model.Available) != 3 {
		t.Fatalf("model settings=%+v", decoded.Model)
	}
	if decoded.Persona.Current != "default" || decoded.Persona.Custom || decoded.Persona.Source != "global" || len(decoded.Persona.Presets) != 4 {
		t.Fatalf("persona settings=%+v", decoded.Persona)
	}
	if decoded.Stream.Enabled || decoded.Stream.Source != "global" {
		t.Fatalf("stream settings=%+v", decoded.Stream)
	}

	status, body = putSettings(t, server.URL, "/api/settings/model", bearer, `{"model":"qwen-alt"}`)
	if status != http.StatusOK {
		t.Fatalf("select model status=%d body=%s", status, body)
	}
	selected, found, err := store.ChatModel(context.Background(), app.db, testChatID)
	if err != nil || !found || selected != "qwen-alt" {
		t.Fatalf("ChatModel=%q found=%v err=%v", selected, found, err)
	}
	status, _ = putSettings(t, server.URL, "/api/settings/model", bearer, `{"model":"not-in-current-list"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown model status=%d", status)
	}
	selected, _, _ = store.ChatModel(context.Background(), app.db, testChatID)
	if selected != "qwen-alt" {
		t.Fatalf("rejected model changed store to %q", selected)
	}
	client := app.model.(*fakeModelClient)
	client.mu.Lock()
	client.models = []string{app.cfg.StrataModel}
	client.mu.Unlock()
	status, body = putSettings(t, server.URL, "/api/settings/model", bearer, `{"model":"qwen-alt"}`)
	if status != http.StatusOK {
		t.Fatalf("fresh cache was not used for model validation: status=%d body=%s", status, body)
	}
	app.modelCache.mu.Lock()
	app.modelCache.fetchedAt = time.Now().Add(-settingsModelCacheTTL)
	app.modelCache.mu.Unlock()
	status, _ = putSettings(t, server.URL, "/api/settings/model", bearer, `{"model":"qwen-alt"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("stale provider model status=%d", status)
	}
	status, body = settingsRequest(t, server.URL, http.MethodDelete, "/api/settings/model", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("reset model status=%d body=%s", status, body)
	}
	if _, found, err := store.ChatModel(context.Background(), app.db, testChatID); err != nil || found {
		t.Fatalf("model reset found=%v err=%v", found, err)
	}
	client.mu.Lock()
	completions := client.completeCalls
	client.mu.Unlock()
	if completions != 0 {
		t.Fatalf("settings made %d completion calls", completions)
	}
}

func TestSettingsPersonaPresetCustomValidationAndReset(t *testing.T) {
	app, server := newTestApp(t)
	var logs bytes.Buffer
	app.logger = slog.New(slog.NewTextHandler(&logs, nil))
	bearer := launchSession(t, app, server)

	status, body := putSettings(t, server.URL, "/api/settings/persona", bearer, `{"preset":"coder"}`)
	if status != http.StatusOK {
		t.Fatalf("preset status=%d body=%s", status, body)
	}
	prompt, found, err := store.ChatPersona(context.Background(), app.db, testChatID)
	if err != nil || !found {
		t.Fatalf("ChatPersona found=%v err=%v", found, err)
	}
	coderPrompt := ""
	for _, preset := range bot.PersonaPresetDefinitions() {
		if preset.Name == "coder" {
			coderPrompt = preset.Prompt
		}
	}
	if prompt != coderPrompt {
		t.Fatal("preset does not reuse the Telegram command prompt")
	}

	privatePersona := "<b>private persona instructions must not be logged</b>"
	status, body = putSettings(t, server.URL, "/api/settings/persona", bearer, `{"custom":`+strconvQuote(privatePersona)+`}`)
	if status != http.StatusOK {
		t.Fatalf("custom status=%d body=%s", status, body)
	}
	prompt, _, _ = store.ChatPersona(context.Background(), app.db, testChatID)
	if prompt != privatePersona {
		t.Fatal("custom persona was not stored exactly")
	}
	status, body = settingsRequest(t, server.URL, http.MethodGet, "/api/settings", bearer, "", "")
	if status != http.StatusOK || strings.Contains(string(body), privatePersona) || !strings.Contains(string(body), `"current":"custom"`) {
		t.Fatalf("custom settings response status=%d body=%s", status, body)
	}
	for _, invalid := range []string{
		`{"custom":""}`, `{"custom":"   "}`, `{"preset":"unknown"}`,
		`{"preset":"coder","custom":"two"}`, `{}`, `{"custom":"x","unknown":true}`,
	} {
		status, _ = putSettings(t, server.URL, "/api/settings/persona", bearer, invalid)
		if status != http.StatusBadRequest {
			t.Errorf("invalid persona %s status=%d", invalid, status)
		}
	}
	status, _ = putSettings(t, server.URL, "/api/settings/persona", bearer, `{"custom":`+strconvQuote(strings.Repeat("x", store.MaxPersonaPromptRunes+1))+`}`)
	if status != http.StatusBadRequest {
		t.Fatalf("4001-rune persona status=%d", status)
	}
	status, _ = putSettings(t, server.URL, "/api/settings/persona", bearer, `{"custom":"bad\u0000text"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("NUL persona status=%d", status)
	}

	exactLimit := strings.Repeat("я", store.MaxPersonaPromptRunes)
	status, body = putSettings(t, server.URL, "/api/settings/persona", bearer, `{"custom":`+strconvQuote(exactLimit)+`}`)
	if status != http.StatusOK {
		t.Fatalf("4000-rune persona status=%d body=%s", status, body)
	}
	prompt, _, _ = store.ChatPersona(context.Background(), app.db, testChatID)
	if len([]rune(prompt)) != store.MaxPersonaPromptRunes {
		t.Fatalf("stored persona runes=%d", len([]rune(prompt)))
	}
	status, body = settingsRequest(t, server.URL, http.MethodDelete, "/api/settings/persona", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("persona reset status=%d body=%s", status, body)
	}
	if _, found, err := store.ChatPersona(context.Background(), app.db, testChatID); err != nil || found {
		t.Fatalf("persona reset found=%v err=%v", found, err)
	}
	if strings.Contains(logs.String(), privatePersona) || strings.Contains(logs.String(), exactLimit) || strings.Contains(logs.String(), app.cfg.ModelsURL) {
		t.Fatalf("sensitive settings data reached logs: %s", logs.String())
	}
}

func TestSettingsStreamOnOffReset(t *testing.T) {
	app, server := newTestApp(t)
	app.cfg.Streaming = true
	bearer := launchSession(t, app, server)
	for _, test := range []struct {
		body    string
		enabled bool
	}{
		{`{"enabled":false}`, false},
		{`{"enabled":true}`, true},
	} {
		status, body := putSettings(t, server.URL, "/api/settings/stream", bearer, test.body)
		if status != http.StatusOK {
			t.Fatalf("stream set status=%d body=%s", status, body)
		}
		enabled, found, err := store.ChatStreaming(context.Background(), app.db, testChatID)
		if err != nil || !found || enabled != test.enabled {
			t.Fatalf("ChatStreaming=%v found=%v err=%v", enabled, found, err)
		}
	}
	status, body := settingsRequest(t, server.URL, http.MethodDelete, "/api/settings/stream", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("stream reset status=%d body=%s", status, body)
	}
	if _, found, err := store.ChatStreaming(context.Background(), app.db, testChatID); err != nil || found {
		t.Fatalf("stream reset found=%v err=%v", found, err)
	}
	_, body = settingsRequest(t, server.URL, http.MethodGet, "/api/settings", bearer, "", "")
	var decoded settingsResponse
	if err := json.Unmarshal(body, &decoded); err != nil || !decoded.Stream.Enabled || decoded.Stream.Source != "global" {
		t.Fatalf("effective global stream=%+v err=%v", decoded.Stream, err)
	}
}

func TestSettingsAuthenticationExpiryAndAllowlistRemoval(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	status, _ := putSettings(t, server.URL, "/api/settings/stream", "", `{"enabled":true}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("missing session status=%d", status)
	}
	now := app.auth.now()
	app.auth.now = func() time.Time { return now.Add(defaultSessionTTL + time.Second) }
	status, _ = putSettings(t, server.URL, "/api/settings/stream", bearer, `{"enabled":true}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("expired session status=%d", status)
	}
	if _, found, _ := store.ChatStreaming(context.Background(), app.db, testChatID); found {
		t.Fatal("unauthorized request changed stream settings")
	}

	app, server = newTestApp(t)
	allowed := true
	app.auth.authorize = func(int64) bool { return allowed }
	bearer = launchSession(t, app, server)
	allowed = false
	status, _ = putSettings(t, server.URL, "/api/settings/model", bearer, `{"model":"qwen-alt"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("removed allowlist status=%d", status)
	}
	if _, found, _ := store.ChatModel(context.Background(), app.db, testChatID); found {
		t.Fatal("removed allowlist request changed model settings")
	}
}

func TestSettingsWrongSignedUserCannotUseTicket(t *testing.T) {
	app, _ := newTestApp(t)
	app.auth.authorize = func(userID int64) bool { return userID == testUserID || userID == 9999 }
	ticket, err := app.NewLaunchTicket(testChatID, testUserID)
	if err != nil {
		t.Fatal(err)
	}
	raw := signedInitData(t, app.cfg.BotToken, 9999, app.auth.now())
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(raw))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Launch-Ticket", ticket)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong signed user status=%d", response.Code)
	}
	if _, found, _ := store.ChatStreaming(context.Background(), app.db, testChatID); found {
		t.Fatal("wrong signed user changed settings")
	}
}

func TestSettingsStrictMethodsJSONAndBodyCap(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	for _, test := range []struct {
		method      string
		path        string
		body        string
		contentType string
		want        int
	}{
		{http.MethodPost, "/api/settings", "", "", http.StatusMethodNotAllowed},
		{http.MethodPatch, "/api/settings/model", `{}`, "application/json", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/settings/persona", `{}`, "application/json", http.StatusMethodNotAllowed},
		{http.MethodPut, "/api/settings/model", `{"model":"qwen-alt","admin":true}`, "application/json", http.StatusBadRequest},
		{http.MethodPut, "/api/settings/model", `{"model":"qwen-alt"} {}`, "application/json", http.StatusBadRequest},
		{http.MethodPut, "/api/settings/stream", `{"enabled":true,"other":1}`, "application/json", http.StatusBadRequest},
		{http.MethodPut, "/api/settings/model", `{"model":"qwen-alt"}`, "text/plain", http.StatusUnsupportedMediaType},
		{http.MethodDelete, "/api/settings/model", `{}`, "application/json", http.StatusBadRequest},
		{http.MethodPut, "/api/documents", `{}`, "application/json", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/api/links", "", "", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/api/tasks", "", "", http.StatusMethodNotAllowed},
	} {
		status, body := settingsRequest(t, server.URL, test.method, test.path, bearer, test.body, test.contentType)
		if status != test.want {
			t.Errorf("%s %s status=%d want=%d body=%s", test.method, test.path, status, test.want, body)
		}
	}
	oversized := `{"custom":"` + strings.Repeat("x", maxBodyBytes) + `"}`
	status, _ := putSettings(t, server.URL, "/api/settings/persona", bearer, oversized)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("settings oversized body status=%d", status)
	}
}

func TestSettingsMiniAppFallbackReadAndMutations(t *testing.T) {
	app, server := newTestApp(t)
	app.cfg.MiniAppTelegramFallback = false
	bearer := launchSession(t, app, server)
	read := func() settingsResponse {
		t.Helper()
		status, body := settingsRequest(t, server.URL, http.MethodGet, "/api/settings", bearer, "", "")
		if status != http.StatusOK {
			t.Fatalf("GET settings status=%d body=%s", status, body)
		}
		var decoded settingsResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}

	if got := read().MiniAppFallback; got.Enabled || got.Source != "global" {
		t.Fatalf("global Mini App fallback=%+v", got)
	}
	status, body := putSettings(t, server.URL, "/api/settings/miniapp-fallback", bearer, `{"enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("set Mini App fallback status=%d body=%s", status, body)
	}
	enabled, found, err := store.ChatMiniAppFallback(context.Background(), app.db, testChatID)
	if err != nil || !found || !enabled {
		t.Fatalf("ChatMiniAppFallback=%v found=%v err=%v", enabled, found, err)
	}
	if got := read().MiniAppFallback; !got.Enabled || got.Source != "chat" {
		t.Fatalf("chat Mini App fallback=%+v", got)
	}
	status, body = putSettings(t, server.URL, "/api/settings/miniapp-fallback", bearer, `{"enabled":false}`)
	if status != http.StatusOK {
		t.Fatalf("unset Mini App fallback status=%d body=%s", status, body)
	}
	if got := read().MiniAppFallback; got.Enabled || got.Source != "chat" {
		t.Fatalf("chat off Mini App fallback=%+v", got)
	}
	status, body = settingsRequest(t, server.URL, http.MethodDelete, "/api/settings/miniapp-fallback", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("reset Mini App fallback status=%d body=%s", status, body)
	}
	if _, found, err := store.ChatMiniAppFallback(context.Background(), app.db, testChatID); err != nil || found {
		t.Fatalf("Mini App fallback reset found=%v err=%v", found, err)
	}
	if got := read().MiniAppFallback; got.Enabled || got.Source != "global" {
		t.Fatalf("after reset Mini App fallback=%+v", got)
	}

	// A rejected mutation must leave the stored row untouched.
	if status, _ := putSettings(t, server.URL, "/api/settings/miniapp-fallback", bearer, `{"enabled":false}`); status != http.StatusOK {
		t.Fatalf("prime off override status=%d", status)
	}
	for _, test := range []struct {
		method      string
		path        string
		body        string
		contentType string
		want        int
	}{
		{http.MethodPost, "/api/settings/miniapp-fallback", `{}`, "application/json", http.StatusMethodNotAllowed},
		{http.MethodPut, "/api/settings/miniapp-fallback", `{"enabled":true,"other":1}`, "application/json", http.StatusBadRequest},
		{http.MethodPut, "/api/settings/miniapp-fallback", `{}`, "application/json", http.StatusBadRequest},
		{http.MethodPut, "/api/settings/miniapp-fallback", `{"enabled":"yes"}`, "application/json", http.StatusBadRequest},
		{http.MethodPut, "/api/settings/miniapp-fallback", `{"enabled":true}`, "text/plain", http.StatusUnsupportedMediaType},
		{http.MethodPut, "/api/settings/miniapp-fallback?enabled=true", `{"enabled":true}`, "application/json", http.StatusBadRequest},
		{http.MethodDelete, "/api/settings/miniapp-fallback?all=1", "", "", http.StatusBadRequest},
		{http.MethodDelete, "/api/settings/miniapp-fallback", `{}`, "application/json", http.StatusBadRequest},
	} {
		status, body := settingsRequest(t, server.URL, test.method, test.path, bearer, test.body, test.contentType)
		if status != test.want {
			t.Errorf("%s %s status=%d want=%d body=%s", test.method, test.path, status, test.want, body)
		}
	}
	if enabled, found, err := store.ChatMiniAppFallback(context.Background(), app.db, testChatID); err != nil || !found || enabled {
		t.Fatalf("rejected mutations changed the override to %v found=%v err=%v", enabled, found, err)
	}

	status, _ = putSettings(t, server.URL, "/api/settings/miniapp-fallback", "", `{"enabled":true}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("missing session status=%d", status)
	}
	now := app.auth.now()
	app.auth.now = func() time.Time { return now.Add(defaultSessionTTL + time.Second) }
	status, _ = putSettings(t, server.URL, "/api/settings/miniapp-fallback", bearer, `{"enabled":true}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("expired session status=%d", status)
	}
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestSettingsDegradedModelListKeepsOtherSettingsAndReset(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	status, body := putSettings(t, server.URL, "/api/settings/model", bearer, `{"model":"qwen-alt"}`)
	if status != http.StatusOK {
		t.Fatalf("prime model selection status=%d body=%s", status, body)
	}
	client := app.model.(*fakeModelClient)
	client.mu.Lock()
	client.modelsErr = context.DeadlineExceeded
	client.mu.Unlock()
	app.modelCache.mu.Lock()
	app.modelCache.fetchedAt = time.Now().Add(-settingsModelCacheTTL)
	app.modelCache.mu.Unlock()

	status, body = settingsRequest(t, server.URL, http.MethodGet, "/api/settings", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("degraded settings status=%d body=%s", status, body)
	}
	var decoded settingsResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Warning != "models_unavailable" || len(decoded.Model.Available) != 3 || decoded.Model.Current != "qwen-alt" {
		t.Fatalf("degraded model settings=%+v warning=%q", decoded.Model, decoded.Warning)
	}
	status, body = putSettings(t, server.URL, "/api/settings/persona", bearer, `{"preset":"coder"}`)
	if status != http.StatusOK {
		t.Fatalf("degraded persona update status=%d body=%s", status, body)
	}
	status, body = putSettings(t, server.URL, "/api/settings/stream", bearer, `{"enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("degraded stream update status=%d body=%s", status, body)
	}
	status, body = putSettings(t, server.URL, "/api/settings/model", bearer, `{"model":"`+app.cfg.StrataModel+`"}`)
	if status != http.StatusBadGateway || !strings.Contains(string(body), `"code":"models_unavailable"`) {
		t.Fatalf("degraded model mutation status=%d body=%s", status, body)
	}
	selected, found, err := store.ChatModel(context.Background(), app.db, testChatID)
	if err != nil || !found || selected != "qwen-alt" {
		t.Fatalf("failed-closed mutation changed model=%q found=%v err=%v", selected, found, err)
	}
	status, body = settingsRequest(t, server.URL, http.MethodDelete, "/api/settings/model", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("degraded model reset status=%d body=%s", status, body)
	}
	if _, found, err := store.ChatModel(context.Background(), app.db, testChatID); err != nil || found {
		t.Fatalf("degraded model reset found=%v err=%v", found, err)
	}
	status, body = settingsRequest(t, server.URL, http.MethodGet, "/api/settings", bearer, "", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"current":"coder"`) || !strings.Contains(string(body), `"enabled":true`) {
		t.Fatalf("other settings did not survive degradation: status=%d body=%s", status, body)
	}
}

func TestSettingsHangingProviderHasBoundedDeadline(t *testing.T) {
	if settingsModelProviderTimeout >= 10*time.Second {
		t.Fatalf("provider deadline %s must be below the HTTP write timeout", settingsModelProviderTimeout)
	}
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	client := app.model.(*fakeModelClient)
	deadlineRemaining := make(chan time.Duration, 1)
	client.modelsFunc = func(ctx context.Context) ([]string, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			deadlineRemaining <- 0
		} else {
			deadlineRemaining <- time.Until(deadline)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	app.modelCache.providerTimeout = 50 * time.Millisecond
	started := time.Now()
	status, body := settingsRequest(t, server.URL, http.MethodGet, "/api/settings", bearer, "", "")
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("hanging provider held settings for %s", elapsed)
	}
	if status != http.StatusOK || !strings.Contains(string(body), `"warning":"models_unavailable"`) || !strings.Contains(string(body), `"available":[]`) {
		t.Fatalf("hanging provider response status=%d body=%s", status, body)
	}
	select {
	case remaining := <-deadlineRemaining:
		if remaining <= 0 || remaining > 60*time.Millisecond {
			t.Fatalf("provider deadline remaining=%s", remaining)
		}
	default:
		t.Fatal("provider did not receive a deadline")
	}
}

func TestSettingsConcurrentModelRefreshIsSingleFlight(t *testing.T) {
	app, _ := newTestApp(t)
	client := app.model.(*fakeModelClient)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	client.modelsFunc = func(ctx context.Context) ([]string, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return []string{app.cfg.StrataModel, "qwen-alt", "qwen-other"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	app.modelCache.providerTimeout = time.Second
	const callers = 6
	start := make(chan struct{})
	type result struct {
		status int
		body   string
	}
	results := make(chan result, callers)
	for index := 0; index < callers; index++ {
		go func() {
			<-start
			req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
			response := httptest.NewRecorder()
			app.settings(response, req, scope{ChatID: testChatID, UserID: testUserID})
			results <- result{status: response.Code, body: response.Body.String()}
		}()
	}
	close(start)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("provider refresh did not start")
	}
	// Watch the flight instead of sampling it once: no caller may start a second provider call while the
	// first one is still open.
	testutil.Consistently(t, 25*time.Millisecond, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.modelsCalls != 1
	}, func() string {
		client.mu.Lock()
		defer client.mu.Unlock()
		return fmt.Sprintf("single-flight refresh made %d provider calls before release", client.modelsCalls)
	})
	close(release)
	for index := 0; index < callers; index++ {
		select {
		case result := <-results:
			if result.status != http.StatusOK || strings.Contains(result.body, `"warning"`) {
				t.Fatalf("concurrent settings response status=%d body=%s", result.status, result.body)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent settings request did not finish")
		}
	}
	client.mu.Lock()
	calls := client.modelsCalls
	client.mu.Unlock()
	if calls != 1 {
		t.Fatalf("single-flight refresh made %d provider calls", calls)
	}
}
