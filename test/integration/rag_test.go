//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot/internal/bot"
	"bot/internal/store"
	"bot/internal/testutil"
)

func TestRAGDocumentEndToEnd(t *testing.T) {
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "Aurora использует локальные модели"})
	cfg := integrationEnv(t, path, strata.BaseURL(), nil)
	db := openStore(t, path)
	startBot(t, t.Context(), cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, storedOffset(t, db))
	if version, err := store.SchemaVersion(context.Background(), db); err != nil || version != 5 {
		t.Fatalf("schema=%d err=%v", version, err)
	}
	tgAPI.AddFile("rag-file", "documents/aurora.txt", "text/plain", []byte("Проект Aurora использует локальные модели."))
	tgAPI.PushDocument(601, chatID, userID, "rag-file", "aurora.txt", "text/plain", "/docs add")
	waitFor(t, "RAG document stored", func() bool { return strings.Contains(tgAPI.Joined(), "сохранён") })
	tgAPI.PushText(602, chatID, userID, "Что известно про Aurora?")
	waitFor(t, "RAG answer", func() bool {
		return strings.Contains(tgAPI.Joined(), "Aurora использует локальные модели")
	})
	body := strata.LastRequestBody()
	if !strings.Contains(body, "Релевантные фрагменты") || !strings.Contains(body, "aurora.txt") || !strings.Contains(body, "Проект Aurora") {
		t.Fatalf("RAG request=%s", body)
	}
	tgAPI.PushText(603, chatID, userID, "/docs delete 1")
	waitFor(t, "RAG delete", func() bool { return strings.Contains(tgAPI.Joined(), "Документ id=1 удалён") })
	docs, err := store.ListRAGDocuments(context.Background(), db, chatID)
	if err != nil || len(docs) != 0 || strata.ChatCalls() != 1 {
		t.Fatalf("docs=%+v chat calls=%d err=%v", docs, strata.ChatCalls(), err)
	}
}
