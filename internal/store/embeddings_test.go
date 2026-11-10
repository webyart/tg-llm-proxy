package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// embeddingFixture stores one document of several chunks and returns the chat, the document and the chunks.
func embeddingFixture(tb testing.TB, dbName string) (context.Context, *sql.DB, int64, int64, []RAGEmbeddingChunk) {
	tb.Helper()
	ctx := context.Background()
	db, err := OpenMemory(ctx, dbName)
	if err != nil {
		tb.Fatalf("OpenMemory: %v", err)
	}
	tb.Cleanup(func() { db.Close() })
	const chatID = int64(700)
	var content strings.Builder
	for index := 0; index < 40; index++ {
		fmt.Fprintf(&content, "Предложение номер %d описывает порядок работы и проверки. ", index)
	}
	documentID, chunks, err := PutRAGDocument(ctx, db, chatID, 11, "embedding.txt", content.String())
	if err != nil {
		tb.Fatalf("PutRAGDocument: %v", err)
	}
	if chunks < 2 {
		tb.Fatalf("the fixture produced %d chunk(s), want at least 2", chunks)
	}
	targets, err := DocumentEmbeddingTargets(ctx, db, documentID)
	if err != nil {
		tb.Fatalf("DocumentEmbeddingTargets: %v", err)
	}
	if len(targets) != chunks {
		tb.Fatalf("targets=%d, chunks=%d", len(targets), chunks)
	}
	return ctx, db, chatID, documentID, targets
}

func TestChunkEmbeddingRoundTrip(t *testing.T) {
	ctx, db, chatID, _, targets := embeddingFixture(t, "embedding_roundtrip_"+t.Name())
	const model = "test-embed"
	vectors := map[int64][]float32{
		targets[0].ID: {0.25, -1.5, 3, 0},
		targets[1].ID: {1, 2, 3, 4},
	}
	for id, vector := range vectors {
		if err := PutChunkEmbedding(ctx, db, chatID, id, model, vector); err != nil {
			t.Fatalf("PutChunkEmbedding(%d): %v", id, err)
		}
	}
	loaded, err := ChunkEmbeddings(ctx, db, chatID, model, []int64{targets[0].ID, targets[1].ID, targets[2].ID})
	if err != nil {
		t.Fatalf("ChunkEmbeddings: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d vectors, want 2 (the third chunk has none)", len(loaded))
	}
	for id, want := range vectors {
		if got := loaded[id]; !reflect.DeepEqual(got, want) {
			t.Errorf("vector of chunk %d = %v, want %v", id, got, want)
		}
	}
	embedded, total, err := EmbeddingStats(ctx, db, chatID, model)
	if err != nil || embedded != 2 || total != len(targets) {
		t.Fatalf("stats=%d/%d err=%v, want 2/%d", embedded, total, err, len(targets))
	}
}

func TestChunkEmbeddingReplacesAndCountsPerModel(t *testing.T) {
	ctx, db, chatID, _, targets := embeddingFixture(t, "embedding_replace_"+t.Name())
	id := targets[0].ID
	if err := PutChunkEmbedding(ctx, db, chatID, id, "model-a", []float32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	// Replacing the same model with the same dimension updates the row.
	if err := PutChunkEmbedding(ctx, db, chatID, id, "model-a", []float32{4, 5, 6}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	loaded, err := ChunkEmbeddings(ctx, db, chatID, "model-a", []int64{id})
	if err != nil || len(loaded) != 1 || !reflect.DeepEqual(loaded[id], []float32{4, 5, 6}) {
		t.Fatalf("loaded=%v err=%v, want the three-dimensional replacement", loaded, err)
	}
	if embedded, _, _ := EmbeddingStats(ctx, db, chatID, "model-a"); embedded != 1 {
		t.Errorf("model-a embedded=%d, want 1", embedded)
	}
	if embedded, _, _ := EmbeddingStats(ctx, db, chatID, "model-b"); embedded != 0 {
		t.Errorf("model-b embedded=%d, want 0", embedded)
	}
}

func TestChunkEmbeddingRejectsWrongDimension(t *testing.T) {
	ctx, db, chatID, _, targets := embeddingFixture(t, "embedding_dimension_"+t.Name())
	id := targets[0].ID
	if err := PutChunkEmbeddingWithDimension(ctx, db, chatID, id, "model-a", 3, []float32{1, 2}); err == nil {
		t.Fatal("a vector shorter than the configured dimension must be refused")
	} else if !strings.Contains(err.Error(), "3 dimensions") || !strings.Contains(err.Error(), "got 2") {
		t.Fatalf("the wrong-length error does not name both dimensions: %v", err)
	}
	if embedded, _, _ := EmbeddingStats(ctx, db, chatID, "model-a"); embedded != 0 {
		t.Fatalf("a rejected vector was stored: embedded=%d", embedded)
	}

	if err := PutChunkEmbeddingWithDimension(ctx, db, chatID, id, "model-a", 3, []float32{1, 2, 3}); err != nil {
		t.Fatalf("the configured dimension must accept the matching vector: %v", err)
	}
	// Once the model is registered, a later write of another length is rejected even without the caller's
	// dimension. The valid row stays untouched.
	if err := PutChunkEmbedding(ctx, db, chatID, id, "model-a", []float32{4, 5}); err == nil {
		t.Fatal("a vector of another length for an already registered model must be refused")
	}
	loaded, err := ChunkEmbeddings(ctx, db, chatID, "model-a", []int64{id})
	if err != nil || !reflect.DeepEqual(loaded[id], []float32{1, 2, 3}) {
		t.Fatalf("the rejected write changed the stored vector: loaded=%v err=%v", loaded, err)
	}
}

func TestChunkEmbeddingKeepsVectorsOfEveryModel(t *testing.T) {
	ctx, db, chatID, _, targets := embeddingFixture(t, "embedding_models_"+t.Name())
	id := targets[0].ID
	if err := PutChunkEmbedding(ctx, db, chatID, id, "model-a", []float32{1, 2}); err != nil {
		t.Fatal(err)
	}
	// A model change must add a vector, not replace the old model's row. Different dimensions are legal
	// across models: the dimension is a property of the model, not of the chunk.
	if err := PutChunkEmbedding(ctx, db, chatID, id, "model-b", []float32{3, 4, 5}); err != nil {
		t.Fatalf("store the second model's vector: %v", err)
	}
	a, err := ChunkEmbeddings(ctx, db, chatID, "model-a", []int64{id})
	if err != nil || !reflect.DeepEqual(a[id], []float32{1, 2}) {
		t.Fatalf("model-a vector disappeared after the model change: %v err=%v", a, err)
	}
	b, err := ChunkEmbeddings(ctx, db, chatID, "model-b", []int64{id})
	if err != nil || !reflect.DeepEqual(b[id], []float32{3, 4, 5}) {
		t.Fatalf("model-b vector = %v err=%v", b, err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rag_chunk_embeddings WHERE chunk_id = ?`, id).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("rows of chunk %d = %d err=%v, want one per model", id, rows, err)
	}
	// The backfill of a third model treats the chunk as missing; the model-a/model-b rows are never reused
	// as a model-c vector. Conversely, model-b must not list the chunk after its own row was stored.
	missing, err := ChunksMissingEmbedding(ctx, db, chatID, "model-c", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range missing {
		found = found || row.ID == id
	}
	if !found {
		t.Fatalf("model-c backfill did not list chunk %d: %+v", id, missing)
	}
	modelBMissing, err := ChunksMissingEmbedding(ctx, db, chatID, "model-b", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range modelBMissing {
		if row.ID == id {
			t.Fatalf("model-b backfill listed chunk %d although its model-b vector exists", id)
		}
	}
}

// TestMigration018PreservesLegacyVectors: a database written before 018 has one row per chunk (016's
// primary key). The additive rebuild must copy it without changing model, dimensions, vector or created_at.
func TestMigration018PreservesLegacyVectors(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 17)
	const chatID = int64(701)
	if err := EnsureChat(ctx, db, chatID, 11, "legacy"); err != nil {
		t.Fatal(err)
	}
	documentID, chunks, err := PutRAGDocument(ctx, db, chatID, 11, "legacy.txt", "старый текст для вектора")
	if err != nil || chunks == 0 {
		t.Fatalf("PutRAGDocument chunks=%d err=%v", chunks, err)
	}
	targets, err := DocumentEmbeddingTargets(ctx, db, documentID)
	if err != nil || len(targets) == 0 {
		t.Fatalf("DocumentEmbeddingTargets=%d err=%v", len(targets), err)
	}
	legacyVector := encodeVector([]float32{0.5, -0.25, 1})
	if _, err := db.ExecContext(ctx, `
		INSERT INTO rag_chunk_embeddings (chunk_id, chat_id, model, dimensions, vector, created_at)
		VALUES (?, ?, 'legacy-model', 3, ?, '2026-01-02 03:04:05')`, targets[0].ID, chatID, legacyVector); err != nil {
		t.Fatalf("seed a 016 row: %v", err)
	}

	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("upgrade to 018: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) {
		t.Fatalf("SchemaVersion=%d err=%v, want %d", version, err, len(MigrationSQL))
	}
	var created string
	var model string
	if err := db.QueryRowContext(ctx, `SELECT model, created_at FROM rag_chunk_embeddings WHERE chunk_id = ?`, targets[0].ID).
		Scan(&model, &created); err != nil {
		t.Fatalf("read the migrated legacy row: %v", err)
	}
	if model != "legacy-model" || created != "2026-01-02 03:04:05" {
		t.Fatalf("migrated row model=%q created_at=%q, want the original values", model, created)
	}
	loaded, err := ChunkEmbeddings(ctx, db, chatID, "legacy-model", []int64{targets[0].ID})
	if err != nil || !reflect.DeepEqual(loaded[targets[0].ID], []float32{0.5, -0.25, 1}) {
		t.Fatalf("legacy vector unreadable after 018: %v err=%v", loaded, err)
	}
	if err := PutChunkEmbedding(ctx, db, chatID, targets[0].ID, "new-model", []float32{1, 2}); err != nil {
		t.Fatalf("a new model after 018: %v", err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rag_chunk_embeddings WHERE chunk_id = ?`, targets[0].ID).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("legacy + new rows = %d err=%v, want 2", rows, err)
	}
}

func TestChunkEmbeddingRejectsForeignChatAndEmptyVector(t *testing.T) {
	ctx, db, chatID, _, targets := embeddingFixture(t, "embedding_reject_"+t.Name())
	id := targets[0].ID
	if err := PutChunkEmbedding(ctx, db, chatID+1, id, "test", []float32{1}); err == nil {
		t.Error("a chunk of another chat must not accept an embedding")
	}
	if err := PutChunkEmbedding(ctx, db, chatID, id, "test", nil); err == nil {
		t.Error("an empty vector must be refused")
	}
	if err := PutChunkEmbedding(ctx, db, chatID, id, "  ", []float32{1}); err == nil {
		t.Error("an empty model id must be refused")
	}
	if err := PutChunkEmbedding(ctx, db, chatID, id+100000, "test", []float32{1}); err == nil {
		t.Error("a missing chunk must be refused")
	}
}

func TestChunkEmbeddingsCascadeWithChunks(t *testing.T) {
	ctx, db, chatID, documentID, targets := embeddingFixture(t, "embedding_cascade_"+t.Name())
	for _, target := range targets {
		if err := PutChunkEmbedding(ctx, db, chatID, target.ID, "test", []float32{1, 2}); err != nil {
			t.Fatal(err)
		}
	}
	countEmbeddings := func() int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rag_chunk_embeddings WHERE chat_id = ?`, chatID).Scan(&count); err != nil {
			t.Fatalf("count embeddings: %v", err)
		}
		return count
	}
	if count := countEmbeddings(); count != len(targets) {
		t.Fatalf("embeddings=%d, want %d", count, len(targets))
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM rag_chunks WHERE id = ?`, targets[0].ID); err != nil {
		t.Fatalf("delete chunk: %v", err)
	}
	if count := countEmbeddings(); count != len(targets)-1 {
		t.Fatalf("after deleting one chunk embeddings=%d, want %d", count, len(targets)-1)
	}
	deleted, err := DeleteRAGDocument(ctx, db, chatID, documentID)
	if err != nil || !deleted {
		t.Fatalf("DeleteRAGDocument=%v err=%v", deleted, err)
	}
	if count := countEmbeddings(); count != 0 {
		t.Fatalf("after deleting the document embeddings=%d, want 0", count)
	}
}

func TestChunksMissingEmbedding(t *testing.T) {
	ctx, db, chatID, _, targets := embeddingFixture(t, "embedding_missing_"+t.Name())
	const model = "test"
	missing, err := ChunksMissingEmbedding(ctx, db, chatID, model, 100)
	if err != nil || len(missing) != len(targets) {
		t.Fatalf("missing=%d err=%v, want all %d", len(missing), err, len(targets))
	}
	if missing[0].ID != targets[0].ID || missing[0].Content == "" || missing[0].Name != "embedding.txt" {
		t.Errorf("first missing chunk=%+v, want the first target with content and name", missing[0])
	}
	if err := PutChunkEmbedding(ctx, db, chatID, targets[0].ID, model, []float32{1}); err != nil {
		t.Fatal(err)
	}
	missing, err = ChunksMissingEmbedding(ctx, db, chatID, model, 100)
	if err != nil || len(missing) != len(targets)-1 {
		t.Fatalf("after one vector missing=%d err=%v, want %d", len(missing), err, len(targets)-1)
	}
	limited, err := ChunksMissingEmbedding(ctx, db, chatID, model, 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limit 1 returned %d rows err=%v", len(limited), err)
	}
	if none, err := ChunksMissingEmbedding(ctx, db, chatID, model, 0); err != nil || none != nil {
		t.Fatalf("limit 0 returned %v err=%v, want nothing", none, err)
	}
	// A vector of another model does not satisfy this model's backfill.
	still, err := ChunksMissingEmbedding(ctx, db, chatID, "other-model", 100)
	if err != nil || len(still) != len(targets) {
		t.Fatalf("another model: missing=%d err=%v, want %d", len(still), err, len(targets))
	}
}

func TestRAGChatIDs(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "embedding_chats_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for _, chatID := range []int64{302, 301} {
		if _, _, err := PutRAGDocument(ctx, db, chatID, 11, "doc.txt", "текст для чата"); err != nil {
			t.Fatalf("PutRAGDocument(%d): %v", chatID, err)
		}
	}
	ids, err := RAGChatIDs(ctx, db)
	if err != nil {
		t.Fatalf("RAGChatIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != 301 || ids[1] != 302 {
		t.Fatalf("ids=%v, want [301 302] in order", ids)
	}
}

func TestVectorEncodingRoundTrip(t *testing.T) {
	vectors := [][]float32{{0}, {1.5, -2.25, 0}, {1e10, -1e-10, 3.5, 7}}
	for _, vector := range vectors {
		blob := encodeVector(vector)
		if len(blob) != len(vector)*4 {
			t.Fatalf("blob length=%d, want %d", len(blob), len(vector)*4)
		}
		decoded, err := decodeVector(blob)
		if err != nil || !reflect.DeepEqual(decoded, vector) {
			t.Fatalf("decoded=%v err=%v, want %v", decoded, err, vector)
		}
	}
	if _, err := decodeVector([]byte{1, 2, 3}); err == nil {
		t.Fatal("a blob of three bytes must be refused")
	}
}
