package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"bot/internal/store"
)

func TestTimezoneSettingsAPIReadWriteResetAndAudit(t *testing.T) {
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
	if decoded.Timezone.Current != app.cfg.Timezone || decoded.Timezone.Default != app.cfg.Timezone || decoded.Timezone.Source != "global" {
		t.Fatalf("default timezone settings=%+v, want %s/global", decoded.Timezone, app.cfg.Timezone)
	}
	if decoded.Timezone.Default != "Asia/Yekaterinburg" {
		t.Fatalf("DEFAULT_TIMEZONE = %q, want the deployment default Asia/Yekaterinburg", decoded.Timezone.Default)
	}

	status, body = putSettings(t, server.URL, "/api/settings/timezone", bearer, `{"timezone":"Europe/Berlin"}`)
	if status != http.StatusOK {
		t.Fatalf("PUT timezone status=%d body=%s", status, body)
	}
	timezone, found, err := store.GetChatTimezone(context.Background(), app.db, testChatID)
	if err != nil || !found || timezone != "Europe/Berlin" {
		t.Fatalf("stored timezone=%q found=%v err=%v", timezone, found, err)
	}

	status, body = settingsRequest(t, server.URL, http.MethodGet, "/api/settings", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET after PUT status=%d body=%s", status, body)
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Timezone.Current != "Europe/Berlin" || decoded.Timezone.Source != "chat" {
		t.Fatalf("chat timezone settings=%+v", decoded.Timezone)
	}

	status, body = settingsRequest(t, server.URL, http.MethodGet, "/api/status", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET status status=%d body=%s", status, body)
	}
	var statusPayload struct {
		Timezone timezoneSettings `json:"timezone"`
	}
	if err := json.Unmarshal(body, &statusPayload); err != nil {
		t.Fatal(err)
	}
	if statusPayload.Timezone.Current != "Europe/Berlin" || statusPayload.Timezone.Source != "chat" {
		t.Fatalf("status timezone=%+v", statusPayload.Timezone)
	}

	var detail string
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT detail FROM events WHERE kind = 'miniapp_mutation' ORDER BY id DESC LIMIT 1`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "operation=settings.timezone.set") || !strings.Contains(detail, "chat_id=") {
		t.Fatalf("set audit detail = %q", detail)
	}

	status, body = putSettings(t, server.URL, "/api/settings/timezone", bearer, `{"timezone":""}`)
	if status != http.StatusOK {
		t.Fatalf("reset timezone status=%d body=%s", status, body)
	}
	if timezone, found, err := store.GetChatTimezone(context.Background(), app.db, testChatID); err != nil || found || timezone != "" {
		t.Fatalf("reset left timezone=%q found=%v err=%v", timezone, found, err)
	}
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT detail FROM events WHERE kind = 'miniapp_mutation' ORDER BY id DESC LIMIT 1`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "operation=settings.timezone.set") {
		t.Fatalf("reset audit detail = %q (the reset uses PUT with an empty value, so it is one settings.timezone.set operation)", detail)
	}
}

func TestTimezoneSettingsAPIRejectsInvalidInputAndUnauthenticatedCalls(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	cases := []struct {
		name        string
		method      string
		path        string
		body        string
		contentType string
		bearer      string
		want        int
	}{
		{name: "unknown zone", method: http.MethodPut, path: "/api/settings/timezone", body: `{"timezone":"Mars/Olympus_Mons"}`, contentType: "application/json", bearer: bearer, want: http.StatusBadRequest},
		{name: "missing field", method: http.MethodPut, path: "/api/settings/timezone", body: `{}`, contentType: "application/json", bearer: bearer, want: http.StatusBadRequest},
		{name: "unknown field", method: http.MethodPut, path: "/api/settings/timezone", body: `{"timezone":"UTC","offset":5}`, contentType: "application/json", bearer: bearer, want: http.StatusBadRequest},
		{name: "trailing value", method: http.MethodPut, path: "/api/settings/timezone", body: `{"timezone":"UTC"} {}`, contentType: "application/json", bearer: bearer, want: http.StatusBadRequest},
		{name: "query", method: http.MethodPut, path: "/api/settings/timezone?timezone=UTC", body: `{"timezone":"UTC"}`, contentType: "application/json", bearer: bearer, want: http.StatusBadRequest},
		{name: "wrong content type", method: http.MethodPut, path: "/api/settings/timezone", body: `{"timezone":"UTC"}`, contentType: "text/plain", bearer: bearer, want: http.StatusUnsupportedMediaType},
		{name: "wrong method", method: http.MethodPost, path: "/api/settings/timezone", body: `{"timezone":"UTC"}`, contentType: "application/json", bearer: bearer, want: http.StatusMethodNotAllowed},
		{name: "no session", method: http.MethodGet, path: "/api/settings", body: "", contentType: "", bearer: "", want: http.StatusUnauthorized},
	}
	for _, test := range cases {
		status, body := settingsRequest(t, server.URL, test.method, test.path, test.bearer, test.body, test.contentType)
		if status != test.want {
			t.Errorf("%s: status=%d want=%d body=%s", test.name, status, test.want, body)
		}
	}
	if timezone, found, err := store.GetChatTimezone(context.Background(), app.db, testChatID); err != nil || found || timezone != "" {
		t.Fatalf("rejected writes stored timezone=%q found=%v err=%v", timezone, found, err)
	}
	count, err := store.CountEvents(context.Background(), app.db, "miniapp_mutation")
	if err != nil || count != 0 {
		t.Fatalf("rejected writes produced %d audit rows err=%v", count, err)
	}
}

func TestTimezoneWritePermissionFollowsOwnerAndAdmin(t *testing.T) {
	app, _ := newTestApp(t)
	if !app.canManageTimezone(context.Background(), testUserID, testChatID) {
		t.Fatal("the owner of a private chat cannot change its timezone")
	}
	if app.canManageTimezone(context.Background(), otherTestUserID, testChatID) {
		t.Fatal("another user was allowed to change a private chat timezone")
	}
	if app.canManageTimezone(context.Background(), testUserID, -100500) {
		t.Fatal("a non-admin was allowed to change a group timezone")
	}
	app.cfg.AdminIDs = []int64{testUserID}
	if !app.canManageTimezone(context.Background(), testUserID, -100500) {
		t.Fatal("an administrator was not allowed to change a group timezone")
	}
}
