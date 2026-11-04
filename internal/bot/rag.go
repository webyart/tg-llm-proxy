package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"bot/internal/model"
	"bot/internal/store"
)

const (
	RAGResultLimit = 3
	RAGMaxRunes    = 2000
)

func (d *Dispatcher) documents(ctx context.Context, ev Event, arg string) error {
	action := strings.TrimSpace(arg)
	if ev.Attachment != nil && ev.Attachment.Kind == AttachmentDocument && strings.HasPrefix(action, "add") {
		return d.addDocument(ctx, ev)
	}
	if action == "" || action == "list" {
		rows, err := store.ListRAGDocuments(ctx, d.deps.DB, ev.ChatID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return d.reply(ctx, ev.ChatID, "Локальная база документов пуста. Отправь текстовый файл с подписью /docs add.")
		}
		lines := []string{"Документы текущего чата:"}
		for _, row := range rows {
			lines = append(lines, fmt.Sprintf("%d. %s — %d байт, %d частей; /docs delete %d", row.ID, row.Name, row.SizeBytes, row.Chunks, row.ID))
		}
		return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
	}
	if strings.HasPrefix(action, "delete ") {
		id, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(action, "delete ")), 10, 64)
		if err != nil || id <= 0 {
			return d.reply(ctx, ev.ChatID, "Используй /docs delete <id> из /docs list.")
		}
		deleted, err := store.DeleteRAGDocument(ctx, d.deps.DB, ev.ChatID, id)
		if err != nil {
			return err
		}
		if !deleted {
			return d.reply(ctx, ev.ChatID, "Документ с таким id не найден в текущем чате.")
		}
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("Документ id=%d удалён.", id))
	}
	if action == "clear" {
		return d.reply(ctx, ev.ChatID, "Для удаления всех локальных документов чата отправь /docs clear confirm.")
	}
	if action == "clear confirm" {
		deleted, err := store.ClearRAGDocuments(ctx, d.deps.DB, ev.ChatID)
		if err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("Удалено документов: %d.", deleted))
	}
	return d.reply(ctx, ev.ChatID, "Команды: /docs, /docs delete <id>, /docs clear. Для добавления отправь текстовый файл с подписью /docs add.")
}

func (d *Dispatcher) addDocument(ctx context.Context, ev Event) error {
	files, err := d.downloadEventAttachments(ctx, ev)
	if err != nil {
		return d.reply(ctx, ev.ChatID, attachmentUserError(err))
	}
	if len(files) != 1 || !documentIsText(ev.Attachment.FileName, ev.Attachment.MimeType, files[0].Data) {
		return d.reply(ctx, ev.ChatID, attachmentUserError(ErrAttachmentType))
	}
	name := safeAttachmentName(ev.Attachment.FileName)
	id, chunks, err := store.PutRAGDocument(ctx, d.deps.DB, ev.ChatID, ev.UserID, name, string(files[0].Data))
	if err != nil {
		return err
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf("Документ %s сохранён с id=%d, частей: %d. Он будет использоваться при следующих вопросах.", name, id, chunks))
}

func (d *Dispatcher) addRAGContext(ctx context.Context, chatID int64, query string, messages []model.Message) ([]model.Message, error) {
	chunks, err := store.SearchRAGChunks(ctx, d.deps.DB, chatID, query, RAGResultLimit, RAGMaxRunes)
	if err != nil || len(chunks) == 0 {
		return messages, err
	}
	var text strings.Builder
	text.WriteString("Релевантные фрагменты локальных документов. Используй их как контекст и при необходимости называй источник.\n")
	for _, chunk := range chunks {
		fmt.Fprintf(&text, "\n[Источник: %s]\n%s\n", chunk.Name, chunk.Content)
	}
	insertAt := 0
	if len(messages) > 0 && messages[0].Role == model.RoleSystem {
		insertAt = 1
	}
	messages = append(messages, model.Message{})
	copy(messages[insertAt+1:], messages[insertAt:])
	messages[insertAt] = model.Message{Role: model.RoleSystem, Content: text.String()}
	return messages, nil
}
