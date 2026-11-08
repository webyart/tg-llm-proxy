package webapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bot/internal/store"
)

type fakeManagementTaskLifecycle struct {
	TaskLifecycle
	created store.Task
	err     error
	calls   int
	chatID  int64
	userID  int64
	goal    string
}

func (f *fakeManagementTaskLifecycle) CreateTask(_ context.Context, chatID int64, userID int64, goal string) (store.Task, error) {
	f.calls++
	f.chatID, f.userID, f.goal = chatID, userID, goal
	if f.err != nil {
		return store.Task{}, f.err
	}
	return f.created, nil
}

type fakeManagementLinkLifecycle struct {
	created  store.SavedLink
	replaced bool
	err      error
	calls    int
	chatID   int64
	userID   int64
	rawURL   string
}

func (f *fakeManagementLinkLifecycle) CreateLink(_ context.Context, chatID int64, userID int64, rawURL string) (store.SavedLink, bool, error) {
	f.calls++
	f.chatID, f.userID, f.rawURL = chatID, userID, rawURL
	if f.err != nil {
		return store.SavedLink{}, false, f.err
	}
	return f.created, f.replaced, nil
}

func managementRequest(t *testing.T, server *httptest.Server, method string, path string, bearer string, body string, contentType string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, responseBody, response.Header
}

func managementJSON(t *testing.T, server *httptest.Server, path string, bearer string, body string) (int, []byte) {
	t.Helper()
	status, responseBody, _ := managementRequest(t, server, http.MethodPost, path, bearer, body, "application/json")
	return status, responseBody
}

func installManagementLifecycles(app *App) (*fakeManagementTaskLifecycle, *fakeManagementLinkLifecycle) {
	task := &fakeManagementTaskLifecycle{created: store.Task{ID: 7101, Status: store.TaskStatusReady, Goal: "private lifecycle goal"}}
	link := &fakeManagementLinkLifecycle{created: store.SavedLink{
		ID: 7201, Title: "Created link", CanonicalURL: "https://example.test/created", Content: "created content", UpdatedAt: time.Now().UTC(),
	}}
	app.SetTaskLifecycle(task)
	app.SetLinkLifecycle(link)
	return task, link
}

func TestHistoryManagementDeletesOnlyVisibleCurrentChatMessages(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, otherTestUserID, otherTestUserID)
	userID := appendWebappMessage(t, app, testChatID, store.RoleUser, "private user message")
	assistantID := appendWebappMessage(t, app, testChatID, store.RoleAssistant, "private assistant message")
	systemID := appendWebappMessage(t, app, testChatID, store.RoleSystem, "private system message")
	otherID := appendWebappMessage(t, app, testChatID+1, store.RoleUser, "other chat message")

	status, body := settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/history/%d/delete", userID), otherBearer, "", "")
	if status != http.StatusNotFound {
		t.Fatalf("cross-chat delete status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/history/%d/delete", otherID), bearer, "", "")
	if status != http.StatusNotFound {
		t.Fatalf("other message delete status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/history/%d/delete", systemID), bearer, "", "")
	if status != http.StatusNotFound {
		t.Fatalf("system delete status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/history/%d/delete", assistantID), bearer, "", "")
	var deleted struct {
		OK bool  `json:"ok"`
		ID int64 `json:"id"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &deleted) != nil || !deleted.OK || deleted.ID != assistantID {
		t.Fatalf("assistant delete status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodPost, "/api/history/clear", bearer, "", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"deleted":2`) {
		t.Fatalf("history clear status=%d body=%s", status, body)
	}
	var currentCount, otherCount int
	if err := app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM messages WHERE chat_id = ?`, testChatID).Scan(&currentCount); err != nil {
		t.Fatal(err)
	}
	if err := app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM messages WHERE chat_id = ?`, testChatID+1).Scan(&otherCount); err != nil {
		t.Fatal(err)
	}
	if currentCount != 0 || otherCount != 1 {
		t.Fatalf("history isolation current=%d other=%d", currentCount, otherCount)
	}
}

func TestHistoryManagementStrictRoutesBodiesAndMethods(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	messageID := appendWebappMessage(t, app, testChatID, store.RoleUser, "preserved")
	path := fmt.Sprintf("/api/history/%d/delete", messageID)
	for _, endpoint := range []string{path, "/api/history/clear"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			status, body, headers := apiRequest(t, server, method, endpoint, bearer)
			if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodPost {
				t.Errorf("%s %s status=%d Allow=%q body=%s", method, endpoint, status, headers.Get("Allow"), body)
			}
		}
		for _, test := range []struct {
			name string
			path string
			body string
		}{
			{"JSON", endpoint, `{}`},
			{"whitespace", endpoint, " \n"},
			{"query", endpoint + "?chat_id=4242", ""},
		} {
			status, body := settingsRequest(t, server.URL, http.MethodPost, test.path, bearer, test.body, "")
			if status != http.StatusBadRequest {
				t.Errorf("%s status=%d body=%s", test.name, status, body)
				continue
			}
			requireAPIErrorCode(t, body, "invalid_body")
		}
		status, body := settingsRequest(t, server.URL, http.MethodPost, endpoint, bearer, strings.Repeat("x", maxBodyBytes+1), "")
		if status != http.StatusRequestEntityTooLarge {
			t.Errorf("oversized %s status=%d body=%s", endpoint, status, body)
		}
	}
	for _, invalid := range []string{
		"/api/history/01/delete", "/api/history/0/delete", "/api/history/-1/delete", "/api/history/+1/delete",
		"/api/history/1.0/delete", "/api/history/9223372036854775808/delete", "/api/history/1/extra/delete",
		"/api/history/delete", "/api/history/1/delete/",
	} {
		status, body := settingsRequest(t, server.URL, http.MethodPost, invalid, bearer, "", "")
		if status != http.StatusNotFound {
			t.Errorf("non-canonical %s status=%d body=%s", invalid, status, body)
		}
	}
	status, body := settingsRequest(t, server.URL, http.MethodPost, path, bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("valid delete after rejected requests status=%d body=%s", status, body)
	}
}

func TestLinkSearchMappingCapAndCreateLifecycle(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, otherTestUserID, otherTestUserID)
	now := time.Now().UTC()
	for index := 0; index < 25; index++ {
		_, _, err := store.PutSavedLink(context.Background(), app.db, store.SavedLink{
			ChatID: testChatID, UserID: testUserID, Title: fmt.Sprintf("NeEdLe %02d", index),
			CanonicalURL: fmt.Sprintf("https://example.test/%02d", index), Content: strings.Repeat("private ", maxLinkRunes),
			CreatedAt: now.Add(time.Duration(index) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := store.PutSavedLink(context.Background(), app.db, store.SavedLink{
		ChatID: testChatID + 1, UserID: testUserID, Title: "needle other-chat-secret",
		CanonicalURL: "https://example.test/other", Content: "other chat content",
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/links/search?q=nEeDlE", bearer)
	if status != http.StatusOK {
		t.Fatalf("link search status=%d body=%s", status, body)
	}
	var result struct {
		Items []linkItem `json:"items"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Items) != store.SavedLinkListLimit {
		t.Fatalf("link search items=%d body=%s err=%v", len(result.Items), body, err)
	}
	if result.Items[0].Title != "NeEdLe 24" || !result.Items[0].Truncated || len([]rune(result.Items[0].Content)) != maxLinkRunes+1 {
		t.Fatalf("mapped link=%+v content runes=%d", result.Items[0], len([]rune(result.Items[0].Content)))
	}
	if strings.Contains(string(body), "other-chat-secret") {
		t.Fatalf("link search leaked another chat: %s", body)
	}
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/links/search?q=absent", bearer)
	if status != http.StatusOK || string(body) != "{\"items\":[]}\n" {
		t.Fatalf("empty link search status=%d body=%s", status, body)
	}

	_, links := installManagementLifecycles(app)
	status, body = managementJSON(t, server, "/api/links", otherBearer, `{"url":"  https://example.test/new  "}`)
	if status != http.StatusOK {
		t.Fatalf("link create status=%d body=%s", status, body)
	}
	var created linkItem
	if err := json.Unmarshal(body, &created); err != nil || created.ID != 7201 || created.URL != "https://example.test/created" {
		t.Fatalf("created link=%+v body=%s err=%v", created, body, err)
	}
	if links.calls != 1 || links.chatID != otherTestUserID || links.userID != otherTestUserID || links.rawURL != "https://example.test/new" {
		t.Fatalf("link lifecycle calls=%d chat=%d user=%d url=%q", links.calls, links.chatID, links.userID, links.rawURL)
	}
}

func TestLinkSearchStrictQueryAndStoreFailure(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	for _, path := range []string{
		"/api/links/search", "/api/links/search?q=", "/api/links/search?q=%20%20", "/api/links/search?q=one&q=two",
		"/api/links/search?q=one&extra=1", "/api/links/search?extra=1", "/api/links/search?q=one;extra=1",
		"/api/links/search?q=" + strings.Repeat("x", store.SavedLinkSearchMaxRunes+1),
	} {
		status, body, _ := apiRequest(t, server, http.MethodGet, path, bearer)
		if status != http.StatusBadRequest {
			t.Errorf("invalid link search %q status=%d body=%s", path, status, body)
			continue
		}
		requireAPIErrorCode(t, body, "invalid_query")
	}
	status, body, headers := apiRequest(t, server, http.MethodPost, "/api/links/search?q=one", bearer)
	if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodGet {
		t.Fatalf("POST link search status=%d Allow=%q body=%s", status, headers.Get("Allow"), body)
	}
	if err := app.db.Close(); err != nil {
		t.Fatal(err)
	}
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/links/search?q=one", bearer)
	if status != http.StatusInternalServerError {
		t.Fatalf("failed link search status=%d body=%s", status, body)
	}
	requireAPIErrorCode(t, body, "read_failed")
}

func TestTaskCreateUsesLifecycleAndReturnsOnlyIDStatus(t *testing.T) {
	app, server := newTestApp(t)
	otherBearer := launchSessionFor(t, app, server, otherTestUserID, otherTestUserID)
	tasks, _ := installManagementLifecycles(app)
	status, body := managementJSON(t, server, "/api/tasks", otherBearer, `{"goal":"  private task goal  "}`)
	if status != http.StatusOK {
		t.Fatalf("task create status=%d body=%s", status, body)
	}
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil || len(response) != 2 || response["id"] != float64(7101) || response["status"] != store.TaskStatusReady {
		t.Fatalf("task create response=%s err=%v", body, err)
	}
	if strings.Contains(string(body), "private lifecycle goal") || tasks.calls != 1 || tasks.chatID != otherTestUserID || tasks.userID != otherTestUserID || tasks.goal != "private task goal" {
		t.Fatalf("task lifecycle calls=%d chat=%d user=%d goal=%q response=%s", tasks.calls, tasks.chatID, tasks.userID, tasks.goal, body)
	}
}

func TestReminderCreateOnceDailyLocationAndScope(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, otherTestUserID, otherTestUserID)
	location := time.FixedZone("UTC+5", 5*60*60)
	app.cfg.ReminderLocation = location

	nextRun := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	onceBody := fmt.Sprintf(`{"text":"  private once  ","cadence":"once","next_run":%q}`, nextRun.Format(time.RFC3339))
	status, body := managementJSON(t, server, "/api/reminders", bearer, onceBody)
	if status != http.StatusOK {
		t.Fatalf("once create status=%d body=%s", status, body)
	}
	var once reminderItem
	if err := json.Unmarshal(body, &once); err != nil || once.Text != "private once" || once.Cadence != store.ReminderCadenceOnce || once.Status != store.ReminderStatusActive {
		t.Fatalf("once response=%+v body=%s err=%v", once, body, err)
	}
	parsedOnce, err := time.Parse(time.RFC3339Nano, once.NextRun)
	if err != nil || !parsedOnce.Equal(nextRun) {
		t.Fatalf("once next_run=%q want=%q err=%v", once.NextRun, nextRun, err)
	}
	var chatID, userID int64
	if err := app.db.QueryRowContext(context.Background(), `SELECT chat_id, user_id FROM reminders WHERE id = ?`, once.ID).Scan(&chatID, &userID); err != nil {
		t.Fatal(err)
	}
	if chatID != testChatID || userID != testUserID {
		t.Fatalf("once scope chat=%d user=%d", chatID, userID)
	}

	dailyTime := time.Now().In(location).Add(2 * time.Hour).Format("15:04")
	status, body = managementJSON(t, server, "/api/reminders", otherBearer,
		fmt.Sprintf(`{"text":"daily","cadence":"daily","daily_time":%q}`, dailyTime))
	if status != http.StatusOK {
		t.Fatalf("daily create status=%d body=%s", status, body)
	}
	var daily reminderItem
	if err := json.Unmarshal(body, &daily); err != nil || daily.DailyTime != dailyTime {
		t.Fatalf("daily response=%+v body=%s err=%v", daily, body, err)
	}
	parsedDaily, err := time.Parse(time.RFC3339Nano, daily.NextRun)
	if err != nil || parsedDaily.In(location).Format("15:04") != dailyTime || !parsedDaily.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("daily next_run=%q location=%q err=%v", daily.NextRun, location, err)
	}
	if err := app.db.QueryRowContext(context.Background(), `SELECT chat_id, user_id FROM reminders WHERE id = ?`, daily.ID).Scan(&chatID, &userID); err != nil {
		t.Fatal(err)
	}
	if chatID != otherTestUserID || userID != otherTestUserID {
		t.Fatalf("daily scope chat=%d user=%d", chatID, userID)
	}

	app.cfg.ReminderLocation = nil
	utcTime := time.Now().UTC().Add(2 * time.Hour).Format("15:04")
	status, body = managementJSON(t, server, "/api/reminders", bearer,
		fmt.Sprintf(`{"text":"UTC fallback","cadence":"daily","daily_time":%q}`, utcTime))
	if status != http.StatusOK {
		t.Fatalf("UTC daily create status=%d body=%s", status, body)
	}
	var utcDaily reminderItem
	if err := json.Unmarshal(body, &utcDaily); err != nil {
		t.Fatal(err)
	}
	parsedUTC, err := time.Parse(time.RFC3339Nano, utcDaily.NextRun)
	if err != nil || parsedUTC.UTC().Format("15:04") != utcTime {
		t.Fatalf("UTC fallback next_run=%q want clock=%q err=%v", utcDaily.NextRun, utcTime, err)
	}
}

func TestCreationStrictJSONAndSemanticValidation(t *testing.T) {
	validBodies := map[string]string{
		"/api/links":     `{"url":"https://example.test"}`,
		"/api/tasks":     `{"goal":"goal"}`,
		"/api/reminders": fmt.Sprintf(`{"text":"text","cadence":"once","next_run":%q}`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339)),
	}
	for _, endpoint := range []string{"/api/links", "/api/tasks", "/api/reminders"} {
		t.Run(endpoint, func(t *testing.T) {
			app, server := newTestApp(t)
			bearer := launchSession(t, app, server)
			tasks, links := installManagementLifecycles(app)
			invalidBodies := []string{"", " \n", "{", "{}", `{"unknown":true}`, validBodies[endpoint] + ` {}`}
			if endpoint == "/api/links" {
				invalidBodies = append(invalidBodies, `{"url":""}`, `{"url":"   "}`, `{"url":"bad\u0000"}`,
					fmt.Sprintf(`{"url":%q}`, strings.Repeat("x", store.SavedLinkMaxURLBytes+1)))
			}
			if endpoint == "/api/tasks" {
				invalidBodies = append(invalidBodies, `{"goal":""}`, `{"goal":"  "}`, `{"goal":"bad\u0000"}`,
					fmt.Sprintf(`{"goal":%q}`, strings.Repeat("й", store.TaskMaxGoalRunes+1)))
			}
			for _, invalid := range invalidBodies {
				status, body := managementJSON(t, server, endpoint, bearer, invalid)
				if status != http.StatusBadRequest {
					t.Errorf("invalid body %q status=%d body=%s", invalid, status, body)
				}
			}
			status, body, _ := managementRequest(t, server, http.MethodPost, endpoint, bearer, validBodies[endpoint], "text/plain")
			if status != http.StatusUnsupportedMediaType {
				t.Errorf("content type status=%d body=%s", status, body)
			}
			status, body = managementJSON(t, server, endpoint+"?chat_id=999", bearer, validBodies[endpoint])
			if status != http.StatusBadRequest {
				t.Errorf("query status=%d body=%s", status, body)
			}
			oversized := `{"x":"` + strings.Repeat("x", maxBodyBytes+1) + `"}`
			status, body = managementJSON(t, server, endpoint, bearer, oversized)
			if status != http.StatusRequestEntityTooLarge {
				t.Errorf("oversized status=%d body=%s", status, body)
			}
			if tasks.calls != 0 || links.calls != 0 {
				t.Fatalf("rejected requests invoked lifecycles tasks=%d links=%d", tasks.calls, links.calls)
			}
			var reminders int
			if err := app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM reminders`).Scan(&reminders); err != nil || reminders != 0 {
				t.Fatalf("rejected requests created %d reminders err=%v", reminders, err)
			}
		})
	}
}

func TestReminderCreateRejectsInvalidSchedulesAndText(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	tooLate := time.Now().UTC().Add(botMaxReminderDelayForTest() + time.Hour).Format(time.RFC3339)
	longText := strings.Repeat("я", store.ReminderMaxTextRunes+1)
	invalid := []string{
		`{"cadence":"once","next_run":"` + future + `"}`,
		`{"text":" ","cadence":"once","next_run":"` + future + `"}`,
		fmt.Sprintf(`{"text":%q,"cadence":"once","next_run":%q}`, longText, future),
		`{"text":"bad\u0000","cadence":"once","next_run":"` + future + `"}`,
		`{"text":"text","cadence":"ONCE","next_run":"` + future + `"}`,
		`{"text":"text","cadence":"once"}`,
		`{"text":"text","cadence":"once","next_run":"not-a-time"}`,
		`{"text":"text","cadence":"once","next_run":"2020-01-01T00:00:00Z"}`,
		`{"text":"text","cadence":"once","next_run":"` + tooLate + `"}`,
		`{"text":"text","cadence":"once","next_run":"` + future + `","daily_time":"09:00"}`,
		`{"text":"text","cadence":"daily"}`,
		`{"text":"text","cadence":"daily","daily_time":"9:00"}`,
		`{"text":"text","cadence":"daily","daily_time":"24:00"}`,
		`{"text":"text","cadence":"daily","daily_time":"09:60"}`,
		`{"text":"text","cadence":"daily","daily_time":"09:00","next_run":"` + future + `"}`,
	}
	for _, body := range invalid {
		status, responseBody := managementJSON(t, server, "/api/reminders", bearer, body)
		if status != http.StatusBadRequest {
			t.Errorf("invalid reminder %s status=%d body=%s", body, status, responseBody)
		}
	}
	boundary := time.Now().UTC().Add(botMaxReminderDelayForTest()).Format(time.RFC3339)
	status, body := managementJSON(t, server, "/api/reminders", bearer,
		fmt.Sprintf(`{"text":%q,"cadence":"once","next_run":%q}`, strings.Repeat("я", store.ReminderMaxTextRunes), boundary))
	if status != http.StatusOK {
		t.Fatalf("maximum reminder status=%d body=%s", status, body)
	}
}

func botMaxReminderDelayForTest() time.Duration {
	return 3650 * 24 * time.Hour
}

func TestCreationMethodsAndStandaloneLifecycleUnavailable(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	for _, endpoint := range []string{"/api/links", "/api/tasks", "/api/reminders"} {
		for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
			status, body, headers := apiRequest(t, server, method, endpoint, bearer)
			if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodGet+", "+http.MethodPost {
				t.Errorf("%s %s status=%d Allow=%q body=%s", method, endpoint, status, headers.Get("Allow"), body)
			}
		}
	}
	for _, test := range []struct {
		path string
		body string
	}{
		{"/api/links", `{"url":"https://example.test"}`},
		{"/api/tasks", `{"goal":"standalone must not create"}`},
	} {
		status, body := managementJSON(t, server, test.path, bearer, test.body)
		if status != http.StatusServiceUnavailable {
			t.Errorf("standalone %s status=%d body=%s", test.path, status, body)
			continue
		}
		requireAPIErrorCode(t, body, "lifecycle_unavailable")
	}
	app.SetTaskLifecycle(nil)
	app.SetLinkLifecycle(nil)
	status, body := managementJSON(t, server, "/api/tasks", bearer, `{"goal":"nil lifecycle"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("nil task lifecycle status=%d body=%s", status, body)
	}
	status, body = managementJSON(t, server, "/api/links", bearer, `{"url":"https://example.test"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("nil link lifecycle status=%d body=%s", status, body)
	}
	count, err := store.CountEvents(context.Background(), app.db, "miniapp_mutation")
	if err != nil || count != 0 {
		t.Fatalf("unavailable lifecycles wrote %d audit events err=%v", count, err)
	}
}

func TestManagementAuthenticationAndCurrentAllowlist(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	tasks, links := installManagementLifecycles(app)
	allowed := true
	app.auth.authorize = func(int64) bool { return allowed }
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	tests := []struct {
		path string
		body string
	}{
		{"/api/history/1/delete", ""},
		{"/api/history/clear", ""},
		{"/api/links", `{"url":"https://example.test"}`},
		{"/api/tasks", `{"goal":"goal"}`},
		{"/api/reminders", fmt.Sprintf(`{"text":"text","cadence":"once","next_run":%q}`, future)},
	}
	for _, removed := range []bool{false, true} {
		allowed = !removed
		activeBearer := bearer
		if !removed {
			activeBearer = ""
		}
		for _, test := range tests {
			contentType := ""
			if test.body != "" {
				contentType = "application/json"
			}
			status, body, _ := managementRequest(t, server, http.MethodPost, test.path, activeBearer, test.body, contentType)
			if status != http.StatusUnauthorized {
				t.Errorf("removed=%v %s status=%d body=%s", removed, test.path, status, body)
			}
		}
	}
	if tasks.calls != 0 || links.calls != 0 {
		t.Fatalf("unauthorized requests invoked lifecycles tasks=%d links=%d", tasks.calls, links.calls)
	}
}

func TestManagementStoreAndLifecycleFailures(t *testing.T) {
	future := func() string { return time.Now().UTC().Add(time.Hour).Format(time.RFC3339) }
	t.Run("history delete", func(t *testing.T) {
		app, server := newTestApp(t)
		bearer := launchSession(t, app, server)
		if err := app.db.Close(); err != nil {
			t.Fatal(err)
		}
		status, body := settingsRequest(t, server.URL, http.MethodPost, "/api/history/1/delete", bearer, "", "")
		if status != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", status, body)
		}
		requireAPIErrorCode(t, body, "history_mutation_failed")
	})
	t.Run("history clear", func(t *testing.T) {
		app, server := newTestApp(t)
		bearer := launchSession(t, app, server)
		if err := app.db.Close(); err != nil {
			t.Fatal(err)
		}
		status, body := settingsRequest(t, server.URL, http.MethodPost, "/api/history/clear", bearer, "", "")
		if status != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", status, body)
		}
		requireAPIErrorCode(t, body, "history_mutation_failed")
	})
	t.Run("reminder create", func(t *testing.T) {
		app, server := newTestApp(t)
		bearer := launchSession(t, app, server)
		if err := app.db.Close(); err != nil {
			t.Fatal(err)
		}
		status, body := managementJSON(t, server, "/api/reminders", bearer,
			fmt.Sprintf(`{"text":"text","cadence":"once","next_run":%q}`, future()))
		if status != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", status, body)
		}
		requireAPIErrorCode(t, body, "reminder_mutation_failed")
	})
	t.Run("link lifecycle", func(t *testing.T) {
		app, server := newTestApp(t)
		bearer := launchSession(t, app, server)
		_, links := installManagementLifecycles(app)
		links.err = errors.New("private link failure")
		status, body := managementJSON(t, server, "/api/links", bearer, `{"url":"https://example.test"}`)
		if status != http.StatusInternalServerError || strings.Contains(string(body), "private link failure") {
			t.Fatalf("status=%d body=%s", status, body)
		}
		requireAPIErrorCode(t, body, "link_mutation_failed")
	})
	t.Run("task lifecycle", func(t *testing.T) {
		app, server := newTestApp(t)
		bearer := launchSession(t, app, server)
		tasks, _ := installManagementLifecycles(app)
		tasks.err = errors.New("private task failure")
		status, body := managementJSON(t, server, "/api/tasks", bearer, `{"goal":"goal"}`)
		if status != http.StatusInternalServerError || strings.Contains(string(body), "private task failure") {
			t.Fatalf("status=%d body=%s", status, body)
		}
		requireAPIErrorCode(t, body, "task_mutation_failed")
	})
}

func TestManagementMutationsUseExistingRateLimit(t *testing.T) {
	for _, test := range []struct {
		path string
		body string
	}{
		{"/api/history/1/delete", ""},
		{"/api/history/clear", ""},
		{"/api/links", `{"url":"https://example.test"}`},
		{"/api/tasks", `{"goal":"goal"}`},
		{"/api/reminders", fmt.Sprintf(`{"text":"text","cadence":"once","next_run":%q}`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339))},
	} {
		t.Run(test.path, func(t *testing.T) {
			app, server := newTestApp(t)
			bearer := launchSession(t, app, server)
			tasks, links := installManagementLifecycles(app)
			app.rateLimits.principals[testUserID] = rateLimitPrincipal{mutation: rateLimitBucket{startedAt: time.Now(), count: mutationRateLimit}}
			contentType := ""
			if test.body != "" {
				contentType = "application/json"
			}
			status, body, headers := managementRequest(t, server, http.MethodPost, test.path, bearer, test.body, contentType)
			if status != http.StatusTooManyRequests || headers.Get("Retry-After") == "" {
				t.Fatalf("rate limited status=%d headers=%v body=%s", status, headers, body)
			}
			requireAPIErrorCode(t, body, "rate_limited")
			if tasks.calls != 0 || links.calls != 0 {
				t.Fatalf("rate limited request invoked lifecycles tasks=%d links=%d", tasks.calls, links.calls)
			}
		})
	}
}

func TestNewManagementAuditOperationsAreContentFree(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		run       func(*testing.T, *App, *httptest.Server, string) string
	}{
		{
			name: "history delete", operation: "history.delete",
			run: func(t *testing.T, app *App, server *httptest.Server, bearer string) string {
				id := appendWebappMessage(t, app, testChatID, store.RoleUser, "private-audit-history")
				status, body := settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/history/%d/delete", id), bearer, "", "")
				if status != http.StatusOK {
					t.Fatalf("status=%d body=%s", status, body)
				}
				return fmt.Sprintf(" id=%d", id)
			},
		},
		{
			name: "history clear", operation: "history.clear",
			run: func(t *testing.T, app *App, server *httptest.Server, bearer string) string {
				appendWebappMessage(t, app, testChatID, store.RoleUser, "private-audit-history-one")
				appendWebappMessage(t, app, testChatID, store.RoleAssistant, "private-audit-history-two")
				status, body := settingsRequest(t, server.URL, http.MethodPost, "/api/history/clear", bearer, "", "")
				if status != http.StatusOK {
					t.Fatalf("status=%d body=%s", status, body)
				}
				return " count=2"
			},
		},
		{
			name: "link add", operation: "links.add",
			run: func(t *testing.T, app *App, server *httptest.Server, bearer string) string {
				installManagementLifecycles(app)
				status, body := managementJSON(t, server, "/api/links", bearer, `{"url":"https://private-audit.example.test"}`)
				if status != http.StatusOK {
					t.Fatalf("status=%d body=%s", status, body)
				}
				return " id=7201"
			},
		},
		{
			name: "task create", operation: "tasks.create",
			run: func(t *testing.T, app *App, server *httptest.Server, bearer string) string {
				installManagementLifecycles(app)
				status, body := managementJSON(t, server, "/api/tasks", bearer, `{"goal":"private-audit-task"}`)
				if status != http.StatusOK {
					t.Fatalf("status=%d body=%s", status, body)
				}
				return " id=7101"
			},
		},
		{
			name: "reminder create", operation: "reminders.create",
			run: func(t *testing.T, app *App, server *httptest.Server, bearer string) string {
				status, body := managementJSON(t, server, "/api/reminders", bearer,
					fmt.Sprintf(`{"text":"private-audit-reminder","cadence":"once","next_run":%q}`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339)))
				if status != http.StatusOK {
					t.Fatalf("status=%d body=%s", status, body)
				}
				var created reminderItem
				if err := json.Unmarshal(body, &created); err != nil {
					t.Fatal(err)
				}
				return fmt.Sprintf(" id=%d", created.ID)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, server := newTestApp(t)
			bearer := launchSession(t, app, server)
			suffix := test.run(t, app, server, bearer)
			var userID int64
			var detail string
			if err := app.db.QueryRowContext(context.Background(), `SELECT user_id, detail FROM events WHERE kind = 'miniapp_mutation'`).Scan(&userID, &detail); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("operation=%s chat_id=%d%s", test.operation, testChatID, suffix)
			if userID != testUserID || detail != want {
				t.Fatalf("audit user=%d detail=%q want=%q", userID, detail, want)
			}
			for _, forbidden := range []string{"private-audit", "https://", "goal", "text", "next_run"} {
				if strings.Contains(detail, forbidden) {
					t.Errorf("audit contains %q: %q", forbidden, detail)
				}
			}
		})
	}
}
