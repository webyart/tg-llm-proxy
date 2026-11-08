-- The vector side of the hybrid RAG search: one embedding per chunk and model.
--
-- The row hangs off the chunk, so deleting a document deletes its vectors with its chunks; the index answers
-- "which chunks of this chat already have a vector of this model" for the backfill and the search. A vector is
-- a float32 little-endian BLOB written by store.PutChunkEmbedding, and dimensions is its length in floats, so a
-- row written by another model (and therefore another dimension) is never silently mixed into a score.
CREATE TABLE IF NOT EXISTS rag_chunk_embeddings (
    chunk_id   INTEGER PRIMARY KEY REFERENCES rag_chunks(id) ON DELETE CASCADE,
    chat_id    INTEGER NOT NULL,
    model      TEXT NOT NULL,
    dimensions INTEGER NOT NULL,
    vector     BLOB NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_rag_chunk_embeddings_chat_model ON rag_chunk_embeddings(chat_id, model);
