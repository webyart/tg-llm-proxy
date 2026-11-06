package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func BenchmarkSearchRAGChunksManyIrrelevant(b *testing.B) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_benchmark_many_irrelevant")
	if err != nil {
		b.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocument(ctx, db, 900, 11, "match.txt", "needle in the matching chunk"); err != nil {
		b.Fatalf("PutRAGDocument: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("BeginTx: %v", err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO rag_documents (chat_id, name, size_bytes) VALUES (?, ?, ?)`, 900, "irrelevant.txt", 0)
	if err != nil {
		tx.Rollback()
		b.Fatalf("insert document: %v", err)
	}
	documentID, err := result.LastInsertId()
	if err != nil {
		tx.Rollback()
		b.Fatalf("LastInsertId: %v", err)
	}
	content := strings.Repeat("ordinary unrelated vocabulary ", 40)
	chunks := make([]string, 9999)
	for index := range chunks {
		chunks[index] = fmt.Sprintf("%s %d", content, index)
	}
	if err := insertRAGChunks(ctx, tx, documentID, 900, chunks); err != nil {
		tx.Rollback()
		b.Fatalf("insert chunks: %v", err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("Commit: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := SearchRAGChunksDetailed(ctx, db, 900, "needle", 5, 2000)
		if err != nil {
			b.Fatalf("SearchRAGChunksDetailed: %v", err)
		}
		if result.TotalChunks != 10000 || result.ScannedChunks != 10000 || result.CorpusTruncated {
			b.Fatalf("unexpected corpus statistics: %+v", result)
		}
		if len(result.Chunks) != 1 || result.Chunks[0].Name != "match.txt" {
			b.Fatalf("unexpected result: %+v", result.Chunks)
		}
	}
}

func BenchmarkSearchRAGChunksManyTermTranscript(b *testing.B) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_benchmark_many_term_transcript")
	if err != nil {
		b.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for _, row := range []struct {
		name    string
		content string
	}{
		{name: "included.txt", content: "term0000 in the selected transcript"},
		{name: "excluded.txt", content: "term2047 beyond the query cap"},
	} {
		if _, _, err := PutRAGDocument(ctx, db, 901, 11, row.name, row.content); err != nil {
			b.Fatalf("PutRAGDocument: %v", err)
		}
	}
	var query strings.Builder
	for index := 0; index < 2048; index++ {
		fmt.Fprintf(&query, "term%04d transcript filler ", index)
	}
	if len([]rune(query.String())) <= RAGQueryMaxRunes {
		b.Fatal("benchmark query does not exercise the rune cap")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		chunks, err := SearchRAGChunks(ctx, db, 901, query.String(), 5, 2000)
		if err != nil {
			b.Fatalf("SearchRAGChunks: %v", err)
		}
		if len(chunks) != 1 || chunks[0].Name != "included.txt" {
			b.Fatalf("unexpected result: %+v", chunks)
		}
	}
}

func BenchmarkSearchRAGChunksAllMatch10000(b *testing.B) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_benchmark_all_match")
	if err != nil {
		b.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocument(ctx, db, 903, 11, "all-match.txt", "needle seed chunk"); err != nil {
		b.Fatalf("PutRAGDocument: %v", err)
	}
	chunks := make([]string, ragSearchChunkScanLimit)
	for index := range chunks {
		chunks[index] = fmt.Sprintf("needle all match transcript %d", index)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM rag_chunks WHERE document_id = 1`); err != nil {
		b.Fatalf("clear seed chunk: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("BeginTx: %v", err)
	}
	if err := insertRAGChunks(ctx, tx, 1, 903, chunks); err != nil {
		tx.Rollback()
		b.Fatalf("insert chunks: %v", err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("Commit: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result, err := SearchRAGChunksDetailed(ctx, db, 903, "needle", 5, 2000)
		if err != nil {
			b.Fatalf("SearchRAGChunksDetailed: %v", err)
		}
		if result.TotalChunks != ragSearchChunkScanLimit || len(result.Chunks) != 5 || result.Chunks[0].Content != "needle all match transcript 9999" {
			b.Fatalf("unexpected result: %+v", result)
		}
	}
}

func BenchmarkSearchRAGChunksCyrillicHeavy(b *testing.B) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_benchmark_cyrillic_heavy")
	if err != nil {
		b.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocument(ctx, db, 904, 11, "кириллица.txt", "ИГОЛКА в нужном фрагменте"); err != nil {
		b.Fatalf("PutRAGDocument: %v", err)
	}
	chunks := make([]string, ragSearchChunkScanLimit)
	chunks[0] = "ИГОЛКА в нужном фрагменте"
	content := strings.Repeat("Обычная совершенно посторонняя лексика ", 20)
	for index := 1; index < len(chunks); index++ {
		chunks[index] = fmt.Sprintf("%s %d", content, index)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM rag_chunks WHERE document_id = 1`); err != nil {
		b.Fatalf("clear seed chunk: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("BeginTx: %v", err)
	}
	if err := insertRAGChunks(ctx, tx, 1, 904, chunks); err != nil {
		tx.Rollback()
		b.Fatalf("insert chunks: %v", err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("Commit: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		chunks, err := SearchRAGChunks(ctx, db, 904, "иголка", 5, 2000)
		if err != nil {
			b.Fatalf("SearchRAGChunks: %v", err)
		}
		if len(chunks) != 1 || chunks[0].Content != "ИГОЛКА в нужном фрагменте" {
			b.Fatalf("unexpected result: %+v", chunks)
		}
	}
}

func BenchmarkPutRAGDocumentBatchedChunks(b *testing.B) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_benchmark_batched_insert")
	if err != nil {
		b.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	content := strings.Repeat("A sentence with enough RAG content to chunk. ", 3000)
	wantChunks := len(chunkRAGText(content))
	if wantChunks <= ragChunkInsertBatchSize {
		b.Fatalf("benchmark has only %d chunks", wantChunks)
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(content)))
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_, chunks, err := PutRAGDocument(ctx, db, 902, 11, "large.txt", content)
		if err != nil {
			b.Fatalf("PutRAGDocument: %v", err)
		}
		if chunks != wantChunks {
			b.Fatalf("chunks=%d, want %d", chunks, wantChunks)
		}
	}
}
