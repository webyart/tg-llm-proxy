package store

import (
	"context"
	"testing"
	"time"
)

func TestResumeRecoveryStateIsPerQuestionAndResetWithSkip(t *testing.T) {
	db := memDB(t)
	ctx := context.Background()
	next := time.Date(2026, 11, 6, 12, 0, 0, 123456789, time.FixedZone("test", 2*60*60))
	if err := SetResumeRecovery(ctx, db, 70, 41, 2, next); err != nil {
		t.Fatalf("SetResumeRecovery: %v", err)
	}
	state, found, err := ResumeRecovery(ctx, db, 70, 41)
	if err != nil || !found {
		t.Fatalf("ResumeRecovery: found=%v err=%v", found, err)
	}
	if state.QuestionID != 41 || state.Attempts != 2 || !state.NextRecoveryAt.Equal(next) {
		t.Fatalf("recovery state = %+v", state)
	}
	if _, found, err := ResumeRecovery(ctx, db, 70, 42); err != nil || found {
		t.Fatalf("another question inherited recovery state: found=%v err=%v", found, err)
	}
	if err := ClearResumeRecovery(ctx, db, 70, 42); err != nil {
		t.Fatalf("ClearResumeRecovery for another question: %v", err)
	}
	if _, found, err := ResumeRecovery(ctx, db, 70, 41); err != nil || !found {
		t.Fatalf("clearing another question removed recovery state: found=%v err=%v", found, err)
	}
	if err := SetResumeSkip(ctx, db, 70, 41); err != nil {
		t.Fatalf("SetResumeSkip: %v", err)
	}
	if err := ClearResumeSkip(ctx, db, 70); err != nil {
		t.Fatalf("ClearResumeSkip: %v", err)
	}
	if _, found, err := ResumeSkip(ctx, db, 70); err != nil || found {
		t.Fatalf("skip remains after unskip: found=%v err=%v", found, err)
	}
	if _, found, err := ResumeRecovery(ctx, db, 70, 41); err != nil || found {
		t.Fatalf("backoff remains after unskip: found=%v err=%v", found, err)
	}
	if err := ClearResumeSkip(ctx, db, 70); err != nil {
		t.Fatalf("repeated ClearResumeSkip: %v", err)
	}
}

func TestResumeRecoveryRejectsMalformedState(t *testing.T) {
	db := memDB(t)
	ctx := context.Background()
	if err := SetState(ctx, db, StateResumeRecoveryPrefix+"70", "41|not-a-count|2026-11-06T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResumeRecovery(ctx, db, 70, 41); err == nil {
		t.Fatal("malformed recovery state must be reported")
	}
}
