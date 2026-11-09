package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tgbot "github.com/go-telegram/bot"
)

func TestDownloaderRejectsBodyBeyondLimit(t *testing.T) {
	payload := strings.Repeat("x", int(DocumentMaxBytes)+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"doc","file_path":"documents/a.txt","file_size":1}}`))
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()
	tg, err := tgbot.New("123:test-token", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}
	downloader := &telegramAttachmentDownloader{bot: tg, client: server.Client()}
	_, err = downloader.Download(context.Background(), Attachment{Kind: AttachmentDocument, FileID: "doc"})
	if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("Download error = %v, want ErrAttachmentTooLarge", err)
	}
}

func TestDownloaderUsesTheBinaryLimitForPDF(t *testing.T) {
	payload := strings.Repeat("x", int(DocumentMaxBytes)+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"doc","file_path":"documents/report.pdf","file_size":1}}`))
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()
	tg, err := tgbot.New("123:test-token", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}
	downloader := &telegramAttachmentDownloader{bot: tg, client: server.Client()}
	// A PDF above the 64 KiB text limit is downloaded: the 20 MiB binary limit applies to it.
	downloaded, err := downloader.Download(context.Background(),
		Attachment{Kind: AttachmentDocument, FileID: "doc", FileName: "report.pdf", MimeType: "application/pdf"})
	if err != nil {
		t.Fatalf("Download PDF error = %v", err)
	}
	if string(downloaded.Data) != payload {
		t.Fatalf("downloaded %d bytes, want %d", len(downloaded.Data), len(payload))
	}
	// The same bytes with a .txt name keep the text limit.
	if _, err := downloader.Download(context.Background(),
		Attachment{Kind: AttachmentDocument, FileID: "doc", FileName: "report.txt", MimeType: "text/plain"}); !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("Download text error = %v, want ErrAttachmentTooLarge", err)
	}
}

func TestDownloaderErrorsDoNotExposeToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"bad file"}`))
	}))
	defer server.Close()
	const token = "123:secret-token-value"
	tg, err := tgbot.New(token, tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}
	downloader := &telegramAttachmentDownloader{bot: tg, client: server.Client()}
	_, err = downloader.Download(context.Background(), Attachment{Kind: AttachmentPhoto, FileID: "photo"})
	if err == nil || strings.Contains(Sanitize(err, token), token) {
		t.Fatalf("sanitized error = %v", err)
	}
}
