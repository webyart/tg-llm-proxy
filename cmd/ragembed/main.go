// Command ragembed fills the vector side of the hybrid RAG search for chunks that do not have one yet.
//
// A database written before v1.48 has documents and chunks but no rag_chunk_embeddings rows, and the bot only
// embeds a document at the moment it is added. This command walks the existing chunks (all chats, or one -chat),
// embeds them through an OpenAI-compatible server and prints the progress. It is safe to run more than once: a
// chunk that already has a vector of the selected model is skipped, so an interrupted run continues where it
// stopped.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"bot/internal/config"
	"bot/internal/embed"
	"bot/internal/store"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "ragembed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer, errOut io.Writer) error {
	flags := flag.NewFlagSet("ragembed", flag.ContinueOnError)
	flags.SetOutput(errOut)
	path := flags.String("path", config.DefaultDBPath, "SQLite database to embed")
	baseURL := flags.String("base-url", config.DefaultRAGEmbeddingsBaseURL, "OpenAI-compatible base URL, /v1 included")
	model := flags.String("model", config.DefaultRAGEmbeddingsModel, "embedding model id; it keys the stored vectors")
	chatID := flags.Int64("chat", 0, "embed only this chat id; 0 embeds every chat that owns chunks")
	batch := flags.Int("batch", embed.MaxBatch, "chunks per request")
	timeout := flags.Duration("timeout", time.Duration(config.DefaultRAGEmbeddingsTimeoutSeconds)*time.Second, "timeout of one request")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*model) == "" {
		return fmt.Errorf("-model is empty")
	}
	if *batch <= 0 {
		return fmt.Errorf("-batch must be positive")
	}
	if *batch > embed.MaxBatch {
		fmt.Fprintf(errOut, "ragembed: -batch %d is above the client cap %d, using %d\n", *batch, embed.MaxBatch, embed.MaxBatch)
		*batch = embed.MaxBatch
	}
	if info, err := os.Stat(*path); err != nil || info.IsDir() {
		return fmt.Errorf("the database %q does not exist", *path)
	}
	db, err := store.Open(ctx, *path)
	if err != nil {
		return err
	}
	defer db.Close()

	chats, err := selectedChats(ctx, db, *chatID)
	if err != nil {
		return err
	}
	if len(chats) == 0 {
		fmt.Fprintln(out, "no chat owns a RAG chunk, nothing to embed")
		return nil
	}
	var missingTotal int
	for _, chat := range chats {
		embedded, total, err := store.EmbeddingStats(ctx, db, chat, *model)
		if err != nil {
			return err
		}
		missingTotal += total - embedded
	}
	if missingTotal == 0 {
		fmt.Fprintf(out, "every chunk of %d chat(s) already has a %s vector, nothing to embed\n", len(chats), *model)
		return nil
	}
	fmt.Fprintf(out, "embedding %d missing chunk(s) of %d chat(s) with %s at %s\n", missingTotal, len(chats), *model, *baseURL)

	client := embed.New(*baseURL, *model, "", *timeout)
	embeddedTotal := 0
	for _, chat := range chats {
		chatEmbedded := 0
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			missing, err := store.ChunksMissingEmbedding(ctx, db, chat, *model, *batch)
			if err != nil {
				return err
			}
			if len(missing) == 0 {
				break
			}
			texts := make([]string, len(missing))
			for index, chunk := range missing {
				texts[index] = chunk.Content
			}
			vectors, err := client.Embed(ctx, texts)
			if err != nil {
				// The same rows would be selected again, so this chat stops here instead of looping forever;
				// the other chats are still tried and the exit code depends on whether anything was stored.
				fmt.Fprintf(errOut, "chat %d: a batch of %d chunk(s) failed: %v\n", chat, len(missing), err)
				break
			}
			if len(vectors) != len(missing) {
				fmt.Fprintf(errOut, "chat %d: %d vectors for %d chunks, batch skipped\n", chat, len(vectors), len(missing))
				break
			}
			stored := 0
			for index, vector := range vectors {
				if err := store.PutChunkEmbedding(ctx, db, chat, missing[index].ID, *model, vector); err != nil {
					return err
				}
				stored++
			}
			chatEmbedded += stored
			embeddedTotal += stored
			fmt.Fprintf(out, "chat %d: embedded %d of this batch, %d in this chat\n", chat, stored, chatEmbedded)
		}
		fmt.Fprintf(out, "chat %d: done, %d chunk(s) embedded\n", chat, chatEmbedded)
	}
	fmt.Fprintf(out, "embedded %d of %d missing chunk(s)\n", embeddedTotal, missingTotal)
	if embeddedTotal == 0 {
		return fmt.Errorf("nothing could be embedded: the endpoint %s did not answer with vectors", *baseURL)
	}
	return nil
}

// selectedChats resolves -chat: a positive id selects one chat, 0 selects every chat that owns a chunk.
func selectedChats(ctx context.Context, db *sql.DB, chatID int64) ([]int64, error) {
	if chatID < 0 {
		return nil, fmt.Errorf("-chat must be positive or 0 for every chat")
	}
	if chatID > 0 {
		return []int64{chatID}, nil
	}
	return store.RAGChatIDs(ctx, db)
}
