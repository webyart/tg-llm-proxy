package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"strconv"
)

// ContentFingerprint is a deterministic SHA-256 over the user-visible rows a backup is supposed to carry:
// messages, events, RAG chunks and RAG vectors, in id order. It complements the counters of dbinspect: two
// files can hold the same number of rows with different content, and a restore drill that compares only
// counts would call that a success.
//
// The function is deliberately tolerant of a pre-migration backup: tables that do not exist yet are skipped,
// and each query uses columns that are present since the table's first migration. The version and the
// counters stay separate keys, so an older schema is still comparable with itself.
func ContentFingerprint(ctx context.Context, db *sql.DB) (string, error) {
	present, err := tablePresence(ctx, db, "messages", "events", "rag_chunks", "rag_chunk_embeddings")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if present["messages"] {
		if err := fingerprintMessages(ctx, db, h); err != nil {
			return "", err
		}
	}
	if present["events"] {
		if err := fingerprintEvents(ctx, db, h); err != nil {
			return "", err
		}
	}
	if present["rag_chunks"] {
		if err := fingerprintRAGChunks(ctx, db, h); err != nil {
			return "", err
		}
	}
	if present["rag_chunk_embeddings"] {
		if err := fingerprintRAGVectors(ctx, db, h); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func tablePresence(ctx context.Context, db *sql.DB, names ...string) (map[string]bool, error) {
	present := make(map[string]bool, len(names))
	for _, name := range names {
		var count int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
			return nil, fmt.Errorf("store.ContentFingerprint: table %s could not be inspected: %w", name, err)
		}
		present[name] = count == 1
	}
	return present, nil
}

func fingerprintMessages(ctx context.Context, db *sql.DB, h hash.Hash) error {
	rows, err := db.QueryContext(ctx,
		`SELECT id, chat_id, role, content, created_at FROM messages ORDER BY id`)
	if err != nil {
		return fmt.Errorf("store.ContentFingerprint: messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, chatID int64
		var role, content, createdAt string
		if err := rows.Scan(&id, &chatID, &role, &content, &createdAt); err != nil {
			return fmt.Errorf("store.ContentFingerprint: messages: %w", err)
		}
		fingerprintInt(h, id)
		fingerprintInt(h, chatID)
		fingerprintString(h, role)
		fingerprintString(h, content)
		fingerprintString(h, createdAt)
	}
	return rows.Err()
}

func fingerprintEvents(ctx context.Context, db *sql.DB, h hash.Hash) error {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, kind, detail, created_at FROM events ORDER BY id`)
	if err != nil {
		return fmt.Errorf("store.ContentFingerprint: events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var userID sql.NullInt64
		var kind string
		var detail sql.NullString
		var createdAt string
		if err := rows.Scan(&id, &userID, &kind, &detail, &createdAt); err != nil {
			return fmt.Errorf("store.ContentFingerprint: events: %w", err)
		}
		fingerprintInt(h, id)
		fingerprintNullInt(h, userID)
		fingerprintString(h, kind)
		fingerprintNullString(h, detail)
		fingerprintString(h, createdAt)
	}
	return rows.Err()
}

func fingerprintRAGChunks(ctx context.Context, db *sql.DB, h hash.Hash) error {
	rows, err := db.QueryContext(ctx,
		`SELECT id, document_id, chat_id, ordinal, content FROM rag_chunks ORDER BY id`)
	if err != nil {
		return fmt.Errorf("store.ContentFingerprint: rag_chunks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, documentID, chatID, ordinal int64
		var content string
		if err := rows.Scan(&id, &documentID, &chatID, &ordinal, &content); err != nil {
			return fmt.Errorf("store.ContentFingerprint: rag_chunks: %w", err)
		}
		fingerprintInt(h, id)
		fingerprintInt(h, documentID)
		fingerprintInt(h, chatID)
		fingerprintInt(h, ordinal)
		fingerprintString(h, content)
	}
	return rows.Err()
}

func fingerprintRAGVectors(ctx context.Context, db *sql.DB, h hash.Hash) error {
	rows, err := db.QueryContext(ctx,
		`SELECT chunk_id, chat_id, model, dimensions, vector FROM rag_chunk_embeddings ORDER BY chunk_id, model`)
	if err != nil {
		return fmt.Errorf("store.ContentFingerprint: rag_chunk_embeddings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var chunkID, chatID int64
		var model string
		var dimensions int
		var vector []byte
		if err := rows.Scan(&chunkID, &chatID, &model, &dimensions, &vector); err != nil {
			return fmt.Errorf("store.ContentFingerprint: rag_chunk_embeddings: %w", err)
		}
		fingerprintInt(h, chunkID)
		fingerprintInt(h, chatID)
		fingerprintString(h, model)
		fingerprintInt(h, int64(dimensions))
		fingerprintBytes(h, vector)
	}
	return rows.Err()
}

// fingerprintField writes a length prefix before every value, so the encoding is unambiguous even when a
// text value contains the separator bytes or an empty string.
func fingerprintString(h hash.Hash, value string) { fingerprintBytes(h, []byte(value)) }

func fingerprintBytes(h hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(value)
}

func fingerprintInt(h hash.Hash, value int64) { fingerprintString(h, strconv.FormatInt(value, 10)) }

func fingerprintNullInt(h hash.Hash, value sql.NullInt64) {
	if !value.Valid {
		fingerprintString(h, "NULL")
		return
	}
	fingerprintString(h, "int")
	fingerprintInt(h, value.Int64)
}

func fingerprintNullString(h hash.Hash, value sql.NullString) {
	if !value.Valid {
		fingerprintString(h, "NULL")
		return
	}
	fingerprintString(h, "text")
	fingerprintString(h, value.String)
}
