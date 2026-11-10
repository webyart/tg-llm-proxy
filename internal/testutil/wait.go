package testutil

// Shared wait helpers.
//
// A test that waits for a goroutine, a poller, a scheduler or a stream has no business guessing a
// duration. `time.Sleep(200 * time.Millisecond)` is silently two things at once: «the thing must happen»
// and «it must happen within 200 ms on the slowest machine that ever runs this suite» — the first half is
// never asserted, the second half breaks on a loaded CI runner. Eventually states the first half and
// retries; Consistently states the second half («nothing else happens for this window») and fails at once
// when it is violated instead of hoping the window was long enough.
//
// Both helpers poll a cheap condition. They do not replace a sleep that IS the subject of the test — a TTL
// expiry, a backoff pause, a debounce window, a rate-limit bucket — those keep their `time.Sleep` with a
// `// deliberate:` comment naming the reason.

import (
	"fmt"
	"testing"
	"time"
)

// pollInterval is how often the wait helpers re-check their condition. Small enough that a correct test
// loses a couple of milliseconds instead of a couple of hundred, large enough that polling a database or
// an HTTP fake costs nothing measurable.
const pollInterval = 2 * time.Millisecond

// Eventually polls cond until it returns true and fails the test when timeout elapses. This is the
// replacement for «sleep, then assert»: the assertion is made by cond on every poll, so the test passes as
// soon as the condition holds on a fast machine and still passes on a slow one.
//
// On failure the reported line is the caller's (t.Helper), and the optional detail funcs are appended to
// the message — the call site usually knows what «it did not happen» means.
func Eventually(t *testing.T, timeout time.Duration, cond func() bool, detail ...func() string) {
	t.Helper()
	if waitUntil(timeout, cond) {
		return
	}
	fail(t, fmt.Sprintf("condition was not met within %s (polled every %s)", timeout, pollInterval), detail)
}

// Consistently asserts that violated stays false for the whole window — the negative twin of Eventually.
// Use it where a bare `time.Sleep` would be followed by «and the counter did not move»: the check now runs
// on every poll and the failure names the moment the unwanted thing happened, instead of comparing two
// samples and leaving the rest of the window unwatched.
func Consistently(t *testing.T, window time.Duration, violated func() bool, detail ...func() string) {
	t.Helper()
	var deadline = time.NewTimer(window)
	defer deadline.Stop()
	var ticker = time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if violated() {
			fail(t, fmt.Sprintf("condition became true within %s, want it false for the whole window", window), detail)
		}
		select {
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}

// waitUntil is Eventually without the assertion, so the caller can word the failure.
func waitUntil(timeout time.Duration, cond func() bool) bool {
	var deadline = time.NewTimer(timeout)
	defer deadline.Stop()
	var ticker = time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if cond() {
			return true
		}
		select {
		case <-deadline.C:
			// One last look: the condition may have become true while the timer fired.
			return cond()
		case <-ticker.C:
		}
	}
}

func fail(t *testing.T, message string, detail []func() string) {
	t.Helper()
	for _, describe := range detail {
		if describe == nil {
			continue
		}
		if text := describe(); text != "" {
			message += ": " + text
		}
	}
	t.Fatalf("%s", message)
}
