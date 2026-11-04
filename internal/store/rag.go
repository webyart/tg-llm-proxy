package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	RAGDocumentLimit = 20
	RAGChunkRunes    = 1000
	RAGChunkOverlap  = 100
)

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

func SearchRAGChunks(ctx context.Context, db *sql.DB, chatID int64, query string, limit int, maxRunes int) ([]RAGChunk, error) {
	terms := lexicalTerms(query)
	if len(terms) == 0 || limit <= 0 || maxRunes <= 0 {
		return []RAGChunk{}, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT c.id, c.document_id, d.name, c.content FROM rag_chunks c
		JOIN rag_documents d ON d.id = c.document_id WHERE c.chat_id = ? ORDER BY c.id DESC LIMIT 10000`, chatID)
	if err != nil {
		return nil, err
	}
	type scoredChunk struct {
		RAGChunk
		id int
	}
	scored := []scoredChunk{}
	phrase := strings.ToLower(strings.TrimSpace(query))
	for rows.Next() {
		var row scoredChunk
		if err := rows.Scan(&row.id, &row.DocumentID, &row.Name, &row.Content); err != nil {
			rows.Close()
			return nil, err
		}
		text := strings.ToLower(row.Content)
		name := strings.ToLower(row.Name)
		score := 0
		for _, term := range terms {
			occurrences := strings.Count(text, term)
			if occurrences > 0 {
				score += 10 + min(occurrences, 3)
			}
			if strings.Contains(name, term) {
				score += 4
			}
		}
		if len([]rune(phrase)) >= 2 && strings.Contains(text, phrase) {
			score += 25
		}
		if score > 0 {
			row.Score = score
			scored = append(scored, row)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
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

func lexicalTerms(query string) []string {
	seen := map[string]bool{}
	terms := []string{}
	for _, term := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len([]rune(term)) >= 2 && !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	return terms
}

func (d RAGDocument) String() string {
	return fmt.Sprintf("%d:%s", d.ID, d.Name)
}
