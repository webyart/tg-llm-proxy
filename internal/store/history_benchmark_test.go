package store

import (
	"context"
	"testing"
)

func BenchmarkSearchMessagesEarlyMatch(b *testing.B) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "history_search_benchmark_early")
	if err != nil {
		b.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	seedHistoryMessages(b, ctx, db, 78, 10000, func(index int) string {
		if index == 9999 {
			return "needle in the newest message"
		}
		return "ordinary message"
	})

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		matches, err := SearchMessages(ctx, db, 78, "needle", 1)
		if err != nil {
			b.Fatalf("SearchMessages: %v", err)
		}
		if len(matches) != 1 || matches[0].ID != 10000 {
			b.Fatalf("unexpected matches: %+v", matches)
		}
	}
}

func BenchmarkSearchMessagesFullScan(b *testing.B) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "history_search_benchmark_full_scan")
	if err != nil {
		b.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	seedHistoryMessages(b, ctx, db, 79, 10000, func(index int) string {
		if index == 9999 {
			return "needle in the newest message"
		}
		return "ordinary message"
	})

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		matches, err := SearchMessages(ctx, db, 79, "absent", 1)
		if err != nil {
			b.Fatalf("SearchMessages: %v", err)
		}
		if len(matches) != 0 {
			b.Fatalf("unexpected matches: %+v", matches)
		}
	}
}
