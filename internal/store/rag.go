package store

import (
	"container/heap"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/bits"
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

const (
	// ragCandidateLimit bounds how many chunks the Go scorer reads. bm25() ranks every matching chunk in SQL,
	// so the cap keeps the best candidates instead of the newest rows: 300 is far above the handful a question
	// returns, yet bounded on a 10 000-chunk corpus. It is also the reported "scored chunks" ceiling.
	ragCandidateLimit       = 300
	ragChunkInsertBatchSize = 100
)

// ragFTSCandidateQuery is the bm25-ranked branch of the candidate union. MATCH and the chat filter are bound
// parameters; user text never reaches the SQL string. ORDER BY rank is ascending because FTS5's bm25() is
// negative and more negative means a better match.
const ragFTSCandidateQuery = `SELECT c.id, c.document_id, d.name, c.content, bm25(rag_chunks_fts) AS rank
	FROM rag_chunks_fts
	JOIN rag_chunks c ON c.id = rag_chunks_fts.rowid
	JOIN rag_documents d ON d.id = c.document_id
	WHERE rag_chunks_fts MATCH ? AND c.chat_id = ?
	ORDER BY rank
	LIMIT ?`

// BM25-lite weights of SearchRAGChunks. ragB is 1 so chunk length is normalised in full: a chunk
// gains only from the terms it actually covers, never from repeating one term. ragScoreScale keeps
// the core term score in the same order of magnitude as the name and phrase bonuses below.
const (
	ragK1                  = 1.2
	ragB                   = 1.0
	ragScoreScale          = 10.0
	ragNameTermBonus       = 4.0
	ragPhraseBonus         = 25.0
	ragDistinctTermBonus   = 12.0
	ragMinimumRelevance    = 5.0
	ragMinimumTermCoverage = 0.25
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

type RAGSearchResult struct {
	Chunks          []RAGChunk
	CorpusTruncated bool
	ScannedChunks   int
	TotalChunks     int
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
	if err := insertRAGChunks(ctx, tx, documentID, chatID, chunks); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return documentID, len(chunks), nil
}

func insertRAGChunks(ctx context.Context, tx *sql.Tx, documentID int64, chatID int64, chunks []string) error {
	for start := 0; start < len(chunks); start += ragChunkInsertBatchSize {
		end := start + ragChunkInsertBatchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		var statement strings.Builder
		statement.WriteString("INSERT INTO rag_chunks (document_id, chat_id, ordinal, content) VALUES ")
		args := make([]any, 0, (end-start)*4)
		for index := start; index < end; index++ {
			if index > start {
				statement.WriteByte(',')
			}
			statement.WriteString("(?, ?, ?, ?)")
			args = append(args, documentID, chatID, index, chunks[index])
		}
		if _, err := tx.ExecContext(ctx, statement.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

func chunkRAGText(content string) []string {
	runes := []rune(strings.TrimSpace(content))
	chunks := []string{}
	for start := 0; start < len(runes); {
		end := start + RAGChunkRunes
		if end >= len(runes) {
			chunks = append(chunks, string(runes[start:]))
			break
		}
		end = ragSentenceChunkEnd(runes, start, end)
		chunk := strings.TrimSpace(string(runes[start:end]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		next := end - RAGChunkOverlap
		if next <= start {
			next = start + 1
		}
		next = ragSentenceOverlapStart(runes, next, end)
		for next < len(runes) && unicode.IsSpace(runes[next]) {
			next++
		}
		if next <= start {
			next = start + 1
		}
		start = next
	}
	return chunks
}

func ragSentenceChunkEnd(runes []rune, start int, limit int) int {
	minimum := start + RAGChunkRunes/2
	for end := limit; end > minimum; end-- {
		if ragSentenceBoundary(runes, end) {
			return end
		}
	}
	return limit
}

func ragSentenceOverlapStart(runes []rune, start int, end int) int {
	for candidate := start; candidate <= end; candidate++ {
		if !ragSentenceBoundary(runes, candidate) {
			continue
		}
		for candidate < len(runes) && unicode.IsSpace(runes[candidate]) {
			candidate++
		}
		return candidate
	}
	return start
}

func ragSentenceBoundary(runes []rune, end int) bool {
	if end <= 0 || end > len(runes) {
		return false
	}
	if runes[end-1] == '\n' {
		return true
	}
	if end < len(runes) && !unicode.IsSpace(runes[end]) {
		return false
	}
	index := end - 1
	for index >= 0 && strings.ContainsRune("\"'”’»)]}", runes[index]) {
		index--
	}
	if index < 0 {
		return false
	}
	return strings.ContainsRune(".!?…", runes[index]) || runes[index] == '\n'
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
	id           int
	tokens       int
	occurred     map[string]int
	textTokens   []string
	nameHits     int
	matchedTerms uint32
	coverage     float64
	rankScore    float64
}

type ragTopKHeap []ragScoredChunk

func (h ragTopKHeap) Len() int { return len(h) }

func (h ragTopKHeap) Less(i, j int) bool {
	if h[i].rankScore != h[j].rankScore {
		return h[i].rankScore < h[j].rankScore
	}
	return h[i].id < h[j].id
}

func (h ragTopKHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *ragTopKHeap) Push(value any) {
	*h = append(*h, value.(ragScoredChunk))
}

func (h *ragTopKHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	old[last] = ragScoredChunk{}
	*h = old[:last]
	return value
}

func ragChunkRanksBefore(left, right ragScoredChunk) bool {
	if left.rankScore != right.rankScore {
		return left.rankScore > right.rankScore
	}
	return left.id > right.id
}

type ragSearchCorpus struct {
	candidates  []ragScoredChunk
	documents   map[string]int
	chunkCount  int
	totalChunks int
	totalTokens int
}

// ragDocumentsMatchingTerms — ids of the chat's documents whose NAME mentions one of the terms. The name is
// part of the score, so a chunk of a document named after the question must stay a candidate even when its own
// text does not repeat the word.
func ragDocumentsMatchingTerms(ctx context.Context, db *sql.DB, chatID int64, terms []string) ([]int64, error) {
	var wanted = make(map[string]bool, len(terms))
	for _, term := range terms {
		wanted[term] = true
	}
	rows, err := db.QueryContext(ctx, `SELECT id, name FROM rag_documents WHERE chat_id = ?`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		for _, token := range lexicalTokens(name) {
			if wanted[token] {
				ids = append(ids, id)
				break
			}
		}
	}
	return ids, rows.Err()
}

// placeholders — "?,?,?" for an IN clause of n values.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// readRAGSearchCorpus loads the chunks the Go scorer will rank. The FTS5 branch asks the database for the
// bm25-best chunks of the chat, so a 10 000-chunk corpus is no longer tokenized in Go on every question. The
// name branch is a separate union: a chunk whose own text does not mention the term still matters when its
// document is named after the question, and name matches are rare enough to reserve room for them. When both
// branches together exceed ragCandidateLimit, the weakest bm25 tail gives way first.
func readRAGSearchCorpus(ctx context.Context, db *sql.DB, chatID int64, terms []string) (ragSearchCorpus, error) {
	var totalChunks int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rag_chunks WHERE chat_id = ?`, chatID).Scan(&totalChunks); err != nil {
		return ragSearchCorpus{}, err
	}
	var expression, ok = ragSearchExpression(terms)
	if !ok {
		return ragSearchCorpus{documents: map[string]int{}, totalChunks: totalChunks}, nil
	}
	var nameDocuments, nameErr = ragDocumentsMatchingTerms(ctx, db, chatID, terms)
	if nameErr != nil {
		return ragSearchCorpus{}, nameErr
	}

	ftsCandidates, err := readRAGFTSCandidates(ctx, db, chatID, expression)
	if err != nil {
		if !ragCandidateExpressionRejected(err) {
			return ragSearchCorpus{}, err
		}
		// A search must not fail because the index rejected the expression: the name union can still answer.
		ftsCandidates = nil
	}
	nameCandidates, err := readRAGNameCandidates(ctx, db, nameDocuments, ftsCandidates)
	if err != nil {
		return ragSearchCorpus{}, err
	}
	if len(nameCandidates) > ragCandidateLimit {
		nameCandidates = nameCandidates[:ragCandidateLimit]
	}
	if budget := ragCandidateLimit - len(nameCandidates); len(ftsCandidates) > budget {
		ftsCandidates = ftsCandidates[:budget]
	}

	termIndexes := make(map[string]int, len(terms))
	for index, term := range terms {
		termIndexes[term] = index
	}
	corpus := ragSearchCorpus{documents: map[string]int{}, totalChunks: totalChunks}
	for _, row := range ftsCandidates {
		ragAccumulateCandidate(&corpus, row, terms, termIndexes)
	}
	for _, row := range nameCandidates {
		ragAccumulateCandidate(&corpus, row, terms, termIndexes)
	}
	return corpus, nil
}

// readRAGFTSCandidates runs the bm25-ranked candidate query. A database written before the index migration (or
// a broken MATCH expression) is reported through ragCandidateExpressionRejected, not as a search failure.
func readRAGFTSCandidates(ctx context.Context, db *sql.DB, chatID int64, expression string) ([]ragScoredChunk, error) {
	rows, err := db.QueryContext(ctx, ragFTSCandidateQuery, expression, chatID, ragCandidateLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []ragScoredChunk
	for rows.Next() {
		var row ragScoredChunk
		var rank float64
		if err := rows.Scan(&row.id, &row.DocumentID, &row.Name, &row.Content, &rank); err != nil {
			return nil, err
		}
		candidates = append(candidates, row)
	}
	return candidates, rows.Err()
}

// ragNameCandidateQuery is the second branch of the candidate union: every chunk of a name-matching document.
// The IN list is placeholders only; document ids are bound arguments. The chat filter is not repeated here
// because ragDocumentsMatchingTerms already selected the ids from this chat, and the document_id index of
// rag_chunks is what the plan can use without walking every chunk of the chat.
func ragNameCandidateQuery(documentIDs int) string {
	return `SELECT c.id, c.document_id, d.name, c.content FROM rag_chunks c
		JOIN rag_documents d ON d.id = c.document_id
		WHERE c.document_id IN (` + placeholders(documentIDs) + `)
		ORDER BY c.id DESC LIMIT ?`
}

// readRAGNameCandidates returns the chunks of documents whose name mentions a term, excluding the chunks the
// FTS branch already selected. It is the second branch of the candidate union.
func readRAGNameCandidates(ctx context.Context, db *sql.DB, documentIDs []int64, already []ragScoredChunk) ([]ragScoredChunk, error) {
	if len(documentIDs) == 0 {
		return nil, nil
	}
	var seen = make(map[int]bool, len(already))
	for _, row := range already {
		seen[row.id] = true
	}
	var query = ragNameCandidateQuery(len(documentIDs))
	var args = make([]any, 0, len(documentIDs)+1)
	for _, id := range documentIDs {
		args = append(args, id)
	}
	args = append(args, ragCandidateLimit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []ragScoredChunk
	for rows.Next() {
		var row ragScoredChunk
		if err := rows.Scan(&row.id, &row.DocumentID, &row.Name, &row.Content); err != nil {
			return nil, err
		}
		if seen[row.id] {
			continue
		}
		seen[row.id] = true
		candidates = append(candidates, row)
	}
	return candidates, rows.Err()
}

// ragAccumulateCandidate tokenizes one SQL-selected chunk and adds its term statistics to the corpus. Every
// selected chunk is counted, but only a chunk with an exact token or name hit becomes a scored candidate.
func ragAccumulateCandidate(corpus *ragSearchCorpus, row ragScoredChunk, terms []string, termIndexes map[string]int) {
	row.tokens, row.occurred = lexicalTokenOccurrences(row.Content, termIndexes)
	nameTerms := uint32(0)
	for _, token := range lexicalTokens(row.Name) {
		index, wanted := termIndexes[token]
		if !wanted {
			continue
		}
		bit := uint32(1) << index
		if nameTerms&bit == 0 {
			row.nameHits++
			nameTerms |= bit
			row.matchedTerms |= bit
		}
	}
	for index, term := range terms {
		if row.occurred[term] > 0 {
			corpus.documents[term]++
			row.matchedTerms |= uint32(1) << index
		}
	}
	corpus.chunkCount++
	corpus.totalTokens += row.tokens
	if len(row.occurred) > 0 || row.nameHits > 0 {
		row.textTokens = lexicalTokens(row.Content)
		corpus.candidates = append(corpus.candidates, row)
	}
}

// ragCandidateExpressionRejected distinguishes an index that cannot answer this MATCH expression (malformed
// FTS5, a literal the query parser cuts short, or a schema older than migration 014) from a database failure:
// only the former degrades to name matches.
func ragCandidateExpressionRejected(err error) bool {
	var message = strings.ToLower(err.Error())
	for _, marker := range []string{"no such table: rag_chunks_fts", "unterminated string", "unrecognized token"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return strings.Contains(message, "fts5") &&
		(strings.Contains(message, "syntax") || strings.Contains(message, "malformed") || strings.Contains(message, "unrecognized"))
}

func lexicalTokenCount(text string) int {
	return len(lexicalTokens(text))
}

// SearchRAGChunks keeps the original result-only API.
func SearchRAGChunks(ctx context.Context, db *sql.DB, chatID int64, query string, limit int, maxRunes int) ([]RAGChunk, error) {
	result, err := SearchRAGChunksDetailed(ctx, db, chatID, query, limit, maxRunes)
	if err != nil {
		return nil, err
	}
	return result.Chunks, nil
}

// SearchRAGChunksDetailed ranks the bm25-best chunks of one chat with the BM25-lite scoring computed in Go.
// SQL selects at most ragCandidateLimit candidates (and their token statistics); the same formula, phrase and
// name bonuses, coverage floor and truncation rules still apply, so quality safeguards are unchanged. The
// result reports when the candidate cap did not cover every matching chunk of the chat.
func SearchRAGChunksDetailed(ctx context.Context, db *sql.DB, chatID int64, query string, limit int, maxRunes int) (RAGSearchResult, error) {
	result := RAGSearchResult{Chunks: []RAGChunk{}}
	query = ragQueryPrefix(query)
	terms := lexicalTerms(query)
	if len(terms) == 0 || limit <= 0 || maxRunes <= 0 {
		return result, nil
	}
	corpus, err := readRAGSearchCorpus(ctx, db, chatID, terms)
	if err != nil {
		return RAGSearchResult{}, err
	}
	result.TotalChunks = corpus.totalChunks
	result.ScannedChunks = len(corpus.candidates)
	result.CorpusTruncated = corpus.chunkCount == ragCandidateLimit && corpus.totalChunks > corpus.chunkCount
	if corpus.chunkCount == 0 {
		return result, nil
	}
	averageTokens := float64(corpus.totalTokens) / float64(corpus.chunkCount)
	if averageTokens < 1 {
		averageTokens = 1
	}
	phraseTokens := lexicalTokens(strings.TrimSpace(query))
	phraseLong := len([]rune(strings.TrimSpace(query))) >= 2
	scored := corpus.candidates
	matched := scored[:0]
	bestCoverage := 0.0
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
		if phraseLong && ragContainsTokenPhrase(row.textTokens, phraseTokens) {
			score += ragPhraseBonus
		}
		minimumRelevance := ragMinimumRelevance
		if len(terms) == 1 {
			minimumRelevance = ragNameTermBonus
		}
		if score < minimumRelevance {
			continue
		}
		row.coverage = float64(bits.OnesCount32(row.matchedTerms)) / float64(len(terms))
		if row.coverage > bestCoverage {
			bestCoverage = row.coverage
		}
		row.rankScore = score
		row.Score = int(math.Round(score))
		matched = append(matched, *row)
	}
	scored = matched
	minimumCoverage := ragMinimumTermCoverage
	if bestCoverage < minimumCoverage {
		minimumCoverage = bestCoverage
	}
	top := &ragTopKHeap{}
	for _, row := range scored {
		if row.coverage < minimumCoverage {
			continue
		}
		if top.Len() < limit {
			heap.Push(top, row)
			continue
		}
		if ragChunkRanksBefore(row, (*top)[0]) {
			(*top)[0] = row
			heap.Fix(top, 0)
		}
	}
	scored = *top
	sort.Slice(scored, func(i, j int) bool {
		return ragChunkRanksBefore(scored[i], scored[j])
	})
	used := 0
	for _, row := range scored {
		if len(result.Chunks) == limit || used >= maxRunes {
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
		result.Chunks = append(result.Chunks, row.RAGChunk)
	}
	return result, nil
}

func ragContainsTokenPhrase(tokens []string, phrase []string) bool {
	if len(phrase) == 0 || len(phrase) > len(tokens) {
		return false
	}
	for start := 0; start+len(phrase) <= len(tokens); start++ {
		matched := true
		for index := range phrase {
			if tokens[start+index] != phrase[index] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func lexicalTokenOccurrences(text string, terms map[string]int) (int, map[string]int) {
	lowered := strings.ToLower(text)
	tokens := 0
	var occurred map[string]int
	start := -1
	for index, r := range lowered {
		if !ragLexicalTokenBoundary(r) {
			if start < 0 {
				start = index
			}
			continue
		}
		if start < 0 {
			continue
		}
		token := lowered[start:index]
		tokens++
		if _, wanted := terms[token]; wanted {
			if occurred == nil {
				occurred = map[string]int{}
			}
			occurred[token]++
		}
		start = -1
	}
	if start >= 0 {
		token := lowered[start:]
		tokens++
		if _, wanted := terms[token]; wanted {
			if occurred == nil {
				occurred = map[string]int{}
			}
			occurred[token]++
		}
	}
	return tokens, occurred
}

func ragLexicalTokenBoundary(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
}

// lexicalTokens splits text into lowercase word tokens. Letters, marks and digits of every script
// stay inside a token, so matching uses Unicode token boundaries rather than byte substrings.
func lexicalTokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), ragLexicalTokenBoundary)
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

var ragStopwords = map[string]bool{
	"about": true, "after": true, "again": true, "against": true, "all": true, "also": true,
	"an": true, "and": true, "any": true, "are": true, "as": true, "at": true, "be": true,
	"because": true, "been": true, "before": true, "by": true,
	"being": true, "below": true, "between": true, "both": true, "but": true, "can": true,
	"could": true, "did": true, "does": true, "doing": true, "down": true, "during": true,
	"each": true, "few": true, "for": true, "from": true, "further": true, "had": true,
	"has": true, "have": true, "having": true, "he": true, "here": true, "hers": true,
	"herself": true, "him": true, "himself": true, "his": true, "how": true, "in": true,
	"into": true, "is": true, "it": true, "its": true, "itself": true, "just": true,
	"me": true, "more": true, "most": true, "myself": true, "no": true, "nor": true,
	"not": true, "now": true, "of": true, "off": true, "on": true, "once": true,
	"only": true, "or": true, "other": true, "our": true, "ours": true, "ourselves": true,
	"out": true, "over": true, "own": true, "same": true, "she": true, "should": true,
	"some": true, "such": true, "than": true, "that": true, "the": true, "their": true,
	"theirs": true, "them": true, "themselves": true, "then": true, "there": true,
	"these": true, "they": true, "this": true, "those": true, "through": true, "to": true,
	"too": true, "under": true, "until": true, "up": true, "us": true, "very": true,
	"was": true, "we": true, "were": true, "what": true, "when": true, "where": true,
	"which": true, "while": true, "who": true, "whom": true, "why": true, "will": true,
	"with": true, "you": true, "your": true, "yours": true, "yourself": true, "yourselves": true,
	"без": true, "более": true, "бы": true, "был": true, "была": true, "были": true,
	"было": true, "быть": true, "вам": true, "вас": true, "ведь": true, "весь": true,
	"во": true, "вот": true, "все": true, "всего": true, "всех": true, "всю": true,
	"всё": true, "вы": true,
	"где": true, "да": true, "даже": true, "для": true, "до": true, "его": true,
	"ее": true, "ей": true, "ему": true, "если": true, "есть": true, "еще": true,
	"же": true, "за": true, "здесь": true, "из": true, "или": true, "им": true,
	"их": true, "как": true, "какая": true, "какой": true, "когда": true, "кто": true,
	"куда": true, "ли": true, "между": true, "меня": true, "мне": true, "много": true,
	"может": true, "можно": true, "мой": true, "моя": true, "мы": true, "на": true,
	"над": true, "нам": true, "нас": true, "не": true, "нее": true, "ней": true,
	"нет": true, "ни": true, "них": true, "ничего": true, "но": true, "ну": true,
	"об": true, "один": true, "она": true,
	"они": true, "от": true, "очень": true, "по": true, "под": true, "после": true,
	"потом": true, "потому": true, "почти": true, "при": true, "про": true, "раз": true,
	"разве": true, "сам": true, "свою": true, "себе": true, "себя": true, "сейчас": true,
	"со": true, "так": true, "такой": true, "там": true, "тебя": true, "тем": true,
	"теперь": true, "то": true, "того": true, "тоже": true, "только": true, "том": true,
	"тот": true, "тут": true, "ты": true, "уже": true, "хоть": true, "чего": true,
	"чем": true, "через": true, "что": true, "чтоб": true, "чтобы": true, "эта": true,
	"эти": true, "этого": true, "этой": true, "этом": true, "этот": true, "эту": true,
}

func lexicalTerms(query string) []string {
	seen := map[string]bool{}
	terms := []string{}
	for _, term := range lexicalTokens(ragQueryPrefix(query)) {
		if len([]rune(term)) >= 2 && !ragStopwords[term] && !seen[term] {
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
