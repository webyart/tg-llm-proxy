package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRAGCustomLimitAllowsReplacement(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_custom_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocumentWithLimit(ctx, db, 100, 11, "one", "текст", 1); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if _, _, err := PutRAGDocumentWithLimit(ctx, db, 100, 11, "two", "текст", 1); !errors.Is(err, ErrRAGDocumentLimit) {
		t.Fatalf("limit err=%v", err)
	}
	if _, _, err := PutRAGDocumentWithLimit(ctx, db, 100, 11, "one", "обновление", 1); err != nil {
		t.Fatalf("replacement at limit: %v", err)
	}
}

func TestRAGRankingPrefersQueryCoverage(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_ranking_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocument(ctx, db, 101, 11, "repeat", strings.Repeat("alpha ", 20)); err != nil {
		t.Fatalf("repeat Put: %v", err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 101, 11, "coverage", "alpha beta gamma"); err != nil {
		t.Fatalf("coverage Put: %v", err)
	}
	chunks, err := SearchRAGChunks(ctx, db, 101, "alpha beta gamma", 2, 2000)
	if err != nil || len(chunks) != 2 || chunks[0].Name != "coverage" {
		t.Fatalf("ranking=%+v err=%v", chunks, err)
	}
}

func TestRAGRankingPrefersRareTermOverCommonTerm(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_rare_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	common := []string{
		"d1.txt", "common notes about widgets",
		"d2.txt", "common widget report",
		"d3.txt", "common widget summary",
		"d4.txt", "common widget log",
	}
	for i := 0; i < len(common); i += 2 {
		if _, _, err := PutRAGDocument(ctx, db, 102, 11, common[i], common[i+1]); err != nil {
			t.Fatalf("Put %s: %v", common[i], err)
		}
	}
	if _, _, err := PutRAGDocument(ctx, db, 102, 11, "d5.txt", "rare zephyr specification"); err != nil {
		t.Fatalf("rare Put: %v", err)
	}
	chunks, err := SearchRAGChunks(ctx, db, 102, "common rare", 5, 2000)
	if err != nil || len(chunks) != 5 {
		t.Fatalf("search=%+v err=%v", chunks, err)
	}
	if chunks[0].Name != "d5.txt" {
		t.Fatalf("rare term did not outrank the common term: %+v", chunks)
	}
	for _, chunk := range chunks[1:] {
		if !strings.Contains(chunk.Content, "common") {
			t.Fatalf("common term chunk missing: %+v", chunks)
		}
		if chunk.Score >= chunks[0].Score {
			t.Fatalf("common chunk score %d is not below rare chunk score %d: %+v", chunk.Score, chunks[0].Score, chunks)
		}
	}
}

func TestRAGRankingPrefersShortChunkOverRepetition(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_short_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocument(ctx, db, 103, 11, "rambling", strings.Repeat("kubernetes ", 40)); err != nil {
		t.Fatalf("rambling Put: %v", err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 103, 11, "precise", "kubernetes ingress controller"); err != nil {
		t.Fatalf("precise Put: %v", err)
	}
	chunks, err := SearchRAGChunks(ctx, db, 103, "kubernetes", 2, 4000)
	if err != nil || len(chunks) != 2 || chunks[0].Name != "precise" {
		t.Fatalf("ranking=%+v err=%v", chunks, err)
	}
	if chunks[0].Score <= chunks[1].Score {
		t.Fatalf("short chunk score %d is not above repeating chunk score %d", chunks[0].Score, chunks[1].Score)
	}
}

func TestRAGRankingIsStableAcrossCalls(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_stable_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for _, name := range []string{"e1.txt", "e2.txt", "e3.txt"} {
		if _, _, err := PutRAGDocument(ctx, db, 104, 11, name, "identical phrase content here"); err != nil {
			t.Fatalf("Put %s: %v", name, err)
		}
	}
	first, err := SearchRAGChunks(ctx, db, 104, "identical content", 3, 2000)
	if err != nil || len(first) != 3 {
		t.Fatalf("search=%+v err=%v", first, err)
	}
	if first[0].Name != "e3.txt" || first[1].Name != "e2.txt" || first[2].Name != "e1.txt" {
		t.Fatalf("equal scores are not ordered by chunk id: %+v", first)
	}
	for call := 0; call < 4; call++ {
		again, err := SearchRAGChunks(ctx, db, 104, "identical content", 3, 2000)
		if err != nil {
			t.Fatalf("repeat search: %v", err)
		}
		for index := range first {
			if again[index].Name != first[index].Name || again[index].Score != first[index].Score {
				t.Fatalf("call %d differs: %+v vs %+v", call, again, first)
			}
		}
	}
}

func TestRAGRankingHandlesCyrillicQueries(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_cyrillic_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	docs := []string{
		"a.txt", "отчёт по продажам за март",
		"b.txt", "отчёт по складу за апрель",
		"c.txt", "отчёт по логистике за май",
		"d.txt", "умная розетка для сети",
	}
	for i := 0; i < len(docs); i += 2 {
		if _, _, err := PutRAGDocument(ctx, db, 105, 11, docs[i], docs[i+1]); err != nil {
			t.Fatalf("Put %s: %v", docs[i], err)
		}
	}
	chunks, err := SearchRAGChunks(ctx, db, 105, "розетка отчёт", 4, 2000)
	if err != nil || len(chunks) != 4 {
		t.Fatalf("cyrillic search=%+v err=%v", chunks, err)
	}
	if chunks[0].Name != "d.txt" {
		t.Fatalf("cyrillic ranking=%+v", chunks)
	}
	for _, chunk := range chunks {
		if chunk.Score <= 0 || chunk.Content == "" {
			t.Fatalf("cyrillic chunk unmatched: %+v", chunk)
		}
	}
	single, err := SearchRAGChunks(ctx, db, 105, "розетка", 4, 2000)
	if err != nil || len(single) != 1 || single[0].Name != "d.txt" {
		t.Fatalf("single cyrillic term search=%+v err=%v", single, err)
	}
}

func TestRAGRankingIncludesNameOnlyMatch(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_name_only_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocument(ctx, db, 106, 11, "needle-reference.txt", "entirely unrelated body"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	chunks, err := SearchRAGChunks(ctx, db, 106, "needle", 4, 2000)
	if err != nil || len(chunks) != 1 || chunks[0].Name != "needle-reference.txt" || chunks[0].Score <= 0 {
		t.Fatalf("name-only search=%+v err=%v", chunks, err)
	}
}

func TestRAGSearchDropsNonmatchingCandidates(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_candidates_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	docs := []string{
		"content.txt", "needle in content",
		"irrelevant.txt", "unrelated body",
		"needle-notes.txt", "another unrelated body",
	}
	for i := 0; i < len(docs); i += 2 {
		if _, _, err := PutRAGDocument(ctx, db, 107, 11, docs[i], docs[i+1]); err != nil {
			t.Fatalf("Put %s: %v", docs[i], err)
		}
	}
	corpus, err := readRAGSearchCorpus(ctx, db, 107, lexicalTerms("needle"))
	if err != nil {
		t.Fatalf("readRAGSearchCorpus: %v", err)
	}
	if corpus.chunkCount != 3 || corpus.totalTokens != 8 || corpus.documents["needle"] != 1 {
		t.Fatalf("corpus statistics=%+v", corpus)
	}
	if len(corpus.candidates) != 2 {
		t.Fatalf("retained candidates=%+v", corpus.candidates)
	}
	for _, candidate := range corpus.candidates {
		if candidate.Name == "irrelevant.txt" {
			t.Fatalf("nonmatching chunk was retained: %+v", candidate)
		}
	}
}

func TestRAGQueryCapsUnicodeRunesBeforeRetrieval(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_query_runes_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for _, row := range []struct {
		name    string
		content string
	}{
		{name: "within.txt", content: "needle inside the bounded query"},
		{name: "beyond.txt", content: "overflow outside the bounded query"},
	} {
		if _, _, err := PutRAGDocument(ctx, db, 108, 11, row.name, row.content); err != nil {
			t.Fatalf("Put %s: %v", row.name, err)
		}
	}
	prefix := "needle " + strings.Repeat("界", RAGQueryMaxRunes-len([]rune("needle ")))
	query := prefix + " overflow"
	if got := ragQueryPrefix(query); got != prefix || len([]rune(got)) != RAGQueryMaxRunes {
		t.Fatalf("bounded query has %d runes", len([]rune(got)))
	}
	chunks, err := SearchRAGChunks(ctx, db, 108, query, 5, 2000)
	if err != nil || len(chunks) != 1 || chunks[0].Name != "within.txt" {
		t.Fatalf("overflow text affected retrieval: %+v err=%v", chunks, err)
	}
}

func TestRAGSearchUsesUnicodeTokenBoundaries(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_boundaries_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for _, row := range []struct {
		name    string
		content string
	}{
		{name: "category.txt", content: "concatenate категория"},
		{name: "exact.txt", content: "Cat кат"},
	} {
		if _, _, err := PutRAGDocument(ctx, db, 120, 11, row.name, row.content); err != nil {
			t.Fatalf("Put %s: %v", row.name, err)
		}
	}
	for _, query := range []string{"cat", "кат"} {
		chunks, err := SearchRAGChunks(ctx, db, 120, query, 5, 2000)
		if err != nil || len(chunks) != 1 || chunks[0].Name != "exact.txt" {
			t.Fatalf("query %q matched a substring: %+v err=%v", query, chunks, err)
		}
	}
}

func TestRAGPhraseMatchingUsesTokenBoundaries(t *testing.T) {
	for _, row := range []struct {
		text    string
		phrase  string
		matched bool
	}{
		{text: "Кот, сел!", phrase: "кот сел", matched: true},
		{text: "котёнок сел", phrase: "кот сел", matched: false},
		{text: "cat satellite", phrase: "cat sat", matched: false},
		{text: "category sat", phrase: "cat sat", matched: false},
		{text: "A cat, then sat down", phrase: "cat then sat", matched: true},
	} {
		matched := ragContainsTokenPhrase(lexicalTokens(row.text), lexicalTokens(row.phrase))
		if matched != row.matched {
			t.Errorf("text %q phrase %q matched=%v, want %v", row.text, row.phrase, matched, row.matched)
		}
	}
}

func TestRAGSearchFiltersEnglishAndRussianStopwords(t *testing.T) {
	terms := lexicalTerms("the and of needle при этом")
	if len(terms) != 1 || terms[0] != "needle" {
		t.Fatalf("lexical terms=%q, want [needle]", terms)
	}

	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_stopwords_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if _, _, err := PutRAGDocument(ctx, db, 121, 11, "stopwords.txt", "the and of при этом"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 121, 11, "needle.txt", "needle"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	chunks, err := SearchRAGChunks(ctx, db, 121, "the and of needle при этом", 5, 2000)
	if err != nil || len(chunks) != 1 || chunks[0].Name != "needle.txt" {
		t.Fatalf("stopword-aware search=%+v err=%v", chunks, err)
	}
	for _, query := range []string{"the and of", "при этом что"} {
		chunks, err := SearchRAGChunks(ctx, db, 121, query, 5, 2000)
		if err != nil || len(chunks) != 0 {
			t.Fatalf("stopword-only query %q returned %+v err=%v", query, chunks, err)
		}
	}
}

func TestRAGRankingCoverageFloorKeepsOnlyUsefulFallback(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_coverage_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	query := "alpha beta gamma delta epsilon"
	if _, _, err := PutRAGDocument(ctx, db, 122, 11, "strong.txt", query); err != nil {
		t.Fatalf("strong Put: %v", err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 122, 11, "weak.txt", "alpha"); err != nil {
		t.Fatalf("weak Put: %v", err)
	}
	chunks, err := SearchRAGChunks(ctx, db, 122, query, 5, 2000)
	if err != nil || len(chunks) != 1 || chunks[0].Name != "strong.txt" {
		t.Fatalf("coverage floor search=%+v err=%v", chunks, err)
	}
	if _, _, err := PutRAGDocument(ctx, db, 123, 11, "fallback.txt", "alpha"); err != nil {
		t.Fatalf("fallback Put: %v", err)
	}
	chunks, err = SearchRAGChunks(ctx, db, 123, query, 5, 2000)
	if err != nil || len(chunks) != 1 || chunks[0].Name != "fallback.txt" {
		t.Fatalf("only useful low-coverage match was dropped: %+v err=%v", chunks, err)
	}
}

func TestRAGQueryKeepsFirst32DistinctLexicalTerms(t *testing.T) {
	allTerms := make([]string, 40)
	queryParts := []string{"term00", "term00"}
	for index := range allTerms {
		allTerms[index] = fmt.Sprintf("term%02d", index)
		queryParts = append(queryParts, allTerms[index])
	}
	query := strings.Join(queryParts, " ")
	terms := lexicalTerms(query)
	if len(terms) != RAGQueryMaxTerms {
		t.Fatalf("lexical terms=%d, want %d", len(terms), RAGQueryMaxTerms)
	}
	for index := range terms {
		if terms[index] != allTerms[index] {
			t.Fatalf("term %d=%q, want %q", index, terms[index], allTerms[index])
		}
	}

	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_query_terms_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for _, row := range []struct {
		name    string
		content string
	}{
		{name: "included.txt", content: "term31"},
		{name: "excluded.txt", content: "term32"},
	} {
		if _, _, err := PutRAGDocument(ctx, db, 109, 11, row.name, row.content); err != nil {
			t.Fatalf("Put %s: %v", row.name, err)
		}
	}
	chunks, err := SearchRAGChunks(ctx, db, 109, query, 5, 2000)
	if err != nil || len(chunks) != 1 || chunks[0].Name != "included.txt" {
		t.Fatalf("terms beyond the cap affected retrieval: %+v err=%v", chunks, err)
	}
}
