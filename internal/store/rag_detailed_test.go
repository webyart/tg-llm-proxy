package store

import (
	"context"
	"fmt"
	"testing"
)

func TestRAGSearchDetailedReportsTruncatedCorpus(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_detailed_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	oldID, _, err := PutRAGDocument(ctx, db, 130, 11, "old.txt", "needle in the oldest chunk")
	if err != nil {
		t.Fatalf("old Put: %v", err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 130, 11, "recent.txt", "needle in the newest matching chunk"); err != nil {
		t.Fatalf("recent Put: %v", err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 131, 11, "isolated.txt", "needle from another chat"); err != nil {
		t.Fatalf("isolated Put: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO rag_documents (chat_id, name, size_bytes) VALUES (?, ?, ?)`, 130, "filler.txt", 0)
	if err != nil {
		tx.Rollback()
		t.Fatalf("insert filler document: %v", err)
	}
	fillerID, err := result.LastInsertId()
	if err != nil {
		tx.Rollback()
		t.Fatalf("LastInsertId: %v", err)
	}
	filler := make([]string, ragSearchChunkScanLimit-1)
	for index := range filler {
		filler[index] = fmt.Sprintf("ordinary unrelated filler %d", index)
	}
	if err := insertRAGChunks(ctx, tx, fillerID, 130, filler); err != nil {
		tx.Rollback()
		t.Fatalf("insert filler chunks: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// The FTS index answers "which chunks mention needle" from the inverted index, so the whole 10 001-chunk
	// corpus is searchable and only the two matching chunks are scored. That is the change: the old code
	// loaded the newest 10 000 chunks, never saw the oldest match, and reported the corpus as truncated.
	detailed, err := SearchRAGChunksDetailed(ctx, db, 130, "needle", 5, 2000)
	if err != nil {
		t.Fatalf("SearchRAGChunksDetailed: %v", err)
	}
	if detailed.TotalChunks != ragSearchChunkScanLimit+1 || detailed.ScannedChunks != 2 || detailed.CorpusTruncated {
		t.Fatalf("unexpected corpus statistics: %+v", detailed)
	}
	if len(detailed.Chunks) != 2 {
		t.Fatalf("both matching chunks must be found in the whole corpus: %+v", detailed.Chunks)
	}
	var names = []string{detailed.Chunks[0].Name, detailed.Chunks[1].Name}
	if names[0] != "old.txt" && names[1] != "old.txt" {
		t.Fatalf("the oldest matching chunk is missing: %+v", detailed.Chunks)
	}
	compatible, err := SearchRAGChunks(ctx, db, 130, "needle", 5, 2000)
	if err != nil || len(compatible) != len(detailed.Chunks) || compatible[0] != detailed.Chunks[0] {
		t.Fatalf("compatibility wrapper=%+v detailed=%+v err=%v", compatible, detailed.Chunks, err)
	}
	isolated, err := SearchRAGChunksDetailed(ctx, db, 131, "needle", 5, 2000)
	if err != nil || isolated.TotalChunks != 1 || isolated.ScannedChunks != 1 || isolated.CorpusTruncated || len(isolated.Chunks) != 1 {
		t.Fatalf("chat isolation statistics=%+v err=%v", isolated, err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM rag_chunks WHERE document_id = ?`, oldID); err != nil {
		t.Fatalf("delete oldest chunk: %v", err)
	}
	exact, err := SearchRAGChunksDetailed(ctx, db, 130, "needle", 5, 2000)
	if err != nil {
		t.Fatalf("search after deletion: %v", err)
	}
	if exact.TotalChunks != ragSearchChunkScanLimit || exact.ScannedChunks != 1 || exact.CorpusTruncated {
		t.Fatalf("corpus statistics after deletion: %+v", exact)
	}
}
