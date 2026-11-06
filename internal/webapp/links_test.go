package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"bot/internal/store"
)

func createWebappLink(t *testing.T, app *App, chatID int64, name string) store.SavedLink {
	t.Helper()
	created, replaced, err := store.PutSavedLink(context.Background(), app.db, store.SavedLink{
		ChatID: chatID, UserID: testUserID, Title: name,
		CanonicalURL: "https://example.test/" + name, Content: "saved link content",
	})
	if err != nil || replaced {
		t.Fatalf("PutSavedLink replaced=%v err=%v", replaced, err)
	}
	return created
}

func webappLinkCount(t *testing.T, app *App, chatID int64) int {
	t.Helper()
	var count int
	if err := app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM saved_links WHERE chat_id = ?`, chatID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func webappLinkExists(t *testing.T, app *App, chatID int64, linkID int64) bool {
	t.Helper()
	var found int
	err := app.db.QueryRowContext(context.Background(), `SELECT 1 FROM saved_links WHERE chat_id = ? AND id = ?`, chatID, linkID).Scan(&found)
	if err != nil {
		return false
	}
	return found == 1
}

func TestLinkDeleteIsScopedAndUsesCanonicalID(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, testChatID+1, testUserID)
	created := createWebappLink(t, app, testChatID, "current")
	other := createWebappLink(t, app, testChatID+1, "other")
	path := fmt.Sprintf("/api/links/%d/delete", created.ID)

	status, body := settingsRequest(t, server.URL, http.MethodPost, path, otherBearer, "", "")
	if status != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("cross-chat delete status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/links/%d/delete", other.ID), bearer, "", "")
	if status != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("wrong-chat delete status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, "/api/links/999999/delete", bearer, "", "")
	if status != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("missing delete status=%d body=%s", status, body)
	}
	if !webappLinkExists(t, app, testChatID, created.ID) || !webappLinkExists(t, app, testChatID+1, other.ID) {
		t.Fatal("rejected delete changed a saved link")
	}

	for _, invalidPath := range []string{
		"/api/links/01/delete", "/api/links/0/delete", "/api/links/-1/delete", "/api/links/+1/delete",
		"/api/links/1.0/delete", "/api/links/9223372036854775808/delete", "/api/links/1/extra/delete",
		"/api/links/delete", "/api/links/1/delete/", "/api/links/unknown",
	} {
		status, body = settingsRequest(t, server.URL, http.MethodPost, invalidPath, bearer, "", "")
		if status != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
			t.Errorf("non-canonical path %q status=%d body=%s", invalidPath, status, body)
		}
	}

	status, body = settingsRequest(t, server.URL, http.MethodPost, path, bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", status, body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded) != 2 || decoded["ok"] != true || decoded["id"] != float64(created.ID) {
		t.Fatalf("delete response=%s err=%v", body, err)
	}
	if webappLinkExists(t, app, testChatID, created.ID) || !webappLinkExists(t, app, testChatID+1, other.ID) {
		t.Fatal("successful delete changed the wrong links")
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, path, bearer, "", "")
	if status != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("second delete status=%d body=%s", status, body)
	}
}

func TestLinkClearDeletesCurrentChatOnlyAndAllowsEmpty(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	createWebappLink(t, app, testChatID, "one")
	createWebappLink(t, app, testChatID, "two")
	other := createWebappLink(t, app, testChatID+1, "other")

	status, body := settingsRequest(t, server.URL, http.MethodPost, "/api/links/clear", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", status, body)
	}
	var decoded struct {
		OK      bool  `json:"ok"`
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || !decoded.OK || decoded.Deleted != 2 {
		t.Fatalf("clear response=%s err=%v", body, err)
	}
	if got := webappLinkCount(t, app, testChatID); got != 0 {
		t.Fatalf("clear left %d current chat links", got)
	}
	if got := webappLinkCount(t, app, testChatID+1); got != 1 || !webappLinkExists(t, app, testChatID+1, other.ID) {
		t.Fatalf("clear changed other chat links: count=%d", got)
	}

	status, body = settingsRequest(t, server.URL, http.MethodPost, "/api/links/clear", bearer, "", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"deleted":0`) {
		t.Fatalf("empty clear status=%d body=%s", status, body)
	}
}

func TestLinkMutationsRequireStrictMethodBodyAndQuery(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	created := createWebappLink(t, app, testChatID, "current")
	paths := []string{"/api/links/clear", fmt.Sprintf("/api/links/%d/delete", created.ID)}

	for _, path := range paths {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			status, body, headers := apiRequest(t, server, method, path, bearer)
			if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodPost {
				t.Errorf("%s %s status=%d Allow=%q body=%s", method, path, status, headers.Get("Allow"), body)
			}
		}
		for _, test := range []struct {
			name string
			path string
			body string
		}{
			{"JSON body", path, `{}`},
			{"invalid body", path, `{`},
			{"whitespace body", path, " \n"},
			{"query", path + "?chat_id=4242", ""},
		} {
			status, body := settingsRequest(t, server.URL, http.MethodPost, test.path, bearer, test.body, "")
			if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"invalid_body"`) {
				t.Errorf("%s %s status=%d body=%s", test.name, path, status, body)
			}
		}
		status, body := settingsRequest(t, server.URL, http.MethodPost, path, bearer, strings.Repeat("x", maxBodyBytes+1), "")
		if status != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), `"code":"body_too_large"`) {
			t.Errorf("oversized %s status=%d body=%s", path, status, body)
		}
	}
	if got := webappLinkCount(t, app, testChatID); got != 1 {
		t.Fatalf("rejected mutations deleted links: count=%d", got)
	}
}

func TestLinkMutationsRejectWrongSessionAndRemovedAllowlist(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	created := createWebappLink(t, app, testChatID, "current")
	paths := []string{"/api/links/clear", fmt.Sprintf("/api/links/%d/delete", created.ID)}
	for _, path := range paths {
		for _, wrongBearer := range []string{"", "not-a-session"} {
			status, body := settingsRequest(t, server.URL, http.MethodPost, path, wrongBearer, "", "")
			if status != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"session_expired"`) {
				t.Errorf("wrong session %s status=%d body=%s", path, status, body)
			}
		}
	}
	if got := webappLinkCount(t, app, testChatID); got != 1 {
		t.Fatalf("wrong sessions deleted links: count=%d", got)
	}

	app, server = newTestApp(t)
	allowed := true
	app.auth.authorize = func(int64) bool { return allowed }
	bearer = launchSession(t, app, server)
	created = createWebappLink(t, app, testChatID, "current")
	allowed = false
	for _, path := range []string{"/api/links/clear", fmt.Sprintf("/api/links/%d/delete", created.ID)} {
		status, body := settingsRequest(t, server.URL, http.MethodPost, path, bearer, "", "")
		if status != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"session_expired"`) {
			t.Errorf("removed allowlist %s status=%d body=%s", path, status, body)
		}
	}
	if got := webappLinkCount(t, app, testChatID); got != 1 {
		t.Fatalf("removed allowlist requests deleted links: count=%d", got)
	}
}

func TestLinkMutationsReportStoreFailure(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	created := createWebappLink(t, app, testChatID, "current")
	if err := app.db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/links/clear", fmt.Sprintf("/api/links/%d/delete", created.ID)} {
		status, body := settingsRequest(t, server.URL, http.MethodPost, path, bearer, "", "")
		if status != http.StatusInternalServerError || !strings.Contains(string(body), `"code":"link_mutation_failed"`) {
			t.Errorf("store failure %s status=%d body=%s", path, status, body)
		}
	}
}
