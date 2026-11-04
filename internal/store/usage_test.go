package store

import (
	"context"
	"testing"
)

func TestAdminUsageMetricsContainOnlyAggregates(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "usage_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if err := LogEvent(ctx, db, 11, EventUsage, "rag"); err != nil {
		t.Fatalf("rag event: %v", err)
	}
	if err := LogEvent(ctx, db, 11, EventUsage, "calculator"); err != nil {
		t.Fatalf("calculator event: %v", err)
	}
	if err := LogEvent(ctx, db, 11, EventUsage, "read_url"); err != nil {
		t.Fatalf("read_url event: %v", err)
	}
	if err := LogEvent(ctx, db, 11, EventUsage, "web_search"); err != nil {
		t.Fatalf("web_search event: %v", err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 90, 11, "note", "текст"); err != nil {
		t.Fatalf("PutRAGDocument: %v", err)
	}
	stats, err := AdminStats(ctx, db)
	if err != nil {
		t.Fatalf("AdminStats: %v", err)
	}
	for _, counter := range []struct {
		name string
		got  int64
	}{
		{"RAGUses", stats.RAGUses},
		{"CalculatorUses", stats.CalculatorUses},
		{"URLReaderUses", stats.URLReaderUses},
		{"WebSearchUses", stats.WebSearchUses},
		{"RAGDocuments", stats.RAGDocuments},
	} {
		if counter.got != 1 {
			t.Errorf("%s=%d, want 1: stats=%+v", counter.name, counter.got, stats)
		}
	}
}
