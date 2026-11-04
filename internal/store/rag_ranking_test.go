package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRAGCustomLimitAllowsReplacement(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_custom_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocumentWithLimit(ctx, db, 100, 11, "one", "текст", 1); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if _, _, err := PutRAGDocumentWithLimit(ctx, db, 100, 11, "two", "текст", 1); !errors.Is(err, ErrRAGDocumentLimit) {
		t.Fatalf("limit err=%v", err)
	}
	if _, _, err := PutRAGDocumentWithLimit(ctx, db, 100, 11, "one", "обновление", 1); err != nil {
		t.Fatalf("replacement at limit: %v", err)
	}
}

func TestRAGRankingPrefersQueryCoverage(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_ranking_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocument(ctx, db, 101, 11, "repeat", strings.Repeat("alpha ", 20)); err != nil {
		t.Fatalf("repeat Put: %v", err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 101, 11, "coverage", "alpha beta gamma"); err != nil {
		t.Fatalf("coverage Put: %v", err)
	}
	chunks, err := SearchRAGChunks(ctx, db, 101, "alpha beta gamma", 2, 2000)
	if err != nil || len(chunks) != 2 || chunks[0].Name != "coverage" {
		t.Fatalf("ranking=%+v err=%v", chunks, err)
	}
}
