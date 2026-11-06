package webapp

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bot/internal/store"
)

func requireRateLimited(t *testing.T, status int, body []byte, headers http.Header, retryAfter string) {
	t.Helper()
	if status != http.StatusTooManyRequests || headers.Get("Retry-After") != retryAfter {
		t.Fatalf("rate limit status=%d Retry-After=%q want=%q body=%s", status, headers.Get("Retry-After"), retryAfter, body)
	}
	requireAPIErrorCode(t, body, "rate_limited")
}

func TestMutationRateLimitIsPerUserAndDoesNotLimitReads(t *testing.T) {
	app, server := newTestApp(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	app.rateLimits.now = func() time.Time { return now }
	otherUserID := testUserID + 1
	app.auth.authorize = func(userID int64) bool { return userID == testUserID || userID == otherUserID }
	bearer := launchSession(t, app, server)
	otherBearer := launchSessionFor(t, app, server, testChatID+1, otherUserID)

	for index := 0; index < mutationRateLimit; index++ {
		status, body := putSettings(t, server.URL, "/api/settings/stream", bearer, `{"enabled":true}`)
		if status != http.StatusOK {
			t.Fatalf("mutation %d status=%d body=%s", index+1, status, body)
		}
	}
	status, body, headers := apiRequest(t, server, http.MethodPut, "/api/settings/stream", bearer)
	requireRateLimited(t, status, body, headers, "60")
	for index := 0; index < mutationRateLimit+10; index++ {
		status, body, _ = apiRequest(t, server, http.MethodGet, "/api/history/search?q=none", bearer)
		if status != http.StatusOK {
			t.Fatalf("ordinary read %d status=%d body=%s", index, status, body)
		}
	}
	status, body = putSettings(t, server.URL, "/api/settings/stream", otherBearer, `{"enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("other user mutation status=%d body=%s", status, body)
	}
	count, err := store.CountEvents(context.Background(), app.db, "miniapp_mutation")
	if err != nil || count != mutationRateLimit+1 {
		t.Fatalf("audit count after rejected mutation=%d err=%v", count, err)
	}

	now = now.Add(30 * time.Second)
	status, body, headers = apiRequest(t, server, http.MethodPut, "/api/settings/stream", bearer)
	requireRateLimited(t, status, body, headers, "30")
	now = now.Add(30 * time.Second)
	status, body = putSettings(t, server.URL, "/api/settings/stream", bearer, `{"enabled":false}`)
	if status != http.StatusOK {
		t.Fatalf("mutation after window status=%d body=%s", status, body)
	}
}

func TestHistoryExportHasIndependentFivePerMinuteWindow(t *testing.T) {
	app, server := newTestApp(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	app.rateLimits.now = func() time.Time { return now }
	bearer := launchSession(t, app, server)
	appendWebappMessage(t, app, testChatID, store.RoleUser, "export content")

	for index := 0; index < exportRateLimit; index++ {
		status, body, _ := apiRequest(t, server, http.MethodGet, "/api/history/export?format=md", bearer)
		if status != http.StatusOK {
			t.Fatalf("export %d status=%d body=%s", index+1, status, body)
		}
	}
	status, body, headers := apiRequest(t, server, http.MethodGet, "/api/history/export?format=json", bearer)
	requireRateLimited(t, status, body, headers, "60")
	status, body = putSettings(t, server.URL, "/api/settings/stream", bearer, `{"enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("mutation shared export bucket status=%d body=%s", status, body)
	}
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/history/search?q=export", bearer)
	if status != http.StatusOK {
		t.Fatalf("ordinary read after export limit status=%d body=%s", status, body)
	}

	now = now.Add(59 * time.Second)
	status, body, headers = apiRequest(t, server, http.MethodGet, "/api/history/export?format=md", bearer)
	requireRateLimited(t, status, body, headers, "1")
	now = now.Add(time.Second)
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/history/export?format=md", bearer)
	if status != http.StatusOK {
		t.Fatalf("export after window status=%d body=%s", status, body)
	}
}

func TestRateLimiterCapacityAndWindowPruning(t *testing.T) {
	limiter := newPerUserRateLimiter()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	for userID := int64(1); userID <= maxRateLimitUsers; userID++ {
		if allowed, _ := limiter.allow(userID, false); !allowed {
			t.Fatalf("principal %d was rejected below capacity", userID)
		}
	}
	if allowed, retry := limiter.allow(maxRateLimitUsers+1, false); allowed || retry != rateLimitWindow {
		t.Fatalf("overflow allowed=%v retry=%s", allowed, retry)
	}
	limiter.mu.Lock()
	count := len(limiter.principals)
	limiter.mu.Unlock()
	if count != maxRateLimitUsers {
		t.Fatalf("principal map size=%d", count)
	}

	now = now.Add(rateLimitWindow)
	if allowed, _ := limiter.allow(maxRateLimitUsers+1, false); !allowed {
		t.Fatal("expired principal capacity was not reusable")
	}
	limiter.mu.Lock()
	count = len(limiter.principals)
	limiter.mu.Unlock()
	if count != 1 {
		t.Fatalf("expired principal map size=%d want=1", count)
	}

	limiter = newPerUserRateLimiter()
	now = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	_, _ = limiter.allow(1, false)
	_, _ = limiter.allow(2, false)
	now = now.Add(30 * time.Second)
	_, _ = limiter.allow(2, true)
	now = now.Add(30 * time.Second)
	_, _ = limiter.allow(3, false)
	limiter.mu.Lock()
	_, firstFound := limiter.principals[1]
	_, secondFound := limiter.principals[2]
	count = len(limiter.principals)
	limiter.mu.Unlock()
	if firstFound || !secondFound || count != 2 {
		t.Fatalf("independent bucket pruning first=%v second=%v count=%d", firstFound, secondFound, count)
	}
}

func TestRateLimiterConcurrentAllowancesAreExact(t *testing.T) {
	limiter := newPerUserRateLimiter()
	const callers = 128
	start := make(chan struct{})
	var mutationAllowed atomic.Int64
	var exportAllowed atomic.Int64
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			<-start
			if allowed, _ := limiter.allow(testUserID, false); allowed {
				mutationAllowed.Add(1)
			}
			if allowed, _ := limiter.allow(testUserID, true); allowed {
				exportAllowed.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	if got := mutationAllowed.Load(); got != mutationRateLimit {
		t.Fatalf("concurrent mutation allowances=%d want=%d", got, mutationRateLimit)
	}
	if got := exportAllowed.Load(); got != exportRateLimit {
		t.Fatalf("concurrent export allowances=%d want=%d", got, exportRateLimit)
	}
	limiter.mu.Lock()
	count := len(limiter.principals)
	principal := limiter.principals[testUserID]
	limiter.mu.Unlock()
	if count != 1 || principal.mutation.count != mutationRateLimit || principal.export.count != exportRateLimit {
		t.Fatalf("concurrent limiter state count=%d principal=%+v", count, principal)
	}
	if allowed, retry := limiter.allow(testUserID, false); allowed || retry <= 0 || retry > rateLimitWindow {
		t.Fatalf("post-concurrency allowed=%v retry=%s", allowed, retry)
	}
}
