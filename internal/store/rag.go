package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	RAGDocumentLimit = 20
	RAGChunkRunes    = 1000
	RAGChunkOverlap  = 100
	RAGQueryMaxRunes = 4000
	RAGQueryMaxTerms = 32
)

// BM25-lite weights of SearchRAGChunks. ragB is 1 so chunk length is normalised in full: a chunk
// gains only from the terms it actually covers, never from repeating one term. ragScoreScale keeps
// the core term score in the same order of magnitude as the name and phrase bonuses below.
const (
	ragK1                = 1.2
	ragB                 = 1.0
	ragScoreScale        = 10.0
	ragNameTermBonus     = 4.0
	ragPhraseBonus       = 25.0
	ragDistinctTermBonus = 12.0
)

// ragLengthPrior multiplies the core term score. BM25 with ragB = 1 normalises by length only
// through the term-frequency denominator, so two chunks with the same term density score the same
// however long they are; this factor is what keeps a chunk that repeats one term below a chunk that
// answers the query in a handful of words.
func ragLengthPrior(tokens int, averageTokens float64) float64 {
	return averageTokens / (averageTokens + float64(tokens))
}

var ErrRAGDocumentLimit = errors.New("the chat reached its RAG document limit")

type RAGDocument struct {
	ID        int64
	ChatID    int64
	Name      string
	SizeBytes int64
	CreatedAt string
	Chunks    int
}

type RAGChunk struct {
	DocumentID int64
	Name       string
	Content    string
	Score      int
}

func PutRAGDocument(ctx context.Context, db *sql.DB, chatID int64, userID int64, name string, content string) (int64, int, error) {
	return PutRAGDocumentWithLimit(ctx, db, chatID, userID, name, content, RAGDocumentLimit)
}

func PutRAGDocumentWithLimit(ctx context.Context, db *sql.DB, chatID int64, userID int64, name string, content string, maxDocuments int) (int64, int, error) {
	if maxDocuments <= 0 {
		return 0, 0, errors.New("RAG document limit must be positive")
	}
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(content) || strings.TrimSpace(content) == "" || strings.ContainsRune(content, 0) {
		return 0, 0, errors.New("RAG document needs a name and valid non-empty UTF-8 text")
	}
	if err := EnsureChat(ctx, db, chatID, userID, ""); err != nil {
		return 0, 0, err
	}
	chunks := chunkRAGText(content)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var existing int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM rag_documents WHERE chat_id = ? AND name = ?`, chatID, name).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	if existing == 0 {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM rag_documents WHERE chat_id = ?`, chatID).Scan(&count); err != nil {
			return 0, 0, err
		}
		if count >= maxDocuments {
			return 0, 0, ErrRAGDocumentLimit
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rag_documents (chat_id, name, size_bytes) VALUES (?, ?, ?)
		ON CONFLICT(chat_id, name) DO UPDATE SET size_bytes = excluded.size_bytes, created_at = datetime('now')`, chatID, name, len(content)); err != nil {
		return 0, 0, err
	}
	var documentID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM rag_documents WHERE chat_id = ? AND name = ?`, chatID, name).Scan(&documentID); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rag_chunks WHERE document_id = ?`, documentID); err != nil {
		return 0, 0, err
	}
	for i, chunk := range chunks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO rag_chunks (document_id, chat_id, ordinal, content) VALUES (?, ?, ?, ?)`, documentID, chatID, i, chunk); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return documentID, len(chunks), nil
}

func chunkRAGText(content string) []string {
	runes := []rune(strings.TrimSpace(content))
	chunks := []string{}
	for start := 0; start < len(runes); {
		end := start + RAGChunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
		start = end - RAGChunkOverlap
	}
	return chunks
}

func ListRAGDocuments(ctx context.Context, db *sql.DB, chatID int64) ([]RAGDocument, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT d.id, d.chat_id, d.name, d.size_bytes, d.created_at, COUNT(c.id)
		FROM rag_documents d LEFT JOIN rag_chunks c ON c.document_id = d.id
		WHERE d.chat_id = ? GROUP BY d.id ORDER BY d.created_at DESC, d.id DESC`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RAGDocument{}
	for rows.Next() {
		var row RAGDocument
		if err := rows.Scan(&row.ID, &row.ChatID, &row.Name, &row.SizeBytes, &row.CreatedAt, &row.Chunks); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func DeleteRAGDocument(ctx context.Context, db *sql.DB, chatID int64, id int64) (bool, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM rag_documents WHERE chat_id = ? AND id = ?`, chatID, id)
	if err != nil {
		return false, err
	}
	count, err := res.RowsAffected()
	return count == 1, err
}

func ClearRAGDocuments(ctx context.Context, db *sql.DB, chatID int64) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM rag_documents WHERE chat_id = ?`, chatID)
	if err != nil {
		return 0, err
	}
	count, err := res.RowsAffected()
	return count, err
}

type ragScoredChunk struct {
	RAGChunk
	id        int
	tokens    int
	occurred  map[string]int
	lowerName string
	lowerText string
	nameHits  int
	rankScore float64
}

type ragSearchCorpus struct {
	candidates  []ragScoredChunk
	documents   map[string]int
	chunkCount  int
	totalTokens int
}

func readRAGSearchCorpus(ctx context.Context, db *sql.DB, chatID int64, terms []string) (ragSearchCorpus, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.id, c.document_id, d.name, c.content FROM rag_chunks c
		JOIN rag_documents d ON d.id = c.document_id WHERE c.chat_id = ? ORDER BY c.id DESC LIMIT 10000`, chatID)
	if err != nil {
		return ragSearchCorpus{}, err
	}
	defer rows.Close()
	corpus := ragSearchCorpus{documents: map[string]int{}}
	for rows.Next() {
		var row ragScoredChunk
		if err := rows.Scan(&row.id, &row.DocumentID, &row.Name, &row.Content); err != nil {
			return ragSearchCorpus{}, err
		}
		row.lowerText = strings.ToLower(row.Content)
		row.lowerName = strings.ToLower(row.Name)
		for _, term := range terms {
			occurrences := strings.Count(row.lowerText, term)
			if occurrences > 0 {
				if row.occurred == nil {
					row.occurred = map[string]int{}
				}
				row.occurred[term] = occurrences
				corpus.documents[term]++
			}
			if strings.Contains(row.lowerName, term) {
				row.nameHits++
			}
		}
		corpus.chunkCount++
		row.tokens = lexicalTokenCount(row.Content)
		corpus.totalTokens += row.tokens
		if len(row.occurred) > 0 || row.nameHits > 0 {
			corpus.candidates = append(corpus.candidates, row)
		}
	}
	if err := rows.Err(); err != nil {
		return ragSearchCorpus{}, err
	}
	return corpus, nil
}

func lexicalTokenCount(text string) int {
	count := 0
	inToken := false
	for _, r := range text {
		isToken := unicode.IsLetter(r) || unicode.IsNumber(r)
		if isToken && !inToken {
			count++
		}
		inToken = isToken
	}
	return count
}

// SearchRAGChunks ranks the chunks of one chat with BM25-lite scoring computed in Go: the chat has
// no FTS index (modernc.org/sqlite builds without FTS5), so the corpus is read, counted and scored
// here. Every query term contributes idf × a saturating term-frequency factor, where the denominator
// carries the chunk length, so a chunk gains from the terms it covers rather than from the amount of
// text it holds. The document-frequency and average-length pass needs the whole corpus, but only
// content or name matches remain candidates for scoring.
func SearchRAGChunks(ctx context.Context, db *sql.DB, chatID int64, query string, limit int, maxRunes int) ([]RAGChunk, error) {
	query = ragQueryPrefix(query)
	terms := lexicalTerms(query)
	if len(terms) == 0 || limit <= 0 || maxRunes <= 0 {
		return []RAGChunk{}, nil
	}
	corpus, err := readRAGSearchCorpus(ctx, db, chatID, terms)
	if err != nil {
		return nil, err
	}
	if corpus.chunkCount == 0 {
		return []RAGChunk{}, nil
	}
	averageTokens := float64(corpus.totalTokens) / float64(corpus.chunkCount)
	if averageTokens < 1 {
		averageTokens = 1
	}
	phrase := strings.ToLower(strings.TrimSpace(query))
	phraseLong := len([]rune(phrase)) >= 2
	scored := corpus.candidates
	matched := scored[:0]
	for index := range scored {
		row := &scored[index]
		lengthNormalizer := ragK1 * ((1 - ragB) + ragB*float64(row.tokens)/averageTokens)
		// The terms slice, not the occurred map, fixes the summation order, so equal chunks get
		// bit-identical scores and the id tie-break decides instead of float noise.
		core := 0.0
		for _, term := range terms {
			frequency := float64(row.occurred[term])
			if frequency == 0 {
				continue
			}
			idf := 1 + math.Log(float64(corpus.chunkCount)/float64(corpus.documents[term]))
			core += idf * frequency * (ragK1 + 1) / (frequency + lengthNormalizer)
		}
		score := core * ragScoreScale * ragLengthPrior(row.tokens, averageTokens)
		score += ragDistinctTermBonus * float64(len(row.occurred))
		score += ragNameTermBonus * float64(row.nameHits)
		if phraseLong && strings.Contains(row.lowerText, phrase) {
			score += ragPhraseBonus
		}
		if score <= 0 {
			continue
		}
		row.rankScore = score
		row.Score = int(math.Round(score))
		matched = append(matched, *row)
	}
	scored = matched
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].rankScore != scored[j].rankScore {
			return scored[i].rankScore > scored[j].rankScore
		}
		return scored[i].id > scored[j].id
	})
	out := []RAGChunk{}
	used := 0
	for _, row := range scored {
		if len(out) == limit || used >= maxRunes {
			break
		}
		content := row.Content
		if used+len([]rune(content)) > maxRunes {
			room := maxRunes - used
			if room <= 0 {
				break
			}
			content = string([]rune(content)[:room])
		}
		row.Content = content
		used += len([]rune(content))
		out = append(out, row.RAGChunk)
	}
	return out, nil
}

// lexicalTokens splits text into the lowercased word tokens the ranking counts; letters and digits
// of any script are token characters, so Cyrillic and Latin are handled the same way.
func lexicalTokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

func ragQueryPrefix(query string) string {
	if len(query) <= RAGQueryMaxRunes {
		return query
	}
	runes := 0
	for index := range query {
		if runes == RAGQueryMaxRunes {
			return query[:index]
		}
		runes++
	}
	return query
}

func lexicalTerms(query string) []string {
	seen := map[string]bool{}
	terms := []string{}
	for _, term := range lexicalTokens(ragQueryPrefix(query)) {
		if len([]rune(term)) >= 2 && !seen[term] {
			seen[term] = true
			terms = append(terms, term)
			if len(terms) == RAGQueryMaxTerms {
				break
			}
		}
	}
	return terms
}

func (d RAGDocument) String() string {
	return fmt.Sprintf("%d:%s", d.ID, d.Name)
}
