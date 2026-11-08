package store

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

// hybridFixture is a chat of two identical chunks. The lexical scorer gives them the same score, so their order
// is the id tie-break (the newer document first); the injected vectors point the other way, which is exactly the
// reordering the hybrid layer has to be able to perform.
func hybridFixture(tb testing.TB, name string) (context.Context, *sql.DB, int64) {
	tb.Helper()
	ctx := context.Background()
	db, err := OpenMemory(ctx, name)
	if err != nil {
		tb.Fatalf("OpenMemory: %v", err)
	}
	tb.Cleanup(func() { db.Close() })
	const chatID = int64(800)
	if _, _, err := PutRAGDocument(ctx, db, chatID, 11, "alpha.txt", "alpha beta"); err != nil {
		tb.Fatalf("Put alpha: %v", err)
	}
	if _, _, err := PutRAGDocument(ctx, db, chatID, 11, "beta.txt", "alpha beta"); err != nil {
		tb.Fatalf("Put beta: %v", err)
	}
	return ctx, db, chatID
}

func hybridDocumentID(t *testing.T, ctx context.Context, db *sql.DB, chatID int64, name string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM rag_documents WHERE chat_id = ? AND name = ?`, chatID, name).Scan(&id); err != nil {
		t.Fatalf("document %s: %v", name, err)
	}
	return id
}

// embedDocument stores one vector for every chunk of the document, which is what the ingestion path does.
func embedDocument(t *testing.T, ctx context.Context, db *sql.DB, chatID, documentID int64, model string, vectorFor func(string) []float32) {
	t.Helper()
	targets, err := DocumentEmbeddingTargets(ctx, db, documentID)
	if err != nil {
		t.Fatalf("DocumentEmbeddingTargets: %v", err)
	}
	for _, target := range targets {
		if err := PutChunkEmbedding(ctx, db, chatID, target.ID, model, vectorFor(target.Content)); err != nil {
			t.Fatalf("PutChunkEmbedding: %v", err)
		}
	}
}

func TestRAGHybridVectorReordersCandidates(t *testing.T) {
	ctx, db, chatID := hybridFixture(t, "rag_hybrid_reorder_"+t.Name())
	const model = "fake-2d"
	embedDocument(t, ctx, db, chatID, hybridDocumentID(t, ctx, db, chatID, "alpha.txt"), model, func(string) []float32 {
		return []float32{1, 0}
	})
	embedDocument(t, ctx, db, chatID, hybridDocumentID(t, ctx, db, chatID, "beta.txt"), model, func(string) []float32 {
		return []float32{0, 1}
	})

	lexical, err := SearchRAGChunksDetailed(ctx, db, chatID, "alpha beta", 5, 1<<20)
	if err != nil {
		t.Fatalf("lexical: %v", err)
	}
	if len(lexical.Chunks) != 2 || lexical.Chunks[0].Name != "beta.txt" {
		t.Fatalf("lexical ranking=%+v, want the newer tie-break first (beta.txt)", lexical.Chunks)
	}

	hybrid, err := SearchRAGChunksWithOptions(ctx, db, chatID, "alpha beta", SearchOptions{
		Limit: 5, MaxRunes: 1 << 20, QueryVector: []float32{1, 0}, EmbeddingModel: model, EmbeddingWeight: DefaultEmbeddingWeight,
	})
	if err != nil {
		t.Fatalf("hybrid: %v", err)
	}
	if len(hybrid.Chunks) != 2 || hybrid.Chunks[0].Name != "alpha.txt" {
		t.Fatalf("hybrid ranking=%+v, want the vector-aligned alpha.txt first", hybrid.Chunks)
	}
	for _, chunk := range hybrid.Chunks {
		if chunk.Score < 0 || chunk.Score > 100 {
			t.Errorf("hybrid score %d is outside the 0..100 scale", chunk.Score)
		}
	}
	if hybrid.Chunks[0].Score <= hybrid.Chunks[1].Score {
		t.Errorf("hybrid scores are not descending: %+v", hybrid.Chunks)
	}
	if hybrid.Chunks[0].Score == lexical.Chunks[0].Score {
		t.Errorf("hybrid score %d equals the lexical score, the scales differ", hybrid.Chunks[0].Score)
	}
}

func TestRAGHybridWithoutVectorsIsTheLexicalRanking(t *testing.T) {
	ctx, db, chatID := hybridFixture(t, "rag_hybrid_empty_"+t.Name())
	lexical, err := SearchRAGChunksDetailed(ctx, db, chatID, "alpha beta", 5, 1<<20)
	if err != nil {
		t.Fatalf("lexical: %v", err)
	}
	hybrid, err := SearchRAGChunksWithOptions(ctx, db, chatID, "alpha beta", SearchOptions{
		Limit: 5, MaxRunes: 1 << 20, QueryVector: []float32{1, 0}, EmbeddingModel: "no-vectors-yet", EmbeddingWeight: 0.4,
	})
	if err != nil {
		t.Fatalf("hybrid: %v", err)
	}
	if !reflect.DeepEqual(lexical, hybrid) {
		t.Fatalf("hybrid without vectors=%+v, want the lexical result %+v", hybrid, lexical)
	}
}

func TestRAGHybridSkipsVectorsOfAnotherDimension(t *testing.T) {
	ctx, db, chatID := hybridFixture(t, "rag_hybrid_dims_"+t.Name())
	// Three dimensions stored, a two-dimensional query: the row belongs to another model and contributes 0.
	embedDocument(t, ctx, db, chatID, hybridDocumentID(t, ctx, db, chatID, "alpha.txt"), "fake-3d", func(string) []float32 {
		return []float32{1, 0, 0}
	})
	lexical, err := SearchRAGChunksDetailed(ctx, db, chatID, "alpha beta", 5, 1<<20)
	if err != nil {
		t.Fatalf("lexical: %v", err)
	}
	hybrid, err := SearchRAGChunksWithOptions(ctx, db, chatID, "alpha beta", SearchOptions{
		Limit: 5, MaxRunes: 1 << 20, QueryVector: []float32{1, 0}, EmbeddingModel: "fake-3d", EmbeddingWeight: 0.4,
	})
	if err != nil {
		t.Fatalf("hybrid: %v", err)
	}
	if !reflect.DeepEqual(lexical, hybrid) {
		t.Fatalf("a dimension mismatch changed the ranking: %+v vs %+v", hybrid, lexical)
	}
}

func TestRAGHybridWeightBounds(t *testing.T) {
	ctx, db, chatID := hybridFixture(t, "rag_hybrid_weight_"+t.Name())
	// A negative weight is clamped to 0, and a zero weight is no hybrid layer at all: the lexical ranking.
	out, err := SearchRAGChunksWithOptions(ctx, db, chatID, "alpha beta", SearchOptions{
		Limit: 5, MaxRunes: 1 << 20, QueryVector: []float32{1, 0}, EmbeddingModel: "fake-2d", EmbeddingWeight: -1,
	})
	if err != nil {
		t.Fatalf("weight -1: %v", err)
	}
	lexical, err := SearchRAGChunksDetailed(ctx, db, chatID, "alpha beta", 5, 1<<20)
	if err != nil {
		t.Fatalf("lexical: %v", err)
	}
	if !reflect.DeepEqual(lexical, out) {
		t.Fatalf("weight -1=%+v, want the lexical result %+v", out, lexical)
	}
}
