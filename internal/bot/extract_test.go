package bot

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"bot/internal/store"
)

// testPDF builds a minimal single-page PDF with one uncompressed content stream. Offsets are computed from the
// actual buffer, so the fixture stays valid when a test pads it to exercise a size boundary. padding > 0 emits a
// PDF comment (ignored by readers) of that many bytes after the header.
func testPDF(content string, padding int) []byte {
	var buffer bytes.Buffer
	buffer.WriteString("%PDF-1.4\n")
	if padding > 0 {
		buffer.WriteByte('%')
		buffer.Write(bytes.Repeat([]byte("p"), padding))
		buffer.WriteByte('\n')
	}
	offsets := make([]int, 6)
	object := func(number int, body string) {
		offsets[number] = buffer.Len()
		fmt.Fprintf(&buffer, "%d 0 obj\n%s\nendobj\n", number, body)
	}
	object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>")
	object(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	object(5, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	xref := buffer.Len()
	buffer.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for number := 1; number <= 5; number++ {
		fmt.Fprintf(&buffer, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&buffer, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return buffer.Bytes()
}

// testEncryptedPDF is the minimal Standard-security fixture from the PDF library's own tests: a 40-bit
// encryption dictionary whose O/U values do not match the empty password.
func testEncryptedPDF() []byte {
	o := strings.Repeat("O", 32)
	u := strings.Repeat("U", 32)
	id := strings.Repeat("I", 16)
	var buffer bytes.Buffer
	buffer.WriteString("%PDF-1.4\n")
	xref := buffer.Len()
	buffer.WriteString("xref\n0 1\n0000000000 65535 f \n")
	fmt.Fprintf(&buffer, "trailer\n<< /Size 1 /ID [(%s)] /Encrypt << /Filter /Standard /V 1 /R 2 /Length 40 /O (%s) /U (%s) /P 0 >> >>\n", id, o, u)
	fmt.Fprintf(&buffer, "startxref\n%d\n%%%%EOF\n", xref)
	return buffer.Bytes()
}

// testDOCX builds a real zip package with word/document.xml and one paragraph per argument.
func testDOCX(paragraphs ...string) []byte {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("word/document.xml")
	if err != nil {
		panic(err)
	}
	var document bytes.Buffer
	document.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	document.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, paragraph := range paragraphs {
		document.WriteString(`<w:p><w:r><w:t>`)
		var escaped bytes.Buffer
		if err := xml.EscapeText(&escaped, []byte(paragraph)); err != nil {
			panic(err)
		}
		document.Write(escaped.Bytes())
		document.WriteString(`</w:t></w:r></w:p>`)
	}
	document.WriteString(`</w:body></w:document>`)
	if _, err := entry.Write(document.Bytes()); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

func TestExtractDocumentTextFromPDF(t *testing.T) {
	data := testPDF("BT /F1 24 Tf 72 720 Td (Hello PDF from Aurora) Tj ET", 0)
	text, err := ExtractDocumentText("report.pdf", "application/pdf", data)
	if err != nil {
		t.Fatalf("ExtractDocumentText: %v", err)
	}
	if !strings.Contains(text, "Hello PDF from Aurora") {
		t.Fatalf("extracted text = %q", text)
	}
	if strings.Contains(text, "%PDF") || strings.Contains(text, "endobj") {
		t.Fatalf("extracted text contains PDF syntax: %q", text)
	}
}

func TestExtractDocumentTextFromPDFWithoutExtension(t *testing.T) {
	data := testPDF("BT /F1 12 Tf 72 720 Td (MIME only) Tj ET", 0)
	text, err := ExtractDocumentText("telegram-upload", "application/pdf", data)
	if err != nil || !strings.Contains(text, "MIME only") {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestExtractDocumentTextRejectsPDFWithoutTextLayer(t *testing.T) {
	// A page with graphics only: a real PDF, one page, no text-showing operator. This is what a scanned
	// document looks like after a PDF printer wrapped its image.
	data := testPDF("q 0 0 612 792 re f Q", 0)
	text, err := ExtractDocumentText("scan.pdf", "application/pdf", data)
	if !errors.Is(err, ErrDocumentNoText) {
		t.Fatalf("text=%q err=%v, want ErrDocumentNoText", text, err)
	}
	if text != "" {
		t.Fatalf("scan-only PDF returned text %q", text)
	}
}

func TestExtractDocumentTextRejectsEncryptedPDF(t *testing.T) {
	if _, err := ExtractDocumentText("secret.pdf", "application/pdf", testEncryptedPDF()); !errors.Is(err, ErrDocumentEncrypted) {
		t.Fatalf("err=%v, want ErrDocumentEncrypted", err)
	}
}

func TestExtractDocumentTextFromDOCX(t *testing.T) {
	data := testDOCX("First paragraph", "Second paragraph")
	text, err := ExtractDocumentText("notes.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", data)
	if err != nil {
		t.Fatalf("ExtractDocumentText: %v", err)
	}
	if !strings.Contains(text, "First paragraph") || !strings.Contains(text, "Second paragraph") {
		t.Fatalf("extracted text = %q", text)
	}
	if strings.Index(text, "First paragraph") > strings.Index(text, "Second paragraph") {
		t.Fatalf("paragraph order lost: %q", text)
	}
}

func TestExtractDocumentTextRejectsLegacyDOC(t *testing.T) {
	data := []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1, 0, 0, 0, 0}
	if _, err := ExtractDocumentText("old.doc", "application/msword", data); !errors.Is(err, ErrDocumentUnsupported) {
		t.Fatalf("err=%v, want ErrDocumentUnsupported", err)
	}
	// The extension is enough even when the transport declares a generic MIME.
	if _, err := ExtractDocumentText("old.doc", "application/octet-stream", data); !errors.Is(err, ErrDocumentUnsupported) {
		t.Fatalf("generic MIME err=%v, want ErrDocumentUnsupported", err)
	}
}

func TestExtractDocumentTextKeepsPlainTextUnchanged(t *testing.T) {
	data := []byte("line one\r\n\r\n\r\n  indented\tline two\n")
	text, err := ExtractDocumentText("notes.txt", "text/plain", data)
	if err != nil {
		t.Fatalf("ExtractDocumentText: %v", err)
	}
	if text != string(data) {
		t.Fatalf("plain text changed: got %q want %q", text, data)
	}
	if _, err := ExtractDocumentText("nul.txt", "text/plain", []byte("a\x00b")); !errors.Is(err, ErrAttachmentType) {
		t.Fatalf("NUL text err=%v, want ErrAttachmentType", err)
	}
	if _, err := ExtractDocumentText("bad.txt", "text/plain", []byte{0xff, 0xfe}); !errors.Is(err, ErrAttachmentType) {
		t.Fatalf("invalid UTF-8 err=%v, want ErrAttachmentType", err)
	}
}

func TestNormalizeDocumentTextStripsControlCharactersAndBlankRuns(t *testing.T) {
	raw := "line one\x00\x01\x02\r\n\r\n \r\n\r\nline two\x7f"
	got := normalizeDocumentText(raw)
	if strings.ContainsAny(got, "\x00\x01\x02\x7f") {
		t.Fatalf("control characters survived: %q", got)
	}
	if got != "line one\n\nline two" {
		t.Fatalf("normalized = %q", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("blank lines were not collapsed: %q", got)
	}
}

func TestNormalizeDocumentTextTruncatesAtTheRuneLimit(t *testing.T) {
	raw := "prefix " + strings.Repeat("x", DocumentTextMaxRunes+500)
	got := normalizeDocumentText(raw)
	if count := utf8.RuneCountInString(got); count != DocumentTextMaxRunes {
		t.Fatalf("runes=%d, want %d", count, DocumentTextMaxRunes)
	}
	if !strings.HasSuffix(got, documentTruncationMarker) {
		t.Fatalf("truncation marker missing at the end: %q", got[len(got)-80:])
	}
	if !strings.HasPrefix(got, "prefix ") {
		t.Fatalf("truncation lost the beginning: %q", got[:40])
	}
}

func TestDocumentLimitDependsOnTheFormat(t *testing.T) {
	for _, test := range []struct {
		name string
		mime string
		want int64
	}{
		{"notes.txt", "text/plain", DocumentMaxBytes},
		{"report.pdf", "application/pdf", DocumentBinaryMaxBytes},
		{"contract.PDF", "application/octet-stream", DocumentBinaryMaxBytes},
		{"brief.docx", "application/octet-stream", DocumentBinaryMaxBytes},
		{"legacy.doc", "application/msword", DocumentBinaryMaxBytes},
		{"export.csv", "application/vnd.ms-excel", DocumentMaxBytes},
	} {
		if got := documentLimit(test.name, test.mime); got != test.want {
			t.Errorf("documentLimit(%q, %q) = %d, want %d", test.name, test.mime, got, test.want)
		}
	}
	if DocumentBinaryMaxBytes <= 15<<20 {
		t.Fatalf("binary limit %d must stay above 15 MiB", DocumentBinaryMaxBytes)
	}
}

func TestIngestRAGDocumentExtractsPDFAndStoresText(t *testing.T) {
	db := testStore(t)
	data := testPDF("BT /F1 18 Tf 72 720 Td (Aurora PDF body) Tj ET", 0)
	result, err := IngestRAGDocument(context.Background(), db, nil, nil, "", 210, 11,
		RAGDocumentInput{Name: "aurora.pdf", MIMEType: "application/pdf", Data: data}, 20)
	if err != nil {
		t.Fatalf("IngestRAGDocument: %v", err)
	}
	if result.Name != "aurora.pdf" || result.Chunks < 1 {
		t.Fatalf("result=%+v", result)
	}
	var content string
	if err := db.QueryRow(`SELECT content FROM rag_chunks WHERE document_id = ? ORDER BY ordinal LIMIT 1`, result.ID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Aurora PDF body") || strings.Contains(content, "%PDF") {
		t.Fatalf("stored chunk = %q", content)
	}
}

func TestIngestRAGDocumentStoresNothingForAScanOnlyPDF(t *testing.T) {
	db := testStore(t)
	data := testPDF("q 0 0 612 792 re f Q", 0)
	_, err := IngestRAGDocument(context.Background(), db, nil, nil, "", 210, 11,
		RAGDocumentInput{Name: "scan.pdf", MIMEType: "application/pdf", Data: data}, 20)
	if !errors.Is(err, ErrDocumentNoText) {
		t.Fatalf("err=%v, want ErrDocumentNoText", err)
	}
	documents, listErr := store.ListRAGDocuments(context.Background(), db, 210)
	if listErr != nil || len(documents) != 0 {
		t.Fatalf("documents=%+v err=%v", documents, listErr)
	}
}

func TestIngestRAGDocumentEnforcesThePerTypeLimit(t *testing.T) {
	db := testStore(t)
	// A .txt over 64 KiB is still rejected before extraction, exactly as before this change.
	_, err := IngestRAGDocument(context.Background(), db, nil, nil, "", 210, 11,
		RAGDocumentInput{Name: "big.txt", MIMEType: "text/plain", Data: bytes.Repeat([]byte("a"), int(DocumentMaxBytes)+1)}, 20)
	if !errors.Is(err, ErrRAGDocumentTooLarge) {
		t.Fatalf("text err=%v, want ErrRAGDocumentTooLarge", err)
	}
	// A PDF just over the binary limit is rejected too; the extraction never runs and nothing is stored.
	_, err = IngestRAGDocument(context.Background(), db, nil, nil, "", 210, 11,
		RAGDocumentInput{Name: "big.pdf", MIMEType: "application/pdf", Data: make([]byte, DocumentBinaryMaxBytes+1)}, 20)
	if !errors.Is(err, ErrRAGDocumentTooLarge) {
		t.Fatalf("pdf err=%v, want ErrRAGDocumentTooLarge", err)
	}
	documents, listErr := store.ListRAGDocuments(context.Background(), db, 210)
	if listErr != nil || len(documents) != 0 {
		t.Fatalf("documents=%+v err=%v", documents, listErr)
	}
}
