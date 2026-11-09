package bot

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"

	"bot/internal/store"
)

// EmbeddingClient is the part of embed.Client the RAG paths use. A nil client keeps RAG lexical-only, which is
// both the default configuration and what every test that does not inject one gets.
type EmbeddingClient interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Model() string
}

// ragQueryVector embeds one query. A failure is logged once for the request and the search continues lexically:
// the embedding server is an optional layer and never a reason an answer fails.
func (d *Dispatcher) ragQueryVector(ctx context.Context, query string) ([]float32, bool) {
	if d.deps.Embeddings == nil || strings.TrimSpace(query) == "" {
		return nil, false
	}
	vectors, err := d.deps.Embeddings.Embed(ctx, []string{query})
	if err != nil {
		d.deps.Logger.Warn("RAG query embedding failed, the search continues lexically",
			"error", Sanitize(err, d.deps.Token))
		return nil, false
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		d.deps.Logger.Warn("RAG query embedding returned no usable vector, the search continues lexically")
		return nil, false
	}
	return vectors[0], true
}

// searchRAG is the one search entry point of the dispatcher: it attaches the query vector when the hybrid layer
// is configured and otherwise calls the store with the lexical request it has always used.
func (d *Dispatcher) searchRAG(ctx context.Context, chatID int64, query string, limit int, maxRunes int) (store.RAGSearchResult, error) {
	options := store.SearchOptions{Limit: limit, MaxRunes: maxRunes}
	if vector, ok := d.ragQueryVector(ctx, query); ok {
		options.QueryVector = vector
		options.EmbeddingModel = d.deps.Embeddings.Model()
		options.EmbeddingWeight = store.DefaultEmbeddingWeight
	}
	return store.SearchRAGChunksWithOptions(ctx, d.deps.DB, chatID, query, options)
}

// embedDocumentChunks is the dispatcher's thin wrapper over EmbedDocumentChunks, kept so the Telegram tests and
// any future caller can use the dispatcher without spelling out its dependencies.
func (d *Dispatcher) embedDocumentChunks(ctx context.Context, chatID, documentID int64) {
	EmbedDocumentChunks(ctx, d.deps.DB, d.deps.Embeddings, d.deps.Logger, d.deps.Token, chatID, documentID)
}

// EmbedDocumentChunks fills the vectors of a document that has just been committed. It is best-effort by
// contract: the document is already stored and searchable lexically, so every failure here is a warning and the
// reply still says the document was saved. It reports whether every chunk received a stored vector, which is
// what POST /api/documents returns as "embedded"; no client, no targets or any failure is false.
//
// The function is exported because the Mini App upload goes through the same ingest path as Telegram /docs add:
// chunking lives in the store and embedding lives here, in one place.
func EmbedDocumentChunks(ctx context.Context, db *sql.DB, client EmbeddingClient, logger *slog.Logger, token string, chatID, documentID int64) bool {
	if client == nil {
		return false
	}
	if logger == nil {
		logger = slog.Default()
	}
	model := client.Model()
	targets, err := store.DocumentEmbeddingTargets(ctx, db, documentID)
	if err != nil {
		logger.Warn("RAG document chunks could not be read for embedding",
			"document_id", documentID, "error", Sanitize(err, token))
		return false
	}
	if len(targets) == 0 {
		return false
	}
	texts := make([]string, len(targets))
	for index, target := range targets {
		texts[index] = target.Content
	}
	// The client sends at most embed.MaxBatch inputs per request and splits a longer document itself, so one
	// call here covers a document of any size while the batch cap stays in one place.
	vectors, err := client.Embed(ctx, texts)
	if err != nil {
		logger.Warn("RAG document embedding failed, the document stays searchable lexically",
			"document_id", documentID, "chunks", len(targets), "error", Sanitize(err, token))
		return false
	}
	if len(vectors) != len(targets) {
		logger.Warn("RAG document embedding returned a different number of vectors",
			"document_id", documentID, "chunks", len(targets), "vectors", len(vectors))
		return false
	}
	stored := 0
	for index, vector := range vectors {
		if err := store.PutChunkEmbedding(ctx, db, chatID, targets[index].ID, model, vector); err != nil {
			logger.Warn("RAG chunk embedding could not be stored",
				"document_id", documentID, "chunk_id", targets[index].ID, "error", Sanitize(err, token))
			continue
		}
		stored++
	}
	logger.Debug("RAG document embedded", "document_id", documentID, "chunks", stored, "model", model)
	return stored == len(targets)
}
