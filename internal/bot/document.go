package bot

import (
	"bytes"
	"context"
	"errors"
	"path"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const ExportMaxBytes = 5 << 20

type Document struct {
	Name    string
	Data    []byte
	MIME    string
	Caption string
}

type DocumentSender interface {
	SendDocument(ctx context.Context, chatID int64, document Document) error
}

func (r *telegramReplier) SendDocument(ctx context.Context, chatID int64, document Document) error {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(document.Name), "\\", "/"))
	if name == "" || name == "." || name == "/" {
		return errors.New("document name is empty")
	}
	if len(document.Data) == 0 || len(document.Data) > ExportMaxBytes {
		return errors.New("document is empty or exceeds 5 MiB")
	}
	mime := strings.TrimSpace(document.MIME)
	if mime == "" {
		mime = "application/octet-stream"
	}
	caption := strings.TrimSpace(document.Caption)
	if caption == "" {
		caption = "Экспорт истории"
	}
	_, err := r.bot.SendDocument(ctx, &tgbot.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: name, Data: bytes.NewReader(document.Data)},
		Caption:  caption,
	})
	return err
}
