package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReserveDailyQuotaBoundariesUTCClassesAndUnlimited(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 3, 2, 20, 0, 0, 0, time.FixedZone("test", 5*60*60))

	for attempt := 0; attempt < 2; attempt++ {
		admitted, err := ReserveDailyQuota(ctx, db, 11, now, DailyQuotaModel, 2)
		if err != nil || !admitted {
			t.Fatalf("reservation %d admitted=%v err=%v", attempt, admitted, err)
		}
	}
	admitted, err := ReserveDailyQuota(ctx, db, 11, now, DailyQuotaModel, 2)
	if err != nil || admitted {
		t.Fatalf("over-limit reservation admitted=%v err=%v", admitted, err)
	}
	used, err := DailyQuotaUsed(ctx, db, 11, now, DailyQuotaModel)
	if err != nil || used != 2 {
		t.Fatalf("used=%d err=%v, want 2", used, err)
	}
	admitted, err = ReserveDailyQuota(ctx, db, 11, now, DailyQuotaNetworkTools, 1)
	if err != nil || !admitted {
		t.Fatalf("independent class admitted=%v err=%v", admitted, err)
	}
	admitted, err = ReserveDailyQuota(ctx, db, 12, now, DailyQuotaModel, 1)
	if err != nil || !admitted {
		t.Fatalf("independent user admitted=%v err=%v", admitted, err)
	}
	admitted, err = ReserveDailyQuota(ctx, db, 12, now, DailyQuotaModel, 0)
	if err != nil || !admitted {
		t.Fatalf("unlimited reservation admitted=%v err=%v", admitted, err)
	}
	used, err = DailyQuotaUsed(ctx, db, 12, now, DailyQuotaModel)
	if err != nil || used != 1 {
		t.Fatalf("unlimited user stored a reservation: used=%d err=%v, want only the earlier limited call", used, err)
	}
	admitted, err = ReserveDailyQuota(ctx, db, 11, now.Add(24*time.Hour), DailyQuotaModel, 1)
	if err != nil || !admitted {
		t.Fatalf("next UTC day admitted=%v err=%v", admitted, err)
	}
	if _, err := ReserveDailyQuota(ctx, db, 11, now, DailyQuotaModel, -1); err == nil {
		t.Fatal("negative limit was accepted")
	}
}

func TestReserveDailyQuotaConcurrentSameUserCannotExceedCap(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const (
		attempts = 32
		limit    = 7
	)
	now := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	var admitted atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, attempts)
	for attempt := 0; attempt < attempts; attempt++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := ReserveDailyQuota(ctx, db, 11, now, DailyQuotaModel, limit)
			if err != nil {
				errs <- err
				return
			}
			if ok {
				admitted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent reservation: %v", err)
	}
	if admitted.Load() != limit {
		t.Fatalf("admitted=%d, want %d", admitted.Load(), limit)
	}
	used, err := DailyQuotaUsed(ctx, db, 11, now, DailyQuotaModel)
	if err != nil || used != limit {
		t.Fatalf("used=%d err=%v, want %d", used, err, limit)
	}
}
