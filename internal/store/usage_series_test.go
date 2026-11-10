package store

import (
	"context"
	"testing"
	"time"
)

func TestUserUsageSeriesHourBucketsAggregateUTC(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "usage_series_hour_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	appendAssistantAt(t, ctx, db, 100, 11, "model-a", 10, 20, "2026-03-02 18:05:00")
	appendAssistantAt(t, ctx, db, 100, 11, "model-a", 5, 7, "2026-03-02 18:45:00")
	appendAssistantAt(t, ctx, db, 100, 11, "model-b", 1, 2, "2026-03-01 19:00:00")
	appendAssistantAt(t, ctx, db, 100, 11, "model-a", 1000, 2000, "2026-03-01 18:59:59")
	appendAssistantAt(t, ctx, db, 200, 12, "model-a", 1000, 2000, "2026-03-02 18:05:00")

	for _, event := range []struct {
		userID    int64
		kind      string
		detail    string
		createdAt string
	}{
		{11, EventModelCall, "model-a", "2026-03-02 18:05:00"},
		{11, EventModelCall, "model-a", "2026-03-02 18:45:00"},
		{11, EventModelCall, "model-b", "2026-03-01 19:00:00"},
		{11, EventUsage, "read_url", "2026-03-02 18:05:00"},
		{11, EventUsage, "calculator", "2026-03-02 18:45:00"},
		{11, EventUsage, "private tool detail", "2026-03-02 18:05:00"},
		{11, EventUsage, "read_url", "2026-03-01 18:59:59"},
		{11, EventCommand, "read_url", "2026-03-02 18:05:00"},
		{12, EventModelCall, "model-a", "2026-03-02 18:05:00"},
		{12, EventUsage, "read_url", "2026-03-02 18:05:00"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO events (user_id, kind, detail, created_at) VALUES (?, ?, ?, ?)`,
			event.userID, event.kind, event.detail, event.createdAt); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}

	now := time.Date(2026, 3, 2, 18, 30, 0, 0, time.UTC)
	series, err := UserUsageSeries(ctx, db, 11, now, UsageRangeHour)
	if err != nil {
		t.Fatalf("UserUsageSeries: %v", err)
	}
	if series.Truncated {
		t.Error("hour range must not be truncated")
	}
	if len(series.Buckets) != 24 {
		t.Fatalf("buckets=%d, want 24", len(series.Buckets))
	}
	if got := series.Buckets[0].Start; !got.Equal(time.Date(2026, 3, 1, 19, 0, 0, 0, time.UTC)) {
		t.Errorf("first bucket=%s, want 2026-03-01T19:00:00Z", got)
	}
	last := series.Buckets[len(series.Buckets)-1]
	if !last.Start.Equal(time.Date(2026, 3, 2, 18, 0, 0, 0, time.UTC)) {
		t.Errorf("last bucket=%s, want 2026-03-02T18:00:00Z", last.Start)
	}
	for index, bucket := range series.Buckets {
		if bucket.Start.Location() != time.UTC || bucket.Start.Minute() != 0 || bucket.Start.Second() != 0 {
			t.Errorf("bucket %d start=%s is not an exact UTC hour", index, bucket.Start)
		}
		if len(bucket.Tools) != 4 {
			t.Errorf("bucket %d tools=%v, want the four fixed tool keys", index, bucket.Tools)
		}
	}
	if last.ModelCalls != 2 || last.TokensIn != 15 || last.TokensOut != 27 || last.ToolsTotal != 2 {
		t.Errorf("current hour bucket=%+v, want 2 calls, 15/27 tokens, 2 tools", last)
	}
	if last.Tools["read_url"] != 1 || last.Tools["calculator"] != 1 {
		t.Errorf("current hour tools=%v, want read_url=1 calculator=1", last.Tools)
	}
	first := series.Buckets[0]
	if first.ModelCalls != 1 || first.TokensIn != 1 || first.TokensOut != 2 {
		t.Errorf("first bucket=%+v, want 1 call and 1/2 tokens", first)
	}
	if series.ModelCalls != 3 || series.TokensIn != 16 || series.TokensOut != 29 || series.ToolsTotal != 2 {
		t.Errorf("range totals=%+v, want calls=3 tokens=16/29 tools=2", series)
	}
	if len(series.Models) != 2 {
		t.Fatalf("models=%+v, want model-a and model-b", series.Models)
	}
	if got := series.Models[0]; got.Model != "model-a" || got.Calls != 2 || got.TokensIn != 15 || got.TokensOut != 27 {
		t.Errorf("model-a=%+v, want 2 calls and 15/27 tokens", got)
	}
	if got := series.Models[1]; got.Model != "model-b" || got.Calls != 1 || got.TokensIn != 1 || got.TokensOut != 2 {
		t.Errorf("model-b=%+v, want 1 call and 1/2 tokens", got)
	}
	if _, ok := series.Tools["private tool detail"]; ok {
		t.Errorf("unknown usage detail leaked into tools: %v", series.Tools)
	}
	if series.Tools["read_url"] != 1 || series.Tools["calculator"] != 1 {
		t.Errorf("range tools=%v, want read_url=1 calculator=1", series.Tools)
	}
}

func TestUserUsageSeriesDayWeekMonthBuckets(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "usage_series_windows_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	for _, createdAt := range []string{
		"2026-03-02 00:00:00",
		"2026-03-01 23:59:59",
		"2026-02-24 00:00:00",
		"2026-02-23 23:59:59",
		"2026-02-22 23:59:59",
		"2025-04-01 00:00:00",
		"2025-03-31 23:59:59",
		"2026-03-02 18:00:00",
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO events (user_id, kind, detail, created_at) VALUES (?, ?, ?, ?)`,
			11, EventModelCall, "model-a", createdAt); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}

	now := time.Date(2026, 3, 2, 18, 30, 0, 0, time.UTC)
	tests := []struct {
		name        string
		rng         UsageRange
		buckets     int
		firstStart  time.Time
		lastStart   time.Time
		totalCalls  int64
		lastCalls   int64
		bucketCalls []int64
	}{
		{
			name: "day", rng: UsageRangeDay, buckets: 7,
			firstStart: time.Date(2026, 2, 24, 0, 0, 0, 0, time.UTC),
			lastStart:  time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
			totalCalls: 4, lastCalls: 2,
			bucketCalls: []int64{1, 0, 0, 0, 0, 1, 2},
		},
		{
			name: "week", rng: UsageRangeWeek, buckets: 8,
			firstStart: time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC),
			lastStart:  time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
			totalCalls: 6, lastCalls: 2,
			bucketCalls: []int64{0, 0, 0, 0, 0, 1, 3, 2},
		},
		{
			name: "month", rng: UsageRangeMonth, buckets: 12,
			firstStart: time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC),
			lastStart:  time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			totalCalls: 7, lastCalls: 3,
			bucketCalls: []int64{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 3},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			series, err := UserUsageSeries(ctx, db, 11, now, test.rng)
			if err != nil {
				t.Fatalf("UserUsageSeries: %v", err)
			}
			if series.Truncated {
				t.Error("public range must not be truncated")
			}
			if len(series.Buckets) != test.buckets {
				t.Fatalf("buckets=%d, want %d", len(series.Buckets), test.buckets)
			}
			if !series.Buckets[0].Start.Equal(test.firstStart) {
				t.Errorf("first bucket=%s, want %s", series.Buckets[0].Start, test.firstStart)
			}
			last := series.Buckets[len(series.Buckets)-1]
			if !last.Start.Equal(test.lastStart) {
				t.Errorf("last bucket=%s, want %s", last.Start, test.lastStart)
			}
			for index := 1; index < len(series.Buckets); index++ {
				previous := series.Buckets[index-1].Start
				var want time.Time
				switch test.rng {
				case UsageRangeDay:
					want = previous.AddDate(0, 0, 1)
				case UsageRangeWeek:
					want = previous.AddDate(0, 0, 7)
				case UsageRangeMonth:
					want = previous.AddDate(0, 1, 0)
				}
				if !series.Buckets[index].Start.Equal(want) {
					t.Errorf("bucket %d start=%s, want %s", index, series.Buckets[index].Start, want)
				}
			}
			if series.ModelCalls != test.totalCalls {
				t.Errorf("total calls=%d, want %d", series.ModelCalls, test.totalCalls)
			}
			if last.ModelCalls != test.lastCalls {
				t.Errorf("last bucket calls=%d, want %d", last.ModelCalls, test.lastCalls)
			}
			for index, want := range test.bucketCalls {
				if got := series.Buckets[index].ModelCalls; got != want {
					t.Errorf("bucket %d calls=%d, want %d", index, got, want)
				}
			}
		})
	}
}

func TestUserUsageSeriesEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "usage_series_empty_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 3, 2, 18, 30, 0, 0, time.UTC)
	for _, rng := range []UsageRange{UsageRangeHour, UsageRangeDay, UsageRangeWeek, UsageRangeMonth} {
		series, err := UserUsageSeries(ctx, db, 11, now, rng)
		if err != nil {
			t.Fatalf("UserUsageSeries(%s): %v", rng, err)
		}
		if len(series.Buckets) == 0 {
			t.Errorf("%s: no buckets", rng)
		}
		if len(series.Models) != 0 || series.ModelCalls != 0 || series.TokensIn != 0 || series.TokensOut != 0 || series.ToolsTotal != 0 {
			t.Errorf("%s: non-zero totals on an empty store: %+v", rng, series)
		}
		if len(series.Tools) != 4 {
			t.Errorf("%s: tools=%v, want four zero keys", rng, series.Tools)
		}
		for _, bucket := range series.Buckets {
			if bucket.ModelCalls != 0 || bucket.TokensIn != 0 || bucket.TokensOut != 0 || bucket.ToolsTotal != 0 || len(bucket.Tools) != 4 {
				t.Errorf("%s: non-zero empty bucket: %+v", rng, bucket)
			}
		}
		if series.Truncated {
			t.Errorf("%s: empty store reported truncation", rng)
		}
	}
}

func TestCapUsageSeriesSpecCapsBucketsAndWindow(t *testing.T) {
	now := time.Date(2026, 3, 2, 18, 0, 0, 0, time.UTC)
	starts := make([]time.Time, 40)
	for index := range starts {
		starts[index] = now.Add(time.Duration(index-39) * time.Hour)
	}
	spec, truncated := capUsageSeriesSpec(usageSeriesSpec{
		starts: starts, end: starts[len(starts)-1].Add(time.Hour), keyLayout: "2006-01-02 15:00:00", bucketSQL: "x",
	})
	if !truncated || len(spec.starts) != MaxUsageSeriesBuckets {
		t.Fatalf("bucket cap: truncated=%v buckets=%d, want true/%d", truncated, len(spec.starts), MaxUsageSeriesBuckets)
	}
	if !spec.starts[len(spec.starts)-1].Equal(now) {
		t.Errorf("bucket cap dropped the newest bucket: last=%s", spec.starts[len(spec.starts)-1])
	}

	long := make([]time.Time, 32)
	for index := range long {
		long[index] = now.AddDate(0, 0, -620+index*20)
	}
	spec, truncated = capUsageSeriesSpec(usageSeriesSpec{
		starts: long, end: now, keyLayout: "2006-01-02", bucketSQL: "x",
	})
	if !truncated {
		t.Error("window cap did not report truncation")
	}
	if len(spec.starts) >= len(long) {
		t.Errorf("window cap kept all %d buckets", len(spec.starts))
	}
	if span := spec.end.Sub(spec.starts[0]); span > MaxUsageSeriesWindow {
		t.Errorf("window after cap=%s, want <= %s", span, MaxUsageSeriesWindow)
	}
}

func TestParseUsageRange(t *testing.T) {
	for _, valid := range []UsageRange{UsageRangeHour, UsageRangeDay, UsageRangeWeek, UsageRangeMonth} {
		parsed, ok := ParseUsageRange(string(valid))
		if !ok || parsed != valid {
			t.Errorf("ParseUsageRange(%q)=%q/%v, want %q/true", valid, parsed, ok, valid)
		}
	}
	for _, invalid := range []string{"", "year", "HOUR", "day ", "hour&range=day"} {
		if _, ok := ParseUsageRange(invalid); ok {
			t.Errorf("ParseUsageRange(%q) accepted an invalid range", invalid)
		}
	}
}
