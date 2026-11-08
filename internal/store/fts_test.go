package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMessageSearchFollowsInsertUpdateAndDelete pins the triggers: the index is not a snapshot, and a message
// that was deleted or edited must stop being findable by its old text.
func TestMessageSearchFollowsInsertUpdateAndDelete(t *testing.T) {
	var ctx = context.Background()
	var db, err = OpenMemory(ctx, "fts-follows-writes")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	var id, appendErr = Append(ctx, db, Message{ChatID: 60, UserID: 11, Role: RoleUser, Content: "привет мир"})
	if appendErr != nil {
		t.Fatalf("Append: %v", appendErr)
	}
	if matches, searchErr := SearchMessages(ctx, db, 60, "прив", 5); searchErr != nil || len(matches) != 1 || matches[0].ID != id {
		t.Fatalf("prefix search after insert = %+v err=%v", matches, searchErr)
	}

	if _, err := db.ExecContext(ctx, `UPDATE messages SET content = 'пока мир' WHERE id = ?`, id); err != nil {
		t.Fatalf("update content: %v", err)
	}
	if matches, searchErr := SearchMessages(ctx, db, 60, "привет", 5); searchErr != nil || len(matches) != 0 {
		t.Fatalf("the old text is still indexed: %+v err=%v", matches, searchErr)
	}
	if matches, searchErr := SearchMessages(ctx, db, 60, "пока", 5); searchErr != nil || len(matches) != 1 {
		t.Fatalf("the new text is not indexed: %+v err=%v", matches, searchErr)
	}

	if _, err := DeleteMessage(ctx, db, 60, id); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if matches, searchErr := SearchMessages(ctx, db, 60, "пока", 5); searchErr != nil || len(matches) != 0 {
		t.Fatalf("a deleted message is still indexed: %+v err=%v", matches, searchErr)
	}
}

// TestMessageSearchTreatsOperatorsAsText: FTS5 has its own grammar, and a search box must never execute it.
func TestMessageSearchTreatsFTSSyntaxAsText(t *testing.T) {
	var ctx = context.Background()
	var db, err = OpenMemory(ctx, "fts-operators")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, err := Append(ctx, db, Message{ChatID: 61, UserID: 11, Role: RoleUser, Content: "alpha beta gamma"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	for _, query := range []string{`alpha OR beta`, `alpha*`, `"alpha"`, `-alpha`, `alpha:beta`, `NEAR(alpha beta)`, `alpha))((`} {
		if _, err := SearchMessages(ctx, db, 61, query, 5); err != nil {
			t.Fatalf("query %q broke the search: %v", query, err)
		}
	}
	// A term that is genuinely present still matches, whatever punctuation surrounds it.
	var matches, searchErr = SearchMessages(ctx, db, 61, "alpha, beta!", 5)
	if searchErr != nil || len(matches) != 1 {
		t.Fatalf("quoted-with-punctuation query = %+v err=%v", matches, searchErr)
	}
}

// TestMessageSearchIndexIsRebuiltOnceForAnOlderDatabase reproduces the upgrade path: a database whose messages
// were written before the index existed, with no triggers and no marker, is indexed on the next open exactly
// once (the marker then prevents a rebuild on every start).
func TestMessageSearchIndexIsRebuiltOnceForAnOlderDatabase(t *testing.T) {
	var ctx = context.Background()
	var path = filepath.Join(t.TempDir(), "older.db")
	var db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if chatErr := EnsureChat(ctx, db, 62, 11, "legacy"); chatErr != nil {
		t.Fatalf("EnsureChat: %v", chatErr)
	}
	if _, insertErr := db.ExecContext(ctx, `INSERT INTO messages (chat_id, user_id, role, content) VALUES (62, 11, 'user', 'legacy searchable text')`); insertErr != nil {
		t.Fatalf("insert legacy row: %v", insertErr)
	}
	// The state of a database that predates the index: no index content, no marker, no triggers.
	for _, statement := range []string{
		`DELETE FROM search_index_meta`,
		`DROP TRIGGER IF EXISTS messages_fts_insert`,
		`DROP TRIGGER IF EXISTS messages_fts_delete`,
		`DROP TRIGGER IF EXISTS messages_fts_update`,
		`INSERT INTO messages_fts(messages_fts) VALUES ('delete-all')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("downgrade state (%s): %v", statement, err)
		}
	}
	if matches, searchErr := SearchMessages(ctx, db, 62, "legacy", 5); searchErr != nil || len(matches) != 0 {
		t.Fatalf("an unindexed database must find nothing before the rebuild: %+v err=%v", matches, searchErr)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var reopened, reopenErr = Open(ctx, path)
	if reopenErr != nil {
		t.Fatalf("reopen: %v", reopenErr)
	}
	defer reopened.Close()
	var matches, searchErr = SearchMessages(ctx, reopened, 62, "legacy", 5)
	if searchErr != nil || len(matches) != 1 {
		t.Fatalf("the rebuild did not index the legacy row: %+v err=%v", matches, searchErr)
	}
	var marker int
	if err := reopened.QueryRowContext(ctx, `SELECT COUNT(*) FROM search_index_meta WHERE name = ?`, "messages_fts").Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("the rebuild marker is missing: count=%d err=%v", marker, err)
	}
}

// TestSearchTermsSplitsAndCaps keeps the expression builder honest: punctuation separates words, FTS operators
// are never passed through, and a very long query cannot become a huge expression.
func TestSearchTermsSplitsAndCaps(t *testing.T) {
	var terms = searchTerms("alpha-beta OR 42, гамма")
	var want = []string{"alpha", "beta", "OR", "42", "гамма"}
	if len(terms) != len(want) {
		t.Fatalf("terms = %v, want %v", terms, want)
	}
	for index, term := range want {
		if terms[index] != term {
			t.Fatalf("terms = %v, want %v", terms, want)
		}
	}
	var many = searchTerms("a b c d e f g h i j k l")
	if len(many) != messageSearchMaxTerms {
		t.Fatalf("a long query produced %d terms, want the cap %d", len(many), messageSearchMaxTerms)
	}
	if expression, ok := messageSearchExpression("   "); ok || expression != "" {
		t.Fatalf("a punctuation-only query produced the expression %q", expression)
	}
}
