package bot

import (
	"context"
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

// embedDocumentChunks fills the vectors of a document that has just been committed. It is best-effort by
// contract: the document is already stored and searchable lexically, so every failure here is a warning and the
// reply still says the document was saved.
func (d *Dispatcher) embedDocumentChunks(ctx context.Context, chatID, documentID int64) {
	if d.deps.Embeddings == nil {
		return
	}
	model := d.deps.Embeddings.Model()
	targets, err := store.DocumentEmbeddingTargets(ctx, d.deps.DB, documentID)
	if err != nil {
		d.deps.Logger.Warn("RAG document chunks could not be read for embedding",
			"document_id", documentID, "error", Sanitize(err, d.deps.Token))
		return
	}
	if len(targets) == 0 {
		return
	}
	texts := make([]string, len(targets))
	for index, target := range targets {
		texts[index] = target.Content
	}
	// The client sends at most embed.MaxBatch inputs per request and splits a longer document itself, so one
	// call here covers a document of any size while the batch cap stays in one place.
	vectors, err := d.deps.Embeddings.Embed(ctx, texts)
	if err != nil {
		d.deps.Logger.Warn("RAG document embedding failed, the document stays searchable lexically",
			"document_id", documentID, "chunks", len(targets), "error", Sanitize(err, d.deps.Token))
		return
	}
	if len(vectors) != len(targets) {
		d.deps.Logger.Warn("RAG document embedding returned a different number of vectors",
			"document_id", documentID, "chunks", len(targets), "vectors", len(vectors))
		return
	}
	stored := 0
	for index, vector := range vectors {
		if err := store.PutChunkEmbedding(ctx, d.deps.DB, chatID, targets[index].ID, model, vector); err != nil {
			d.deps.Logger.Warn("RAG chunk embedding could not be stored",
				"document_id", documentID, "chunk_id", targets[index].ID, "error", Sanitize(err, d.deps.Token))
			continue
		}
		stored++
	}
	d.deps.Logger.Debug("RAG document embedded", "document_id", documentID, "chunks", stored, "model", model)
}
