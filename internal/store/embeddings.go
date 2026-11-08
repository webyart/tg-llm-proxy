package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

// DefaultEmbeddingWeight is the w of the hybrid score: final = (1-w)*lexical + w*cosine. 0.4 is a deliberate
// bias towards the ranking that is already measured by the quality gate — a vector match can lift a chunk, but
// it cannot outrank a chunk the lexical scorer is sure about. The value is a constant of the scorer, not a row
// of the configuration table: one number to review next to the formula, not another way to break a start.
const DefaultEmbeddingWeight = 0.4

// ragEmbeddingChatBatch bounds one IN question about vectors. SQLite's bound-parameter limit is far higher, but
// a modest batch keeps one search's memory flat on a chat with thousands of chunks.
const ragEmbeddingChatBatch = 400

// RAGEmbeddingChunk is one chunk that needs a vector. RAGChunk (the search result) deliberately has no row id,
// while a backfill has to name the chunk it writes to, so this is its own type.
type RAGEmbeddingChunk struct {
	ID         int64
	DocumentID int64
	Name       string
	Content    string
}

// PutChunkEmbedding stores (or replaces) the vector of one chunk. The vector is written as a float32
// little-endian BLOB, and dimensions is its length, so a row of another model is recognisable without decoding.
func PutChunkEmbedding(ctx context.Context, db *sql.DB, chatID int64, chunkID int64, model string, vector []float32) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return errors.New("store: an embedding needs a model id")
	}
	if len(vector) == 0 {
		return errors.New("store: an embedding needs at least one dimension")
	}
	var owner int64
	err := db.QueryRowContext(ctx, `SELECT chat_id FROM rag_chunks WHERE id = ?`, chunkID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: chunk %d does not exist, its embedding cannot be stored", chunkID)
	}
	if err != nil {
		return err
	}
	if owner != chatID {
		return fmt.Errorf("store: chunk %d belongs to chat %d, not %d", chunkID, owner, chatID)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO rag_chunk_embeddings (chunk_id, chat_id, model, dimensions, vector, created_at)
		VALUES (?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(chunk_id) DO UPDATE SET
			chat_id = excluded.chat_id,
			model = excluded.model,
			dimensions = excluded.dimensions,
			vector = excluded.vector,
			created_at = excluded.created_at`,
		chunkID, chatID, model, len(vector), encodeVector(vector))
	return err
}

// ChunkEmbeddings returns the stored vectors of the named chunks for one model. A chunk without a row of this
// model is absent from the map rather than an error: the caller treats its cosine as zero.
func ChunkEmbeddings(ctx context.Context, db *sql.DB, chatID int64, model string, chunkIDs []int64) (map[int64][]float32, error) {
	out := make(map[int64][]float32, len(chunkIDs))
	if len(chunkIDs) == 0 {
		return out, nil
	}
	var prefix = ragEmbeddingSelect
	for start := 0; start < len(chunkIDs); start += ragEmbeddingChatBatch {
		end := start + ragEmbeddingChatBatch
		if end > len(chunkIDs) {
			end = len(chunkIDs)
		}
		args := make([]any, 0, len(chunkIDs[start:end])+2)
		args = append(args, chatID, model)
		for _, id := range chunkIDs[start:end] {
			args = append(args, id)
		}
		rows, err := db.QueryContext(ctx, prefix+` AND chunk_id IN (`+placeholders(end-start)+`) ORDER BY chunk_id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var blob []byte
			if err := rows.Scan(&id, &blob); err != nil {
				rows.Close()
				return nil, err
			}
			vector, err := decodeVector(blob)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: the vector of chunk %d: %w", id, err)
			}
			out[id] = vector
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// ragEmbeddingSelect is the shared projection of a vector row; callers append the filter they need.
const ragEmbeddingSelect = `SELECT chunk_id, vector FROM rag_chunk_embeddings WHERE chat_id = ? AND model = ?`

// ChunksMissingEmbedding returns up to limit chunks of the chat that have no vector of this model yet, oldest
// first: the backfill and the ingestion path both need the same list, and a stable order makes progress
// visible between runs.
func ChunksMissingEmbedding(ctx context.Context, db *sql.DB, chatID int64, model string, limit int) ([]RAGEmbeddingChunk, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT c.id, c.document_id, d.name, c.content
		FROM rag_chunks c
		JOIN rag_documents d ON d.id = c.document_id
		LEFT JOIN rag_chunk_embeddings e ON e.chunk_id = c.id AND e.model = ?
		WHERE c.chat_id = ? AND e.chunk_id IS NULL
		ORDER BY c.id
		LIMIT ?`, strings.TrimSpace(model), chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RAGEmbeddingChunk
	for rows.Next() {
		var row RAGEmbeddingChunk
		if err := rows.Scan(&row.ID, &row.DocumentID, &row.Name, &row.Content); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// DocumentEmbeddingTargets returns every chunk of one document: ingestion embeds exactly the chunks it has just
// written, not the older ones a backfill would find.
func DocumentEmbeddingTargets(ctx context.Context, db *sql.DB, documentID int64) ([]RAGEmbeddingChunk, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.id, c.document_id, d.name, c.content
		FROM rag_chunks c
		JOIN rag_documents d ON d.id = c.document_id
		WHERE c.document_id = ?
		ORDER BY c.ordinal`, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RAGEmbeddingChunk
	for rows.Next() {
		var row RAGEmbeddingChunk
		if err := rows.Scan(&row.ID, &row.DocumentID, &row.Name, &row.Content); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// EmbeddingStats reports the coverage of one chat and model: how many chunks have a vector and how many exist.
func EmbeddingStats(ctx context.Context, db *sql.DB, chatID int64, model string) (embedded, total int, err error) {
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rag_chunks WHERE chat_id = ?`, chatID).Scan(&total); err != nil {
		return 0, 0, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rag_chunk_embeddings WHERE chat_id = ? AND model = ?`,
		chatID, strings.TrimSpace(model)).Scan(&embedded); err != nil {
		return 0, 0, err
	}
	return embedded, total, nil
}

// RAGChatIDs lists the chats that own at least one chunk, for the backfill that must cover every chat.
func RAGChatIDs(ctx context.Context, db *sql.DB) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT chat_id FROM rag_chunks ORDER BY chat_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var chatID int64
		if err := rows.Scan(&chatID); err != nil {
			return nil, err
		}
		out = append(out, chatID)
	}
	return out, rows.Err()
}

// encodeVector writes a float32 slice as a little-endian BLOB. The byte order is fixed by the format, so a
// database copied between machines keeps decoding to the same numbers.
func encodeVector(vector []float32) []byte {
	out := make([]byte, 4*len(vector))
	for index, value := range vector {
		binary.LittleEndian.PutUint32(out[index*4:], math.Float32bits(value))
	}
	return out
}

// decodeVector is the inverse of encodeVector. A blob whose length is not a multiple of four is corrupt, and
// naming the chunk is the only way to find it.
func decodeVector(blob []byte) ([]float32, error) {
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("the stored vector has %d bytes, not a multiple of 4", len(blob))
	}
	out := make([]float32, len(blob)/4)
	for index := range out {
		out[index] = math.Float32frombits(binary.LittleEndian.Uint32(blob[index*4:]))
	}
	return out, nil
}
