# 0004 — FTS5 external-content indexes, built at open time

**Status:** accepted (2026-11-08, replaces the scan-based search)

## Context

Both searches used to load rows into Go and match them there: `/search` read the newest 10 000 messages of a
chat and ran `strings.Contains` on each, and RAG search loaded the newest 10 000 chunks and tokenized every one
of them on every question. That made a match older than the window invisible and every search cost tens of
megabytes of allocation. Details and measurements are in `docs/CODE_REVIEW_2026-11-08.md` (findings 4.2.1, 4.2.2).

## Decision

Two FTS5 indexes, `messages_fts` and `rag_chunks_fts`, both **external-content** tables
(`content='<table>'`, `content_rowid='id'`): they store the inverted index only and read the text back from the
content table, so nothing is duplicated on disk.

- The virtual tables come from migrations 013 and 014, so the schema is versioned and travels with a backup.
- The synchronisation triggers are created at **open time** (`store.ensureSearchIndexes`), not in a migration:
  a `CREATE TRIGGER` body is `BEGIN … END` with internal semicolons, and the migration splitter
  (`store.Statements`) is deliberately lexical and would cut it apart.
- A one-time `rebuild` runs when `search_index_meta` has no marker for the index. It covers a database written
  before the index existed and a backup restored from one; the marker keeps later starts from re-indexing.
- Queries are built from words, never from raw user text: `messages_fts` asks for all words with a prefix on the
  last one, `rag_chunks_fts` for any term as a prefix (a superset of exact matches). FTS operators typed by a
  user stay text.

## Consequences

- `/search` covers the whole history; the truncation notice now means "more matches than one answer shows", not
  "the scan stopped early".
- RAG candidates come from the index, and the Go scorer is unchanged; document-name matches are added separately
  because the name lives in `rag_documents`.
- The tokenizer difference between FTS5 `unicode61` and the project's `lexicalTokens` is mitigated by asking for
  prefixes, which can only widen the candidate set.
- Revisit if the ranking moves into SQLite (BM25 over `messages_fts`), or when Postgres replaces the store.
