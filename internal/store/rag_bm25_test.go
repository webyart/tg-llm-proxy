package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// insertRAGBulkChunks inserts one document with count chunks directly, bypassing the public API: tests and
// benchmarks need corpora larger than the 20-document limit and much larger than a single text would chunk.
func insertRAGBulkChunks(tb testing.TB, ctx context.Context, db *sql.DB, chatID int64, name string, count int, content func(int) string) int64 {
	tb.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		tb.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO rag_documents (chat_id, name, size_bytes) VALUES (?, ?, 0)`, chatID, name)
	if err != nil {
		tb.Fatalf("insert document %s: %v", name, err)
	}
	documentID, err := result.LastInsertId()
	if err != nil {
		tb.Fatalf("LastInsertId: %v", err)
	}
	chunks := make([]string, count)
	for index := range chunks {
		chunks[index] = content(index)
	}
	if err := insertRAGChunks(ctx, tx, documentID, chatID, chunks); err != nil {
		tb.Fatalf("insert chunks: %v", err)
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("Commit: %v", err)
	}
	return documentID
}

func TestRAGBM25CandidatesPreferRareTerms(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_bm25_rare_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	// The rare chunk is inserted first, so the old "newest rows" selection would have dropped it.
	if _, _, err := PutRAGDocument(ctx, db, 150, 11, "rare.txt", "zephyr common rare marker"); err != nil {
		t.Fatalf("rare Put: %v", err)
	}
	insertRAGBulkChunks(t, ctx, db, 150, "common.txt", 320, func(index int) string {
		return fmt.Sprintf("common filler %d", index)
	})

	corpus, err := readRAGSearchCorpus(ctx, db, 150, lexicalTerms("common zephyr"))
	if err != nil {
		t.Fatalf("readRAGSearchCorpus: %v", err)
	}
	if corpus.chunkCount != ragCandidateLimit || corpus.totalChunks != 321 {
		t.Fatalf("candidate statistics=%+v", corpus)
	}
	if len(corpus.candidates) == 0 || !strings.Contains(corpus.candidates[0].Content, "zephyr") {
		t.Fatalf("bm25 did not put the rare-term chunk first: %+v", corpus.candidates[:min(len(corpus.candidates), 3)])
	}

	result, err := SearchRAGChunksDetailed(ctx, db, 150, "common zephyr", 5, 4000)
	if err != nil {
		t.Fatalf("SearchRAGChunksDetailed: %v", err)
	}
	if result.TotalChunks != 321 || result.ScannedChunks != ragCandidateLimit || !result.CorpusTruncated {
		t.Fatalf("candidate cap is not reported: %+v", result)
	}
	if len(result.Chunks) == 0 || result.Chunks[0].Name != "rare.txt" {
		t.Fatalf("rare chunk did not win the ranking: %+v", result.Chunks)
	}
}

func TestRAGBM25NameMatchSurvivesCandidateCap(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_bm25_name_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocument(ctx, db, 151, 11, "zephyr-notes.txt", "entirely unrelated body"); err != nil {
		t.Fatalf("name-only Put: %v", err)
	}
	insertRAGBulkChunks(t, ctx, db, 151, "bulk.txt", 320, func(index int) string {
		return fmt.Sprintf("zephyr body %d", index)
	})

	corpus, err := readRAGSearchCorpus(ctx, db, 151, lexicalTerms("zephyr"))
	if err != nil {
		t.Fatalf("readRAGSearchCorpus: %v", err)
	}
	if corpus.chunkCount != ragCandidateLimit {
		t.Fatalf("candidate cap not applied: %+v", corpus)
	}
	var named bool
	for _, candidate := range corpus.candidates {
		if candidate.Name == "zephyr-notes.txt" {
			named = true
		}
	}
	if !named {
		t.Fatalf("the name-only document lost its room to the bm25 tail: %+v", corpus.candidates)
	}

	result, err := SearchRAGChunksDetailed(ctx, db, 151, "zephyr", 400, 1<<20)
	if err != nil {
		t.Fatalf("SearchRAGChunksDetailed: %v", err)
	}
	if len(result.Chunks) != ragCandidateLimit {
		t.Fatalf("scored chunks=%d, want %d", len(result.Chunks), ragCandidateLimit)
	}
	named = false
	for _, chunk := range result.Chunks {
		if chunk.Name == "zephyr-notes.txt" {
			named = true
		}
	}
	if !named {
		t.Fatalf("a file-name match did not return its document's chunks: %+v", result.Chunks)
	}
}

func TestRAGBM25CandidateLimitIsEnforcedAndReported(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_bm25_limit_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	if err := EnsureChat(ctx, db, 152, 11, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	insertRAGBulkChunks(t, ctx, db, 152, "alpha.txt", 305, func(index int) string {
		return fmt.Sprintf("alpha token %d", index)
	})
	capped, err := SearchRAGChunksDetailed(ctx, db, 152, "alpha", 5, 2000)
	if err != nil {
		t.Fatalf("capped search: %v", err)
	}
	if capped.TotalChunks != 305 || capped.ScannedChunks != ragCandidateLimit || !capped.CorpusTruncated {
		t.Fatalf("cap statistics=%+v", capped)
	}
	if len(capped.Chunks) != 5 {
		t.Fatalf("result limit ignored: %d chunks", len(capped.Chunks))
	}

	if _, _, err := PutRAGDocument(ctx, db, 153, 11, "one.txt", "alpha"); err != nil {
		t.Fatalf("one Put: %v", err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 153, 11, "two.txt", "alpha"); err != nil {
		t.Fatalf("two Put: %v", err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 153, 11, "three.txt", "unrelated body"); err != nil {
		t.Fatalf("three Put: %v", err)
	}
	small, err := SearchRAGChunksDetailed(ctx, db, 153, "alpha", 5, 2000)
	if err != nil {
		t.Fatalf("small search: %v", err)
	}
	if small.TotalChunks != 3 || small.ScannedChunks != 2 || small.CorpusTruncated {
		t.Fatalf("small corpus statistics=%+v", small)
	}
}

func TestRAGBM25CandidateQueryUsesFTSIndex(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_bm25_plan_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	documentID, _, err := PutRAGDocument(ctx, db, 160, 11, "plan.txt", "needle in the planned query")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	expression, ok := ragSearchExpression(lexicalTerms("needle"))
	if !ok {
		t.Fatal("no FTS expression")
	}

	ftsPlan := explainRAGQuery(t, ctx, db, ragFTSCandidateQuery, expression, int64(160), ragCandidateLimit)
	var ftsIndexed bool
	for _, detail := range ftsPlan {
		lowered := strings.ToLower(detail)
		if strings.Contains(lowered, "virtual table index") {
			ftsIndexed = true
		}
		if strings.HasPrefix(lowered, "scan c ") || strings.HasPrefix(lowered, "scan rag_chunks ") {
			t.Fatalf("the candidate query scans rag_chunks: %s", detail)
		}
	}
	if !ftsIndexed {
		t.Fatalf("the candidate query does not use the FTS index: %v", ftsPlan)
	}

	namePlan := explainRAGQuery(t, ctx, db, ragNameCandidateQuery(1), documentID, ragCandidateLimit)
	var nameIndexed bool
	for _, detail := range namePlan {
		lowered := strings.ToLower(detail)
		if strings.HasPrefix(lowered, "scan c ") || strings.HasPrefix(lowered, "scan rag_chunks ") {
			t.Fatalf("the name union scans rag_chunks: %s", detail)
		}
		if strings.HasPrefix(lowered, "search c using index") {
			nameIndexed = true
		}
	}
	if !nameIndexed {
		t.Fatalf("the name union has no index on rag_chunks: %v", namePlan)
	}
}

func explainRAGQuery(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return plan
}

func TestRAGBM25EmptyExpressionReturnsNothingWithoutError(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_bm25_empty_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocument(ctx, db, 161, 11, "needle.txt", "needle text"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	corpus, err := readRAGSearchCorpus(ctx, db, 161, nil)
	if err != nil {
		t.Fatalf("empty expression failed the search: %v", err)
	}
	if corpus.totalChunks != 1 || corpus.chunkCount != 0 || len(corpus.candidates) != 0 {
		t.Fatalf("empty expression corpus=%+v", corpus)
	}
	result, err := SearchRAGChunksDetailed(ctx, db, 161, "the and of", 5, 2000)
	if err != nil || len(result.Chunks) != 0 {
		t.Fatalf("stopword-only search result=%+v err=%v", result, err)
	}
}

func TestRAGBM25InvalidExpressionDoesNotFailTheSearch(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_bm25_invalid_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocument(ctx, db, 162, 11, "needle.txt", "needle text"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// A NUL byte cannot appear in a term produced by lexicalTerms, but an internal caller may pass one: FTS5
	// rejects the expression, and the search must degrade to the name union rather than return an error.
	corpus, err := readRAGSearchCorpus(ctx, db, 162, []string{"bad\x00term"})
	if err != nil {
		t.Fatalf("an invalid FTS expression failed the search: %v", err)
	}
	if corpus.totalChunks != 1 || corpus.chunkCount != 0 || len(corpus.candidates) != 0 {
		t.Fatalf("invalid expression corpus=%+v", corpus)
	}
}

func TestRAGCandidateExpressionRejected(t *testing.T) {
	for _, row := range []struct {
		err  error
		want bool
	}{
		{errors.New("SQL logic error: fts5: syntax error near \"\"\"\"*"), true},
		{errors.New("SQL logic error: unterminated string (1)"), true},
		{errors.New("no such table: rag_chunks_fts"), true},
		{errors.New("database is locked (5) (SQLITE_BUSY)"), false},
		{errors.New("SQL logic error: no such column: c.missing"), false},
	} {
		if got := ragCandidateExpressionRejected(row.err); got != row.want {
			t.Errorf("ragCandidateExpressionRejected(%q)=%v, want %v", row.err, got, row.want)
		}
	}
}

func BenchmarkRAGSearch(b *testing.B) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_benchmark_bm25")
	if err != nil {
		b.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocument(ctx, db, 170, 11, "rare.txt", "rareterm common appears exactly once"); err != nil {
		b.Fatalf("rare Put: %v", err)
	}
	insertRAGBulkChunks(b, ctx, db, 170, "bulk.txt", 4999, func(index int) string {
		return fmt.Sprintf("common unrelated transcript filler %d", index)
	})

	for _, benchmark := range []struct {
		name    string
		query   string
		scanned int
	}{
		{name: "rare term", query: "rareterm", scanned: 1},
		{name: "common term capped", query: "common", scanned: ragCandidateLimit},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				result, err := SearchRAGChunksDetailed(ctx, db, 170, benchmark.query, 5, 2000)
				if err != nil {
					b.Fatalf("SearchRAGChunksDetailed: %v", err)
				}
				if result.TotalChunks != 5000 || result.ScannedChunks != benchmark.scanned {
					b.Fatalf("unexpected statistics: %+v", result)
				}
			}
		})
	}
}
