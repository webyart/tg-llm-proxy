package bot

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

const (
	// DocumentBinaryMaxBytes is the cap of a PDF or DOCX attachment on both ingest paths. It is exactly the
	// Telegram Bot API getFile download ceiling (20 MiB), so the Mini App cannot accept a file the bot could not
	// download anyway. DocumentMaxBytes stays the 64 KiB cap of a plain-text document.
	DocumentBinaryMaxBytes = int64(20 << 20)
	// DocumentTextMaxRunes bounds what extraction hands to the store: a large PDF/DOCX is accepted, but only
	// this much text is kept. The store then chunks that text exactly as it chunks any other document.
	DocumentTextMaxRunes = 200_000
	// documentExtractedBytesLimit is the byte budget of the extracted text this package copies out of a parser:
	// four bytes per rune covers the widest UTF-8 encoding of DocumentTextMaxRunes runes, so the output cap
	// never requires a second full copy of a much larger parser buffer.
	documentExtractedBytesLimit = 4 * DocumentTextMaxRunes
	// documentDOCXXMLBytes bounds how much of a zip entry word/document.xml is decompressed. A document with
	// more XML than this still yields its first DocumentTextMaxRunes runes; the rest is formatting or an
	// archive bomb and is not needed for ingestion.
	documentDOCXXMLBytes = int64(16 << 20)
	// documentTruncationMarker is appended when the extracted text exceeded DocumentTextMaxRunes. The marker
	// counts towards the cap, so the stored text is never longer than DocumentTextMaxRunes runes.
	documentTruncationMarker = "\n\n[… текст обрезан до 200 000 символов]"
)

// Errors of ExtractDocumentText. IngestRAGDocument returns them unchanged; both transports turn them into a
// readable message and store nothing.
var (
	// ErrDocumentUnsupported is the legacy binary .doc: the format is known, but parsing it is deliberately out
	// of scope.
	ErrDocumentUnsupported = errors.New("legacy .doc is not supported; save it as .docx or .pdf")
	// ErrDocumentEncrypted is a password-protected PDF. The library never guesses a password.
	ErrDocumentEncrypted = errors.New("PDF is encrypted; remove the password and retry")
	// ErrDocumentNoText is a valid container with nothing extractable: a scanned PDF, a PDF with only vector
	// graphics, or a DOCX that contains only images. OCR is not part of the bot.
	ErrDocumentNoText = errors.New("no text layer — OCR is not available")
	// ErrDocumentCorrupt is a malformed file of a supported format.
	ErrDocumentCorrupt = errors.New("document is malformed")
)

type documentKind int

const (
	documentKindUnknown documentKind = iota
	documentKindText
	documentKindPDF
	documentKindDOCX
	documentKindDOC
)

// documentLimit is the size cap of one document, chosen by the same type detection extraction uses: 64 KiB for
// text, 20 MiB for PDF/DOCX. It replaces the old kind-only Telegram limit and is also what IngestRAGDocument
// enforces, so both transports agree.
func documentLimit(name string, declaredMIME string) int64 {
	if isBinaryDocument(name, declaredMIME) {
		return DocumentBinaryMaxBytes
	}
	return DocumentMaxBytes
}

func isBinaryDocument(name string, declaredMIME string) bool {
	switch documentKindOf(name, declaredMIME) {
	case documentKindPDF, documentKindDOCX, documentKindDOC:
		return true
	default:
		return false
	}
}

// documentKindOf resolves the format from the extension first and the declared MIME second. An explicit
// extension wins over a vendor or generic MIME (the same rule as textDocumentExtension), because a picker may
// report application/octet-stream for a perfectly ordinary .pdf.
func documentKindOf(name string, declaredMIME string) documentKind {
	switch strings.ToLower(path.Ext(strings.TrimSpace(name))) {
	case ".pdf":
		return documentKindPDF
	case ".docx":
		return documentKindDOCX
	case ".doc":
		return documentKindDOC
	}
	mime := strings.ToLower(strings.TrimSpace(strings.Split(declaredMIME, ";")[0]))
	switch mime {
	case "application/pdf":
		return documentKindPDF
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return documentKindDOCX
	case "application/msword":
		return documentKindDOC
	}
	if textDocumentExtension(name) || strings.HasPrefix(mime, "text/") || textualApplicationMIME(mime) {
		return documentKindText
	}
	return documentKindUnknown
}

// ExtractDocumentText turns the bytes of one supported document into the text the RAG store indexes. It is the
// single entry point of Telegram /docs add and POST /api/documents, so both transports accept and reject exactly
// the same files. Plain text keeps the historical documentIsText validation and is returned byte-for-byte;
// PDF and DOCX are extracted and then normalized (valid UTF-8, no NUL/control characters, collapsed blank
// lines, DocumentTextMaxRunes cap with a marker). A known format with nothing to extract returns
// ErrDocumentNoText, never an empty document.
func ExtractDocumentText(name, declaredMIME string, data []byte) (string, error) {
	switch documentKindOf(name, declaredMIME) {
	case documentKindPDF:
		if len(data) == 0 {
			return "", ErrDocumentNoText
		}
		text, err := extractPDFText(data)
		if err != nil {
			return "", err
		}
		return finishExtractedText(text)
	case documentKindDOCX:
		if len(data) == 0 {
			return "", ErrDocumentNoText
		}
		text, err := extractDOCXText(data)
		if err != nil {
			return "", err
		}
		return finishExtractedText(text)
	case documentKindDOC:
		return "", ErrDocumentUnsupported
	case documentKindText:
		// The historical text contract is unchanged: UTF-8, no NUL/control characters, the extension/MIME list.
		if !documentIsText(name, declaredMIME, data) {
			return "", ErrAttachmentType
		}
		return string(data), nil
	default:
		return "", ErrAttachmentType
	}
}

// finishExtractedText normalizes a binary extraction and enforces the output contract. Empty text after
// trimming means the container had no text layer, which is a distinct user-facing condition from an empty
// upload.
func finishExtractedText(text string) (string, error) {
	normalized := normalizeDocumentText(text)
	if strings.TrimSpace(normalized) == "" {
		return "", ErrDocumentNoText
	}
	return normalized, nil
}

// extractPDFText is the pure-Go PDF path (github.com/ledongthuc/pdf, MIT, no cgo). A PDF is untrusted input:
// every call is wrapped in recover because the parser is allowed to panic on a malformed cross-reference table
// or content stream. The parser materializes the document object graph and the plain text in memory, so a
// 20 MiB input is the bound of the transient buffer (FlateDecode streams can expand; only the first
// documentExtractedBytesLimit bytes of text are copied out afterwards).
func extractPDFText(data []byte) (text string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			text = ""
			err = fmt.Errorf("%w: PDF parser panic", ErrDocumentCorrupt)
		}
	}()
	reader, openErr := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if openErr != nil {
		return "", pdfExtractionError(openErr)
	}
	if reader.NumPage() <= 0 {
		return "", ErrDocumentNoText
	}
	plain, textErr := reader.GetPlainText()
	if textErr != nil {
		return "", pdfExtractionError(textErr)
	}
	var extracted strings.Builder
	if _, copyErr := io.Copy(&extracted, io.LimitReader(plain, documentExtractedBytesLimit)); copyErr != nil {
		return "", fmt.Errorf("%w: %v", ErrDocumentCorrupt, copyErr)
	}
	return extracted.String(), nil
}

func pdfExtractionError(err error) error {
	if errors.Is(err, pdf.ErrInvalidPassword) || strings.Contains(strings.ToLower(err.Error()), "encrypt") {
		return fmt.Errorf("%w: %v", ErrDocumentEncrypted, err)
	}
	return fmt.Errorf("%w: %v", ErrDocumentCorrupt, err)
}

// extractDOCXText parses word/document.xml with archive/zip and encoding/xml: no new dependency. It only reads
// the XML entry, never other parts of the package. The decoder runs over a bounded reader and the collected text
// stops at the output budget, so a zip bomb is bounded by decompression of documentDOCXXMLBytes, not by the
// uncompressed size declared in the archive.
func extractDOCXText(data []byte) (text string, err error) {
	archive, zipErr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if zipErr != nil {
		return "", fmt.Errorf("%w: not a DOCX package", ErrDocumentCorrupt)
	}
	var document *zip.File
	for _, file := range archive.File {
		if file.Name == "word/document.xml" {
			document = file
			break
		}
	}
	if document == nil {
		return "", fmt.Errorf("%w: word/document.xml is missing", ErrDocumentCorrupt)
	}
	entry, openErr := document.Open()
	if openErr != nil {
		return "", fmt.Errorf("%w: %v", ErrDocumentCorrupt, openErr)
	}
	defer entry.Close()

	decoder := xml.NewDecoder(io.LimitReader(entry, documentDOCXXMLBytes))
	var extracted strings.Builder
	inText := false
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			// A truncated XML prefix can still contain usable text (an oversized document.xml is cut by the
			// bounded reader); anything else is malformed.
			if extracted.Len() > 0 && (errors.Is(tokenErr, io.ErrUnexpectedEOF) || strings.Contains(tokenErr.Error(), "unexpected EOF")) {
				break
			}
			return "", fmt.Errorf("%w: %v", ErrDocumentCorrupt, tokenErr)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "t":
				inText = true
			case "tab":
				extracted.WriteByte('\t')
			case "br":
				extracted.WriteByte('\n')
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "t":
				inText = false
			case "p":
				// A paragraph end is a line break; consecutive empty paragraphs are collapsed by normalization.
				if extracted.Len() > 0 {
					extracted.WriteByte('\n')
				}
			}
		case xml.CharData:
			if inText {
				extracted.Write(element)
				if extracted.Len() >= documentExtractedBytesLimit {
					return extracted.String(), nil
				}
			}
		}
	}
	return extracted.String(), nil
}

// normalizeDocumentText applies the output contract: valid UTF-8, no NUL or control characters other than
// newline/tab, no runs of blank lines, no surrounding whitespace, and at most DocumentTextMaxRunes runes
// including the truncation marker. It is unexported; the test exercises it directly.
func normalizeDocumentText(text string) string {
	// Invalid UTF-8 is replaced rather than rejected: a PDF font may emit a stray byte, and the surrounding
	// readable text is still worth indexing. U+FFFD is valid UTF-8.
	text = strings.ToValidUTF8(text, "\uFFFD")
	cleaned := strings.Builder{}
	cleaned.Grow(len(text))
	for _, r := range text {
		if r == 0 || unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}
		cleaned.WriteRune(r)
	}
	// Lines, not raw newlines: a PDF commonly emits " \n \n" between blocks, and those spaces must not defeat
	// the collapse. CRLF is normalized first so a blank line is always one empty line.
	normalized := strings.ReplaceAll(cleaned.String(), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	out := make([]string, 0, len(lines))
	blankLines := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blankLines++
			// At most one blank line between two non-empty lines; a run of 2+ blank lines collapses.
			if blankLines > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blankLines = 0
		out = append(out, line)
	}
	trimmed := strings.TrimSpace(strings.Join(out, "\n"))
	if utf8.RuneCountInString(trimmed) <= DocumentTextMaxRunes {
		return trimmed
	}
	markerRunes := utf8.RuneCountInString(documentTruncationMarker)
	keep := DocumentTextMaxRunes - markerRunes
	if keep < 0 {
		keep = 0
	}
	return string([]rune(trimmed)[:keep]) + documentTruncationMarker
}
