package webapp

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRecoverPanicsAnswers500WithTheErrorEnvelope: a panic used to close the connection with no body at
// all, so the frontend could only show a network error.
func TestRecoverPanicsAnswers500WithTheErrorEnvelope(t *testing.T) {
	var logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	var handler = recoverPanics(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("row content that must not reach the log")
	}))
	var recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/history", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("the 500 is not the JSON envelope: %v (%s)", err, recorder.Body.String())
	}
	if payload.Error.Code != "internal_error" {
		t.Fatalf("error code = %q, want internal_error", payload.Error.Code)
	}
	if strings.Contains(recorder.Body.String(), "row content") {
		t.Fatal("the panic text reached the response body")
	}
}

// TestRecoverPanicsKeepsAWrittenResponse honest: once headers are out, the wrapper must not try to send a
// second one.
func TestRecoverPanicsKeepsAWrittenResponse(t *testing.T) {
	var logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	var handler = recoverPanics(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("partial"))
		panic("late failure")
	}))
	var recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/history", nil))
	if recorder.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the 418 the handler already sent", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "partial") {
		t.Fatalf("body = %q, want the partial response", recorder.Body.String())
	}
}

// TestSessionRateLimitIsPerUser: the exchange creates a 15-minute session, and the session table is global.
// One account must not be able to fill it.
func TestSessionRateLimitIsPerUser(t *testing.T) {
	var limiter = newPerUserRateLimiter()
	for i := 0; i < sessionRateLimit; i++ {
		if allowed, _ := limiter.allowSession(11); !allowed {
			t.Fatalf("session %d of user 11 was refused inside the budget", i+1)
		}
	}
	if allowed, retryAfter := limiter.allowSession(11); allowed || retryAfter <= 0 {
		t.Fatalf("session budget of user 11 is not enforced: allowed=%v retry=%s", allowed, retryAfter)
	}
	if allowed, _ := limiter.allowSession(12); !allowed {
		t.Fatal("the budget of user 11 leaked into user 12")
	}
	if allowed, _ := limiter.allow(11, false); !allowed {
		t.Fatal("the session budget consumed the mutation budget of the same user")
	}
}

// TestLaunchTicketIsPrivateChatOnly: the ticket scope is the chat, so a group ticket would let one member
// read the history and documents of another. The issuer refuses it outright.
func TestLaunchTicketIsPrivateChatOnly(t *testing.T) {
	var app, _ = newTestApp(t)
	if _, err := app.NewLaunchTicket(-100500, testUserID); !errors.Is(err, ErrPrivateOnly) {
		t.Fatalf("group ticket = %v, want ErrPrivateOnly", err)
	}
	if _, err := app.NewLaunchTicket(testUserID, testUserID); err != nil {
		t.Fatalf("private ticket refused: %v", err)
	}
}

// TestSessionExchangeAnswers429AfterTheBudget drives the real endpoint: seven exchanges inside a minute,
// the seventh must be refused with Retry-After instead of creating another session.
func TestSessionExchangeAnswers429AfterTheBudget(t *testing.T) {
	var app, server = newTestApp(t)
	var initData = signedInitData(t, app.cfg.BotToken, testUserID, time.Now())

	var last *http.Response
	for i := 0; i < sessionRateLimit+1; i++ {
		var request, err = http.NewRequest(http.MethodPost, server.URL+"/api/session", strings.NewReader(initData))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		request.Header.Set("Content-Type", "text/plain")
		var response, doErr = http.DefaultClient.Do(request)
		if doErr != nil {
			t.Fatalf("exchange %d: %v", i+1, doErr)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		last = response
		if i < sessionRateLimit && response.StatusCode != http.StatusOK {
			t.Fatalf("exchange %d = %d, want 200", i+1, response.StatusCode)
		}
	}
	if last.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the %d-th exchange = %d, want 429", sessionRateLimit+1, last.StatusCode)
	}
	if last.Header.Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After: the client cannot know when to retry")
	}
}
