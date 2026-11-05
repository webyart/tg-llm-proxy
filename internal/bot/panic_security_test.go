package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"bot/internal/config"
)

func TestRecoveredPanicRedactsConfiguredSecretsAndBoundsText(t *testing.T) {
	var logs bytes.Buffer
	replier := &fakeReplier{}
	d := New(Deps{
		Cfg: config.Config{
			BotToken:           "config-token-secret",
			StrataKey:          "model-key-secret",
			WebhookSecretToken: "webhook-secret",
		},
		Token:   "dependency-token-secret",
		Replier: replier,
		Logger:  slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	panicValue := "dependency-token-secret config-token-secret model-key-secret webhook-secret " + strings.Repeat("🙂", recoveredPanicMaxBytes)
	err := d.recovered(func(ctx context.Context, ev Event, cmd Command) error {
		panic(panicValue)
	})(context.Background(), Event{ChatID: 100, UserID: 11}, Command{Name: "ask"})
	if err == nil {
		t.Fatal("recovered panic must be returned as an error")
	}
	var entry struct {
		Panic string `json:"panic"`
	}
	if decodeErr := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry); decodeErr != nil {
		t.Fatalf("decode recovery log: %v; log=%q", decodeErr, logs.String())
	}
	for _, secret := range []string{"dependency-token-secret", "config-token-secret", "model-key-secret", "webhook-secret"} {
		if strings.Contains(entry.Panic, secret) || strings.Contains(err.Error(), secret) {
			t.Errorf("panic output contains configured secret %q: log=%q err=%q", secret, entry.Panic, err)
		}
	}
	if !strings.Contains(entry.Panic, "***") {
		t.Errorf("sanitized panic has no redaction marker: %q", entry.Panic)
	}
	if len(entry.Panic) > recoveredPanicMaxBytes || !utf8.ValidString(entry.Panic) {
		t.Errorf("panic is not bounded valid UTF-8: bytes=%d valid=%v", len(entry.Panic), utf8.ValidString(entry.Panic))
	}
	if want := "panic in the handler of /ask: " + entry.Panic; err.Error() != want {
		t.Errorf("returned and logged panic differ: err=%q want=%q", err, want)
	}
	if replier.count() != 1 || !strings.Contains(replier.joined(), msgPanic) {
		t.Errorf("normal panic notice changed: %q", replier.joined())
	}
}

type panicNoteReplier struct {
	panicValue string
}

func (r *panicNoteReplier) Send(ctx context.Context, chatID int64, text string) error {
	return nil
}

func (r *panicNoteReplier) noteChatUser(chatID int64, userID int64) {
	panic(r.panicValue)
}

func TestPreDispatchPanicRedactsConfiguredSecretsAndBoundsText(t *testing.T) {
	var logs bytes.Buffer
	replier := &panicNoteReplier{panicValue: "model-key-secret webhook-secret " + strings.Repeat("x", recoveredPanicMaxBytes*2)}
	d := New(Deps{
		Cfg: config.Config{
			StrataKey:          "model-key-secret",
			WebhookSecretToken: "webhook-secret",
		},
		Replier: replier,
		Logger:  slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	d.telegramHandler(context.Background(), nil, &models.Update{
		ID: 1,
		Message: &models.Message{
			ID:   2,
			Chat: models.Chat{ID: 100},
			Text: "hello",
		},
	})
	var entry struct {
		Panic string `json:"panic"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry); err != nil {
		t.Fatalf("decode pre-dispatch recovery log: %v; log=%q", err, logs.String())
	}
	for _, secret := range []string{"model-key-secret", "webhook-secret"} {
		if strings.Contains(entry.Panic, secret) {
			t.Errorf("pre-dispatch panic contains configured secret %q: %q", secret, entry.Panic)
		}
	}
	if !strings.Contains(entry.Panic, "***") || len(entry.Panic) > recoveredPanicMaxBytes {
		t.Errorf("pre-dispatch panic was not sanitized and bounded: %q", entry.Panic)
	}
}
