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
	if _, _, err := PutRAGDocument(ctx, db, 90, 11, "note", "текст"); err != nil {
		t.Fatalf("PutRAGDocument: %v", err)
	}
	stats, err := AdminStats(ctx, db)
	if err != nil || stats.RAGUses != 1 || stats.CalculatorUses != 1 || stats.RAGDocuments != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}
