// reply_test.go — §12 UX and §16 change 7: the measure of Telegram is a SYMBOL, so the cut of a reply
// goes by runes, and the escaping happens BEFORE the cut (a chunk that ends with a dangling escape
// makes Telegram answer «Can't parse entities» instead of the answer of §6).
package bot

import (
	"strings"
	"testing"

	"bot/internal/config"
)

// TestEscapePerParseMode — the PARSE_MODE values of §7.
func TestEscapePerParseMode(t *testing.T) {
	var raw = "4 _5* [6] (7) `8` 9# +10 =11 |12 {13} .14 & <b>"

	if got := Escape(raw, ParsePlain); got != raw {
		t.Fatalf("plain must send the text as it is (§7), got %q", got)
	}
	var md = Escape(raw, ParseMarkdownV2)
	if !strings.Contains(md, `\_`) || !strings.Contains(md, `\*`) || !strings.Contains(md, `\(`) {
		t.Fatalf("MarkdownV2 must escape the characters of Telegram, got %q", md)
	}
	if strings.Contains(md, "<b>") {
		t.Fatalf("MarkdownV2 must escape the angle brackets too, got %q", md)
	}
	var html = Escape(raw, ParseHTML)
	if !strings.Contains(html, "&lt;b&gt;") || !strings.Contains(html, "&amp;") {
		t.Fatalf("HTML must escape the entities of Telegram, got %q", html)
	}
	if strings.Contains(html, "&amp;lt;") {
		t.Fatalf("HTML must not double-escape its own entities, got %q", html)
	}
	// The §7 default is MarkdownV2, so a config with no PARSE_MODE must escape the same way.
	var cfg, problems = config.LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "t:k", "STRATA_KEY": "k"})
	if len(problems) > 0 {
		t.Fatalf("config.LoadFrom rejected the defaults (§7): %v", problems)
	}
	if cfg.ParseMode != config.DefaultParseMode {
		t.Fatalf("PARSE_MODE must default to %s (§7), got %q", config.DefaultParseMode, cfg.ParseMode)
	}
	if Escape("текст_с_подчёркиванием", cfg.ParseMode) == "текст_с_подчёркиванием" {
		t.Fatalf("the default parse mode must escape underscores, got %q", Escape("текст_с_подчёркиванием", cfg.ParseMode))
	}
}

// TestChunkMeasuresSymbolsNotBytes — §16 change 7: MAX_MESSAGE_CHUNK counts symbols. «кириллица»
// costs two bytes per character, so a byte-based cut can also split a rune.
func TestChunkMeasuresSymbolsNotBytes(t *testing.T) {
	var text = strings.Repeat("абвгд ", 400) // 2400 runes, 4800 bytes
	var chunks = Chunk(text, 1000)
	var total = 0
	var i = 0
	for i < len(chunks) {
		if Runes(chunks[i]) > 1000 {
			t.Fatalf("a chunk of %d runes breaks the limit of §7 (§12 UX), got %d", 1000, Runes(chunks[i]))
		}
		if strings.ContainsRune(chunks[i], 0xFFFD) {
			t.Fatalf("a chunk must not contain a replacement rune (§16 change 7: cut by runes), got %q", chunks[i])
		}
		total += Runes(chunks[i])
		i++
	}
	if total != Runes(text) {
		t.Fatalf("chunking must not lose characters, got %d of %d (§13 риск «ответ рвёт текст»)", total, Runes(text))
	}
	if len(chunks) < 2 {
		t.Fatalf("a text above the limit must be split, got %d chunks", len(chunks))
	}
}

// TestChunkDoesNotSplitWords — §12 UX: a cut in the middle of a word is the failure §13 lists, so a
// whitespace boundary inside the last fifth of the chunk is preferred.
func TestChunkDoesNotSplitWords(t *testing.T) {
	var text = strings.Repeat("слово ", 200)
	var chunks = Chunk(text, 120)
	var i = 0
	for i < len(chunks) {
		if strings.HasPrefix(chunks[i], "лово") {
			t.Fatalf("a chunk must not start in the middle of a word (§12 UX), got %q", chunks[i])
		}
		i++
	}
}

// TestChunkKeepsTheEscapePairTogether — §16 change 7 «экранирование до разбиения» has a second half:
// after the escaping the cut must not land between a backslash and the character it protects.
func TestChunkKeepsTheEscapePairTogether(t *testing.T) {
	// Escape turns each "x." into `x\.`, so the escaped text is 120 symbols with an escape pair every
	// three symbols — a cut at 41 lands exactly between the backslash and the dot.
	var raw = strings.Repeat("x.", 40)
	var escaped = Escape(raw, ParseMarkdownV2)
	if !strings.Contains(escaped, `x\.`) {
		t.Fatalf("the fixture must be escaped by the parse mode of §7, got %q", escaped)
	}
	if Runes(escaped) != 120 {
		t.Fatalf("the fixture must be 120 symbols after escaping, got %d", Runes(escaped))
	}

	var chunks = ChunkEscaped(escaped, 41)
	var i = 0
	for i < len(chunks) {
		if strings.HasSuffix(chunks[i], `\`) {
			t.Fatalf("a chunk must not end with a dangling escape (§16 change 7), got %q", chunks[i])
		}
		i++
	}
	// The naive Chunk shows what the fix protects against: the same escaped text cut at the same size
	// leaves a chunk ending mid-escape, and Telegram answers «Can't parse entities» (§12 UX).
	var naive = Chunk(escaped, 41)
	var dangling = false
	var j = 0
	for j < len(naive) {
		if strings.HasSuffix(naive[j], `\`) {
			dangling = true
		}
		j++
	}
	if !dangling {
		t.Fatalf("the fixture must reproduce the dangling escape the fix protects against")
	}
}

// TestChunkShortTextIsOnePiece — the common case of §6 must not be cut at all.
func TestChunkShortTextIsOnePiece(t *testing.T) {
	var chunks = Chunk("короткий ответ", int(config.DefaultMaxChunk))
	if len(chunks) != 1 || chunks[0] != "короткий ответ" {
		t.Fatalf("a short answer must travel as one message (§12 UX), got %q", chunks)
	}
	if empty := Chunk("", 10); empty != nil {
		t.Fatalf("an empty text sends nothing (§6), got %q", empty)
	}
	// MAX_MESSAGE_CHUNK of §7 is the fallback when the caller has no budget of its own.
	var over = Chunk(strings.Repeat("a", int(config.DefaultMaxChunk)+1), 0)
	if len(over) != 2 {
		t.Fatalf("maxRunes<=0 must fall back to MAX_MESSAGE_CHUNK of §7, got %d chunks", len(over))
	}
}

// TestCutRunesMarksTheCut — §5 «обрезка входящего текста с пометкой» with MAX_INPUT_CHARS of §7.
func TestCutRunesMarksTheCut(t *testing.T) {
	var text = strings.Repeat("д", 100)
	var cut, changed = CutRunes(text, 40)
	if !changed {
		t.Fatalf("a text above the budget must be reported as cut (§5)")
	}
	if Runes(cut) != 40+Runes(TruncatedMarker) {
		t.Fatalf("the cut must keep the budget and add the marker of §5, got %d runes", Runes(cut))
	}
	if !strings.HasSuffix(cut, TruncatedMarker) {
		t.Fatalf("the notice of §5 must be visible to the model, got %q", cut)
	}
	var untouched, wasCut = CutRunes("короткий", 40)
	if wasCut || untouched != "короткий" {
		t.Fatalf("a text inside the budget must stay untouched (§7), got %q changed=%v", untouched, wasCut)
	}
	var off, offCut = CutRunes(text, 0)
	if offCut || off != text {
		t.Fatalf("MAX_INPUT_CHARS=0 must mean no cut, got %q changed=%v", off, offCut)
	}
}
