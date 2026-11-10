package bot

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestDispatchersDoNotShareApprovalOrDocumentState — both registries used to be package-level maps keyed by
// the *Dispatcher. Two dispatchers in one process (production has one, tests have hundreds) must not see each
// other's pending Mini App approvals or document confirmations, and each set must live on its own dispatcher.
func TestDispatchersDoNotShareApprovalOrDocumentState(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	d1 := newTestDispatcher(t, cfg, nil, nil, testStoreNamed(t, "registry_one"))
	d2 := newTestDispatcher(t, cfg, nil, nil, testStoreNamed(t, "registry_two"))
	ctx := context.Background()

	if miniAppApprovalsFor(d1) == miniAppApprovalsFor(d2) {
		t.Fatal("two dispatchers share one Mini App approval set")
	}
	if documentConfirmationsFor(d1) == documentConfirmationsFor(d2) {
		t.Fatal("two dispatchers share one document confirmation set")
	}

	pending := &miniAppApproval{
		ChatID: 100, UserID: 11, Tool: "read_url",
		ExpiresAt: time.Now().Add(time.Minute), decision: make(chan bool, 1),
	}
	if err := d1.registerMiniAppApproval("approval-owner-token", pending); err != nil {
		t.Fatalf("registerMiniAppApproval: %v", err)
	}
	if activeMiniAppApprovals(d1) != 1 || activeMiniAppApprovals(d2) != 0 {
		t.Fatalf("approval counts leaked across dispatchers: d1=%d d2=%d",
			activeMiniAppApprovals(d1), activeMiniAppApprovals(d2))
	}
	if err := d2.ResolveMiniAppApproval(ctx, "approval-owner-token", 100, 11, true); !errors.Is(err, ErrMiniAppApprovalNotFound) {
		t.Fatalf("another dispatcher resolved the approval: %v", err)
	}
	if err := d1.ResolveMiniAppApproval(ctx, "approval-owner-token", 100, 11, true); err != nil {
		t.Fatalf("the owning dispatcher could not resolve its approval: %v", err)
	}

	ev := testEvent(100, 11, 2100, "")
	d1.rememberDocumentConfirmation("document-owner-token", ev, "delete", 7)
	if _, status := consumeDocumentConfirmation(d2, "document-owner-token", ev); status != deleteConfirmationStale {
		t.Fatalf("another dispatcher consumed the document confirmation: status=%v", status)
	}
	confirmation, status := consumeDocumentConfirmation(d1, "document-owner-token", ev)
	if status != deleteConfirmationValid || confirmation.DocumentID != 7 {
		t.Fatalf("the owning dispatcher lost its confirmation: %+v status=%v", confirmation, status)
	}
}
