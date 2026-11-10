package model

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

const lateSSELine = "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n"

// lateBody is an SSE body that outlives the idle watchdog on purpose: it stays silent long enough for
// the watchdog to fire, then keeps producing valid events. A real net/http body would be cancelled by
// the watchdog's cancelStall; this one is the worst case — the transport has already handed over a
// buffer nobody is reading any more, and the reading goroutine must not park on the beat channel.
type lateBody struct {
	mu     sync.Mutex
	reads  int
	closed bool
}

func (b *lateBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	index := b.reads
	b.reads++
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return 0, io.EOF
	}
	// One long pause after the first few events: the watchdog's Idle budget expires inside it.
	if index == 3 {
		// deliberate: the stall IS the subject — this pause must outlive the 20 ms idle budget below.
		time.Sleep(80 * time.Millisecond)
	}
	return copy(p, lateSSELine), nil
}

func (b *lateBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

type fixedTransport struct {
	body io.ReadCloser
}

func (t fixedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	var header = http.Header{}
	header.Set("Content-Type", "text/event-stream")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       t.body,
		Request:    &http.Request{},
	}, nil
}

// TestStreamReturnsWhenBodyOutlivesTheWatchdog is the regression test for the blocking beat: with a
// body that keeps answering after the stall was declared, `beat <- struct{}{}` used to park the
// scanner forever, so Stream never returned and the model slot plus the per-chat lock stayed taken.
func TestStreamReturnsWhenBodyOutlivesTheWatchdog(t *testing.T) {
	var body = &lateBody{}
	var client, err = New(Options{
		ChatURL:    "http://strata.test/v1/chat/completions",
		HTTPClient: &http.Client{Transport: fixedTransport{body: body}},
		Stream:     StreamSettings{Idle: 20 * time.Millisecond, Total: 10 * time.Second},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	type result struct {
		err error
	}
	var done = make(chan result, 1)
	go func() {
		var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, streamErr := client.Stream(ctx, Request{Model: "m"}, StreamHandler{})
		done <- result{err: streamErr}
	}()

	select {
	case got := <-done:
		if !errors.Is(got.err, ErrStreamStalled) {
			t.Fatalf("Stream returned %v, want ErrStreamStalled", got.err)
		}
	case <-time.After(4 * time.Second):
		body.Close()
		t.Fatal("Stream did not return: the reading goroutine is blocked on the stall beat")
	}
	body.Close()
}

// TestBeatStallNeverBlocks pins the two properties the loop relies on: a full buffer is not waited on,
// and a watchdog that has already given up is reported instead of being fed.
func TestBeatStallNeverBlocks(t *testing.T) {
	var beat = make(chan struct{}, 1)
	var stalled = make(chan struct{})
	beat <- struct{}{}

	var finished = make(chan bool, 1)
	go func() { finished <- beatStall(beat, stalled) }()
	select {
	case got := <-finished:
		if got {
			t.Fatal("beatStall reported a stall while the watchdog is still alive")
		}
	case <-time.After(time.Second):
		t.Fatal("beatStall blocked on a full beat channel")
	}

	close(stalled)
	if !beatStall(beat, stalled) {
		t.Fatal("beatStall did not report the stall of a watchdog that has given up")
	}
}

// TestBeatStallStillFeedsTheWatchdog keeps the positive case honest: an empty channel receives exactly
// one beat and nothing is reported.
func TestBeatStallStillFeedsTheWatchdog(t *testing.T) {
	var beat = make(chan struct{}, 1)
	var stalled = make(chan struct{})
	if beatStall(beat, stalled) {
		t.Fatal("beatStall reported a stall while the watchdog is alive")
	}
	select {
	case <-beat:
	default:
		t.Fatal("beatStall did not queue a beat")
	}
}
