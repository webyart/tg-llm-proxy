-- The RAG term index: FTS5 over rag_chunks.content.
--
-- RAG search used to load the newest 10 000 chunks of a chat and tokenize every one of them in Go on every
-- question. The index answers "which chunks mention these terms" from the inverted index instead, and the Go
-- scorer keeps its behaviour but only runs over the candidates. Triggers are created at open time for the
-- same reason as for messages_fts (store.ensureSearchIndexes).
CREATE VIRTUAL TABLE IF NOT EXISTS rag_chunks_fts USING fts5(
    content,
    content='rag_chunks',
    content_rowid='id',
    tokenize='unicode61'
);
