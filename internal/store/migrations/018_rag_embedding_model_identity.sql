-- The model is part of the identity of a stored vector.
--
-- 016 already carried `model`, but the primary key was chunk_id alone, so a PutChunkEmbedding of another
-- model REPLACED the previous model's vector. Changing RAG_EMBEDDINGS_MODEL therefore destroyed vectors that
-- the old model still needed: they looked "missing" and the old rows could not be read back. The composite
-- primary key is the additive fix — a vector belongs to (chunk, model), never to a chunk in general — and the
-- copy below keeps every existing row, including its created_at and the model id it was written under.
--
-- This is a genuine primary-key rebuild, not an ADD COLUMN: 016 already has the model column. It is safe
-- because (a) the runner executes the whole migration in one transaction, so a failure leaves the 017 table
-- exactly as it was, (b) chunk_id was unique, so the copy cannot invent a duplicate, and (c) the new table
-- carries the same FK with ON DELETE CASCADE, so document deletion keeps removing vectors. Reversing in place
-- is possible without a backup: the old shape is `SELECT … FROM rag_chunk_embeddings GROUP BY chunk_id`
-- (one row per chunk), which is exactly the table 016 described.
DROP TABLE IF EXISTS rag_chunk_embeddings_018;
CREATE TABLE rag_chunk_embeddings_018 (
    chunk_id   INTEGER NOT NULL REFERENCES rag_chunks(id) ON DELETE CASCADE,
    chat_id    INTEGER NOT NULL,
    model      TEXT NOT NULL,
    dimensions INTEGER NOT NULL,
    vector     BLOB NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (chunk_id, model)
);

INSERT INTO rag_chunk_embeddings_018 (chunk_id, chat_id, model, dimensions, vector, created_at)
    SELECT chunk_id, chat_id, model, dimensions, vector, created_at FROM rag_chunk_embeddings;

-- Dropping the 016 table drops its (chat_id, model) index too; the index is recreated below on the new table.
DROP TABLE rag_chunk_embeddings;
ALTER TABLE rag_chunk_embeddings_018 RENAME TO rag_chunk_embeddings;
CREATE INDEX IF NOT EXISTS idx_rag_chunk_embeddings_chat_model ON rag_chunk_embeddings(chat_id, model);

-- The dimension a model was written with, so a later write cannot silently change it. Only models whose
-- historical rows already agree on one dimension are registered; a mixed model stays unregistered and the
-- writer reports the conflict instead of guessing. No vector is touched by this backfill.
CREATE TABLE IF NOT EXISTS rag_embedding_model_dimensions (
    model      TEXT PRIMARY KEY,
    dimensions INTEGER NOT NULL CHECK (dimensions > 0),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT OR IGNORE INTO rag_embedding_model_dimensions (model, dimensions)
    SELECT model, MIN(dimensions)
    FROM rag_chunk_embeddings
    GROUP BY model
    HAVING MIN(dimensions) = MAX(dimensions) AND MIN(dimensions) > 0;
