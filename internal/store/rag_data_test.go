package store

import (
	"context"
	"testing"
	"time"
)

func TestRAGExportAndRetention(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_data_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	oldID, _, err := PutRAGDocument(ctx, db, 110, 11, "old.txt", "старый текст")
	if err != nil {
		t.Fatalf("old Put: %v", err)
	}
	newID, _, err := PutRAGDocument(ctx, db, 110, 11, "new.txt", "новый текст")
	if err != nil {
		t.Fatalf("new Put: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE rag_documents SET created_at = '2000-01-01 00:00:00' WHERE id = ?`, oldID); err != nil {
		t.Fatalf("age old document: %v", err)
	}
	exported, err := ExportRAGDocuments(ctx, db, 110, 10000)
	if err != nil || len(exported) != 2 {
		t.Fatalf("export=%+v err=%v", exported, err)
	}
	for _, document := range exported {
		if len(document.Chunks) != 1 || document.Chunks[0] == "" {
			t.Fatalf("document content missing: %+v", document)
		}
	}
	cutoff := time.Now().AddDate(0, 0, -30)
	preview, err := PrunePreview(ctx, db, PruneOptions{RAGBefore: cutoff})
	if err != nil || preview.RAGDocuments != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	deleted, err := Prune(ctx, db, PruneOptions{RAGBefore: cutoff})
	if err != nil || deleted.RAGDocuments != 1 {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
	var chunks int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rag_chunks WHERE document_id = ?`, oldID).Scan(&chunks); err != nil || chunks != 0 {
		t.Fatalf("old chunks=%d err=%v", chunks, err)
	}
	docs, err := ListRAGDocuments(ctx, db, 110)
	if err != nil || len(docs) != 1 || docs[0].ID != newID {
		t.Fatalf("remaining docs=%+v err=%v", docs, err)
	}
}
