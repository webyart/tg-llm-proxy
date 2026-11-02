// reply.go — the outgoing side of §12 UX: «ответы ≤4096 символов (лимит Telegram считается в
// символах, не в байтах), разбиение по рунам» and §16 change 7 («экранирование до разбиения»):
// escaping first, cutting second, otherwise a chunk can end with a lone backslash and Telegram
// answers «не понимает форматирование».
package bot

import (
	"context"
	"strings"
	"unicode"

	"bot/internal/config"
)

// The PARSE_MODE values of §7 ("MarkdownV2 | HTML | plain"). config.Load validates the row against
// exactly these three names, so a typo cannot reach here.
const (
	ParseMarkdownV2 = "MarkdownV2"
	ParseHTML       = "HTML"
	ParsePlain      = "plain"
)

// htmlEscaper — Telegram's HTML entities (§12 UX). strings.NewReplacer scans once, which is why
// "&" is safe to expand here without double-escaping the "&" of "&lt;".
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Escape — the parse mode of §7 applied to the outgoing text. MarkdownV2 preserves the markup Telegram
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

// Chunk — the cut of a text into pieces of at most maxRunes CHARACTERS (§16 change 7: the limit of
// Telegram counts symbols, so `len([]rune)` is the measure and a byte cut would split кириллица).
// A whitespace boundary inside the last fifth of the chunk is preferred; if none exists the chunk is
// still cut at maxRunes, because a partial rune is worse than a cut mid-word (§13 risk «ответ рвёт
// текст»).
func Chunk(text string, maxRunes int) []string {
	return chunk([]rune(text), maxRunes, false)
}

// ChunkEscaped — the same cut for an ALREADY escaped text (§16 change 7 fixes the order: escape,
// then cut). MarkdownV2 puts a backslash before every special character, so a cut that lands right
// after a backslash would send a chunk ending in a dangling escape, and Telegram answers «Can't
// parse entities» instead of the answer (§12 UX). The fix keeps the escape and its character in the
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

// chunksOf — the outgoing text of §6 reduced to the pieces one Telegram chat can receive: escaped once with the
// PARSE_MODE of §7, then cut to MAX_MESSAGE_CHUNK (§16 change 7: «экранирование до разбиения», because a chunk
// that ends with a lone backslash is the 400 Telegram answers «не понимает форматирование»). MarkdownV2 is the
// only mode whose escaping marks change the length of a piece mid-word, so it is the mode the escape-aware cut
// is written for; HTML and plain cut on whitespace alone.
//
// This is a function of its own because §15 needs the SAME pieces: a streamed answer ends as one edited message
// plus the tail, and two copies of the chunking rule is how §12 UX starts disagreeing with itself.
func (d *Dispatcher) chunksOf(text string) []string {
	var escaped = Escape(text, d.deps.Cfg.ParseMode)
	if d.deps.Cfg.ParseMode != ParseMarkdownV2 {
		return Chunk(escaped, int(d.deps.Cfg.MaxMessageChunk))
	}
	return ChunkEscaped(escaped, int(d.deps.Cfg.MaxMessageChunk))
}

// Send — an answer of §6 through the Replier: escaped once, then chunked to MAX_MESSAGE_CHUNK (§7)
// and sent in order. §10 День 6 adds the idempotency of the outgoing side here (a chunk that was
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

// Runes — the character count Telegram measures a limit against (§12 UX: symbols, not bytes).
func Runes(text string) int { return len([]rune(text)) }

// CutRunes — the truncation of §7 (MAX_INPUT_CHARS, MAX_CONTEXT_CHARS) by characters, with the
// marker §5 asks for («обрезка входящего текста с пометкой»).
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

// TruncatedMarker — the notice of §5 «обрезка входящего текста с пометкой», in the language the user
// speaks (§6 answers are Russian).
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
