//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"bot/internal/bot"
	"bot/internal/store"
	"bot/internal/testutil"
)

func TestPhotoAndTextDocumentOverFakeTelegram(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey,
		testutil.StrataReply{Content: "фото получено", Model: "fake-vision"},
		testutil.StrataReply{Content: "документ получен", Model: "fake-model"},
	)
	cfg := integrationEnv(t, path, strata.BaseURL(), nil)
	db := openStore(t, path)
	run := startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{
		ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second, Workers: 2,
	}, storedOffset(t, db))
	_ = run

	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}
	tgAPI.AddFile("photo-file", "photos/test.jpg", "image/jpeg", jpeg)
	tgAPI.PushPhoto(201, chatID, userID, "photo-file", "Что здесь?")
	waitFor(t, "photo answer", func() bool { return strings.Contains(tgAPI.Joined(), "фото получено") })
	var photoBody struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(strata.LastRequestBody()), &photoBody); err != nil {
		t.Fatalf("photo request JSON: %v", err)
	}
	last := photoBody.Messages[len(photoBody.Messages)-1].Content
	if !strings.Contains(string(last), `"image_url"`) || !strings.Contains(string(last), `data:image/jpeg;base64,`) || !strings.Contains(string(last), "Что здесь?") {
		t.Fatalf("photo content = %s", last)
	}

	tgAPI.AddFile("text-file", "documents/notes.txt", "text/plain", []byte("строка один\nстрока два"))
	tgAPI.PushDocument(202, chatID, userID, "text-file", "notes.txt", "text/plain", "Сделай выжимку")
	waitFor(t, "document answer", func() bool { return strings.Contains(tgAPI.Joined(), "документ получен") })
	body := strata.LastRequestBody()
	if !strings.Contains(body, "Сделай выжимку") || !strings.Contains(body, "строка один\\nстрока два") {
		t.Fatalf("document request lost caption or content: %s", body)
	}
	if tgAPI.Calls("getFile") != 2 || tgAPI.Calls("download") != 2 {
		t.Fatalf("getFile=%d download=%d, want 2 each", tgAPI.Calls("getFile"), tgAPI.Calls("download"))
	}
	rows := rowsOf(t, db)
	if len(rows) != 4 {
		t.Fatalf("stored messages = %d, want 4", len(rows))
	}
	for _, row := range rows {
		if strings.Contains(row.Content, "строка один") || strings.Contains(row.Content, "base64") {
			t.Fatal("attachment contents must not be persisted in SQLite")
		}
	}
}

func TestOversizedDocumentIsRejectedBeforeDownload(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "не должно быть"})
	cfg := integrationEnv(t, path, strata.BaseURL(), nil)
	db := openStore(t, path)
	startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, storedOffset(t, db))

	tgAPI.AddFile("large-file", "documents/large.txt", "text/plain", make([]byte, bot.DocumentMaxBytes+1))
	tgAPI.PushDocument(203, chatID, userID, "large-file", "large.txt", "text/plain", "прочитай")
	waitFor(t, "oversize rejection", func() bool { return strings.Contains(tgAPI.Joined(), "слишком большое") })
	if tgAPI.Calls("getFile") != 0 || tgAPI.Calls("download") != 0 || strata.ChatCalls() != 0 {
		t.Fatalf("oversize calls: getFile=%d download=%d model=%d", tgAPI.Calls("getFile"), tgAPI.Calls("download"), strata.ChatCalls())
	}
	rows, err := store.RecentByChat(context.Background(), db, chatID, 10)
	if err != nil || len(rows) != 1 || rows[0].Role != store.RoleUser {
		t.Fatalf("stored rows=%+v err=%v", rows, err)
	}
}
