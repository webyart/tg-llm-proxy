package webapp

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"bot/internal/bot"
	"bot/internal/config"
	"bot/internal/store"
)

const (
	// documentUploadOverhead is the room multipart framing and the optional name part need above the largest
	// accepted binary document. One MiB is comfortably above a boundary string, part headers and a 4 KiB name;
	// it is not tied to MEDIA_MAX_MB, because a PDF/DOCX follows bot.DocumentBinaryMaxBytes (the Telegram getFile
	// ceiling) while a plain text file keeps the much smaller bot.DocumentMaxBytes.
	documentUploadOverhead = 1 << 20
	// documentUploadNameBytes bounds the optional name override before safeAttachmentName trims it to 120 runes.
	documentUploadNameBytes = 4 << 10
	// documentUploadReadBudget is the read deadline for ONE upload request. The server-wide ReadTimeout stays 5s
	// (fast-fail for JSON), but a document over a mobile link can legitimately take minutes: a 2 MiB PDF already
	// exceeded 5s from a phone and arrived as a truncated body, which the handler could only report as 400.
	documentUploadReadBudget = 5 * time.Minute
	// documentUploadWriteBudget covers the response after the body was read: the server-wide WriteTimeout is 10 s,
	// which a 12-second upload already exceeded and Traefik reported as 502.
	documentUploadWriteBudget = 15 * time.Minute
)

// documentUploadBodyLimit is the request cap of POST /api/documents: the 20 MiB binary document cap plus
// multipart overhead. The stricter per-type limits (64 KiB text, 20 MiB PDF/DOCX) are still enforced from the
// shared ingest helper, so this cap only bounds how much of a request the Mini App buffers.
func documentUploadBodyLimit(_ config.Config) int64 {
	return bot.DocumentBinaryMaxBytes + documentUploadOverhead
}

// uploadDocument handles POST /api/documents: one multipart file part, an optional name override, and no ID in
// the body. The chat and user come from the authenticated session only; unknown parts (a spoofed chat_id, for
// example) are skipped, never read as scope.
func (a *App) uploadDocument(w http.ResponseWriter, req *http.Request, current scope) {
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Document upload does not accept query parameters.")
		return
	}
	mediaType, parameters, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		writeError(w, http.StatusUnsupportedMediaType, "content_type", "Send one multipart/form-data file part.")
		return
	}
	// The server-wide read timeout is intentionally short; the upload needs its own deadline or a slow phone
	// connection is cut after five seconds and the user sees a 400 for a perfectly valid file.
	var control = http.NewResponseController(w)
	if deadlineErr := control.SetReadDeadline(time.Now().Add(documentUploadReadBudget)); deadlineErr != nil {
		// Not every middleware chain supports the controller; the server-wide ReadTimeout is the backstop, so this
		// is diagnostic only and must not fail an otherwise valid upload.
		a.logger.Debug("the upload read deadline could not be extended", "chat_id", current.ChatID, "error", deadlineErr.Error())
	}
	if deadlineErr := control.SetWriteDeadline(time.Now().Add(documentUploadWriteBudget)); deadlineErr != nil {
		a.logger.Debug("the upload write deadline could not be extended", "chat_id", current.ChatID, "error", deadlineErr.Error())
	}
	// limitInFlight already wrapped the body with the same cap; wrapping again keeps the handler honest when it
	// is called directly, and the inner reader still reports http.MaxBytesError.
	req.Body = http.MaxBytesReader(w, req.Body, documentUploadBodyLimit(a.cfg))
	reader := multipart.NewReader(req.Body, parameters["boundary"])
	var (
		data         []byte
		fileName     string
		fileMIME     string
		overrideName string
		haveFile     bool
		haveName     bool
	)
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			a.documentUploadReadFailed(w, req, current, partErr)
			return
		}
		switch part.FormName() {
		case "file":
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
		case "name":
			if haveName {
				_ = part.Close()
				writeError(w, http.StatusBadRequest, "invalid_body", "Send at most one name field.")
				return
			}
			haveName = true
			var raw []byte
			raw, partErr = io.ReadAll(io.LimitReader(part, documentUploadNameBytes))
			if partErr == nil {
				overrideName = strings.TrimSpace(string(raw))
			}
		default:
			// Unknown fields are ignored on purpose: the session decides the chat and the user.
		}
		_ = part.Close()
		if partErr != nil {
			a.documentUploadReadFailed(w, req, current, partErr)
			return
		}
	}
	if !haveFile {
		writeError(w, http.StatusBadRequest, "invalid_body", "Send one file part.")
		return
	}
	if overrideName != "" {
		fileName = overrideName
	}
	result, err := bot.IngestRAGDocument(req.Context(), a.db, a.embeddings, a.logger, a.cfg.BotToken,
		current.ChatID, current.UserID,
		bot.RAGDocumentInput{Name: fileName, MIMEType: fileMIME, Data: data},
		int(a.cfg.RAGMaxDocuments))
	switch {
	case err == nil:
	case errors.Is(err, store.ErrRAGDocumentLimit):
		writeError(w, http.StatusConflict, "document_limit", "This chat reached its document limit (RAG_MAX_DOCUMENTS). Delete a document and retry.")
		return
	case errors.Is(err, bot.ErrRAGDocumentEmpty):
		writeError(w, http.StatusBadRequest, "document_empty", "The document is empty.")
		return
	case errors.Is(err, bot.ErrRAGDocumentTooLarge):
		writeError(w, http.StatusBadRequest, "document_too_large", "A text document may be at most 64 KiB; a PDF or DOCX at most 20 MiB.")
		return
	case errors.Is(err, bot.ErrDocumentNoText):
		writeError(w, http.StatusBadRequest, "document_no_text", "The document has no text layer — OCR is not available.")
		return
	case errors.Is(err, bot.ErrDocumentEncrypted):
		writeError(w, http.StatusBadRequest, "document_encrypted", "The PDF is password-protected. Remove the password and retry.")
		return
	case errors.Is(err, bot.ErrDocumentUnsupported):
		writeError(w, http.StatusBadRequest, "unsupported_document", "Legacy .doc is not supported. Save the document as .docx or .pdf.")
		return
	case errors.Is(err, bot.ErrDocumentCorrupt):
		writeError(w, http.StatusBadRequest, "invalid_document", "The file could not be read as a PDF or DOCX.")
		return
	case errors.Is(err, bot.ErrAttachmentType):
		writeError(w, http.StatusBadRequest, "unsupported_document", "Send a PDF, DOCX or UTF-8 text document of a supported type.")
		return
	default:
		if errors.Is(err, context.Canceled) || errors.Is(err, req.Context().Err()) {
			return
		}
		a.logger.Error("Mini App document upload failed", "chat_id", current.ChatID, "error", err)
		writeError(w, http.StatusInternalServerError, "document_mutation_failed", "The document could not be saved.")
		return
	}
	// One line with metadata only: the document text never reaches the log.
	a.logger.Info("Mini App RAG document uploaded",
		"chat_id", current.ChatID, "user_id", current.UserID, "document_id", result.ID,
		"name", result.Name, "bytes", len(data), "chunks", result.Chunks, "embedded", result.Embedded)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": result.ID, "name": result.Name, "chunks": result.Chunks, "embedded": result.Embedded,
	})
}

// documentUploadReadFailed maps the two ways reading a multipart body can fail: the request cap (413) and a
// malformed or truncated body (400).
func (a *App) documentUploadReadFailed(w http.ResponseWriter, req *http.Request, current scope, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "Request body is too large.")
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, req.Context().Err()) {
		return
	}
	a.logger.Warn("Mini App document upload body could not be read",
		"chat_id", current.ChatID, "content_length", req.ContentLength, "error", err.Error())
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		writeError(w, http.StatusRequestTimeout, "upload_timeout", "The upload was interrupted. Check the connection and send the file again.")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_body", "Send one multipart file part.")
}
