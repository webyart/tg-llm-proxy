package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bot/internal/store"
)

// fakeEmbeddingClient is the hybrid client of these tests: a two-dimensional fake whose first dimension is the
// "restart" topic, so a paraphrase of that topic is close to the chunk that describes it.
type fakeEmbeddingClient struct {
	calls int
	err   error
}

func (f *fakeEmbeddingClient) Model() string { return "fake-2d" }

func (f *fakeEmbeddingClient) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for index, text := range texts {
		if strings.Contains(strings.ToLower(text), "перезапус") || strings.Contains(strings.ToLower(text), "systemctl") {
			out[index] = []float32{1, 0}
		} else {
			out[index] = []float32{0, 1}
		}
	}
	return out, nil
}

func TestRAGIngestionStoresChunkEmbeddings(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, nil, db)
	d.deps.Embeddings = &fakeEmbeddingClient{}

	const chatID = int64(420)
	documentID, chunks, err := store.PutRAGDocument(ctx, db, chatID, 11, "restart.md",
		"Перезапуск сервиса выполняется командой systemctl restart bot.service.")
	if err != nil {
		t.Fatalf("PutRAGDocument: %v", err)
	}
	if chunks != 1 {
		t.Fatalf("chunks=%d, want 1", chunks)
	}
	d.embedDocumentChunks(ctx, chatID, documentID)

	embedded, total, err := store.EmbeddingStats(ctx, db, chatID, "fake-2d")
	if err != nil || embedded != 1 || total != 1 {
		t.Fatalf("embedding stats=%d/%d err=%v, want 1/1", embedded, total, err)
	}

	// The lexical query has no term of the document; the vector is what finds it.
	result, err := d.searchRAG(ctx, chatID, "как перезапустить службу", 5, 1<<20)
	if err != nil {
		t.Fatalf("searchRAG: %v", err)
	}
	if len(result.Chunks) == 0 || result.Chunks[0].Name != "restart.md" {
		t.Fatalf("hybrid search=%+v, want the restart document first", result.Chunks)
	}
}

func TestRAGSearchFallsBackWhenEmbeddingFails(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, nil, db)
	fake := &fakeEmbeddingClient{err: errors.New("embedding server is down")}
	d.deps.Embeddings = fake

	const chatID = int64(421)
	if _, _, err := store.PutRAGDocument(ctx, db, chatID, 11, "handbook.txt", "Aurora deployment uses a local runbook."); err != nil {
		t.Fatalf("PutRAGDocument: %v", err)
	}
	result, err := d.searchRAG(ctx, chatID, "Aurora deployment", 5, 1<<20)
	if err != nil {
		t.Fatalf("a failing embedding server must not fail the search: %v", err)
	}
	if len(result.Chunks) != 1 || result.Chunks[0].Name != "handbook.txt" {
		t.Fatalf("lexical fallback=%+v, want the handbook chunk", result.Chunks)
	}
	if fake.calls != 1 {
		t.Fatalf("embedding calls=%d, want exactly one per request", fake.calls)
	}
}

func TestRAGIngestionFailureLeavesTheDocumentSearchable(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, nil, db)
	d.deps.Embeddings = &fakeEmbeddingClient{err: errors.New("embedding server is down")}

	const chatID = int64(422)
	documentID, chunks, err := store.PutRAGDocument(ctx, db, chatID, 11, "aurora.txt", "Aurora local runbook")
	if err != nil {
		t.Fatalf("PutRAGDocument: %v", err)
	}
	if chunks != 1 {
		t.Fatalf("chunks=%d, want 1", chunks)
	}
	d.embedDocumentChunks(ctx, chatID, documentID)
	if embedded, total, err := store.EmbeddingStats(ctx, db, chatID, "fake-2d"); err != nil || embedded != 0 || total != 1 {
		t.Fatalf("embedding stats=%d/%d err=%v, want 0/1 after a failed embedding", embedded, total, err)
	}
	result, err := d.searchRAG(ctx, chatID, "Aurora runbook", 5, 1<<20)
	if err != nil || len(result.Chunks) != 1 || result.Chunks[0].Name != "aurora.txt" {
		t.Fatalf("the document is not searchable after the failure: result=%+v err=%v", result.Chunks, err)
	}
}

func TestRAGWithoutEmbeddingClientIsLexical(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, nil, db)
	const chatID = int64(423)
	if _, _, err := store.PutRAGDocument(ctx, db, chatID, 11, "handbook.txt", "Aurora deployment uses a local runbook."); err != nil {
		t.Fatalf("PutRAGDocument: %v", err)
	}
	result, err := d.searchRAG(ctx, chatID, "Aurora", 5, 1<<20)
	if err != nil || len(result.Chunks) != 1 {
		t.Fatalf("lexical search=%+v err=%v", result.Chunks, err)
	}
}
