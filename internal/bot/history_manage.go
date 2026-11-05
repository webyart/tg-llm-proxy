package bot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"

	"bot/internal/store"
)

const (
	ExportRowLimit        = 10000
	SearchRowLimit        = 20
	deleteConfirmationTTL = 5 * time.Minute
)

type exportMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	Model     string `json:"model,omitempty"`
	TokensIn  *int64 `json:"tokens_in,omitempty"`
	TokensOut *int64 `json:"tokens_out,omitempty"`
}

type historyExportWindow struct {
	Limit           int    `json:"limit"`
	Count           int    `json:"count"`
	OldestID        int64  `json:"oldest_id,omitempty"`
	NewestID        int64  `json:"newest_id,omitempty"`
	OldestCreatedAt string `json:"oldest_created_at,omitempty"`
	NewestCreatedAt string `json:"newest_created_at,omitempty"`
}

type historyExport struct {
	ChatID     int64               `json:"chat_id"`
	ExportedAt string              `json:"exported_at"`
	Truncated  bool                `json:"truncated"`
	Warning    string              `json:"warning,omitempty"`
	Window     historyExportWindow `json:"window"`
	Messages   []exportMessage     `json:"messages"`
}

type historyExportMetadata struct {
	ChatID     int64               `json:"chat_id"`
	ExportedAt string              `json:"exported_at"`
	Truncated  bool                `json:"truncated"`
	Warning    string              `json:"warning,omitempty"`
	Window     historyExportWindow `json:"window"`
}

var errHistoryExportTooLarge = errors.New("history export exceeds byte limit")

type historyExportBuffer struct {
	bytes.Buffer
}

func (b *historyExportBuffer) reserve(next int) {
	if b.Cap() == 0 {
		b.Buffer.Grow(64 * 1024)
	}
	if next > b.Cap()-b.Len() {
		b.Buffer.Grow(ExportMaxBytes - b.Len())
	}
}

func (b *historyExportBuffer) Write(p []byte) (int, error) {
	if len(p) > ExportMaxBytes-b.Len() {
		return 0, errHistoryExportTooLarge
	}
	b.reserve(len(p))
	return b.Buffer.Write(p)
}

func (b *historyExportBuffer) WriteString(text string) (int, error) {
	if len(text) > ExportMaxBytes-b.Len() {
		return 0, errHistoryExportTooLarge
	}
	b.reserve(len(text))
	return b.Buffer.WriteString(text)
}

type deleteConfirmation struct {
	ChatID    int64
	UserID    int64
	MessageID int64
	ExpiresAt time.Time
}

type deleteConfirmationSet struct {
	mu      sync.Mutex
	pending map[string]deleteConfirmation
}

var dispatcherDeleteConfirmations sync.Map

func deleteConfirmationsFor(d *Dispatcher) *deleteConfirmationSet {
	set, _ := dispatcherDeleteConfirmations.LoadOrStore(d, &deleteConfirmationSet{pending: map[string]deleteConfirmation{}})
	return set.(*deleteConfirmationSet)
}

func newDeleteConfirmationToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func historyWindow(ctx context.Context, d *Dispatcher, chatID int64) ([]store.Message, bool, error) {
	rows, err := store.ExportMessages(ctx, d.deps.DB, chatID, ExportRowLimit+1)
	if err != nil {
		return nil, false, err
	}
	truncated := len(rows) > ExportRowLimit
	if truncated {
		rows = rows[len(rows)-ExportRowLimit:]
	}
	return rows, truncated, nil
}

func exportWindow(rows store.MessageExportWindow) historyExportWindow {
	return historyExportWindow{
		Limit:           ExportRowLimit,
		Count:           rows.Count,
		OldestID:        rows.OldestID,
		NewestID:        rows.NewestID,
		OldestCreatedAt: rows.OldestCreatedAt,
		NewestCreatedAt: rows.NewestCreatedAt,
	}
}

type historyExportSerializer struct {
	format       string
	chatID       int64
	output       historyExportBuffer
	messageCount int
}

func (s *historyExportSerializer) start(rows store.MessageExportWindow, truncated bool) error {
	if rows.Count == 0 {
		return nil
	}
	window := exportWindow(rows)
	warning := exportWarning(rows.Count, truncated)
	if s.format == "json" {
		metadata, err := json.MarshalIndent(historyExportMetadata{
			ChatID:     s.chatID,
			ExportedAt: time.Now().UTC().Format(time.RFC3339),
			Truncated:  truncated,
			Warning:    warning,
			Window:     window,
		}, "", "  ")
		if err != nil {
			return err
		}
		if _, err := s.output.Write(metadata[:len(metadata)-1]); err != nil {
			return err
		}
		return s.writeString(",\n  \"messages\": [\n")
	}
	if _, err := fmt.Fprintf(&s.output, "# История чата %d\n\n", s.chatID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(&s.output, "Область экспорта: последние %d сообщений; включено: %d.\n", ExportRowLimit, rows.Count); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(&s.output, "Диапазон: ID %d–%d; время %s — %s.\n\n", window.OldestID, window.NewestID, window.OldestCreatedAt, window.NewestCreatedAt); err != nil {
		return err
	}
	if warning != "" {
		if _, err := fmt.Fprintf(&s.output, "> Внимание: %s\n\n", warning); err != nil {
			return err
		}
	}
	return nil
}

func (s *historyExportSerializer) add(row store.Message) error {
	if s.format != "json" {
		if _, err := fmt.Fprintf(&s.output, "## %s · %s\n\n", row.Role, row.CreatedAt); err != nil {
			return err
		}
		if err := s.writeString(row.Content); err != nil {
			return err
		}
		return s.writeString("\n\n")
	}
	if row.ContentLength > int64(ExportMaxBytes-s.output.Len()) || len(row.Content) > ExportMaxBytes-s.output.Len() {
		return errHistoryExportTooLarge
	}
	if s.messageCount > 0 {
		if err := s.writeString(",\n"); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(exportMessage{Role: row.Role, Content: row.Content, CreatedAt: row.CreatedAt, Model: row.Model, TokensIn: row.TokensIn, TokensOut: row.TokensOut}, "    ", "  ")
	if err != nil {
		return err
	}
	if err := s.writeString("    "); err != nil {
		return err
	}
	if _, err := s.output.Write(encoded); err != nil {
		return err
	}
	s.messageCount++
	return nil
}

func (s *historyExportSerializer) finish() ([]byte, error) {
	if s.format == "json" {
		if err := s.writeString("\n  ]\n}"); err != nil {
			return nil, err
		}
	}
	return s.output.Bytes(), nil
}

func (s *historyExportSerializer) writeString(text string) error {
	_, err := s.output.WriteString(text)
	return err
}

func exportWarning(rowCount int, truncated bool) string {
	if truncated {
		return fmt.Sprintf("Экспорт ограничен последними %d сообщениями; более старые сообщения не включены.", ExportRowLimit)
	}
	if rowCount == ExportRowLimit {
		return fmt.Sprintf("Достигнуто окно экспорта: последние %d сообщений.", ExportRowLimit)
	}
	return ""
}

func (d *Dispatcher) exportHistory(ctx context.Context, ev Event, arg string) error {
	format := strings.ToLower(strings.TrimSpace(arg))
	if format == "" {
		format = "md"
	}
	if format != "md" && format != "json" {
		return d.reply(ctx, ev.ChatID, "Используй /export md или /export json.")
	}
	serializer := historyExportSerializer{format: format, chatID: ev.ChatID}
	window, _, err := store.StreamExportMessagesBounded(ctx, d.deps.DB, ev.ChatID, ExportRowLimit, ExportMaxBytes, serializer.start, serializer.add)
	var data []byte
	if err == nil && window.Count > 0 {
		data, err = serializer.finish()
	}
	if errors.Is(err, errHistoryExportTooLarge) || errors.Is(err, store.ErrExportContentTooLarge) {
		return d.reply(ctx, ev.ChatID, "Экспорт превышает 5 МиБ. Удали старую историю или используй более поздний чат.")
	}
	if err != nil {
		return err
	}
	if window.Count == 0 {
		return d.reply(ctx, ev.ChatID, msgHistoryEmpty)
	}
	sender, ok := d.deps.Replier.(DocumentSender)
	if !ok {
		return errors.New("telegram document sender is unavailable")
	}
	return sender.SendDocument(ctx, ev.ChatID, Document{Name: fmt.Sprintf("history-%d.%s", ev.ChatID, format), Data: data, MIME: exportMIME(format), Caption: "Экспорт истории"})
}

func (d *Dispatcher) searchHistory(ctx context.Context, ev Event, arg string) error {
	query := strings.TrimSpace(arg)
	if query == "" {
		return d.reply(ctx, ev.ChatID, "Нужен текст поиска: /search <текст>.")
	}
	rows, truncated, err := historyWindow(ctx, d, ev.ChatID)
	if err != nil {
		return err
	}
	needle := strings.ToLower(query)
	matches := make([]store.Message, 0, SearchRowLimit)
	for i := len(rows) - 1; i >= 0 && len(matches) < SearchRowLimit; i-- {
		if strings.Contains(strings.ToLower(rows[i].Content), needle) {
			matches = append(matches, rows[i])
		}
	}
	for i, j := 0, len(matches)-1; i < j; i, j = i+1, j-1 {
		matches[i], matches[j] = matches[j], matches[i]
	}
	scope := fmt.Sprintf("Область поиска: последние %d сообщений; вывод — до %d совпадений.", ExportRowLimit, SearchRowLimit)
	if truncated {
		scope += fmt.Sprintf(" Внимание: сообщения старше последних %d не проверялись.", ExportRowLimit)
	}
	if len(matches) == 0 {
		return d.reply(ctx, ev.ChatID, "Совпадений не найдено. "+scope)
	}
	lines := []string{fmt.Sprintf("Найдено сообщений: %d. %s", len(matches), scope)}
	for _, row := range matches {
		lines = append(lines, fmt.Sprintf("%d. %s · %s: %s", row.ID, row.Role, row.CreatedAt, brief(row.Content, 160)))
	}
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

func (d *Dispatcher) promptDeleteHistory(ctx context.Context, ev Event) error {
	token, err := newDeleteConfirmationToken()
	if err != nil {
		return err
	}
	markup := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "Подтвердить удаление", CallbackData: "delete:confirm:" + token},
		{Text: "Отмена", CallbackData: "delete:cancel:" + token},
	}}}
	text := Escape("Удалить всю историю этого чата? Настройки модели и persona сохранятся. Кнопки действуют 5 минут.", d.deps.Cfg.ParseMode)
	if sender, ok := d.deps.Replier.(interface {
		SendMenuWithID(context.Context, int64, string, models.InlineKeyboardMarkup) (int64, error)
	}); ok {
		messageID, err := sender.SendMenuWithID(ctx, ev.ChatID, text, markup)
		if err != nil {
			return err
		}
		if messageID <= 0 {
			return errors.New("telegram menu sender returned no message id")
		}
		d.rememberDeleteConfirmation(token, ev, messageID)
		return nil
	}
	if sender, ok := d.deps.Replier.(interface {
		SendMenu(context.Context, int64, string, models.InlineKeyboardMarkup) error
	}); ok {
		if err := sender.SendMenu(ctx, ev.ChatID, text, markup); err != nil {
			return err
		}
		d.rememberDeleteConfirmation(token, ev, 0)
		return nil
	}
	return d.reply(ctx, ev.ChatID, "Для подтверждения отправь /delete confirm. Для отмены — /delete cancel.")
}

func (d *Dispatcher) rememberDeleteConfirmation(token string, ev Event, messageID int64) {
	set := deleteConfirmationsFor(d)
	now := time.Now()
	set.mu.Lock()
	defer set.mu.Unlock()
	for key, pending := range set.pending {
		if !now.Before(pending.ExpiresAt) || pending.ChatID == ev.ChatID {
			delete(set.pending, key)
		}
	}
	set.pending[token] = deleteConfirmation{ChatID: ev.ChatID, UserID: ev.UserID, MessageID: messageID, ExpiresAt: now.Add(deleteConfirmationTTL)}
}

type deleteConfirmationStatus int

const (
	deleteConfirmationValid deleteConfirmationStatus = iota
	deleteConfirmationStale
	deleteConfirmationWrongUser
	deleteConfirmationWrongMessage
)

func consumeDeleteConfirmation(d *Dispatcher, token string, ev Event) deleteConfirmationStatus {
	set := deleteConfirmationsFor(d)
	set.mu.Lock()
	defer set.mu.Unlock()
	pending, ok := set.pending[token]
	if !ok {
		return deleteConfirmationStale
	}
	if !time.Now().Before(pending.ExpiresAt) {
		delete(set.pending, token)
		return deleteConfirmationStale
	}
	if pending.ChatID != ev.ChatID || (pending.MessageID != 0 && pending.MessageID != ev.TelegramMessageID) {
		return deleteConfirmationWrongMessage
	}
	if pending.UserID != ev.UserID {
		return deleteConfirmationWrongUser
	}
	delete(set.pending, token)
	return deleteConfirmationValid
}

func (d *Dispatcher) removeDeleteMenu(ctx context.Context, chatID int64, messageID int64) {
	if messageID <= 0 {
		return
	}
	remover, ok := d.deps.Replier.(interface {
		RemoveMenu(context.Context, int64, int64) error
	})
	if !ok {
		return
	}
	if err := remover.RemoveMenu(ctx, chatID, messageID); err != nil {
		d.deps.Logger.Warn("failed to remove delete confirmation keyboard", "chat_id", chatID, "message_id", messageID, "error", Sanitize(err, d.deps.Token))
	}
}

func (d *Dispatcher) handleDeleteCallback(ctx context.Context, ev Event, arg string) error {
	action, token, ok := strings.Cut(strings.TrimSpace(arg), ":")
	if !ok || (action != "confirm" && action != "cancel") {
		return d.reply(ctx, ev.ChatID, "Это меню удаления истекло или уже использовано. Отправь /delete заново.")
	}
	status := consumeDeleteConfirmation(d, token, ev)
	switch status {
	case deleteConfirmationStale:
		err := d.reply(ctx, ev.ChatID, "Это меню удаления истекло или уже использовано. Отправь /delete заново.")
		d.removeDeleteMenu(ctx, ev.ChatID, ev.TelegramMessageID)
		return err
	case deleteConfirmationWrongUser:
		return d.reply(ctx, ev.ChatID, "Это меню удаления принадлежит другому пользователю. Отправь /delete самостоятельно.")
	case deleteConfirmationWrongMessage:
		return d.reply(ctx, ev.ChatID, "Это меню удаления недействительно для этого чата или сообщения. Отправь /delete заново.")
	}
	if action == "cancel" {
		err := d.reply(ctx, ev.ChatID, "Удаление истории отменено.")
		d.removeDeleteMenu(ctx, ev.ChatID, ev.TelegramMessageID)
		return err
	}
	deleted, deleteErr := store.DeleteChatHistory(ctx, d.deps.DB, ev.ChatID)
	d.removeDeleteMenu(ctx, ev.ChatID, ev.TelegramMessageID)
	if deleteErr != nil {
		return deleteErr
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf("Удалено сообщений: %d. Настройки чата сохранены.", deleted))
}

func (d *Dispatcher) deleteHistory(ctx context.Context, ev Event, arg string) error {
	action := strings.ToLower(strings.TrimSpace(arg))
	switch action {
	case "":
		return d.promptDeleteHistory(ctx, ev)
	case "cancel":
		return d.reply(ctx, ev.ChatID, "Удаление истории отменено.")
	case "confirm":
		deleted, err := store.DeleteChatHistory(ctx, d.deps.DB, ev.ChatID)
		if err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("Удалено сообщений: %d. Настройки чата сохранены.", deleted))
	default:
		if callback, ok := strings.CutPrefix(action, "callback "); ok {
			return d.handleDeleteCallback(ctx, ev, callback)
		}
		return d.reply(ctx, ev.ChatID, "Используй /delete, затем подтверди или отмени удаление кнопкой.")
	}
}

func exportMIME(format string) string {
	if format == "json" {
		return "application/json"
	}
	return "text/markdown"
}
