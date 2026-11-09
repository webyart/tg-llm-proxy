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
	AttachmentPhoto     = "photo"
	AttachmentDocument  = "document"
	AttachmentVoice     = "voice"
	AttachmentAudio     = "audio"
	AttachmentVideoNote = "video_note"
	PhotoMaxBytes       = int64(10 << 20)
	DocumentMaxBytes    = int64(64 << 10)
	VoiceMaxBytes       = int64(10 << 20)
	VoiceMaxSeconds     = 300
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
	bot              *tgbot.Bot
	client           *http.Client
	mediaMaxBytes    int64
	speechMaxSeconds int
}

func (d *telegramAttachmentDownloader) Download(ctx context.Context, attachment Attachment) (DownloadedAttachment, error) {
	limit := attachmentLimit(attachment)
	if limit == 0 {
		return DownloadedAttachment{}, ErrAttachmentType
	}
	if (attachment.Kind == AttachmentPhoto || isSpeechAttachment(&attachment)) && d.mediaMaxBytes > 0 {
		limit = d.mediaMaxBytes
	}
	if attachment.Size > limit {
		return DownloadedAttachment{}, ErrAttachmentTooLarge
	}
	speechMaxSeconds := d.speechMaxSeconds
	if speechMaxSeconds <= 0 {
		speechMaxSeconds = VoiceMaxSeconds
	}
	if isSpeechAttachment(&attachment) && attachment.Duration > speechMaxSeconds {
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

// attachmentLimit is per attachment, not per kind: a PDF/DOCX document gets the 20 MiB Bot API getFile ceiling,
// while any other document keeps the 64 KiB text cap. The media override below still applies only to photos and
// speech.
func attachmentLimit(attachment Attachment) int64 {
	switch attachment.Kind {
	case AttachmentPhoto:
		return PhotoMaxBytes
	case AttachmentDocument:
		return documentLimit(attachment.FileName, attachment.MimeType)
	case AttachmentVoice, AttachmentAudio, AttachmentVideoNote:
		return VoiceMaxBytes
	default:
		return 0
	}
}

func isSpeechAttachment(attachment *Attachment) bool {
	if attachment == nil {
		return false
	}
	switch attachment.Kind {
	case AttachmentVoice, AttachmentAudio, AttachmentVideoNote:
		return true
	default:
		return false
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
	case AttachmentAudio:
		return "Ответь на аудиосообщение."
	case AttachmentVideoNote:
		return "Ответь на видеосообщение."
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
	case AttachmentAudio:
		return "[Аудио: ожидает транскрипции]\n" + text
	case AttachmentVideoNote:
		return "[Видеосообщение: ожидает транскрипции]\n" + text
	default:
		return "[Документ: " + safeAttachmentName(ev.Attachment.FileName) + "]\n" + text
	}
}

func speechStoredMarker(kind string) string {
	switch kind {
	case AttachmentAudio:
		return "[Аудио]"
	case AttachmentVideoNote:
		return "[Видеосообщение]"
	default:
		return "[Голосовое]"
	}
}

func isStoredAttachment(text string) bool {
	return strings.HasPrefix(text, "[Фото]\n") ||
		strings.HasPrefix(text, "[Документ: ") ||
		strings.HasPrefix(text, "[Голосовое: ожидает транскрипции]\n") ||
		strings.HasPrefix(text, "[Аудио: ожидает транскрипции]\n") ||
		strings.HasPrefix(text, "[Видеосообщение: ожидает транскрипции]\n")
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
	// An explicitly textual EXTENSION wins over a MIME the caller does not know: browsers and file managers report
	// .csv as application/vnd.ms-excel, .md as application/x-markdown, and a picker may send a vendor type. The
	// bytes already passed the UTF-8/control check above, which is what actually separates text from binary.
	if textDocumentExtension(name) {
		return true
	}
	return false
}

// textDocumentExtension — the one list both the Telegram and the Mini App upload paths accept.
func textDocumentExtension(name string) bool {
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
	return caption + "\n\nТранскрипция аудио/видеосообщения:\n" + strings.TrimSpace(transcript)
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
	case errors.Is(err, ErrAttachmentTooLarge), errors.Is(err, ErrRAGDocumentTooLarge):
		return "Вложение слишком большое. Для текста лимит — 64 КиБ, для PDF/DOCX — 20 МиБ (предел Bot API getFile); фото и аудио/видео ограничивает MEDIA_MAX_MB."
	case errors.Is(err, ErrRAGDocumentEmpty):
		return "Документ пустой: в нём нет текста, который можно сохранить."
	case errors.Is(err, ErrVoiceTooLong):
		return "Аудио или видеосообщение слишком длинное. Лимит задаёт SPEECH_MAX_SECONDS (по умолчанию — 5 минут)."
	case errors.Is(err, ErrDocumentNoText):
		return "В документе нет текстового слоя — OCR недоступен. Загрузи PDF с текстом или распознай его отдельно."
	case errors.Is(err, ErrDocumentEncrypted):
		return "PDF защищён паролем. Сними пароль и отправь файл повторно."
	case errors.Is(err, ErrDocumentUnsupported):
		return "Старый формат .doc не поддерживается. Сохрани документ как .docx или .pdf."
	case errors.Is(err, ErrDocumentCorrupt):
		return "Не удалось разобрать документ. Проверь, что файл не повреждён, и отправь его повторно."
	case errors.Is(err, ErrAttachmentType):
		return "Поддерживаются фото JPEG/PNG, текстовые документы UTF-8, PDF и DOCX, голосовые и аудиосообщения, а также видеосообщения."
	case errors.Is(err, ErrAttachmentUnavailable):
		return "Не удалось получить вложение из Telegram. Отправь его повторно."
	default:
		return "Не удалось прочитать вложение. Проверь файл и отправь его повторно."
	}
}
