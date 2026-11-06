package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"bot/internal/bot"
	"bot/internal/store"
)

func readTTSSettings(t *testing.T, serverURL string, bearer string) settingsResponse {
	t.Helper()
	status, body := settingsRequest(t, serverURL, http.MethodGet, "/api/settings", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET settings status=%d body=%s", status, body)
	}
	var response settingsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestTTSSpeedSettingsDefaultsPersistenceResetAndChatIsolation(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, testChatID+1, testUserID)

	settings := readTTSSettings(t, server.URL, bearer)
	if settings.TTS.Enabled || settings.TTS.Current != 1 || settings.TTS.Default != 1 || !reflect.DeepEqual(settings.TTS.Choices, []float64{0.75, 1, 1.25, 1.5}) {
		t.Fatalf("default TTS settings=%+v", settings.TTS)
	}
	status, body := putSettings(t, server.URL, "/api/settings/tts-speed", bearer, `{"speed":"0.75"}`)
	if status != http.StatusOK {
		t.Fatalf("set TTS speed status=%d body=%s", status, body)
	}
	if speed, err := bot.TTSSpeed(context.Background(), app.db, testChatID); err != nil || speed != 0.75 {
		t.Fatalf("TTSSpeed=%v err=%v", speed, err)
	}
	if value, found, err := store.GetState(context.Background(), app.db, "tts_speed:"+strconv.FormatInt(testChatID, 10)); err != nil || !found || value != "0.75" {
		t.Fatalf("stored TTS speed=%q found=%v err=%v", value, found, err)
	}
	if settings = readTTSSettings(t, server.URL, bearer); settings.TTS.Current != 0.75 {
		t.Fatalf("current TTS settings=%+v", settings.TTS)
	}
	if settings = readTTSSettings(t, server.URL, otherBearer); settings.TTS.Current != 1 {
		t.Fatalf("other chat inherited TTS speed: %+v", settings.TTS)
	}

	status, body = putSettings(t, server.URL, "/api/settings/tts-speed", otherBearer, `{"speed":"1.5"}`)
	if status != http.StatusOK {
		t.Fatalf("other chat set status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodDelete, "/api/settings/tts-speed", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("reset TTS speed status=%d body=%s", status, body)
	}
	if _, found, err := store.GetState(context.Background(), app.db, "tts_speed:"+strconv.FormatInt(testChatID, 10)); err != nil || found {
		t.Fatalf("TTS state after reset found=%v err=%v", found, err)
	}
	if speed, err := bot.TTSSpeed(context.Background(), app.db, testChatID); err != nil || speed != 1 {
		t.Fatalf("reset TTSSpeed=%v err=%v", speed, err)
	}
	if speed, err := bot.TTSSpeed(context.Background(), app.db, testChatID+1); err != nil || speed != 1.5 {
		t.Fatalf("reset changed other chat speed=%v err=%v", speed, err)
	}

	app.cfg.TTSEnabled = true
	if settings = readTTSSettings(t, server.URL, bearer); !settings.TTS.Enabled || settings.TTS.Current != 1 {
		t.Fatalf("enabled/reset TTS settings=%+v", settings.TTS)
	}
	client := app.model.(*fakeModelClient)
	client.mu.Lock()
	completions := client.completeCalls
	client.mu.Unlock()
	if completions != 0 {
		t.Fatalf("TTS settings made %d completion calls", completions)
	}
}

func TestTTSSpeedStrictJSONMethodsQueryAndAuthentication(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	status, body := putSettings(t, server.URL, "/api/settings/tts-speed", bearer, `{"speed":"1.25"}`)
	if status != http.StatusOK {
		t.Fatalf("prime TTS speed status=%d body=%s", status, body)
	}

	for _, test := range []struct {
		body        string
		contentType string
		want        int
	}{
		{`{}`, "application/json", http.StatusBadRequest},
		{`{"speed":null}`, "application/json", http.StatusBadRequest},
		{`{"speed":1}`, "application/json", http.StatusBadRequest},
		{`{"speed":true}`, "application/json", http.StatusBadRequest},
		{`{"speed":""}`, "application/json", http.StatusBadRequest},
		{`{"speed":"1.0"}`, "application/json", http.StatusBadRequest},
		{`{"speed":" 1"}`, "application/json", http.StatusBadRequest},
		{`{"speed":"2"}`, "application/json", http.StatusBadRequest},
		{`{"speed":"1","other":true}`, "application/json", http.StatusBadRequest},
		{`{"speed":"1"} {}`, "application/json", http.StatusBadRequest},
		{`{"speed":"1"`, "application/json", http.StatusBadRequest},
		{`{"speed":"1"}`, "text/plain", http.StatusUnsupportedMediaType},
	} {
		status, body = settingsRequest(t, server.URL, http.MethodPut, "/api/settings/tts-speed", bearer, test.body, test.contentType)
		if status != test.want {
			t.Errorf("invalid TTS body %q status=%d want=%d body=%s", test.body, status, test.want, body)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch} {
		status, body, headers := apiRequest(t, server, method, "/api/settings/tts-speed", bearer)
		if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodPut+", "+http.MethodDelete {
			t.Errorf("%s TTS speed status=%d Allow=%q body=%s", method, status, headers.Get("Allow"), body)
		}
	}
	status, body = putSettings(t, server.URL, "/api/settings/tts-speed?speed=1", bearer, `{"speed":"1"}`)
	if status != http.StatusBadRequest {
		t.Errorf("TTS set query status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodDelete, "/api/settings/tts-speed?speed=1", bearer, "", "")
	if status != http.StatusBadRequest {
		t.Errorf("TTS reset query status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodDelete, "/api/settings/tts-speed", bearer, `{}`, "application/json")
	if status != http.StatusBadRequest {
		t.Errorf("TTS reset body status=%d body=%s", status, body)
	}
	status, body = putSettings(t, server.URL, "/api/settings/tts-speed", bearer, `{"speed":"`+strings.Repeat("x", maxBodyBytes)+`"}`)
	if status != http.StatusRequestEntityTooLarge {
		t.Errorf("TTS oversized body status=%d body=%s", status, body)
	}
	status, body = putSettings(t, server.URL, "/api/settings/tts-speed", "", `{"speed":"1"}`)
	if status != http.StatusUnauthorized {
		t.Errorf("TTS missing bearer status=%d body=%s", status, body)
	}
	if speed, err := bot.TTSSpeed(context.Background(), app.db, testChatID); err != nil || speed != 1.25 {
		t.Fatalf("rejected requests changed TTSSpeed=%v err=%v", speed, err)
	}

	allowed := true
	app.auth.authorize = func(int64) bool { return allowed }
	allowed = false
	status, body = putSettings(t, server.URL, "/api/settings/tts-speed", bearer, `{"speed":"1"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("removed allowlist status=%d body=%s", status, body)
	}
	if speed, err := bot.TTSSpeed(context.Background(), app.db, testChatID); err != nil || speed != 1.25 {
		t.Fatalf("allowlist rejection changed TTSSpeed=%v err=%v", speed, err)
	}
}

func TestTTSSpeedAuditIsContentFreeAndSkipsFailures(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	status, body := putSettings(t, server.URL, "/api/settings/tts-speed", bearer, `{"speed":"0.75"}`)
	if status != http.StatusOK {
		t.Fatalf("audited set status=%d body=%s", status, body)
	}
	status, body = settingsRequest(t, server.URL, http.MethodDelete, "/api/settings/tts-speed", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("audited reset status=%d body=%s", status, body)
	}
	status, body = putSettings(t, server.URL, "/api/settings/tts-speed", bearer, `{"speed":"2"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid audited set status=%d body=%s", status, body)
	}
	_ = readTTSSettings(t, server.URL, bearer)

	rows, err := app.db.QueryContext(context.Background(), `SELECT user_id, detail FROM events WHERE kind = 'miniapp_mutation' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var userID int64
		var detail string
		if err := rows.Scan(&userID, &detail); err != nil {
			t.Fatal(err)
		}
		if userID != testUserID {
			t.Errorf("audit user_id=%d", userID)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"operation=settings.tts_speed.set chat_id=" + strconv.FormatInt(testChatID, 10),
		"operation=settings.tts_speed.reset chat_id=" + strconv.FormatInt(testChatID, 10),
	}
	if !reflect.DeepEqual(details, want) {
		t.Fatalf("TTS audit details=%v want=%v", details, want)
	}
	for _, detail := range details {
		if strings.Contains(detail, "0.75") || strings.Contains(detail, "speed=") || strings.Contains(detail, "enabled") {
			t.Errorf("TTS audit contains mutation content: %q", detail)
		}
	}
}
