-- The message search index: FTS5 over messages.content.
--
-- The virtual table and its bookkeeping live in a migration so the schema is versioned and travels with a
-- backup; the three triggers that keep the index in sync are created at open time (store.ensureSearchIndexes),
-- because a CREATE TRIGGER carries a BEGIN … END body whose inner semicolons the deliberately lexical
-- migration splitter would cut apart.
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    content,
    content='messages',
    content_rowid='id',
    tokenize='unicode61'
);

-- search_index_meta records that the one-time `rebuild` of the index has run, so opening a database with a
-- history does not re-index it on every start.
CREATE TABLE IF NOT EXISTS search_index_meta (
    name  TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
