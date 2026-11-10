package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"bot/internal/store"
)

// adminApp returns an app whose test user is an administrator, a non-admin session, and the shared
// server. The admin's private chat and a group are registered so the list contains both id signs.
func adminApp(t *testing.T) (*App, *httptest.Server, string, string) {
	t.Helper()
	app, server := newTestAppWithRaw(t, map[string]string{
		"TELEGRAM_ADMIN_USERS": strconv.FormatInt(testUserID, 10),
	})
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, otherTestUserID, otherTestUserID)
	ctx := context.Background()
	for _, chat := range []struct {
		id    int64
		title string
	}{{testChatID, "private chat"}, {-100123, "group chat"}} {
		if err := store.EnsureChat(ctx, app.db, chat.id, testUserID, chat.title); err != nil {
			t.Fatalf("EnsureChat(%d): %v", chat.id, err)
		}
	}
	return app, server, bearer, otherBearer
}

func decodeAdminChats(t *testing.T, body []byte) []adminChatItem {
	t.Helper()
	var response struct {
		Items []adminChatItem `json:"items"`
		Limit int             `json:"limit"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("admin chats JSON: %v body=%s", err, body)
	}
	if response.Limit != 50 {
		t.Fatalf("limit=%d, want the default 50", response.Limit)
	}
	return response.Items
}

func findAdminChat(t *testing.T, items []adminChatItem, id int64) adminChatItem {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("chat %d is missing from %+v", id, items)
	return adminChatItem{}
}

func adminLimitsPath(chatID int64) string {
	return "/api/admin/chats/" + strconv.FormatInt(chatID, 10) + "/limits"
}

func TestAdminChatsListShowsUsageTypesAndLimits(t *testing.T) {
	app, server, bearer, _ := adminApp(t)
	ctx := context.Background()
	if err := store.SetChatLimits(ctx, app.db, store.ChatLimits{
		ChatID: -100123, DailyModelLimit: 10, DailyNetworkLimit: 5, PerMinuteLimit: 2,
		DisabledTools: []string{store.ChatToolWebSearch},
	}); err != nil {
		t.Fatalf("SetChatLimits: %v", err)
	}
	for _, event := range []struct {
		chatID int64
		kind   string
		detail string
	}{
		{-100123, store.EventModelCall, "model-a"},
		{-100123, store.EventModelCall, "model-a"},
		{-100123, store.EventUsage, store.ChatToolReadURL},
		{-100123, store.EventUsage, store.ChatToolWebSearch},
		{-100123, store.EventUsage, store.ChatToolCalculator},
		{testChatID, store.EventModelCall, "model-a"},
	} {
		if err := store.LogChatEvent(ctx, app.db, event.chatID, testUserID, event.kind, event.detail); err != nil {
			t.Fatalf("LogChatEvent: %v", err)
		}
	}
	if _, err := store.Append(ctx, app.db, store.Message{ChatID: -100123, UserID: testUserID, Role: store.RoleUser, Content: "hi"}); err != nil {
		t.Fatal(err)
	}

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/admin/chats?limit=50", bearer)
	if status != http.StatusOK {
		t.Fatalf("GET admin chats status=%d body=%s", status, body)
	}
	items := decodeAdminChats(t, body)
	if len(items) != 2 {
		t.Fatalf("items=%+v, want two chats", items)
	}
	group := findAdminChat(t, items, -100123)
	if group.Title != "group chat" || group.Type != "group" || group.UserID != testUserID {
		t.Fatalf("group item=%+v", group)
	}
	if group.Today.ModelCalls != 2 || group.Today.ReadURL != 1 || group.Today.WebSearch != 1 || group.Today.Calculator != 1 {
		t.Fatalf("group today=%+v", group.Today)
	}
	if !group.HasLimits || group.Limits.DailyModelLimit != 10 || group.Limits.DailyNetworkLimit != 5 || group.Limits.PerMinuteLimit != 2 {
		t.Fatalf("group limits=%+v has=%v", group.Limits, group.HasLimits)
	}
	if len(group.Limits.DisabledTools) != 1 || group.Limits.DisabledTools[0] != store.ChatToolWebSearch {
		t.Fatalf("group disabled tools=%v", group.Limits.DisabledTools)
	}
	private := findAdminChat(t, items, testChatID)
	if private.Type != "private" || private.Today.ModelCalls != 1 || private.HasLimits {
		t.Fatalf("private item=%+v", private)
	}
	if private.Limits.DisabledTools == nil {
		t.Fatal("disabled_tools must be an empty array, not null")
	}
}

func TestAdminChatsRequiresAdminAndValidLimit(t *testing.T) {
	_, server, bearer, otherBearer := adminApp(t)
	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/admin/chats", otherBearer)
	if status != http.StatusForbidden || !strings.Contains(string(body), "admin_required") {
		t.Fatalf("non-admin status=%d body=%s, want 403 admin_required", status, body)
	}
	status, body, _ = apiRequest(t, server, http.MethodGet, adminLimitsPath(testChatID), otherBearer)
	if status != http.StatusForbidden {
		t.Fatalf("non-admin limits status=%d body=%s", status, body)
	}
	for _, query := range []string{"?limit=0", "?limit=201", "?limit=abc", "?limit=50&limit=60", "?other=1"} {
		status, body, _ = apiRequest(t, server, http.MethodGet, "/api/admin/chats"+query, bearer)
		if status != http.StatusBadRequest {
			t.Errorf("GET %s status=%d body=%s, want 400", query, status, body)
		}
	}
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/admin/chats?limit=1", bearer)
	if status != http.StatusOK {
		t.Fatalf("limit=1 status=%d body=%s", status, body)
	}
	var one struct {
		Items []adminChatItem `json:"items"`
		Limit int             `json:"limit"`
	}
	if err := json.Unmarshal(body, &one); err != nil || len(one.Items) != 1 || one.Limit != 1 {
		t.Fatalf("limit=1 response=%s err=%v", body, err)
	}
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/admin/chats/abc/limits", bearer)
	if status != http.StatusNotFound {
		t.Fatalf("malformed limits id status=%d body=%s, want 404", status, body)
	}
}

func TestAdminChatLimitsSetValidationAndReset(t *testing.T) {
	app, server, bearer, _ := adminApp(t)
	ctx := context.Background()

	valid := `{"daily_model_limit":12,"daily_network_limit":6,"per_minute_limit":3,"disabled_tools":["web_search","calculator"]}`
	status, body := putSettings(t, server.URL, adminLimitsPath(-100123), bearer, valid)
	if status != http.StatusOK {
		t.Fatalf("PUT limits status=%d body=%s", status, body)
	}
	var putResponse struct {
		OK     bool            `json:"ok"`
		ID     int64           `json:"id"`
		Limits adminChatLimits `json:"limits"`
	}
	if err := json.Unmarshal(body, &putResponse); err != nil {
		t.Fatalf("PUT limits JSON: %v body=%s", err, body)
	}
	if !putResponse.OK || putResponse.ID != -100123 || putResponse.Limits.PerMinuteLimit != 3 {
		t.Fatalf("PUT response=%+v", putResponse)
	}
	limits, found, err := store.GetChatLimits(ctx, app.db, -100123)
	if err != nil || !found {
		t.Fatalf("stored limits found=%v err=%v", found, err)
	}
	if limits.DailyModelLimit != 12 || limits.DailyNetworkLimit != 6 || limits.PerMinuteLimit != 3 ||
		len(limits.DisabledTools) != 2 {
		t.Fatalf("stored limits=%+v", limits)
	}

	for name, test := range map[string]struct {
		body   string
		status int
	}{
		"negative":         {`{"daily_model_limit":-1,"daily_network_limit":0,"per_minute_limit":0,"disabled_tools":[]}`, http.StatusBadRequest},
		"too_large":        {`{"daily_model_limit":1000001,"daily_network_limit":0,"per_minute_limit":0,"disabled_tools":[]}`, http.StatusBadRequest},
		"unknown_tool":     {`{"daily_model_limit":1,"daily_network_limit":0,"per_minute_limit":0,"disabled_tools":["shell"]}`, http.StatusBadRequest},
		"missing_field":    {`{"daily_model_limit":1,"daily_network_limit":0,"per_minute_limit":0}`, http.StatusBadRequest},
		"unknown_field":    {`{"daily_model_limit":1,"daily_network_limit":0,"per_minute_limit":0,"disabled_tools":[],"extra":1}`, http.StatusBadRequest},
		"wrong_type":       {`{"daily_model_limit":"1","daily_network_limit":0,"per_minute_limit":0,"disabled_tools":[]}`, http.StatusBadRequest},
		"not_an_object":    {`[1,2,3]`, http.StatusBadRequest},
		"trailing_content": {valid + `{}`, http.StatusBadRequest},
	} {
		status, body := putSettings(t, server.URL, adminLimitsPath(-100123), bearer, test.body)
		if status != test.status {
			t.Errorf("%s: status=%d body=%s, want %d", name, status, body, test.status)
		}
	}

	// Unknown chat: the body is valid, the target is not.
	status, body = putSettings(t, server.URL, adminLimitsPath(987654), bearer, valid)
	if status != http.StatusNotFound || !strings.Contains(string(body), "chat_not_found") {
		t.Fatalf("unknown chat status=%d body=%s, want 404 chat_not_found", status, body)
	}
	// Content type, query parameters and methods are part of the strict contract.
	status, body = settingsRequest(t, server.URL, http.MethodPut, adminLimitsPath(-100123), bearer, valid, "text/plain")
	if status != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain status=%d body=%s, want 415", status, body)
	}
	status, body = putSettings(t, server.URL, adminLimitsPath(-100123)+"?x=1", bearer, valid)
	if status != http.StatusBadRequest {
		t.Fatalf("query parameter status=%d body=%s, want 400", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, adminLimitsPath(-100123), bearer, valid, "application/json")
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d body=%s, want 405", status, body)
	}

	status, body = settingsRequest(t, server.URL, http.MethodDelete, adminLimitsPath(-100123), bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("DELETE limits status=%d body=%s", status, body)
	}
	if _, found, err := store.GetChatLimits(ctx, app.db, -100123); err != nil || found {
		t.Fatalf("limits survived the reset: found=%v err=%v", found, err)
	}
	// Reset of a chat that never had limits is still a 200 (idempotent policy), and an unknown chat 404.
	if status, body = settingsRequest(t, server.URL, http.MethodDelete, adminLimitsPath(-100123), bearer, "", ""); status != http.StatusOK {
		t.Fatalf("second DELETE status=%d body=%s", status, body)
	}
	if status, body = settingsRequest(t, server.URL, http.MethodDelete, adminLimitsPath(987654), bearer, "", ""); status != http.StatusNotFound {
		t.Fatalf("DELETE unknown chat status=%d body=%s", status, body)
	}
}

func TestAdminChatLimitsMutationsAreAuditedAndHiddenFromNonAdmins(t *testing.T) {
	app, server, bearer, otherBearer := adminApp(t)
	status, body := putSettings(t, server.URL, adminLimitsPath(testChatID), bearer,
		`{"daily_model_limit":1,"daily_network_limit":0,"per_minute_limit":0,"disabled_tools":[]}`)
	if status != http.StatusOK {
		t.Fatalf("SET status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodDelete, adminLimitsPath(testChatID), bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("RESET status=%d body=%s", status, body)
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
	for _, want := range []string{"operation=admin.limits.set", "operation=admin.limits.reset", "id=" + strconv.FormatInt(testChatID, 10)} {
		if !strings.Contains(joined, want) {
			t.Errorf("audit log misses %q: %s", want, joined)
		}
	}

	// A non-admin can neither set nor reset; nothing is written and the audit log is untouched.
	before := len(details)
	status, body = putSettings(t, server.URL, adminLimitsPath(testChatID), otherBearer,
		`{"daily_model_limit":5,"daily_network_limit":0,"per_minute_limit":0,"disabled_tools":[]}`)
	if status != http.StatusForbidden {
		t.Fatalf("non-admin SET status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodDelete, adminLimitsPath(testChatID), otherBearer, "", "")
	if status != http.StatusForbidden {
		t.Fatalf("non-admin RESET status=%d body=%s", status, body)
	}
	var after int
	if err := app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE kind = 'miniapp_mutation'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("a refused admin mutation was audited: before=%d after=%d", before, after)
	}
	if _, found, err := store.GetChatLimits(context.Background(), app.db, testChatID); err != nil || found {
		t.Fatalf("a refused mutation wrote limits: found=%v err=%v", found, err)
	}
}

func TestSettingsReportsIsAdmin(t *testing.T) {
	app, server := newTestAppWithRaw(t, map[string]string{"TELEGRAM_ADMIN_USERS": strconv.FormatInt(testUserID, 10)})
	bearer := launchSession(t, app, server)
	status, body := settingsRequest(t, server.URL, http.MethodGet, "/api/settings", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET settings status=%d body=%s", status, body)
	}
	var response struct {
		IsAdmin bool `json:"is_admin"`
	}
	if err := json.Unmarshal(body, &response); err != nil || !response.IsAdmin {
		t.Fatalf("is_admin=%v err=%v body=%s", response.IsAdmin, err, body)
	}

	plain, plainServer := newTestApp(t)
	plainBearer := launchSession(t, plain, plainServer)
	status, body = settingsRequest(t, plainServer.URL, http.MethodGet, "/api/settings", plainBearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET settings without admins status=%d body=%s", status, body)
	}
	if err := json.Unmarshal(body, &response); err != nil || response.IsAdmin {
		t.Fatalf("is_admin without TELEGRAM_ADMIN_USERS=%v err=%v body=%s", response.IsAdmin, err, body)
	}
}
