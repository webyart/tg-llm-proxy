package webapp

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"bot/internal/store"
)

func TestAuditResponseWriterForwardsResponseControllerDeadlines(t *testing.T) {
	underlying := &deadlineResponseWriter{}
	audited := &auditResponseWriter{ResponseWriter: underlying, status: http.StatusOK}
	control := http.NewResponseController(audited)
	readDeadline := time.Now().Add(3 * time.Second)
	writeDeadline := readDeadline.Add(time.Minute)
	if err := control.SetReadDeadline(readDeadline); err != nil {
		t.Fatalf("SetReadDeadline through the audit writer = %v, want the underlying writer", err)
	}
	if err := control.SetWriteDeadline(writeDeadline); err != nil {
		t.Fatalf("SetWriteDeadline through the audit writer = %v, want the underlying writer", err)
	}
	if !underlying.readDeadline.Equal(readDeadline) {
		t.Fatalf("underlying read deadline = %s, want %s", underlying.readDeadline, readDeadline)
	}
	if !underlying.writeDeadline.Equal(writeDeadline) {
		t.Fatalf("underlying write deadline = %s, want %s", underlying.writeDeadline, writeDeadline)
	}
}

// deadlineResponseWriter is a ResponseWriter whose connection-level deadline methods the controller
// can reach only through auditResponseWriter.Unwrap.
type deadlineResponseWriter struct {
	readDeadline  time.Time
	writeDeadline time.Time
}

func (w *deadlineResponseWriter) Header() http.Header            { return http.Header{} }
func (w *deadlineResponseWriter) Write(body []byte) (int, error) { return len(body), nil }
func (w *deadlineResponseWriter) WriteHeader(int)                {}
func (w *deadlineResponseWriter) SetReadDeadline(deadline time.Time) error {
	w.readDeadline = deadline
	return nil
}
func (w *deadlineResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.writeDeadline = deadline
	return nil
}

func TestMiniAppMutationAuditCoversAllAreasWithoutContent(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *App, string, string) string
	}{
		{
			name: "document delete",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				created := createWebappDocument(t, app, testChatID, "private-audit-content")
				path := fmt.Sprintf("/api/documents/%d/delete", created)
				assertAuditMutationStatus(t, serverURL, path, bearer, "", "")
				return fmt.Sprintf("operation=documents.delete chat_id=%d id=%d", testChatID, created)
			},
		},
		{
			name: "document clear",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				createWebappDocument(t, app, testChatID, "private-audit-content-one")
				createWebappDocument(t, app, testChatID, "private-audit-content-two")
				assertAuditMutationStatus(t, serverURL, "/api/documents/clear", bearer, "", "")
				return fmt.Sprintf("operation=documents.clear chat_id=%d count=2", testChatID)
			},
		},
		{
			name: "link delete",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				created := createWebappLink(t, app, testChatID, "private-audit-content")
				assertAuditMutationStatus(t, serverURL, fmt.Sprintf("/api/links/%d/delete", created.ID), bearer, "", "")
				return fmt.Sprintf("operation=links.delete chat_id=%d id=%d", testChatID, created.ID)
			},
		},
		{
			name: "link clear",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				createWebappLink(t, app, testChatID, "private-audit-content-one")
				createWebappLink(t, app, testChatID, "private-audit-content-two")
				assertAuditMutationStatus(t, serverURL, "/api/links/clear", bearer, "", "")
				return fmt.Sprintf("operation=links.clear chat_id=%d count=2", testChatID)
			},
		},
		{
			name: "task cancel",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				created := createWebappTask(t, app, testChatID, time.Now().UTC())
				assertAuditMutationStatus(t, serverURL, "/api/tasks/cancel", bearer, "", "")
				return fmt.Sprintf("operation=tasks.cancel chat_id=%d id=%d", testChatID, created.ID)
			},
		},
		{
			name: "task resume",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				now := time.Now().UTC()
				created := createWebappTask(t, app, testChatID, now)
				if _, changed, err := store.CancelTask(context.Background(), app.db, testChatID, now); err != nil || !changed {
					t.Fatalf("CancelTask changed=%v err=%v", changed, err)
				}
				assertAuditMutationStatus(t, serverURL, fmt.Sprintf("/api/tasks/%d/resume", created.ID), bearer, "", "")
				return fmt.Sprintf("operation=tasks.resume chat_id=%d id=%d", testChatID, created.ID)
			},
		},
		{
			name: "task delete",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				created := createWebappTask(t, app, testChatID, time.Now().UTC())
				setWebappTaskStatus(t, app, created.ID, store.TaskStatusCompleted)
				assertAuditMutationStatus(t, serverURL, fmt.Sprintf("/api/tasks/%d/delete", created.ID), bearer, "", "")
				return fmt.Sprintf("operation=tasks.delete chat_id=%d id=%d", testChatID, created.ID)
			},
		},
		{
			name: "task clear",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				first := createWebappTask(t, app, testChatID, time.Now().UTC())
				setWebappTaskStatus(t, app, first.ID, store.TaskStatusCompleted)
				createWebappTask(t, app, testChatID, time.Now().UTC().Add(time.Second))
				assertAuditMutationStatus(t, serverURL, "/api/tasks/clear", bearer, "", "")
				return fmt.Sprintf("operation=tasks.clear chat_id=%d count=2", testChatID)
			},
		},
		{
			name: "settings model set",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				assertAuditMutationStatus(t, serverURL, "/api/settings/model", bearer, `{"model":"qwen-alt"}`, http.MethodPut)
				return fmt.Sprintf("operation=settings.model.set chat_id=%d", testChatID)
			},
		},
		{
			name: "settings model reset",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				assertAuditMutationStatus(t, serverURL, "/api/settings/model", bearer, "", http.MethodDelete)
				return fmt.Sprintf("operation=settings.model.reset chat_id=%d", testChatID)
			},
		},
		{
			name: "settings persona set",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				assertAuditMutationStatus(t, serverURL, "/api/settings/persona", bearer, `{"custom":"private-audit-content"}`, http.MethodPut)
				return fmt.Sprintf("operation=settings.persona.set chat_id=%d", testChatID)
			},
		},
		{
			name: "settings persona reset",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				assertAuditMutationStatus(t, serverURL, "/api/settings/persona", bearer, "", http.MethodDelete)
				return fmt.Sprintf("operation=settings.persona.reset chat_id=%d", testChatID)
			},
		},
		{
			name: "settings stream set",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				assertAuditMutationStatus(t, serverURL, "/api/settings/stream", bearer, `{"enabled":true}`, http.MethodPut)
				return fmt.Sprintf("operation=settings.stream.set chat_id=%d", testChatID)
			},
		},
		{
			name: "settings stream reset",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				assertAuditMutationStatus(t, serverURL, "/api/settings/stream", bearer, "", http.MethodDelete)
				return fmt.Sprintf("operation=settings.stream.reset chat_id=%d", testChatID)
			},
		},
		{
			name: "settings mini app fallback set",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				assertAuditMutationStatus(t, serverURL, "/api/settings/miniapp-fallback", bearer, `{"enabled":false}`, http.MethodPut)
				return fmt.Sprintf("operation=settings.miniapp_fallback.set chat_id=%d", testChatID)
			},
		},
		{
			name: "settings mini app fallback reset",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				assertAuditMutationStatus(t, serverURL, "/api/settings/miniapp-fallback", bearer, "", http.MethodDelete)
				return fmt.Sprintf("operation=settings.miniapp_fallback.reset chat_id=%d", testChatID)
			},
		},
		{
			name: "reminder cancel",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				created := createWebappReminder(t, app, testChatID, "private-audit-content", store.ReminderCadenceOnce, time.Now().UTC().Add(time.Hour))
				assertAuditMutationStatus(t, serverURL, fmt.Sprintf("/api/reminders/%d/cancel", created.ID), bearer, "", "")
				return fmt.Sprintf("operation=reminders.cancel chat_id=%d id=%d", testChatID, created.ID)
			},
		},
		{
			name: "reminder retry",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				created := createWebappReminder(t, app, testChatID, "private-audit-content", store.ReminderCadenceOnce, time.Now().UTC())
				failWebappReminder(t, app, created.ID, time.Now().UTC())
				assertAuditMutationStatus(t, serverURL, fmt.Sprintf("/api/reminders/%d/retry", created.ID), bearer, "", "")
				return fmt.Sprintf("operation=reminders.retry chat_id=%d id=%d", testChatID, created.ID)
			},
		},
		{
			name: "reminder delete",
			run: func(t *testing.T, app *App, serverURL string, bearer string) string {
				created := createWebappReminder(t, app, testChatID, "private-audit-content", store.ReminderCadenceOnce, time.Now().UTC())
				failWebappReminder(t, app, created.ID, time.Now().UTC())
				assertAuditMutationStatus(t, serverURL, fmt.Sprintf("/api/reminders/%d/delete", created.ID), bearer, "", "")
				return fmt.Sprintf("operation=reminders.delete chat_id=%d id=%d", testChatID, created.ID)
			},
		},
	}

	detailPattern := regexp.MustCompile(`^operation=[a-z._]+ chat_id=-?[0-9]+( (id|count)=-?[0-9]+)?$`)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, server := newTestApp(t)
			bearer := launchSession(t, app, server)
			want := test.run(t, app, server.URL, bearer)
			count, err := store.CountEvents(context.Background(), app.db, "miniapp_mutation")
			if err != nil || count != 1 {
				t.Fatalf("audit count=%d err=%v", count, err)
			}
			var userID int64
			var detail string
			if err := app.db.QueryRowContext(context.Background(), `SELECT user_id, detail FROM events WHERE kind = 'miniapp_mutation'`).Scan(&userID, &detail); err != nil {
				t.Fatal(err)
			}
			if userID != testUserID || detail != want || !detailPattern.MatchString(detail) {
				t.Fatalf("audit user=%d detail=%q want=%q", userID, detail, want)
			}
			for _, forbidden := range []string{"private-audit-content", "qwen-alt", "https://", "enabled", "custom"} {
				if strings.Contains(detail, forbidden) {
					t.Errorf("audit detail contains %q: %q", forbidden, detail)
				}
			}
		})
	}
}

func assertAuditMutationStatus(t *testing.T, serverURL string, path string, bearer string, body string, method string) {
	t.Helper()
	if method == "" {
		method = http.MethodPost
	}
	contentType := ""
	if method == http.MethodPut {
		contentType = "application/json"
	}
	status, responseBody := settingsRequest(t, serverURL, method, path, bearer, body, contentType)
	if status != http.StatusOK {
		t.Fatalf("%s %s status=%d body=%s", method, path, status, responseBody)
	}
}

func TestMiniAppAuditSkipsReadsFailuresAndSessionRevocation(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	now := time.Now().UTC()
	active := createWebappReminder(t, app, testChatID, "private-audit-content", store.ReminderCadenceOnce, now.Add(time.Hour))

	for _, test := range []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/api/documents/search?q=private-audit-content", "", http.StatusOK},
		{http.MethodGet, "/api/reminders", "", http.StatusOK},
		{http.MethodPost, "/api/documents/999999/delete", "", http.StatusNotFound},
		{http.MethodPost, fmt.Sprintf("/api/reminders/%d/delete", active.ID), "", http.StatusConflict},
		{http.MethodPut, "/api/settings/persona", `{"custom":""}`, http.StatusBadRequest},
	} {
		contentType := ""
		if test.method == http.MethodPut {
			contentType = "application/json"
		}
		status, body := settingsRequest(t, server.URL, test.method, test.path, bearer, test.body, contentType)
		if status != test.want {
			t.Errorf("%s %s status=%d want=%d body=%s", test.method, test.path, status, test.want, body)
		}
	}
	status, body := settingsRequest(t, server.URL, http.MethodDelete, "/api/session", bearer, "", "")
	if status != http.StatusOK {
		t.Fatalf("session revoke status=%d body=%s", status, body)
	}
	count, err := store.CountEvents(context.Background(), app.db, "miniapp_mutation")
	if err != nil || count != 0 {
		t.Fatalf("non-mutations wrote %d audit events err=%v", count, err)
	}
}

func TestMiniAppAuditFailureDoesNotRollbackOrChangeResponse(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	created := createWebappDocument(t, app, testChatID, "private-audit-content")
	var logs bytes.Buffer
	app.logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := app.db.ExecContext(context.Background(), `CREATE TRIGGER reject_miniapp_audit
		BEFORE INSERT ON events WHEN NEW.kind = 'miniapp_mutation'
		BEGIN SELECT RAISE(ABORT, 'audit storage intentionally unavailable'); END`); err != nil {
		t.Fatal(err)
	}

	status, body := settingsRequest(t, server.URL, http.MethodPost, fmt.Sprintf("/api/documents/%d/delete", created), bearer, "", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("delete with failed audit status=%d body=%s", status, body)
	}
	if webappDocumentExists(t, app, testChatID, created) {
		t.Fatal("failed audit rolled back the document deletion")
	}
	if strings.Contains(string(body), "audit storage") || strings.Contains(strings.ToLower(string(body)), "internal") {
		t.Fatalf("failed audit exposed internal details: %s", body)
	}
	count, err := store.CountEvents(context.Background(), app.db, "miniapp_mutation")
	if err != nil || count != 0 {
		t.Fatalf("failed audit count=%d err=%v", count, err)
	}
	if !strings.Contains(logs.String(), "Mini App mutation audit failed") {
		t.Fatalf("failed audit was not logged best-effort: %s", logs.String())
	}
	if strings.Contains(logs.String(), "private-audit-content") {
		t.Fatalf("audit failure logged private content: %s", logs.String())
	}
}
