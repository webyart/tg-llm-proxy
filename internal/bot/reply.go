// escaping first, cutting second, otherwise a chunk can end with a lone backslash and Telegram
// answers «не понимает форматирование».
package bot

import (
	"context"
	"strings"
	"unicode"

	"bot/internal/config"
)

// exactly these three names, so a typo cannot reach here.
const (
	ParseMarkdownV2 = "MarkdownV2"
	ParseHTML       = "HTML"
	ParsePlain      = "plain"
)

// "&" is safe to expand here without double-escaping the "&" of "&lt;".
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// renders and escapes everything else through the library's own special-character set.
func Escape(text string, parseMode string) string {
	switch parseMode {
	case ParseMarkdownV2:
		return formatMarkdownV2(text)
	case ParseHTML:
		return htmlEscaper.Replace(text)
	default:
		return text
	}
}

// Telegram counts symbols, so `len([]rune)` is the measure and a byte cut would split кириллица).
// A whitespace boundary inside the last fifth of the chunk is preferred; if none exists the chunk is
// текст»).
func Chunk(text string, maxRunes int) []string {
	return chunk([]rune(text), maxRunes, false)
}

// then cut). MarkdownV2 puts a backslash before every special character, so a cut that lands right
// after a backslash would send a chunk ending in a dangling escape, and Telegram answers «Can't
// same chunk.
func ChunkEscaped(text string, maxRunes int) []string {
	return chunk([]rune(text), maxRunes, true)
}

func chunk(runes []rune, maxRunes int, escapeAware bool) []string {
	if maxRunes <= 0 {
		maxRunes = int(config.DefaultMaxChunk)
	}
	if len(runes) == 0 {
		return nil
	}
	if len(runes) <= maxRunes {
		return []string{string(runes)}
	}

	var out = []string{}
	var rest = runes
	var window = maxRunes / 5
	if window < 1 {
		window = 1
	}
	for len(rest) > 0 {
		if len(rest) <= maxRunes {
			out = append(out, string(rest))
			break
		}
		var cut = maxRunes
		var limit = maxRunes - window
		if limit < 0 {
			limit = 0
		}
		var i = maxRunes - 1
		for i > limit {
			if unicode.IsSpace(rest[i]) {
				break
			}
			i--
		}
		if i > limit {
			cut = i + 1
		}
		if escapeAware && cut > 1 && trailingEscapes(rest[:cut])%2 == 1 {
			// The chunk would end in the middle of an escape pair — move the pair to the next chunk.
			cut--
		}
		out = append(out, string(rest[:cut]))
		rest = rest[cut:]
		// the whitespace the cut landed on belongs to no chunk
		for len(rest) > 0 && unicode.IsSpace(rest[0]) {
			rest = rest[1:]
		}
	}
	if escapeAware && len(out) > 0 {
		var last = []rune(out[len(out)-1])
		if trailingEscapes(last)%2 == 1 {
			// Nothing follows the final escape, so the backslash is dropped rather than sent broken.
			out[len(out)-1] = string(last[:len(last)-1])
		}
	}
	return out
}

// trailingEscapes — how many consecutive backslashes end the slice (odd means a dangling escape).
func trailingEscapes(runes []rune) int {
	var n = 0
	var i = len(runes) - 1
	for i >= 0 && runes[i] == '\\' {
		n++
		i--
	}
	return n
}

// that ends with a lone backslash is the 400 Telegram answers «не понимает форматирование»). MarkdownV2 is the
// only mode whose escaping marks change the length of a piece mid-word, so it is the mode the escape-aware cut
// is written for; HTML and plain cut on whitespace alone.
func (d *Dispatcher) chunksOf(text string) []string {
	if d.deps.Cfg.ParseMode != ParseMarkdownV2 {
		return Chunk(Escape(text, d.deps.Cfg.ParseMode), int(d.deps.Cfg.MaxMessageChunk))
	}
	return markdownV2Chunks(text, int(d.deps.Cfg.MaxMessageChunk))
}

func markdownV2Chunks(text string, maxRunes int) []string {
	blocks := codeFencePattern.FindAllStringIndex(text, -1)
	if len(blocks) == 0 {
		return markdownV2TextChunks(text, maxRunes)
	}
	out := []string{}
	cursor := 0
	for _, block := range blocks {
		out = append(out, markdownV2TextChunks(text[cursor:block[0]], maxRunes)...)
		out = append(out, markdownV2CodeChunks(text[block[0]:block[1]], maxRunes)...)
		cursor = block[1]
	}
	out = append(out, markdownV2TextChunks(text[cursor:], maxRunes)...)
	return packMarkdownV2Chunks(out, maxRunes)
}

func packMarkdownV2Chunks(parts []string, maxRunes int) []string {
	packed := []string{}
	for _, part := range parts {
		if len(packed) == 0 {
			packed = append(packed, part)
			continue
		}
		last := len(packed) - 1
		if Runes(packed[last])+1+Runes(part) <= maxRunes {
			packed[last] += "\n" + part
		} else {
			packed = append(packed, part)
		}
	}
	return packed
}

func markdownV2TextChunks(text string, maxRunes int) []string {
	if text == "" {
		return nil
	}
	formatted := formatMarkdownV2(text)
	if Runes(formatted) <= maxRunes {
		return []string{formatted}
	}
	budget := (maxRunes - 16) / 2
	if budget < 1 {
		budget = 1
	}
	parts := Chunk(text, budget)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, formatMarkdownV2(part))
	}
	return out
}

func markdownV2CodeChunks(block string, maxRunes int) []string {
	formatted := formatMarkdownV2(block)
	if Runes(formatted) <= maxRunes {
		return []string{formatted}
	}
	newline := strings.Index(block, "\n")
	match := codeFencePattern.FindStringSubmatch(block)
	if newline < 0 || match == nil {
		return markdownV2TextChunks(block, maxRunes)
	}
	opening := block[:newline]
	budget := (maxRunes - Runes(opening) - 16) / 2
	if budget < 1 {
		budget = 1
	}
	parts := Chunk(match[1], budget)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if !strings.HasSuffix(part, "\n") {
			part += "\n"
		}
		out = append(out, formatMarkdownV2(opening+"\n"+part+"```"))
	}
	return out
}

// already delivered must not be delivered twice).
func (d *Dispatcher) Send(ctx context.Context, chatID int64, text string) error {
	var chunks = d.chunksOf(text)
	var i = 0
	for i < len(chunks) {
		if err := d.deps.Replier.Send(ctx, chatID, chunks[i]); err != nil {
			return err
		}
		i++
	}
	return nil
}

func Runes(text string) int { return len([]rune(text)) }

func CutRunes(text string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return text, false
	}
	var runes = []rune(text)
	if len(runes) <= maxRunes {
		return text, false
	}
	return strings.TrimRight(string(runes[:maxRunes]), " \t\n\r") + TruncatedMarker, true
}

const TruncatedMarker = "… (текст обрезан по MAX_INPUT_CHARS)"

func isParseEntitiesError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "can't parse entities")
}

func plainForParseFallback(text string, parseMode string) string {
	switch parseMode {
	case ParseMarkdownV2:
		runes := []rune(text)
		out := make([]rune, 0, len(runes))
		for i := 0; i < len(runes); i++ {
			if runes[i] == '\\' && i+1 < len(runes) && strings.ContainsRune("_*[]()~`>#+-=|{}.!\\", runes[i+1]) {
				continue
			}
			out = append(out, runes[i])
		}
		return string(out)
	case ParseHTML:
		return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(text)
	default:
		return text
	}
}
