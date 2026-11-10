package webapp

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bot/internal/bot"
	"bot/internal/config"
)

const (
	// transcribeUploadOverhead is the room multipart framing and the file part headers need above
	// MEDIA_MAX_MB. A recorded clip is a single binary part with a short name, so 64 KiB is generous.
	transcribeUploadOverhead = 64 << 10
	// transcribeReadBudget is the read deadline for one voice upload: the server-wide ReadTimeout covers the
	// slow case, but the handler declares its own deadline like POST /api/documents so a direct call (tests,
	// another mux) keeps the same honesty.
	transcribeReadBudget = 2 * time.Minute
	// transcribeWriteBudget covers transcription plus the small JSON response; it starts when the handler
	// starts, so it must be longer than the provider budget, like the document upload write budget.
	transcribeWriteBudget = 5 * time.Minute
)

// transcribeBodyLimit is the request cap of POST /api/transcribe: MEDIA_MAX_MB (the same limit the
// Telegram voice path applies) plus multipart framing. The dispatcher still checks the decoded audio
// against MEDIA_MAX_MB, so this only bounds how much of a request the Mini App buffers.
func transcribeBodyLimit(cfg config.Config) int64 {
	limit := cfg.MediaMaxBytes
	if limit <= 0 {
		limit = 10 << 20
	}
	return limit + transcribeUploadOverhead
}

// transcribe handles POST /api/transcribe: one multipart `file` part with the recording, the chat and
// user taken from the authenticated session. It returns {"text":"…"} and never stores the audio: the
// backend only validates and forwards the bytes to the same model.Transcriber the Telegram voice note
// uses. Query parameters, extra file parts and unknown fields are refused or skipped; the body is bounded
// by MEDIA_MAX_MB + overhead and the response is a small JSON envelope with a stable error code.
func (a *App) transcribe(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPost {
		methodAllowed(w, http.MethodPost)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Voice transcription does not accept query parameters.")
		return
	}
	mediaType, parameters, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		writeError(w, http.StatusUnsupportedMediaType, "content_type", "Send one multipart/form-data file part with the audio.")
		return
	}
	// Same pattern as POST /api/documents: the handler keeps its own deadlines even though the listener
	// now runs with long ReadTimeout/WriteTimeout. The write side must also cover the transcriber call.
	control := http.NewResponseController(w)
	if deadlineErr := control.SetReadDeadline(time.Now().Add(transcribeReadBudget)); deadlineErr != nil {
		a.logger.Debug("the transcription read deadline could not be extended", "chat_id", current.ChatID, "error", deadlineErr.Error())
	}
	if deadlineErr := control.SetWriteDeadline(time.Now().Add(transcribeWriteBudget)); deadlineErr != nil {
		a.logger.Debug("the transcription write deadline could not be extended", "chat_id", current.ChatID, "error", deadlineErr.Error())
	}
	// limitInFlight already applied this cap; wrapping again keeps the handler honest when it is called
	// directly, and the inner reader still reports http.MaxBytesError.
	req.Body = http.MaxBytesReader(w, req.Body, transcribeBodyLimit(a.cfg))
	reader := multipart.NewReader(req.Body, parameters["boundary"])
	var (
		data     []byte
		fileName string
		fileMIME string
		haveFile bool
	)
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			a.transcribeReadFailed(w, req, current, partErr)
			return
		}
		if part.FormName() != "file" {
			// Unknown fields are ignored on purpose: the session decides the chat and the user.
			_ = part.Close()
			continue
		}
		if haveFile {
			_ = part.Close()
			writeError(w, http.StatusBadRequest, "invalid_body", "Send exactly one file part.")
			return
		}
		haveFile = true
		fileName = part.FileName()
		if strings.TrimSpace(fileName) == "" {
			_ = part.Close()
			writeError(w, http.StatusBadRequest, "invalid_body", "The file part needs a filename.")
			return
		}
		fileMIME = part.Header.Get("Content-Type")
		// The body cap bounds this read; reading the whole part is what turns an oversized body into 413
		// instead of a premature 400.
		data, partErr = io.ReadAll(part)
		_ = part.Close()
		if partErr != nil {
			a.transcribeReadFailed(w, req, current, partErr)
			return
		}
	}
	if !haveFile {
		writeError(w, http.StatusBadRequest, "invalid_body", "Send one file part with the recorded audio.")
		return
	}
	if a.chat == nil {
		writeError(w, http.StatusServiceUnavailable, "chat_unavailable", "The chat is unavailable in this Mini App deployment.")
		return
	}
	text, err := a.chat.TranscribeForMiniApp(req.Context(), current.ChatID, current.UserID, data, fileName, fileMIME)
	switch {
	case err == nil:
	case errors.Is(err, bot.ErrMiniAppDenied):
		writeError(w, http.StatusForbidden, "chat_forbidden", "The Mini App chat is available only to allowlisted users.")
		return
	case errors.Is(err, bot.ErrMiniAppUnavailable):
		writeError(w, http.StatusServiceUnavailable, "chat_unavailable", "The chat is unavailable in this Mini App deployment.")
		return
	case errors.Is(err, bot.ErrAttachmentTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "The recording is larger than MEDIA_MAX_MB.")
		return
	case errors.Is(err, bot.ErrAttachmentType):
		writeError(w, http.StatusBadRequest, "unsupported_audio", "Send an audio recording in a supported format.")
		return
	case errors.Is(err, bot.ErrMiniAppAudioTooShort), errors.Is(err, bot.ErrMiniAppEmptyTranscription):
		writeError(w, http.StatusBadRequest, "audio_too_short", "The recording is too short or has no recognizable speech.")
		return
	case errors.Is(err, bot.ErrMiniAppTranscriptionUnavailable):
		a.logger.Error("Mini App transcription is not configured", "chat_id", current.ChatID, "user_id", current.UserID)
		writeError(w, http.StatusInternalServerError, "transcription_failed", "Transcription is unavailable right now.")
		return
	}
	var limited *bot.MiniAppRateLimitError
	if errors.As(err, &limited) {
		seconds := int64((limited.RetryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests. Try again later.")
		return
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, req.Context().Err()) {
			return
		}
		a.logger.Error("Mini App transcription failed", "chat_id", current.ChatID, "user_id", current.UserID, "error", a.chatErrorMessage(err))
		writeError(w, http.StatusInternalServerError, "transcription_failed", "The recording could not be transcribed.")
		return
	}
	// Metadata only: the transcript is returned to the client, the audio is not stored and neither is
	// written to the log.
	a.logger.Info("Mini App voice transcribed",
		"chat_id", current.ChatID, "user_id", current.UserID, "bytes", len(data), "runes", len([]rune(text)))
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// transcribeReadFailed mirrors documentUploadReadFailed: the request cap is 413, a canceled client gets
// no response, a timeout is 408 and a malformed/truncated multipart body is 400.
func (a *App) transcribeReadFailed(w http.ResponseWriter, req *http.Request, current scope, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "Request body is too large.")
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, req.Context().Err()) {
		return
	}
	a.logger.Warn("Mini App transcription body could not be read",
		"chat_id", current.ChatID, "content_length", req.ContentLength, "error", err.Error())
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		writeError(w, http.StatusRequestTimeout, "upload_timeout", "The upload was interrupted. Check the connection and send the recording again.")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_body", "Send one multipart file part with the recorded audio.")
}
