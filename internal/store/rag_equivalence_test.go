package store

import (
	"context"
	"reflect"
	"testing"
)

func TestRAGLexicalTokenOccurrencesGoldenEquivalence(t *testing.T) {
	for _, row := range []struct {
		text     string
		terms    []string
		tokens   int
		occurred map[string]int
	}{
		{
			text:   "  THE, кот! СЕЛ... Straße ΔELTA １２3 e\u0301 東京 ",
			terms:  []string{"the", "кот", "сел", "straße", "δelta", "１２3", "e\u0301", "東京"},
			tokens: 8,
			occurred: map[string]int{
				"the": 1, "кот": 1, "сел": 1, "straße": 1,
				"δelta": 1, "１２3": 1, "e\u0301": 1, "東京": 1,
			},
		},
		{
			text:     "Кот-кот КОТ; котёнок the\nthe",
			terms:    []string{"кот", "the"},
			tokens:   6,
			occurred: map[string]int{"кот": 3, "the": 2},
		},
		{
			text:     "a\u0301 A\u0301",
			terms:    []string{"a\u0301"},
			tokens:   2,
			occurred: map[string]int{"a\u0301": 2},
		},
		{text: ",... !!!", terms: []string{"the"}, tokens: 0},
	} {
		terms := make(map[string]int, len(row.terms))
		for index, term := range row.terms {
			terms[term] = index
		}
		tokens, occurred := lexicalTokenOccurrences(row.text, terms)
		if tokens != row.tokens || !reflect.DeepEqual(occurred, row.occurred) {
			t.Errorf("text %q: tokens=%d occurred=%v, want %d %v", row.text, tokens, occurred, row.tokens, row.occurred)
		}
		reference := map[string]int{}
		for _, token := range lexicalTokens(row.text) {
			if _, wanted := terms[token]; wanted {
				reference[token]++
			}
		}
		if len(reference) == 0 {
			reference = nil
		}
		if tokens != len(lexicalTokens(row.text)) || !reflect.DeepEqual(occurred, reference) {
			t.Errorf("text %q differs from lexicalTokens: tokens=%d occurred=%v, reference=%v", row.text, tokens, occurred, reference)
		}
	}
}

func TestRAGSearchGoldenEquivalence(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, "rag_golden_"+t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	for _, row := range []struct {
		name    string
		content string
	}{
		{name: "substring.txt", content: "category concatenate котёнок сел needlework"},
		{name: "КОТ NEEDLE-reference.txt", content: "Совсем посторонний текст"},
		{name: "plain.txt", content: "кот сел needle"},
		{name: "tie-old.txt", content: "КОТ; СЕЛ! NEEDLE"},
		{name: "tie-new.txt", content: "КОТ; СЕЛ! NEEDLE"},
		{name: "unicode.txt", content: "кот сел needle Straße 東京 e\u0301"},
		{name: "phrase.txt", content: "THE кот, СЕЛ! needle — Straße 東京 e\u0301"},
	} {
		if _, _, err := PutRAGDocument(ctx, db, 140, 11, row.name, row.content); err != nil {
			t.Fatalf("Put %s: %v", row.name, err)
		}
	}

	for _, row := range []struct {
		name  string
		limit int
		want  []RAGChunk
	}{
		{name: "all matches", limit: 10, want: []RAGChunk{
			{DocumentID: 7, Name: "phrase.txt", Content: "THE кот, СЕЛ! needle — Straße 東京 e\u0301", Score: 72},
			{DocumentID: 5, Name: "tie-new.txt", Content: "КОТ; СЕЛ! NEEDLE", Score: 63},
			{DocumentID: 4, Name: "tie-old.txt", Content: "КОТ; СЕЛ! NEEDLE", Score: 63},
			{DocumentID: 3, Name: "plain.txt", Content: "кот сел needle", Score: 63},
			{DocumentID: 6, Name: "unicode.txt", Content: "кот сел needle Straße 東京 e\u0301", Score: 49},
			{DocumentID: 1, Name: "substring.txt", Content: "category concatenate котёнок сел needlework", Score: 17},
			{DocumentID: 2, Name: "КОТ NEEDLE-reference.txt", Content: "Совсем посторонний текст", Score: 8},
		}},
		{name: "bounded top k", limit: 3, want: []RAGChunk{
			{DocumentID: 7, Name: "phrase.txt", Content: "THE кот, СЕЛ! needle — Straße 東京 e\u0301", Score: 72},
			{DocumentID: 5, Name: "tie-new.txt", Content: "КОТ; СЕЛ! NEEDLE", Score: 63},
			{DocumentID: 4, Name: "tie-old.txt", Content: "КОТ; СЕЛ! NEEDLE", Score: 63},
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			chunks, err := SearchRAGChunks(ctx, db, 140, "the КОТ, СЕЛ! NEEDLE", row.limit, 2000)
			if err != nil {
				t.Fatalf("SearchRAGChunks: %v", err)
			}
			if !reflect.DeepEqual(chunks, row.want) {
				t.Fatalf("chunks=%#v, want %#v", chunks, row.want)
			}
		})
	}
}
