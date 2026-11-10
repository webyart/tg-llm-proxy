// Voice input in the Mini App is the SAME transcription path as a Telegram voice note: the configured
// model.Transcriber (the client is not re-created here), the shared speech allowlist and signature check
// (speechFormat / detectSpeechFormat / speechSignatureMatches), MEDIA_MAX_MB, the per-user allowlist and
// the per-minute budget. There is no second transcription client and no second validation table, and the
// audio is never stored: only the returned transcript leaves this function.
//
// Parity matrix (Telegram bot vs Mini App), voice row:
//
//	Telegram : voice/audio/video_note -> Dispatcher.ask -> Transcribe -> model prompt, stored marker
//	Mini App : composer mic -> POST /api/transcribe -> Transcribe -> transcript in the composer
//
// Voice is now on both clients. Telegram keeps the `[Голосовое]` marker and answers in the chat; the
// Mini App returns the transcript, which the user may edit and send as ordinary text (never auto-sent).
package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"bot/internal/model"
	"bot/internal/store"
)

const (
	// MiniAppMinAudioBytes rejects an empty or effectively empty recording before it reaches the
	// transcriber. A MediaRecorder container with a real spoken second is a few KiB; 256 B is only the
	// WebM/Ogg header, so this cannot cut a legitimate utterance.
	MiniAppMinAudioBytes = 256
	// MiniAppMaxTranscriptRunes is the composer bound: a transcript longer than one Mini App message
	// (MiniAppMaxTextRunes) is trimmed to what the client can actually send and show.
	MiniAppMaxTranscriptRunes = MiniAppMaxTextRunes
)

var (
	// ErrMiniAppAudioTooShort — empty or header-only audio (mapped by the web layer to 400 audio_too_short).
	ErrMiniAppAudioTooShort = errors.New("the Mini App audio is too short")
	// ErrMiniAppEmptyTranscription — the transcriber answered with no text. A provider that returns an
	// empty transcript is an input problem for the user, not a server fault, so the web layer maps it to
	// 400 audio_too_short as well.
	ErrMiniAppEmptyTranscription = errors.New("the transcription is empty")
	// ErrMiniAppTranscriptionUnavailable — the configured model client has no Transcribe method.
	ErrMiniAppTranscriptionUnavailable = errors.New("transcription is not configured for the Mini App")
	// ErrMiniAppTranscriptionFailed — the transcriber itself failed. The provider body stays in the log,
	// never in the HTTP response.
	ErrMiniAppTranscriptionFailed = errors.New("the Mini App transcription failed")
)

// TranscribeForMiniApp validates one recorded clip exactly like the Telegram voice handler and returns
// the trimmed transcript. Errors:
//
//	ErrMiniAppDenied                    — user not in TELEGRAM_ALLOWED_USERS
//	ErrAttachmentType                   — not an allowed audio MIME/extension or a bad container signature
//	ErrMiniAppAudioTooShort             — empty or header-only audio
//	ErrAttachmentTooLarge               — larger than MEDIA_MAX_MB
//	*MiniAppRateLimitError              — per-minute budget spent
//	ErrMiniAppTranscriptionUnavailable  — the model client has no Transcriber
//	ErrMiniAppEmptyTranscription        — the provider returned no text
//	ErrMiniAppTranscriptionFailed       — the provider call failed (cause is logged)
//	context.Canceled                    — the client went away
func (d *Dispatcher) TranscribeForMiniApp(ctx context.Context, chatID, userID int64, data []byte, filename, mime string) (string, error) {
	if d == nil || d.deps.DB == nil || d.deps.Model == nil {
		return "", ErrMiniAppUnavailable
	}
	if err := d.miniAppAllowed(ctx, userID); err != nil {
		return "", err
	}
	if err := validateMiniAppAudio(filename, mime, data); err != nil {
		return "", err
	}
	limit := d.deps.Cfg.MediaMaxBytes
	if limit <= 0 {
		limit = 10 << 20
	}
	if int64(len(data)) > limit {
		return "", ErrAttachmentTooLarge
	}
	transcriber, ok := d.deps.Model.(model.Transcriber)
	if !ok {
		return "", ErrMiniAppTranscriptionUnavailable
	}
	// The same per-minute budget as /ask and the Mini App chat: one recorded clip is one request.
	if d.deps.Limit != nil && !d.deps.Limit.Allow(userID) {
		wait := d.deps.Limit.RetryAfter(userID)
		if wait <= 0 {
			wait = time.Minute
		}
		d.logEvent(ctx, userID, store.EventLimit, "miniapp_transcribe")
		return "", &MiniAppRateLimitError{RetryAfter: wait}
	}
	d.logEvent(ctx, userID, store.EventCommand, "miniapp_transcribe")

	name, speechMIME := miniAppSpeechMetadata(filename, mime, data)
	text, err := transcriber.Transcribe(ctx, model.TranscriptionRequest{
		Model: d.deps.Cfg.TranscriptionModel, FileName: name, MIME: speechMIME, Data: data,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		// The audit of a failure must not ride a context that is already gone, exactly like commands.go.
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditBudget)
		defer cancel()
		if errors.Is(err, model.ErrEmptyTranscription) {
			d.logEvent(auditCtx, userID, store.EventModelError, "transcription:empty")
			return "", ErrMiniAppEmptyTranscription
		}
		if d.deps.Logger != nil {
			d.deps.Logger.Error("Mini App speech transcription failed",
				"chat_id", chatID, "user_id", userID, "error", d.sanitizeMiniAppError(err))
		}
		d.logEvent(auditCtx, userID, store.EventModelError, "transcription:failed")
		return "", fmt.Errorf("%w: %v", ErrMiniAppTranscriptionFailed, err)
	}
	text = strings.TrimSpace(text)
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("%w: the provider returned invalid UTF-8", ErrMiniAppTranscriptionFailed)
	}
	if text == "" {
		// A transcriber that answers ("", nil) is still not an answer; the Telegram path gets this case as
		// model.ErrEmptyTranscription from the HTTP client, and both must fail closed.
		d.logEvent(ctx, userID, store.EventModelError, "transcription:empty")
		return "", ErrMiniAppEmptyTranscription
	}
	if runes := []rune(text); len(runes) > MiniAppMaxTranscriptRunes {
		text = string(runes[:MiniAppMaxTranscriptRunes])
	}
	return text, nil
}

// validateMiniAppAudio is the Mini App entry to the shared speech validation. Order matters for the
// error codes: a declared non-audio MIME is rejected before the body is inspected, an empty body with
// an audio declaration is "too short" rather than "unsupported", and only then is the container
// signature checked. That is the same allowlist the Telegram voice handler uses.
func validateMiniAppAudio(filename, mime string, data []byte) error {
	attachment := Attachment{Kind: AttachmentAudio, FileName: filename, MimeType: mime}
	format := speechFormat(attachment)
	if format == "unsupported" {
		return ErrAttachmentType
	}
	if len(data) < MiniAppMinAudioBytes {
		if format == "" {
			// Unknown declaration AND no bytes to sniff: there is no audio type to report.
			return ErrAttachmentType
		}
		return ErrMiniAppAudioTooShort
	}
	if format == "" {
		format = detectSpeechFormat(data)
	}
	if !speechSignatureMatches(format, data) {
		return ErrAttachmentType
	}
	return nil
}

// miniAppSpeechMetadata builds the filename and the base MIME for model.TranscriptionRequest. The
// browser sends "audio/webm;codecs=opus" for the file part; the transcriber must receive the base type,
// while the shared speechFormat already ignores parameters for the allowlist.
func miniAppSpeechMetadata(filename, mime string, data []byte) (string, string) {
	format := speechFormat(Attachment{Kind: AttachmentAudio, FileName: filename, MimeType: mime})
	if format == "" || format == "unsupported" {
		format = detectSpeechFormat(data)
	}
	extensions := map[string]string{
		"ogg": ".ogg", "mp3": ".mp3", "wav": ".wav", "mp4": ".mp4",
		"webm": ".webm", "flac": ".flac", "aac": ".aac",
	}
	name := strings.TrimSpace(filename)
	if name == "" {
		name = "voice" + extensions[format]
	}
	base := strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	if base == "" || base == "application/octet-stream" {
		base = map[string]string{
			"ogg": "audio/ogg", "mp3": "audio/mpeg", "wav": "audio/wav", "mp4": "audio/mp4",
			"webm": "audio/webm", "flac": "audio/flac", "aac": "audio/aac",
		}[format]
	}
	return safeAttachmentName(name), base
}
