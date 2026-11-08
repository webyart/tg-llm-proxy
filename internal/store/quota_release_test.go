package store

import (
	"context"
	"testing"
	"time"
)

// TestReleaseDailyQuotaReturnsTheLastReservation — `used` carries CHECK (used > 0), so releasing the only
// reservation has to delete the row instead of writing a zero the schema refuses.
func TestReleaseDailyQuotaReturnsTheLastReservation(t *testing.T) {
	var ctx = context.Background()
	var db, err = OpenMemory(ctx, "quota-release")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	var now = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	var limit = int64(5)
	for i := 0; i < 3; i++ {
		admitted, reserveErr := ReserveDailyQuota(ctx, db, 11, now, DailyQuotaModel, limit)
		if reserveErr != nil || !admitted {
			t.Fatalf("reserve %d: admitted=%v err=%v", i+1, admitted, reserveErr)
		}
	}
	if err := ReleaseDailyQuota(ctx, db, 11, now, DailyQuotaModel); err != nil {
		t.Fatalf("ReleaseDailyQuota: %v", err)
	}
	used, err := DailyQuotaUsed(ctx, db, 11, now, DailyQuotaModel)
	if err != nil || used != 2 {
		t.Fatalf("used after one release = %d err=%v, want 2", used, err)
	}

	for i := 0; i < 2; i++ {
		if err := ReleaseDailyQuota(ctx, db, 11, now, DailyQuotaModel); err != nil {
			t.Fatalf("release %d: %v", i+1, err)
		}
	}
	used, err = DailyQuotaUsed(ctx, db, 11, now, DailyQuotaModel)
	if err != nil || used != 0 {
		t.Fatalf("used after all releases = %d err=%v, want 0", used, err)
	}

	// Releasing an empty day is a no-op, not an error: a refund can race another refund.
	if err := ReleaseDailyQuota(ctx, db, 11, now, DailyQuotaModel); err != nil {
		t.Fatalf("release on an empty day: %v", err)
	}
	// And the budget is fully usable again.
	for i := 0; i < 5; i++ {
		admitted, reserveErr := ReserveDailyQuota(ctx, db, 11, now, DailyQuotaModel, limit)
		if reserveErr != nil || !admitted {
			t.Fatalf("reserve after refund %d: admitted=%v err=%v", i+1, admitted, reserveErr)
		}
	}
	if admitted, reserveErr := ReserveDailyQuota(ctx, db, 11, now, DailyQuotaModel, limit); reserveErr != nil || admitted {
		t.Fatalf("the budget was not restored: admitted=%v err=%v", admitted, reserveErr)
	}
}

// TestReleaseDailyQuotaKeepsClassesApart: the model budget and the network-tool budget are separate rows of
// the same day, and a refund of one must not touch the other.
func TestReleaseDailyQuotaKeepsClassesApart(t *testing.T) {
	var ctx = context.Background()
	var db, err = OpenMemory(ctx, "quota-release-classes")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	var now = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	if _, err := ReserveDailyQuota(ctx, db, 11, now, DailyQuotaModel, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := ReserveDailyQuota(ctx, db, 11, now, DailyQuotaNetworkTools, 5); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseDailyQuota(ctx, db, 11, now, DailyQuotaModel); err != nil {
		t.Fatalf("ReleaseDailyQuota: %v", err)
	}
	model, err := DailyQuotaUsed(ctx, db, 11, now, DailyQuotaModel)
	if err != nil || model != 0 {
		t.Fatalf("model used = %d err=%v, want 0", model, err)
	}
	network, err := DailyQuotaUsed(ctx, db, 11, now, DailyQuotaNetworkTools)
	if err != nil || network != 1 {
		t.Fatalf("network used = %d err=%v, want the untouched 1", network, err)
	}
}
