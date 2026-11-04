package bot

import (
	"strings"
	"testing"
)

func TestMarkdownV2PreservesTelegramFormatting(t *testing.T) {
	raw := "## Заголовок\n**Жирный** и _курсив_ и `x`\n[site](https://example.com)\n1. пункт"
	got := Escape(raw, ParseMarkdownV2)
	want := "*Заголовок*\n*Жирный* и _курсив_ и `x`\n[site](https://example.com)\n1\\. пункт"
	if got != want {
		t.Fatalf("MarkdownV2 formatting was not preserved:\n got %q\nwant %q", got, want)
	}
}

func TestMarkdownV2EscapesOrdinaryTextAndRejectsUnsafeLinks(t *testing.T) {
	got := Escape("1. обычный - текст [click](javascript:alert(1))", ParseMarkdownV2)
	if !strings.Contains(got, `1\. обычный \- текст`) {
		t.Fatalf("ordinary Markdown punctuation must be escaped: %q", got)
	}
	if strings.Contains(got, "[click](javascript:") {
		t.Fatalf("a javascript link must not be preserved: %q", got)
	}
}

func TestMarkdownV2CodeIsLiteralAndUsesCodeEscaping(t *testing.T) {
	raw := "```go\n**не форматировать** и `x`\n```"
	got := Escape(raw, ParseMarkdownV2)
	if !strings.Contains(got, "**не форматировать**") {
		t.Fatalf("code content must not become bold: %q", got)
	}
	if !strings.Contains(got, "\\`x\\`") {
		t.Fatalf("backticks inside code must be escaped for Telegram's code context: %q", got)
	}
}

func TestMarkdownV2EscapesInsideFormatting(t *testing.T) {
	got := Escape("**a_b**", ParseMarkdownV2)
	if got != "*a\\_b*" {
		t.Fatalf("ordinary underscore inside bold must still be escaped, got %q", got)
	}
	if strings.Contains(got, "@@TGMD") {
		t.Fatalf("an internal placeholder leaked into the answer: %q", got)
	}
}

func TestMarkdownV2EscapesUnderscoresInsideIdentifiers(t *testing.T) {
	raw := "TELEGRAM_ALLOWED_USERS; read_url calls: 0; web_search calls: 0"
	want := `TELEGRAM\_ALLOWED\_USERS; read\_url calls: 0; web\_search calls: 0`
	if got := Escape(raw, ParseMarkdownV2); got != want {
		t.Fatalf("identifiers must stay literal:\n got %q\nwant %q", got, want)
	}
}

func TestMarkdownV2PreservesItalicAfterIdentifier(t *testing.T) {
	got := Escape("read_url и _курсив_", ParseMarkdownV2)
	want := `read\_url и _курсив_`
	if got != want {
		t.Fatalf("italic after an identifier must be preserved: got %q, want %q", got, want)
	}
}
