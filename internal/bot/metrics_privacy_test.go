package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"bot/internal/metrics"
)

// TestMetricsHaveNoChatOrUserLabels dumps the /metrics body while one update is queued behind a chat lock.
// The old queue_depth{chat_id="…"} was unbounded, kept a series per chat forever and published who talks to
// the bot; the aggregate gauge must stay label-free. A regression that adds chat_id/user_id to any dispatcher
// family makes this test fail on the rendered exposition, not on an internal map.
func TestMetricsHaveNoChatOrUserLabels(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS":        "11",
		"MAX_CONCURRENT_MODEL_REQUESTS": "1",
	})
	m := newBlockingAdmissionModel()
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, testStore(t))
	t.Cleanup(m.unblock)

	reg := metrics.NewRegistry()
	d.SetMetrics(reg)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.Dispatch(context.Background(), testEvent(100, 11, 1, "первый")); err != nil {
			t.Errorf("first Dispatch: %v", err)
		}
	}()
	awaitAdmissionSignal(t, m.started)

	// The second update is admitted but waits for the first one's chat lock: queue_depth must count it.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.Dispatch(context.Background(), testEvent(100, 11, 2, "второй")); err != nil {
			t.Errorf("queued Dispatch: %v", err)
		}
	}()
	waitAdmission(t, func() bool { return d.deps.Locks.Waiting(100) == 1 })

	rec := httptest.NewRecorder()
	metrics.Handler(reg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	for _, forbidden := range []string{"chat_id", "user_id"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("/metrics exposes %q:\n%s", forbidden, body)
		}
	}
	var depth string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "queue_depth") {
			depth = line
		}
	}
	if depth != "queue_depth 1" {
		t.Fatalf("queue_depth line = %q, want the unlabelled aggregate \"queue_depth 1\":\n%s", depth, body)
	}
	if !strings.Contains(body, "busy_chats 1") {
		t.Fatalf("busy_chats is not 1 while a chat lock is held:\n%s", body)
	}

	m.unblock()
	wg.Wait()
}
