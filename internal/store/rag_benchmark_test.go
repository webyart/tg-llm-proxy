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
	for i := 0; i < 9999; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO rag_chunks (document_id, chat_id, ordinal, content) VALUES (?, ?, ?, ?)`, documentID, 900, i, fmt.Sprintf("%s %d", content, i)); err != nil {
			tx.Rollback()
			b.Fatalf("insert chunk %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("Commit: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		chunks, err := SearchRAGChunks(ctx, db, 900, "needle", 5, 2000)
		if err != nil {
			b.Fatalf("SearchRAGChunks: %v", err)
		}
		if len(chunks) != 1 || chunks[0].Name != "match.txt" {
			b.Fatalf("unexpected result: %+v", chunks)
		}
	}
}
