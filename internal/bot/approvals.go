// Per-action network approvals for the Mini App.
//
// The Telegram path pauses the whole answer inside promptNetworkApproval and resumes it from a
// callback query. The Mini App has no callback query: its answer is one live SSE response, so the
// same pause happens inside runToolLoop, on the goroutine that owns the stream. The waiter created
// here is what the loop calls instead of the Telegram menu:
//
//	register a pending approval (token, chat, user, tool, arguments, TTL) in this Dispatcher's set
//	emit `event: approval` to the SSE stream
//	block until POST /api/chat/approval delivers a decision, the TTL expires or the client goes away
//
// The decision endpoint never waits: it finds the token, validates the session scope, marks the
// entry used and wakes the waiter. The waiting stream is the only place that resumes the tool loop.
package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"bot/internal/model"
)

// MiniAppApprovalTimeout is the lifetime of one Mini App approval. It is a variable, not a constant,
// so tests can inject a short TTL instead of waiting two minutes.
var MiniAppApprovalTimeout = 2 * time.Minute

const (
	// miniAppApprovalTombstone is how long an expired or already-used token is remembered, so a late
	// or replayed decision is answered with "expired/used" (410) instead of "unknown" (404).
	miniAppApprovalTombstone = 5 * time.Minute
	miniAppApprovalGlobalCap = 16
	miniAppApprovalUserCap   = 4
	miniAppApprovalSummary   = 300
	// miniAppApprovalRefusal is the answer stored when the user denies the action. It is the same
	// wording the Telegram cancel button shows: the tool never runs and the loop does not continue.
	miniAppApprovalRefusal = "Сетевое действие отменено. Запрос не выполнен."
)

var (
	// ErrMiniAppApprovalNotFound — an unknown token, or a token of another chat/user. The web layer
	// answers 404 so a valid token cannot be probed from another session.
	ErrMiniAppApprovalNotFound = errors.New("network approval token is unknown for this chat")
	// ErrMiniAppApprovalExpired — the token existed but its TTL passed or it was already used.
	ErrMiniAppApprovalExpired = errors.New("network approval token expired or was already used")
	// errMiniAppApprovalTimeout is an internal failure: the waiter itself expired. The HTTP stream
	// carries the clear user-facing sentence from miniAppErrorText.
	errMiniAppApprovalTimeout = errors.New("network approval timed out")
	// errMiniAppApprovalBusy — the per-user/global pending cap is full. The question is not answered
	// and the model loop stops with a clear error instead of parking another stream.
	errMiniAppApprovalBusy = errors.New("too many pending network approvals")
)

type miniAppApproval struct {
	ChatID    int64
	UserID    int64
	Tool      string
	Arguments string
	ExpiresAt time.Time
	// decision carries exactly one value. It is buffered and never closed: a decision that arrives
	// after the waiter has gone is dropped instead of panicking on a closed channel.
	decision chan bool
	// used is set by the decision endpoint (or by the waiter when it times out/cancels). The entry
	// stays in the map as a tombstone until miniAppApprovalTombstone passes.
	used bool
}

type miniAppApprovalSet struct {
	mu      sync.Mutex
	pending map[string]*miniAppApproval
}

// miniAppApprovals is a package-level map keyed by the Dispatcher. The per-dispatcher state lives
// in miniAppApprovalSet; the map only makes the set reachable from approvals.go without adding a
// field to the Dispatcher struct (which this change is not allowed to touch). One entry per live
// dispatcher is expected: production builds one.
var miniAppApprovals = struct {
	mu   sync.Mutex
	sets map[*Dispatcher]*miniAppApprovalSet
}{sets: map[*Dispatcher]*miniAppApprovalSet{}}

func miniAppApprovalsFor(d *Dispatcher) *miniAppApprovalSet {
	miniAppApprovals.mu.Lock()
	defer miniAppApprovals.mu.Unlock()
	set, found := miniAppApprovals.sets[d]
	if !found {
		set = &miniAppApprovalSet{pending: map[string]*miniAppApproval{}}
		miniAppApprovals.sets[d] = set
	}
	return set
}

// pruneMiniAppApprovalsLocked turns expired live entries into tombstones and drops tombstones that
// are older than their grace. The caller holds set.mu.
func pruneMiniAppApprovalsLocked(set *miniAppApprovalSet, now time.Time) {
	for token, pending := range set.pending {
		if !pending.used {
			if now.Before(pending.ExpiresAt) {
				continue
			}
			pending.used = true
		}
		if now.After(pending.ExpiresAt.Add(miniAppApprovalTombstone)) {
			delete(set.pending, token)
		}
	}
}

func countMiniAppApprovalsLocked(set *miniAppApprovalSet, userID int64) (user, global int) {
	for _, pending := range set.pending {
		if pending.used {
			continue
		}
		global++
		if pending.UserID == userID {
			user++
		}
	}
	return user, global
}

func activeMiniAppApprovals(d *Dispatcher) int {
	set := miniAppApprovalsFor(d)
	set.mu.Lock()
	defer set.mu.Unlock()
	_, global := countMiniAppApprovalsLocked(set, 0)
	return global
}

// miniAppApprovalSummaryText is the bounded, sanitized tool-argument text of the approval card. It
// never contains a configured secret, and control characters cannot break the SSE framing.
func (d *Dispatcher) miniAppApprovalSummaryText(call model.ToolCall) string {
	text := strings.TrimSpace(call.Arguments)
	if text == "" {
		text = "{}"
	}
	text = d.sanitizeMiniAppError(errors.New(text))
	text = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, text)
	runes := []rune(text)
	if len(runes) > miniAppApprovalSummary {
		return string(runes[:miniAppApprovalSummary]) + "…"
	}
	return text
}

// registerMiniAppApproval stores one pending approval and enforces the caps. The caller must call
// retireMiniAppApproval when its waiter stops, whatever the reason.
func (d *Dispatcher) registerMiniAppApproval(token string, pending *miniAppApproval) error {
	set := miniAppApprovalsFor(d)
	set.mu.Lock()
	defer set.mu.Unlock()
	now := time.Now()
	pruneMiniAppApprovalsLocked(set, now)
	user, global := countMiniAppApprovalsLocked(set, pending.UserID)
	if user >= miniAppApprovalUserCap || global >= miniAppApprovalGlobalCap {
		return errMiniAppApprovalBusy
	}
	set.pending[token] = pending
	return nil
}

// retireMiniAppApproval turns the live entry into a tombstone if the waiter is leaving without a
// decision (timeout or client disconnect). The decision endpoint may already have marked it used.
func retireMiniAppApproval(d *Dispatcher, token string, pending *miniAppApproval) {
	set := miniAppApprovalsFor(d)
	set.mu.Lock()
	if current, found := set.pending[token]; found && current == pending {
		current.used = true
	}
	set.mu.Unlock()
}

// ResolveMiniAppApproval delivers the Mini App user's decision to the waiting stream. It never
// blocks: the waiter owns the response, this method only wakes it.
//
// The chat/user pair must equal the pair the token was created for; a mismatch is reported as an
// unknown token (404 in the web layer) so another session cannot probe a valid token.
func (d *Dispatcher) ResolveMiniAppApproval(ctx context.Context, token string, chatID, userID int64, approve bool) error {
	_ = ctx
	if d == nil || token == "" {
		return ErrMiniAppApprovalNotFound
	}
	set := miniAppApprovalsFor(d)
	set.mu.Lock()
	now := time.Now()
	pruneMiniAppApprovalsLocked(set, now)
	pending, found := set.pending[token]
	if !found {
		set.mu.Unlock()
		return ErrMiniAppApprovalNotFound
	}
	if pending.ChatID != chatID || pending.UserID != userID {
		set.mu.Unlock()
		return ErrMiniAppApprovalNotFound
	}
	if pending.used || !now.Before(pending.ExpiresAt) {
		pending.used = true
		set.mu.Unlock()
		return ErrMiniAppApprovalExpired
	}
	pending.used = true
	set.mu.Unlock()
	select {
	case pending.decision <- approve:
	default:
		// The waiter is gone (or its buffered slot is already taken). The token is used either way.
	}
	return nil
}

// awaitMiniAppApproval is the approvalWaiter installed on the Mini App continuation. It must run on
// the goroutine that owns the SSE response: the stream stays open while it waits.
func (d *Dispatcher) awaitMiniAppApproval(ctx context.Context, emit func(MiniAppStreamEvent) error, chatID, userID int64, call model.ToolCall) (bool, error) {
	token, err := newDeleteConfirmationToken()
	if err != nil {
		return false, err
	}
	now := time.Now()
	pending := &miniAppApproval{
		ChatID: chatID, UserID: userID, Tool: call.Name, Arguments: call.Arguments,
		ExpiresAt: now.Add(MiniAppApprovalTimeout), decision: make(chan bool, 1),
	}
	if err := d.registerMiniAppApproval(token, pending); err != nil {
		return false, err
	}
	defer retireMiniAppApproval(d, token, pending)

	seconds := int(MiniAppApprovalTimeout / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	if emit != nil {
		if err := emit(MiniAppStreamEvent{Approval: &MiniAppApprovalEvent{
			Token: token, Tool: call.Name, Summary: d.miniAppApprovalSummaryText(call),
			ExpiresInSeconds: seconds,
		}}); err != nil {
			// The client is already gone: stop immediately instead of waiting for a decision that
			// has nowhere to go.
			return false, err
		}
	}

	delay := time.Until(pending.ExpiresAt)
	if delay <= 0 {
		return false, fmt.Errorf("%w: no decision within %s", errMiniAppApprovalTimeout, MiniAppApprovalTimeout)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case approved := <-pending.decision:
		return approved, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
		return false, fmt.Errorf("%w: no decision within %s", errMiniAppApprovalTimeout, MiniAppApprovalTimeout)
	}
}
