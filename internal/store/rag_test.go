package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRAGDocumentsReplaceSearchAndDelete(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if version, err := SchemaVersion(ctx, db); err != nil || version != 5 {
		t.Fatalf("schema=%d err=%v", version, err)
	}
	long := "Привет МИР. " + strings.Repeat("документ ", 400)
	id, chunks, err := PutRAGDocument(ctx, db, 80, 11, "notes.txt", long)
	if err != nil || id == 0 || chunks != 4 {
		t.Fatalf("Put id=%d chunks=%d err=%v", id, chunks, err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 81, 12, "other.txt", "Привет из другого чата"); err != nil {
		t.Fatalf("other Put: %v", err)
	}
	found, err := SearchRAGChunks(ctx, db, 80, "привет", 3, 2000)
	if err != nil || len(found) == 0 || found[0].Name != "notes.txt" || !strings.Contains(found[0].Content, "Привет МИР") {
		t.Fatalf("search=%+v err=%v", found, err)
	}
	other, err := SearchRAGChunks(ctx, db, 81, "привет", 3, 2000)
	if err != nil || len(other) != 1 || other[0].Name != "other.txt" {
		t.Fatalf("other search=%+v err=%v", other, err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 80, 11, "notes.txt", "новая версия документа"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	docs, err := ListRAGDocuments(ctx, db, 80)
	if err != nil || len(docs) != 1 || docs[0].Chunks != 1 {
		t.Fatalf("docs=%+v err=%v", docs, err)
	}
	old, err := SearchRAGChunks(ctx, db, 80, "привет", 3, 2000)
	if err != nil || len(old) != 0 {
		t.Fatalf("replaced content remains=%+v err=%v", old, err)
	}
	deleted, err := DeleteRAGDocument(ctx, db, 80, id)
	if err != nil || !deleted {
		t.Fatalf("deleted=%v err=%v", deleted, err)
	}
	var chunksLeft int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rag_chunks WHERE document_id = ?`, id).Scan(&chunksLeft); err != nil || chunksLeft != 0 {
		t.Fatalf("chunks left=%d err=%v", chunksLeft, err)
	}
}

func TestRAGDocumentLimit(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_limit_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for i := 0; i < RAGDocumentLimit; i++ {
		if _, _, err := PutRAGDocument(ctx, db, 82, 11, string(rune('a'+i)), "текст"); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	if _, _, err := PutRAGDocument(ctx, db, 82, 11, "overflow", "текст"); !errors.Is(err, ErrRAGDocumentLimit) {
		t.Fatalf("limit err=%v", err)
	}
}
