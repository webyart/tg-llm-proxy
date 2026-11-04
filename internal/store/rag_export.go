package store

import (
	"context"
	"database/sql"
	"errors"
)

var ErrRAGExportTooLarge = errors.New("RAG export exceeds the configured byte limit")

type RAGExportDocument struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	CreatedAt string   `json:"created_at"`
	Chunks    []string `json:"chunks"`
}

func ExportRAGDocuments(ctx context.Context, db *sql.DB, chatID int64, maxBytes int) ([]RAGExportDocument, error) {
	if maxBytes <= 0 {
		return nil, errors.New("RAG export limit must be positive")
	}
	documents, err := ListRAGDocuments(ctx, db, chatID)
	if err != nil {
		return nil, err
	}
	out := make([]RAGExportDocument, 0, len(documents))
	used := 0
	for _, document := range documents {
		rows, err := db.QueryContext(ctx, `SELECT content FROM rag_chunks WHERE document_id = ? ORDER BY ordinal ASC`, document.ID)
		if err != nil {
			return nil, err
		}
		exported := RAGExportDocument{ID: document.ID, Name: document.Name, CreatedAt: document.CreatedAt, Chunks: []string{}}
		for rows.Next() {
			var content string
			if err := rows.Scan(&content); err != nil {
				rows.Close()
				return nil, err
			}
			exported.Chunks = append(exported.Chunks, content)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		used += len(document.Name) + len(exported.Chunks)
		for _, chunk := range exported.Chunks {
			used += len(chunk)
		}
		if used > maxBytes {
			return nil, ErrRAGExportTooLarge
		}
		out = append(out, exported)
	}
	return out, nil
}
