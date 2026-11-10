package webapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
)

// A private Telegram chat is the only chat a Mini App session may belong to, and in a private chat the chat
// id IS the user id. The tests therefore use one id for both; otherTestUserID is a second private chat, used
// to prove that one session cannot read the data of another.
const testUserID = int64(1717)

const testChatID = testUserID

const otherTestUserID = testUserID + 1

type fakeModelClient struct {
	mu            sync.Mutex
	models        []string
	modelsErr     error
	modelsFunc    func(context.Context) ([]string, error)
	modelsCalls   int
	completeCalls int
}

func (f *fakeModelClient) Complete(context.Context, model.Request) (model.Response, error) {
	f.mu.Lock()
	f.completeCalls++
	f.mu.Unlock()
	return model.Response{}, errors.New("Mini App tests must not call completions")
}

func (f *fakeModelClient) Models(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	f.modelsCalls++
	modelsFunc := f.modelsFunc
	models := append([]string{}, f.models...)
	modelsErr := f.modelsErr
	f.mu.Unlock()
	if modelsFunc != nil {
		return modelsFunc(ctx)
	}
	return models, modelsErr
}

func newTestApp(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	return newTestAppWithRaw(t, nil)
}

// newTestAppWithRaw is newTestApp with extra config rows. The document upload tests use it to put the request
// cap (MEDIA_MAX_MB) at its minimum instead of building an 11 MiB body.
func newTestAppWithRaw(t *testing.T, extra map[string]string) (*App, *httptest.Server) {
	t.Helper()
	var raw = map[string]string{}
	raw["TELEGRAM_BOT_TOKEN"] = "123:test"
	raw["TELEGRAM_ALLOWED_USERS"] = strconv.FormatInt(testUserID, 10) + "," + strconv.FormatInt(otherTestUserID, 10)
	raw["MINIAPP_ENABLED"] = "true"
	raw["MINIAPP_PUBLIC_URL"] = "https://example.test/app"
	for name, value := range extra {
		raw[name] = value
	}
	cfg, problems := config.LoadFrom(raw)
	if len(problems) != 0 {
		t.Fatalf("config: %v", problems)
	}
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "miniapp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	client := &fakeModelClient{models: []string{cfg.StrataModel, "qwen-alt", "qwen-other"}}
	app := New(cfg, db, client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	return app, server
}

func launchSession(t *testing.T, app *App, server *httptest.Server) string {
	t.Helper()
	return launchSessionFor(t, app, server, testChatID, testUserID)
}

func launchSessionFor(t *testing.T, app *App, server *httptest.Server, chatID int64, userID int64) string {
	t.Helper()
	ticket, err := app.NewLaunchTicket(chatID, userID)
	if err != nil {
		t.Fatal(err)
	}
	raw := signedInitData(t, app.cfg.BotToken, userID, app.auth.now())
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(raw))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Launch-Ticket", ticket)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("exchange status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Token == "" {
		t.Fatalf("exchange response=%q err=%v", response.Body.String(), err)
	}
	return body.Token
}

func apiRequest(t *testing.T, server *httptest.Server, method string, path string, bearer string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body, response.Header
}

func decodePage(t *testing.T, body []byte) ([]map[string]any, *int64) {
	t.Helper()
	var decoded struct {
		Items []map[string]any `json:"items"`
		Next  *int64           `json:"next_before_id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("page JSON=%s err=%v", body, err)
	}
	return decoded.Items, decoded.Next
}

func seedChatData(t *testing.T, app *App) {
	t.Helper()
	ctx := context.Background()
	for index := 0; index < 12; index++ {
		content := fmt.Sprintf("chat message %d", index)
		if index == 11 {
			content = "<script>alert('not html')</script> " + strings.Repeat("x", maxHistoryRunes+100)
		}
		if _, err := store.Append(ctx, app.db, store.Message{ChatID: testChatID, UserID: testUserID, Role: store.RoleUser, Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Append(ctx, app.db, store.Message{ChatID: 999, UserID: testUserID, Role: store.RoleUser, Content: "other-chat-secret"}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 12; index++ {
		name := fmt.Sprintf("document-%02d", index)
		if _, _, err := store.PutRAGDocument(ctx, app.db, testChatID, testUserID, name, "private document body"); err != nil {
			t.Fatal(err)
		}
		link := store.SavedLink{ChatID: testChatID, UserID: testUserID, Title: fmt.Sprintf("link-%02d", index),
			CanonicalURL: fmt.Sprintf("https://example.test/%d", index), Content: strings.Repeat("summary ", 200)}
		if _, _, err := store.PutSavedLink(ctx, app.db, link); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 12; index++ {
		_, err := app.db.ExecContext(ctx, `INSERT INTO tasks
			(chat_id, user_id, goal, model, plan_json, results_json, next_step, status, attempts, due_at, expires_at, created_at, updated_at, error)
			VALUES (?, ?, ?, 'test-model', '[{"tool":"private-tool-arguments"}]', '["private-result"]', 0, 'completed', 1,
			'2026-01-01 00:00:00', '2026-01-02 00:00:00', '2026-01-01 00:00:00', '2026-01-01 00:00:00', 'private-error')`,
			testChatID, testUserID, fmt.Sprintf("task-%02d", index))
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadOnlyAPIPaginationScopeAndContentBounds(t *testing.T) {
	app, server := newTestApp(t)
	seedChatData(t, app)
	bearer := launchSession(t, app, server)

	for _, endpoint := range []string{"history", "documents", "links", "tasks"} {
		status, body, _ := apiRequest(t, server, http.MethodGet, "/api/"+endpoint+"?limit=10", bearer)
		if status != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, status, body)
		}
		items, next := decodePage(t, body)
		if len(items) != 10 || next == nil {
			t.Fatalf("%s first page len=%d next=%v", endpoint, len(items), next)
		}
		joined := string(body)
		for _, forbidden := range []string{"other-chat-secret", "private document body", "private-tool-arguments", "private-result", "private-error", "claim_token", "continuation_json", "plan_json"} {
			if strings.Contains(joined, forbidden) {
				t.Errorf("%s leaked %q: %s", endpoint, forbidden, joined)
			}
		}
		status, body, _ = apiRequest(t, server, http.MethodGet, fmt.Sprintf("/api/%s?limit=10&before=%d", endpoint, *next), bearer)
		if status != http.StatusOK {
			t.Fatalf("%s second page status=%d", endpoint, status)
		}
		items, next = decodePage(t, body)
		if len(items) != 2 || next != nil {
			t.Fatalf("%s second page len=%d next=%v", endpoint, len(items), next)
		}
	}

	_, historyBody, _ := apiRequest(t, server, http.MethodGet, "/api/history?limit=1", bearer)
	var history struct {
		Items []historyItem `json:"items"`
	}
	if err := json.Unmarshal(historyBody, &history); err != nil || len(history.Items) != 1 {
		t.Fatalf("history decode=%s err=%v", historyBody, err)
	}
	if !history.Items[0].Truncated || len([]rune(history.Items[0].Content)) > maxHistoryRunes+1 {
		t.Fatalf("history content was not bounded: runes=%d truncated=%v", len([]rune(history.Items[0].Content)), history.Items[0].Truncated)
	}
	_, linksBody, _ := apiRequest(t, server, http.MethodGet, "/api/links?limit=1", bearer)
	var links struct {
		Items []linkItem `json:"items"`
	}
	if err := json.Unmarshal(linksBody, &links); err != nil || len(links.Items) != 1 || !links.Items[0].Truncated || len([]rune(links.Items[0].Content)) > maxLinkRunes+1 {
		t.Fatalf("link content was not bounded: %s err=%v", linksBody, err)
	}

	status, statusBody, _ := apiRequest(t, server, http.MethodGet, "/api/status", bearer)
	if status != http.StatusOK || !strings.Contains(string(statusBody), `"messages":12`) || !strings.Contains(string(statusBody), `"documents":12`) {
		t.Fatalf("status=%d body=%s", status, statusBody)
	}
}

func TestEndpointIsolationAndSessionRevocation(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/status", http.StatusOK},
		{http.MethodGet, "/api/events", http.StatusNotFound},
		{http.MethodGet, "/api/admin", http.StatusNotFound},
		{http.MethodPost, "/api/history", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/api/tasks", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/history?chat_id=999", http.StatusBadRequest},
		{http.MethodGet, "/api/history?before=0", http.StatusBadRequest},
		{http.MethodGet, "/api/history?limit=21", http.StatusBadRequest},
	} {
		status, body, _ := apiRequest(t, server, test.method, test.path, bearer)
		if status != test.want {
			t.Errorf("%s %s status=%d want=%d body=%s", test.method, test.path, status, test.want, body)
		}
		var strict map[string]any
		if test.want != http.StatusOK && json.Unmarshal(body, &strict) != nil {
			t.Errorf("%s %s returned non-JSON error: %q", test.method, test.path, body)
		}
	}
	status, _, _ := apiRequest(t, server, http.MethodGet, "/api/status", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("missing bearer status=%d", status)
	}
	status, _, _ = apiRequest(t, server, http.MethodDelete, "/api/session", bearer)
	if status != http.StatusOK {
		t.Fatalf("revoke status=%d", status)
	}
	status, _, _ = apiRequest(t, server, http.MethodGet, "/api/status", bearer)
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked bearer status=%d", status)
	}
}

func TestReadOnlyAPIRejectsMethodsWithAllowGet(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/status"},
		{http.MethodPut, "/api/history"},
		{http.MethodPatch, "/api/documents"},
		{http.MethodPut, "/api/usage"},
		{http.MethodDelete, "/api/settings"},
	} {
		status, body, headers := apiRequest(t, server, test.method, test.path, bearer)
		wantAllow := http.MethodGet
		if test.path == "/api/documents" {
			wantAllow = http.MethodGet + ", " + http.MethodPost
		}
		if status != http.StatusMethodNotAllowed || headers.Get("Allow") != wantAllow {
			t.Errorf("%s %s status=%d Allow=%q body=%s", test.method, test.path, status, headers.Get("Allow"), body)
		}
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || payload.Error.Code != "method_not_allowed" {
			t.Errorf("%s %s returned an invalid error body: %q err=%v", test.method, test.path, body, err)
		}
	}
}

func TestBodyCapDoesNotLogInitData(t *testing.T) {
	var logs bytes.Buffer
	app, _ := newTestApp(t)
	app.logger = slog.New(slog.NewTextHandler(&logs, nil))
	secretShapedBody := "raw-init-data-must-not-be-logged=" + strings.Repeat("x", maxBodyBytes)
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(secretShapedBody))
	req.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(logs.String(), "raw-init-data-must-not-be-logged") {
		t.Fatalf("raw initData reached logs: %s", logs.String())
	}
}

func TestGlobalInFlightCap(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	handler := limitInFlight(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}), 1, 0, 0)
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		close(done)
	}()
	<-entered
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("in-flight cap status=%d", response.Code)
	}
	close(release)
	<-done
}

func TestHealthzIsPublicMinimalJSONWithSecurityHeaders(t *testing.T) {
	app, server := newTestApp(t)
	seedChatData(t, app)

	status, body, headers := apiRequest(t, server, http.MethodGet, "/healthz", "")
	if status != http.StatusOK {
		t.Fatalf("GET /healthz status=%d body=%s", status, body)
	}
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("GET /healthz returned invalid JSON: %v", err)
	}
	if len(payload) != 1 || payload["status"] != "ok" {
		t.Fatalf("GET /healthz body=%s", body)
	}
	for _, forbidden := range []string{"123:test", "other-chat-secret", "private document body", "private-tool-arguments", "private-result", "private-error", "STRATA_API_KEY"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("GET /healthz contains %q", forbidden)
		}
	}
	if got := headers.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("GET /healthz Content-Type=%q", got)
	}
	for name, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store",
	} {
		if got := headers.Get(name); got != want {
			t.Errorf("GET /healthz %s=%q want=%q", name, got, want)
		}
	}
	if csp := headers.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("GET /healthz CSP=%q", csp)
	}

	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete} {
		status, body, headers = apiRequest(t, server, method, "/healthz", "")
		if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodGet {
			t.Errorf("%s /healthz status=%d Allow=%q body=%s", method, status, headers.Get("Allow"), body)
		}
	}
}

func TestStaticCSPAndNoExternalScriptsOrSecrets(t *testing.T) {
	_, server := newTestApp(t)
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		status, body, headers := apiRequest(t, server, http.MethodGet, path, "")
		if status != http.StatusOK {
			t.Fatalf("GET %s status=%d", path, status)
		}
		if csp := headers.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "connect-src 'self'") {
			t.Errorf("GET %s CSP=%q", path, csp)
		}
		text := string(body)
		for _, forbidden := range []string{"123:test", "https://telegram.org/js", "https://cdn.", "initDataUnsafe", "STRATA_API_KEY"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("GET %s contains %q", path, forbidden)
			}
		}
	}
}

func TestListenerAllowsLoopbackAndContainerBridge(t *testing.T) {
	app, server := newTestApp(t)
	server.Close()
	if _, err := Listen(app, "192.0.2.1:0"); err == nil {
		t.Fatal("public listener was accepted")
	}
	bridge, err := Listen(app, "0.0.0.0:0")
	if err != nil {
		t.Fatalf("container bridge was rejected: %v", err)
	}
	defer bridge.Close()
	listener, err := Listen(app, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if listener.srv.MaxHeaderBytes != maxHeaderBytes || listener.srv.ReadTimeout <= 0 || listener.srv.WriteTimeout <= 0 {
		t.Fatalf("HTTP limits are missing: %+v", listener.srv)
	}
}

func TestStatusPendingHonorsResumeSkipNewerQuestionAndReset(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	ctx := context.Background()
	questionID, err := store.Append(ctx, app.db, store.Message{ChatID: testChatID, UserID: testUserID, Role: store.RoleUser, Content: "interrupted question"})
	if err != nil {
		t.Fatal(err)
	}
	type statusState struct {
		Pending            bool `json:"pending"`
		RecoverySuppressed bool `json:"recovery_suppressed"`
	}
	readStatus := func() statusState {
		t.Helper()
		status, body, _ := apiRequest(t, server, http.MethodGet, "/api/status", bearer)
		if status != http.StatusOK {
			t.Fatalf("status code=%d body=%s", status, body)
		}
		var state statusState
		if err := json.Unmarshal(body, &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	if state := readStatus(); !state.Pending || state.RecoverySuppressed {
		t.Fatalf("new question state=%+v", state)
	}
	if err := store.SetResumeSkip(ctx, app.db, testChatID, questionID); err != nil {
		t.Fatal(err)
	}
	if state := readStatus(); !state.Pending || !state.RecoverySuppressed {
		t.Fatalf("skipped question state=%+v", state)
	}
	if _, err := store.Append(ctx, app.db, store.Message{ChatID: testChatID, UserID: testUserID, Role: store.RoleUser, Content: "newer question"}); err != nil {
		t.Fatal(err)
	}
	if state := readStatus(); !state.Pending || state.RecoverySuppressed {
		t.Fatalf("newer question inherited suppression: %+v", state)
	}
	if _, err := store.ClearChat(ctx, app.db, testChatID); err != nil {
		t.Fatal(err)
	}
	if state := readStatus(); state.Pending || state.RecoverySuppressed {
		t.Fatalf("reset exposed hidden pending state: %+v", state)
	}
}

func staticResponse(t *testing.T, app *App, path string, prefix string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if prefix != "" {
		req.Header.Set("X-Forwarded-Prefix", prefix)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", path, response.Code, response.Body.String())
	}
	return response
}

func TestIndexForwardedPrefixInjectionAndRootUnchanged(t *testing.T) {
	app, _ := newTestApp(t)
	original, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"", "/"} {
		response := staticResponse(t, app, "/", prefix)
		if !bytes.Equal(response.Body.Bytes(), original) {
			t.Errorf("prefix %q changed the direct root page", prefix)
		}
	}
	for _, test := range []struct {
		prefix string
		want   string
	}{
		{prefix: "/miniapp", want: "/miniapp"},
		{prefix: "/miniapp/", want: "/miniapp"},
		{prefix: "/telegram/miniapp/", want: "/telegram/miniapp"},
	} {
		response := staticResponse(t, app, "/", test.prefix)
		body := response.Body.String()
		for _, want := range []string{
			`<meta name="miniapp-base" content="` + test.want + `">`,
			`href="` + test.want + `/style.css"`,
			`src="` + test.want + `/app.js"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("prefix %q index misses %q", test.prefix, want)
			}
		}
		if got := response.Header().Get("Content-Length"); got != strconv.Itoa(response.Body.Len()) {
			t.Errorf("prefix %q Content-Length=%q body length=%d", test.prefix, got, response.Body.Len())
		}
	}
}

func TestIndexRejectsUnsafeForwardedPrefixes(t *testing.T) {
	app, _ := newTestApp(t)
	original, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{
		"miniapp",
		"https://evil.test/miniapp",
		"//evil.test",
		"/miniapp/../admin",
		"/miniapp/./admin",
		`/miniapp\admin`,
		"/miniapp?next=/admin",
		"/miniapp#fragment",
		`/miniapp"><script>alert(1)</script>`,
		"/miniapp%2f..",
		"/miniapp//admin",
	} {
		response := staticResponse(t, app, "/", prefix)
		if !bytes.Equal(response.Body.Bytes(), original) {
			t.Errorf("unsafe prefix %q was injected into index: %s", prefix, response.Body.String())
		}
	}
}

func TestForwardedPrefixDoesNotRewriteJavaScriptOrCSS(t *testing.T) {
	app, _ := newTestApp(t)
	for _, path := range []string{"/app.js", "/style.css"} {
		original, err := staticFiles.ReadFile("static" + path)
		if err != nil {
			t.Fatal(err)
		}
		response := staticResponse(t, app, path, "/miniapp")
		if !bytes.Equal(response.Body.Bytes(), original) {
			t.Errorf("GET %s was rewritten with a forwarded prefix", path)
		}
	}
}
