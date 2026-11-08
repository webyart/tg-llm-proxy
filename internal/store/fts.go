package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// The full-text indexes keep FTS5 in sync with the tables they describe.
//
// The virtual tables come from migrations (013 for messages, 014 for RAG chunks); the triggers are created
// here, at open time, because a CREATE TRIGGER carries a BEGIN … END body whose inner semicolons the lexical
// migration splitter (store.Statements) would cut into separate statements. Creating them on every open is
// idempotent (IF NOT EXISTS).
//
// Each index is an external-content table: it stores the inverted index only and reads the text back from its
// content table, so a message or a chunk is not duplicated on disk.
type searchIndex struct {
	// marker is the row of search_index_meta that says the one-time rebuild has run.
	marker string
	// table is the FTS5 virtual table; it doubles as the rebuild command target.
	table    string
	triggers []string
}

var searchIndexes = []searchIndex{
	{marker: "messages_fts", table: "messages_fts", triggers: []string{
		`CREATE TRIGGER IF NOT EXISTS messages_fts_insert AFTER INSERT ON messages BEGIN
			INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS messages_fts_delete AFTER DELETE ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS messages_fts_update AFTER UPDATE OF content ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
			INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
		END`,
	}},
	{marker: "rag_chunks_fts", table: "rag_chunks_fts", triggers: []string{
		`CREATE TRIGGER IF NOT EXISTS rag_chunks_fts_insert AFTER INSERT ON rag_chunks BEGIN
			INSERT INTO rag_chunks_fts(rowid, content) VALUES (new.id, new.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS rag_chunks_fts_delete AFTER DELETE ON rag_chunks BEGIN
			INSERT INTO rag_chunks_fts(rag_chunks_fts, rowid, content) VALUES ('delete', old.id, old.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS rag_chunks_fts_update AFTER UPDATE OF content ON rag_chunks BEGIN
			INSERT INTO rag_chunks_fts(rag_chunks_fts, rowid, content) VALUES ('delete', old.id, old.content);
			INSERT INTO rag_chunks_fts(rowid, content) VALUES (new.id, new.content);
		END`,
	}},
}

// messageSearchMaxTerms bounds the expression: a query of fifty words is not a search, it is a payload.
const messageSearchMaxTerms = 8

// ensureSearchIndexes creates the FTS triggers and rebuilds each index exactly once: a database written before
// the index existed, or restored from a backup taken before it, would otherwise answer every search with an
// empty result until someone rebuilt it by hand.
func ensureSearchIndexes(ctx context.Context, db *sql.DB) error {
	for _, index := range searchIndexes {
		// A database that has not reached the migration of this index yet (a test constructing an older schema,
		// or a backup from before the release) has no such table. Open applies every migration before calling
		// this, so absence here means the schema is deliberately older, not that the index is missing.
		var present int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'view') AND name = ?`, index.table).Scan(&present); err != nil {
			return fmt.Errorf("store: the %s index could not be inspected: %w", index.table, err)
		}
		if present == 0 {
			continue
		}
		for _, statement := range index.triggers {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("store: the %s trigger could not be created: %w", index.table, err)
			}
		}
		var marker string
		err := db.QueryRowContext(ctx, `SELECT value FROM search_index_meta WHERE name = ?`, index.marker).Scan(&marker)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: the %s index state could not be read: %w", index.table, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO `+index.table+`(`+index.table+`) VALUES ('rebuild')`); err != nil {
			return fmt.Errorf("store: the %s index could not be rebuilt: %w", index.table, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO search_index_meta (name, value) VALUES (?, 'built')`, index.marker); err != nil {
			return fmt.Errorf("store: the %s index marker could not be stored: %w", index.table, err)
		}
	}
	return nil
}

// searchTerms — the words of a user query, in order, at most messageSearchMaxTerms of them. Everything that
// is not a letter or a digit separates words, so FTS5 operators written by a user (`OR`, `*`, `-`, `:`) are
// never passed through as syntax.
func searchTerms(query string) []string {
	var terms []string
	var current []rune
	var flush = func() {
		if len(current) > 0 {
			terms = append(terms, string(current))
			current = current[:0]
		}
	}
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current = append(current, r)
			if len(terms) >= messageSearchMaxTerms {
				return terms[:messageSearchMaxTerms]
			}
			continue
		}
		flush()
		if len(terms) >= messageSearchMaxTerms {
			return terms[:messageSearchMaxTerms]
		}
	}
	flush()
	if len(terms) > messageSearchMaxTerms {
		terms = terms[:messageSearchMaxTerms]
	}
	return terms
}

// messageSearchExpression — an FTS5 expression for the terms: every word must be present, and the last one is
// a prefix, so «прив» still finds «привет» the way the substring search it replaces did. Quoting makes each
// term a phrase of its own, which is what keeps a user's punctuation and operators out of the grammar.
func messageSearchExpression(query string) (string, bool) {
	var terms = searchTerms(query)
	if len(terms) == 0 {
		return "", false
	}
	var parts = make([]string, 0, len(terms))
	for index, term := range terms {
		var quoted = quoteFTSTerm(term)
		if index == len(terms)-1 {
			quoted += "*"
		}
		parts = append(parts, quoted)
	}
	return strings.Join(parts, " AND "), true
}

// ragSearchExpression — any of the term must be present. The terms are already lexical tokens of the query, and
// a prefix query is a superset of an exact match, so the Go scorer sees every chunk it would have scored while
// the database skips the rest.
func ragSearchExpression(terms []string) (string, bool) {
	if len(terms) == 0 {
		return "", false
	}
	var parts = make([]string, 0, len(terms))
	for _, term := range terms {
		parts = append(parts, quoteFTSTerm(term)+"*")
	}
	return strings.Join(parts, " OR "), true
}

// quoteFTSTerm — one term as an FTS5 string literal. Doubling the quote is the only escape the grammar has.
func quoteFTSTerm(term string) string {
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
}
