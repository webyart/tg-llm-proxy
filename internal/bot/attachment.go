package bot

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	tgbot "github.com/go-telegram/bot"
)

const (
	AttachmentPhoto    = "photo"
	AttachmentDocument = "document"
	AttachmentVoice    = "voice"
	PhotoMaxBytes      = int64(10 << 20)
	DocumentMaxBytes   = int64(64 << 10)
	VoiceMaxBytes      = int64(10 << 20)
	VoiceMaxSeconds    = 300
)

var (
	ErrAttachmentTooLarge    = errors.New("attachment is too large")
	ErrAttachmentUnavailable = errors.New("attachment is unavailable")
	ErrAttachmentType        = errors.New("attachment type is not supported")
	ErrVoiceTooLong          = errors.New("voice message is too long")
)

type Attachment struct {
	Kind     string
	FileID   string
	FileName string
	MimeType string
	Size     int64
	Duration int
}

type DownloadedAttachment struct {
	Data []byte
	MIME string
}

type AttachmentDownloader interface {
	Download(ctx context.Context, attachment Attachment) (DownloadedAttachment, error)
}

type telegramAttachmentDownloader struct {
	bot    *tgbot.Bot
	client *http.Client
}

func (d *telegramAttachmentDownloader) Download(ctx context.Context, attachment Attachment) (DownloadedAttachment, error) {
	limit := attachmentLimit(attachment.Kind)
	if limit == 0 {
		return DownloadedAttachment{}, ErrAttachmentType
	}
	if attachment.Size > limit {
		return DownloadedAttachment{}, ErrAttachmentTooLarge
	}
	if attachment.Kind == AttachmentVoice && attachment.Duration > VoiceMaxSeconds {
		return DownloadedAttachment{}, ErrVoiceTooLong
	}
	file, err := d.bot.GetFile(ctx, &tgbot.GetFileParams{FileID: attachment.FileID})
	if err != nil {
		return DownloadedAttachment{}, fmt.Errorf("telegram getFile: %w", err)
	}
	if file == nil || strings.TrimSpace(file.FilePath) == "" {
		return DownloadedAttachment{}, ErrAttachmentUnavailable
	}
	if file.FileSize > limit {
		return DownloadedAttachment{}, ErrAttachmentTooLarge
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.bot.FileDownloadLink(file), nil)
	if err != nil {
		return DownloadedAttachment{}, ErrAttachmentUnavailable
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return DownloadedAttachment{}, fmt.Errorf("telegram file download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DownloadedAttachment{}, fmt.Errorf("telegram file download answered %d: %w", resp.StatusCode, ErrAttachmentUnavailable)
	}
	if resp.ContentLength > limit {
		return DownloadedAttachment{}, ErrAttachmentTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return DownloadedAttachment{}, fmt.Errorf("telegram file read: %w", err)
	}
	if int64(len(data)) > limit {
		return DownloadedAttachment{}, ErrAttachmentTooLarge
	}
	if len(data) == 0 {
		return DownloadedAttachment{}, ErrAttachmentUnavailable
	}
	return DownloadedAttachment{Data: data, MIME: http.DetectContentType(data)}, nil
}

func attachmentLimit(kind string) int64 {
	switch kind {
	case AttachmentPhoto:
		return PhotoMaxBytes
	case AttachmentDocument:
		return DocumentMaxBytes
	case AttachmentVoice:
		return VoiceMaxBytes
	default:
		return 0
	}
}

func attachmentPrompt(ev Event) string {
	text := strings.TrimSpace(ev.Text)
	if text != "" || ev.Attachment == nil {
		return text
	}
	switch ev.Attachment.Kind {
	case AttachmentPhoto:
		return "Опиши изображение."
	case AttachmentVoice:
		return "Ответь на голосовое сообщение."
	default:
		return "Прочитай документ."
	}
}

func storedAttachmentQuestion(ev Event) string {
	text := attachmentPrompt(ev)
	if ev.Attachment == nil {
		return text
	}
	switch ev.Attachment.Kind {
	case AttachmentPhoto:
		return "[Фото]\n" + text
	case AttachmentVoice:
		return "[Голосовое: ожидает транскрипции]\n" + text
	default:
		return "[Документ: " + safeAttachmentName(ev.Attachment.FileName) + "]\n" + text
	}
}

func isStoredAttachment(text string) bool {
	return strings.HasPrefix(text, "[Фото]\n") || strings.HasPrefix(text, "[Документ: ") || strings.HasPrefix(text, "[Голосовое: ожидает транскрипции]\n")
}

func safeAttachmentName(name string) string {
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "document"
	}
	if Runes(name) > 120 {
		name = string([]rune(name)[:120])
	}
	return name
}

func documentIsText(name string, declaredMIME string, data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for _, r := range string(data) {
		if r == 0 || unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' && r != '\f' {
			return false
		}
	}
	mime := strings.ToLower(strings.TrimSpace(strings.Split(declaredMIME, ";")[0]))
	if strings.HasPrefix(mime, "text/") || textualApplicationMIME(mime) {
		return true
	}
	if mime != "" && mime != "application/octet-stream" {
		return false
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".txt", ".md", ".markdown", ".log", ".csv", ".tsv", ".json", ".jsonl", ".xml", ".yaml", ".yml", ".toml", ".ini", ".cfg", ".html", ".css", ".js", ".ts", ".go", ".py", ".java", ".c", ".h", ".cpp", ".cs", ".rs", ".sh", ".ps1", ".sql":
		return true
	default:
		return false
	}
}

func textualApplicationMIME(mime string) bool {
	switch mime {
	case "application/json", "application/ld+json", "application/xml", "application/xhtml+xml", "application/x-yaml", "application/yaml", "application/x-ndjson", "application/sql", "application/javascript", "application/x-sh", "application/x-httpd-php":
		return true
	default:
		return false
	}
}

func imageDataURI(data DownloadedAttachment) (string, error) {
	if data.MIME != "image/jpeg" && data.MIME != "image/png" {
		return "", ErrAttachmentType
	}
	return "data:" + data.MIME + ";base64," + base64.StdEncoding.EncodeToString(data.Data), nil
}

func documentModelPrompt(caption string, name string, content string, maxRunes int) string {
	text := caption + "\n\nДокумент " + name + ":\n" + content
	if Runes(text) > maxRunes && maxRunes > 0 {
		runes := []rune(text)
		marker := "… (документ обрезан по MAX_CONTEXT_CHARS)"
		if maxRunes > Runes(marker) {
			text = string(runes[:maxRunes-Runes(marker)]) + marker
		}
	}
	return text
}

func voiceModelPrompt(caption string, transcript string) string {
	return caption + "\n\nТранскрипция голосового сообщения:\n" + strings.TrimSpace(transcript)
}

func attachmentErrorKind(err error) string {
	switch {
	case errors.Is(err, ErrAttachmentTooLarge):
		return "too_large"
	case errors.Is(err, ErrVoiceTooLong):
		return "too_long"
	case errors.Is(err, ErrAttachmentType):
		return "unsupported_type"
	case errors.Is(err, ErrAttachmentUnavailable):
		return "unavailable"
	default:
		return "download_failed"
	}
}

func attachmentUserError(err error) string {
	switch {
	case errors.Is(err, ErrAttachmentTooLarge):
		return "Вложение слишком большое. Лимит: 10 МиБ для фото и voice, 64 КиБ для текстового документа."
	case errors.Is(err, ErrVoiceTooLong):
		return "Голосовое сообщение слишком длинное. Лимит — 5 минут."
	case errors.Is(err, ErrAttachmentType):
		return "Поддерживаются фото JPEG/PNG, текстовые документы UTF-8 и голосовые сообщения OGG/Opus."
	case errors.Is(err, ErrAttachmentUnavailable):
		return "Не удалось получить вложение из Telegram. Отправь его повторно."
	default:
		return "Не удалось прочитать вложение. Проверь файл и отправь его повторно."
	}
}
